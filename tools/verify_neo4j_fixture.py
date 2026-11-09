#!/usr/bin/env python3
"""Accept the private Bolt foundation against a uniquely owned official Neo4j node."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import socket
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
IMAGE = 'neo4j@sha256:409728716bc239f9fa046368ac6ce6ef280f9e5f0bcb7cdd75031a4465cc192d'
VERSION = '5.26.21'
MODES = ('anonymous', 'basic', 'tls', 'mtls')
REQUIRED = (
    'AuthenticationIdentityAndTransport',
    'ExactParametersTemporalSpatialAndGraph',
    'CompletionErrorsAndBoundedResults',
    'RoutingAndOriginalDatabase',
    'LostCanceledAndOriginalCloseNeverReplay',
    'BlockedCancellationTerminationAndCleanup',
)


def remove_owned(identifier, nonce):
    if not re.fullmatch('[0-9a-f]{64}', identifier):
        raise RuntimeError('Invalid immutable fixture ID')
    result = subprocess.run(['docker', 'inspect', identifier], capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError('Cannot inspect owned Neo4j fixture')
    rows = json.loads(result.stdout)
    if len(rows) != 1:
        raise RuntimeError('Ambiguous fixture inspection')
    row = rows[0]
    labels = row['Config'].get('Labels') or {}
    if row['Id'] != identifier or labels.get('nyadb.fixture') != 'neo4j' or labels.get('nyadb.fixture.nonce') != nonce:
        raise RuntimeError('Fixture ownership changed; refuse cleanup')
    subprocess.run(['docker', 'rm', '-f', '-v', identifier], check=True, stdout=subprocess.DEVNULL)


def check_acceptance(log):
    for name in REQUIRED:
        if not re.search(r'^--- PASS: TestNeo4jNative' + name + r' \(', log, re.M):
            raise RuntimeError('Missing native acceptance: ' + name)
    if re.search(r'^\s*--- (SKIP|FAIL):', log, re.M) or 'WARNING: DATA RACE' in log:
        raise RuntimeError('Skipped, failed or racing native acceptance')


def openssl(*args, cwd):
    result = subprocess.run(['openssl', *args], cwd=cwd, capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError('Cannot generate owned fixture TLS certificates')


def certificates(folder):
    openssl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', 'ca.key', '-out', 'ca.crt', '-days', '2', '-subj', '/CN=NyaDB owned Neo4j CA', cwd=folder)
    for name, usage, san in [('server', 'serverAuth', 'DNS:localhost'), ('client', 'clientAuth', 'DNS:owned-client')]:
        openssl('req', '-newkey', 'rsa:2048', '-nodes', '-keyout', name+'.key', '-out', name+'.csr', '-subj', '/CN='+name, cwd=folder)
        (folder/(name+'.ext')).write_text('basicConstraints=CA:FALSE\nextendedKeyUsage='+usage+'\nsubjectAltName='+san+'\n')
        openssl('x509', '-req', '-in', name+'.csr', '-CA', 'ca.crt', '-CAkey', 'ca.key', '-CAcreateserial', '-out', name+'.crt', '-days', '2', '-extfile', name+'.ext', cwd=folder)
    openssl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', 'wrong.key', '-out', 'wrong.crt', '-days', '2', '-subj', '/CN=Untrusted CA', cwd=folder)


def verify(mode, race):
    nonce = uuid.uuid4().hex
    build = ROOT/'build/neo4j'
    build.mkdir(parents=True, exist_ok=True)
    report_path = build/('owned-fixture-'+nonce+'.json')
    log_path = build/('owned-fixture-'+nonce+'.log')
    report = {'nonce': nonce, 'mode': mode, 'version': VERSION, 'image': IMAGE,
              'scope': 'private-native-foundation', 'race': race, 'passed': False,
              'source_hashes': {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest()
                  for p in sorted(list((ROOT/'internal/services/neo4j').glob('*.go')) + list((ROOT/'internal/services/connections').glob('neo4j*.go')) + [ROOT/'go.mod', ROOT/'go.sum', ROOT/'tests/test_neo4j_fixture.py', Path(__file__).resolve()])}}
    identifier = None
    try:
        with tempfile.TemporaryDirectory(prefix='nyadb-neo4j-'+nonce+'-', dir=build) as raw:
            private = Path(raw)
            secret = 'Nya_'+secrets.token_urlsafe(24)
            envfile = private/'docker.env'
            envfile.write_text('NEO4J_AUTH='+('none' if mode == 'anonymous' else 'neo4j/'+secret)+'\n')
            envfile.chmod(0o600)
            with socket.socket() as sock:
                sock.bind(('127.0.0.1', 0))
                port = sock.getsockname()[1]
            conf = private/'conf'
            conf.mkdir(mode=0o755)
            config = [
                'server.default_listen_address=0.0.0.0', 'server.default_advertised_address=localhost',
                'server.bolt.advertised_address=localhost:'+str(port),
                'server.http.enabled=false', 'server.https.enabled=false',
                'server.memory.heap.initial_size=256m', 'server.memory.heap.max_size=512m',
                'server.memory.pagecache.size=256m',
            ]
            cfg = {'URI': 'bolt://localhost:'+str(port), 'Auth': 'none' if mode == 'anonymous' else 'basic',
                   'Username': '' if mode == 'anonymous' else 'neo4j', 'Database': 'neo4j',
                   'ConnectTimeoutSeconds': 5, 'RequestTimeoutSeconds': 40}
            mounts = ['--mount', 'type=bind,src='+str(conf)+',dst=/conf']
            if mode in ('tls', 'mtls'):
                certs = private/'certs'
                certs.mkdir(mode=0o755)
                certificates(certs)
                trusted = certs/'trusted'
                trusted.mkdir(mode=0o755)
                (trusted/'ca.crt').write_bytes((certs/'ca.crt').read_bytes())
                (certs/'revoked').mkdir(mode=0o755)
                # The private parent remains mode 0700 on the host; only the
                # exact certificate directory is bind-mounted into this node.
                for name in ('server.crt', 'server.key', 'ca.crt'):
                    (certs/name).chmod(0o644)
                config += ['server.bolt.tls_level=REQUIRED', 'dbms.ssl.policy.bolt.enabled=true',
                    'dbms.ssl.policy.bolt.base_directory=/owned-certs',
                    'dbms.ssl.policy.bolt.private_key=server.key', 'dbms.ssl.policy.bolt.public_certificate=server.crt',
                    'dbms.ssl.policy.bolt.client_auth='+('REQUIRE' if mode == 'mtls' else 'NONE')]
                mounts += ['--mount', 'type=bind,src='+str(certs)+',dst=/owned-certs,readonly']
                cfg.update(URI='bolt+s://localhost:'+str(port), CAFile=str(certs/'ca.crt'))
                if mode == 'mtls':
                    cfg.update(ClientCertFile=str(certs/'client.crt'), ClientKeyFile=str(certs/'client.key'))
            (conf/'neo4j.conf').write_text('\n'.join(config)+'\n')
            args = ['docker', 'create', '--label', 'nyadb.fixture=neo4j', '--label', 'nyadb.fixture.nonce='+nonce,
                    '--env-file', str(envfile), '-p', '127.0.0.1:'+str(port)+':7687', *mounts, IMAGE]
            created = subprocess.run(args, capture_output=True, text=True)
            if created.returncode or not re.fullmatch('[0-9a-f]{64}\n?', created.stdout):
                raise RuntimeError('Cannot create isolated Neo4j fixture')
            identifier = created.stdout.strip()
            report['container_id'] = identifier
            subprocess.run(['docker', 'start', identifier], check=True, stdout=subprocess.DEVNULL)
            deadline = time.monotonic()+150
            while time.monotonic() < deadline:
                logs = subprocess.run(['docker', 'logs', identifier], capture_output=True, text=True)
                if 'Started.' in logs.stdout:
                    break
                running = subprocess.run(['docker', 'inspect', '--format', '{{.State.Running}}', identifier], capture_output=True, text=True)
                if running.stdout.strip() != 'true':
                    raise RuntimeError('Owned Neo4j failed startup; inspect the retained local startup log')
                time.sleep(1)
            else:
                raise RuntimeError('Owned Neo4j startup timed out')
            fixture = private/'native.json'
            fixture.write_text(json.dumps({'Config': cfg, 'Secret': '' if mode == 'anonymous' else secret,
                'Mode': mode, 'Version': VERSION, 'Nonce': nonce, 'WrongCAFile': str(private/'certs/wrong.crt')}))
            fixture.chmod(0o600)
            env = os.environ.copy()
            env['NYADB_NEO4J_IT'] = '1'
            env['NYADB_NEO4J_CONFIG_FILE'] = str(fixture)
            env['CGO_ENABLED'] = '1' if race else '0'
            command = ['go', 'test'] + (['-race'] if race else []) + ['./internal/services/neo4j', '-run', '^TestNeo4jNative', '-count=1', '-timeout=8m', '-v']
            result = subprocess.run(command, cwd=ROOT, env=env, capture_output=True, text=True)
            log = result.stdout+result.stderr
            # Preserve evidence without ever retaining generated credentials.
            log = log.replace(secret, '[redacted]')
            log_path.write_text(log)
            if result.returncode:
                raise RuntimeError('Native Neo4j verification failed; see '+str(log_path.relative_to(ROOT)))
            check_acceptance(log)
            report['passed'] = True
            report['accepted'] = list(REQUIRED)
    finally:
        if identifier:
            startup = subprocess.run(['docker', 'logs', identifier], capture_output=True, text=True)
            (build/('owned-fixture-'+nonce+'-startup.log')).write_text((startup.stdout+startup.stderr).replace(secret, '[redacted]'))
            remove_owned(identifier, nonce)
            report['container_removed'] = True
        report['private_files_removed'] = True
        report_path.write_text(json.dumps(report, indent=2)+'\n')
        print(str(report_path.relative_to(ROOT)))
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--mode', choices=MODES, default='basic')
    parser.add_argument('--race', action='store_true')
    args = parser.parse_args()
    verify(args.mode, args.race)


if __name__ == '__main__':
    main()
