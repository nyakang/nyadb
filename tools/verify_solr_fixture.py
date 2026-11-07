#!/usr/bin/env python3
"""Verify native Solr on an owned immutable-ID Docker fixture, with real Jetty TLS."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import signal
import ssl
import subprocess
import tempfile
import time
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
IMAGE = 'solr@sha256:b1bc1e670a0f7f107bc9c37f2c9dcfb7f1a67351bb1cd49bea837105defa263b'
BUILD = '386ed096a1946c488cfe576a19a147bdb1153508'


def remove_owned(identifier, nonce):
    if not isinstance(identifier, str) or not re.fullmatch('[0-9a-f]{64}', identifier):
        raise RuntimeError('Invalid immutable fixture ID')
    result = subprocess.run(['docker', 'inspect', identifier], capture_output=True, text=True)
    if result.returncode:
        return
    record = json.loads(result.stdout)[0]
    labels = record['Config'].get('Labels') or {}
    if record['Id'] != identifier or labels.get('nyadb.fixture') != 'solr' or labels.get('nyadb.fixture.nonce') != nonce:
        raise RuntimeError('Fixture ownership changed; refuse cleanup')
    subprocess.run(['docker', 'rm', '-f', '-v', identifier], check=True, stdout=subprocess.DEVNULL)


def verify(plain=False, race=False):
    base, output = ROOT/'temp/solr', ROOT/'build/solr'
    base.mkdir(parents=True, exist_ok=True)
    output.mkdir(parents=True, exist_ok=True)
    nonce, identifier = uuid.uuid4().hex, None
    evidence = {'nonce': nonce, 'image': IMAGE, 'tls': not plain, 'scope': 'private_native_solr_transport_foundation', 'storage': 'owned_private_bind_mount'}
    def shutdown(signum, _frame):
        raise SystemExit(128+signum)
    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGINT, shutdown)
    with tempfile.TemporaryDirectory(prefix='fixture-', dir=base) as temporary:
        folder = Path(temporary)
        folder.chmod(0o700)
        password = '' if plain else 'Nya'+secrets.token_hex(20)+'@9'
        password_file = folder/'password'
        password_file.write_text(password)
        password_file.chmod(0o600)
        server = folder/'server'
        server.mkdir(mode=0o700)
        data = server/'data'
        data.mkdir(mode=0o700)
        if os.getuid() == 0:
            os.chown(server, 8983, 8983)
            os.chown(data, 8983, 8983)
        bootstrap = folder/'owned-configsets.sh'
        bootstrap.write_text('mkdir -p /var/solr/data/configsets\ncp -r /opt/solr/server/solr/configsets/_default /var/solr/data/configsets/\n')
        bootstrap.chmod(0o444)
        args = ['--memory=2g', '--cpus=3', '-e', 'SOLR_HEAP=512m', '--mount', 'type=bind,src='+str(server)+',dst=/var/solr', '--mount', 'type=bind,src='+str(bootstrap)+',dst=/docker-entrypoint-initdb.d/owned-configsets.sh,readonly']
        tls = folder/'tls'
        if not plain:
            tls.mkdir(mode=0o700)
            keystore_secret = 'Store'+secrets.token_hex(20)
            keystore_password_file = folder/'keystore-password'
            keystore_password_file.write_text(keystore_secret)
            keystore_password_file.chmod(0o600)
            def openssl(*arguments):
                subprocess.run(['openssl', *arguments], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            openssl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2', '-subj', '/CN=NyaDB owned Solr CA', '-keyout', str(folder/'ca.key'), '-out', str(tls/'ca.crt'), '-addext', 'basicConstraints=critical,CA:TRUE', '-addext', 'keyUsage=critical,keyCertSign,cRLSign')
            openssl('req', '-new', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN=localhost', '-keyout', str(folder/'server.key'), '-out', str(folder/'server.csr'))
            extensions = folder/'extensions'
            extensions.write_text('basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:localhost,IP:127.0.0.1\nauthorityKeyIdentifier=keyid,issuer\n')
            openssl('x509', '-req', '-in', str(folder/'server.csr'), '-CA', str(tls/'ca.crt'), '-CAkey', str(folder/'ca.key'), '-CAcreateserial', '-CAserial', str(folder/'ca.srl'), '-days', '2', '-sha256', '-extfile', str(extensions), '-out', str(folder/'server.crt'))
            openssl('pkcs12', '-export', '-in', str(folder/'server.crt'), '-inkey', str(folder/'server.key'), '-certfile', str(tls/'ca.crt'), '-out', str(tls/'server.p12'), '-name', 'owned-solr', '-passout', 'file:'+str(keystore_password_file))
            environment = folder/'environment'
            environment.write_text('SOLR_SSL_ENABLED=true\nSOLR_SSL_KEY_STORE=/var/solr/tls/server.p12\nSOLR_SSL_KEY_STORE_TYPE=PKCS12\nSOLR_SSL_KEY_STORE_PASSWORD='+keystore_secret+'\nSOLR_SSL_TRUST_STORE=/var/solr/tls/server.p12\nSOLR_SSL_TRUST_STORE_TYPE=PKCS12\nSOLR_SSL_TRUST_STORE_PASSWORD='+keystore_secret+'\nSOLR_SSL_NEED_CLIENT_AUTH=false\nSOLR_SSL_WANT_CLIENT_AUTH=false\nSOLR_SSL_CHECK_PEER_NAME=true\n')
            environment.chmod(0o600)
            # Solr's original SHA-256 BasicAuth provider: SHA256(SHA256(salt+password)).
            salt = secrets.token_bytes(32)
            salted = base64.b64encode(hashlib.sha256(hashlib.sha256(salt+password.encode()).digest()).digest()).decode()+' '+base64.b64encode(salt).decode()
            security = data/'security.json'
            security.write_text(json.dumps({'authentication': {'class': 'solr.BasicAuthPlugin', 'blockUnknown': True, 'credentials': {'native-admin': salted}}}))
            security.chmod(0o400)
            if os.getuid() == 0:
                os.chown(tls, 8983, 8983)
                os.chown(security, 8983, 8983)
            for leaf in tls.iterdir():
                leaf.chmod(0o444 if leaf.suffix == '.crt' else 0o400)
                if os.getuid() == 0:
                    os.chown(leaf, 8983, 8983)
            args += ['--env-file', str(environment), '--mount', 'type=bind,src='+str(tls)+',dst=/var/solr/tls,readonly']
        evidence_path = output/('owned-fixture-'+nonce+'.json')
        try:
            subprocess.run(['docker', 'image', 'inspect', IMAGE], check=True, stdout=subprocess.DEVNULL)
            identifier = subprocess.check_output(['docker', 'create', '--name', 'nyadb-solr-it-'+nonce[:12], '--label', 'nyadb.fixture=solr', '--label', 'nyadb.fixture.nonce='+nonce, '-p', '127.0.0.1::8983', *args, IMAGE, 'solr-precreate', 'nyadb_fixture'], text=True).strip()
            evidence['id'] = identifier
            subprocess.run(['docker', 'start', identifier], check=True, stdout=subprocess.DEVNULL)
            inspected = json.loads(subprocess.check_output(['docker', 'inspect', identifier], text=True))[0]
            if not inspected['State']['Running']:
                raise RuntimeError('Native fixture exited before port publication; inspect captured server log')
            bindings = inspected['NetworkSettings']['Ports']['8983/tcp']
            if len(bindings) != 1 or bindings[0]['HostIp'] != '127.0.0.1':
                raise RuntimeError('Fixture must bind a single loopback port')
            endpoint = ('http' if plain else 'https')+'://127.0.0.1:'+bindings[0]['HostPort']+'/solr'
            context = None if plain else ssl.create_default_context(cafile=str(tls/'ca.crt'))
            headers = {} if plain else {'Authorization': 'Basic '+base64.b64encode(('native-admin:'+password).encode()).decode()}
            def get(path):
                with urllib.request.urlopen(urllib.request.Request(endpoint+path, headers=headers), context=context, timeout=10) as response:
                    return json.load(response)
            end = time.monotonic()+180
            while True:
                try:
                    identity = get('/admin/info/system?wt=json')
                    cores = get('/admin/cores?action=STATUS&wt=json')
                    if identity.get('mode') == 'std' and identity.get('lucene', {}).get('solr-spec-version') == '9.9.0' and BUILD in identity['lucene']['solr-impl-version'] and 'nyadb_fixture' in cores.get('status', {}):
                        break
                except Exception:
                    pass
                if time.monotonic() >= end:
                    raise RuntimeError('Native Solr readiness failed')
                time.sleep(1)
            (output/('identity-'+nonce+'.json')).write_text(json.dumps({'mode': identity['mode'], 'lucene': identity['lucene']}, indent=2)+'\n')
            probe = folder/'probe.json'
            probe.write_text(json.dumps({'endpoint': endpoint, 'auth': 'none' if plain else 'basic', 'username': '' if plain else 'native-admin', 'password_file': str(password_file), 'ca_file': '' if plain else str(tls/'ca.crt'), 'server_name': ''}))
            probe.chmod(0o600)
            print('Native Solr 9.9.0 ready; fixture nonce: '+nonce, flush=True)
            sources = sorted((ROOT/'internal/services/solr').glob('*.go'))+sorted((ROOT/'internal/services/connections').glob('solr*.go'))+[Path(__file__).resolve(), ROOT/'go.mod', ROOT/'go.sum']
            source_hashes = {str(path.relative_to(ROOT)): hashlib.sha256(path.read_bytes()).hexdigest() for path in sources}
            evidence['source_sha256'] = source_hashes
            env = os.environ.copy()
            env.update({'NYADB_SOLR_IT': '1', 'NYADB_SOLR_CONFIG': str(probe)})
            logfile = output/('acceptance-'+nonce+'.log')
            command = ['go', 'test', '-count=1', '-v', *(['-race'] if race else []), './internal/services/connections', './internal/services/solr']
            with logfile.open('w') as log:
                result = subprocess.run(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=600)
            text = logfile.read_text()
            required = ['TransportAuthenticationIdentityAndExactDocuments', 'LostPostAndGETCoreMutationsNeverReplay', 'CancelSettlesOriginalWorkerAndKeepsOtherClient', 'ConflictCanApplyPrefixWithoutReplay']
            if any(hashlib.sha256((ROOT/path).read_bytes()).hexdigest() != digest for path, digest in source_hashes.items()):
                raise RuntimeError('Verification sources changed during acceptance; rerun on stable sources')
            if result.returncode or any('--- PASS: TestSolrNative'+name not in text for name in required):
                raise RuntimeError('Native Solr acceptance failed; inspect captured log')
            evidence.update({'status': 'passed_with_race' if race else 'passed', 'log': str(logfile.relative_to(ROOT)), 'log_sha256': hashlib.sha256(logfile.read_bytes()).hexdigest()})
            print('Native Solr acceptance passed', flush=True)
        finally:
            if identifier:
                logs = subprocess.run(['docker', 'logs', identifier], capture_output=True, text=True)
                text = logs.stdout+logs.stderr
                for secret in [password, '' if plain else keystore_secret]:
                    if secret:
                        text = text.replace(secret, '[redacted]')
                lines = [('[redacted sensitive startup line]' if re.search(r'password|token|authorization|credential|secret|basicauth', line, re.I) else line) for line in text.splitlines()]
                (output/('server-'+nonce+'.log')).write_text('\n'.join(lines)+'\n')
                remove_owned(identifier, nonce)
                evidence['fixture_cleaned'] = True
            evidence_path.write_text(json.dumps(evidence, indent=2)+'\n')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--plain', action='store_true')
    parser.add_argument('--race', action='store_true')
    args = parser.parse_args()
    verify(args.plain, args.race)
