#!/usr/bin/env python3
"""Verify Meilisearch native task/search transport on a unique owned Docker node."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import ssl
import subprocess
import tempfile
import time
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
IMAGE = 'getmeili/meilisearch@sha256:b7ba431bf6f9b26cc86132798f3640964147a699cf23f026b74c6c05dbb1d60d'
VERSION = '1.56.1'
COMMIT = '421053d3f573efb3c1e5d857efb4e0408e599fd3'
REQUIRED = [
    'AuthenticationVersionAndExactJSON',
    'TaskAdmissionAndFailureEvidence',
    'SearchLimitsAndReadonlyErrors',
    'LostAdmissionResponseNeverReplays',
    'CanceledOriginalSearchAndWaitClose',
]
SCOPED = 'ScopedKeyPermissionsAndRevocation'
MODES = ('anonymous', 'bearer', 'tls', 'mtls')


def remove_owned(identifier, nonce):
    if not isinstance(identifier, str) or not re.fullmatch('[0-9a-f]{64}', identifier):
        raise RuntimeError('Invalid immutable fixture ID')
    result = subprocess.run(['docker', 'inspect', identifier], capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError('Cannot inspect owned fixture for cleanup')
    records = json.loads(result.stdout)
    if len(records) != 1:
        raise RuntimeError('Ambiguous fixture inspection')
    record = records[0]
    labels = record['Config'].get('Labels') or {}
    if record['Id'] != identifier or labels.get('nyadb.fixture') != 'meilisearch' or labels.get('nyadb.fixture.nonce') != nonce:
        raise RuntimeError('Fixture ownership changed; refuse cleanup')
    subprocess.run(['docker', 'rm', '-f', '-v', identifier], check=True, stdout=subprocess.DEVNULL)


def source_hashes():
    paths = sorted((ROOT/'internal/services/meilisearch').glob('*.go')) + sorted((ROOT/'internal/services/connections').glob('*.go')) + [ROOT/'go.mod', ROOT/'go.sum', Path(__file__).resolve(), ROOT/'tests/test_meilisearch_fixture.py']
    return {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in paths}


def required_passes(mode):
    if mode not in MODES:
        raise ValueError('Invalid native fixture mode')
    return REQUIRED + ([] if mode == 'anonymous' else [SCOPED])


def test_command(mode, race):
    selected = '^TestMeilisearchNative(' + '|'.join(required_passes(mode)) + ')$'
    return ['go', 'test', '-count=1', '-timeout=5m', '-v', *(['-race'] if race else []), '-run', selected, './internal/services/meilisearch']


def check_acceptance(log, mode):
    for name in required_passes(mode):
        if not re.search(r'^--- PASS: TestMeilisearchNative'+name+r' \(', log, re.M):
            raise RuntimeError('Missing required native PASS: '+name)
    if re.search(r'^\s*--- (SKIP|FAIL):', log, re.M) or 'WARNING: DATA RACE' in log:
        raise RuntimeError('Unexpected skipped/failed test or data race in native acceptance')
    if mode == 'mtls' and 'native mTLS accepted owned client certificate and rejected its absence' not in log:
        raise RuntimeError('Missing native mandatory-client-certificate evidence')


def redacted_server_log(raw, key):
    raw = raw.replace(key, '[redacted]') if key else raw
    return '\n'.join('[redacted sensitive startup line]' if re.search(r'key|token|password|secret|bearer|authorization|credential|[a-f0-9]{40,}', line, re.I) else line for line in raw.splitlines())+'\n'


def private_write(path, raw):
    path.write_text(raw)
    path.chmod(0o600)


def certificates(folder, mutual):
    def openssl(*args):
        subprocess.run(['openssl', *args], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    openssl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', str(folder/'ca.key'), '-out', str(folder/'ca.crt'), '-days', '2', '-subj', '/CN=NyaDB owned Meilisearch CA', '-addext', 'basicConstraints=critical,CA:TRUE', '-addext', 'keyUsage=critical,keyCertSign,cRLSign')
    for role in ['server'] + (['client'] if mutual else []):
        openssl('req', '-newkey', 'rsa:2048', '-nodes', '-keyout', str(folder/(role+'.key')), '-out', str(folder/(role+'.csr')), '-subj', '/CN='+('localhost' if role == 'server' else 'NyaDB owned client'))
        ext = 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage='+('serverAuth' if role == 'server' else 'clientAuth')+'\nauthorityKeyIdentifier=keyid,issuer\nsubjectKeyIdentifier=hash\n'
        if role == 'server':
            ext += 'subjectAltName=DNS:localhost,IP:127.0.0.1\n'
        private_write(folder/(role+'.ext'), ext)
        openssl('x509', '-req', '-in', str(folder/(role+'.csr')), '-CA', str(folder/'ca.crt'), '-CAkey', str(folder/'ca.key'), '-CAcreateserial', '-days', '2', '-sha256', '-extfile', str(folder/(role+'.ext')), '-out', str(folder/(role+'.crt')))
    for path in folder.iterdir():
        path.chmod(0o600)


def verify(mode='tls', race=False):
    required_passes(mode)
    output = ROOT/'build/meilisearch'
    output.mkdir(parents=True, exist_ok=True)
    nonce = uuid.uuid4().hex
    identifier = None
    evidence_path = output/('owned-fixture-'+nonce+'.json')
    evidence = dict(nonce=nonce, image=IMAGE, requested_version=VERSION, mode=mode, scope='private_native_transport_tasks_search', status='failed', fixture_cleaned=False, private_files_cleaned=False)
    try:
        with tempfile.TemporaryDirectory(prefix='owned-'+nonce+'-', dir=output) as private:
            folder = Path(private)
            key = '' if mode == 'anonymous' else secrets.token_hex(32)
            config = ['env = '+json.dumps('development' if mode == 'anonymous' else 'production'), 'http_addr = "0.0.0.0:7700"', 'no_analytics = true']
            if key:
                config += ['master_key = '+json.dumps(key)]
                private_write(folder/'api-key', key)
            if mode in ('tls', 'mtls'):
                certificates(folder, mode == 'mtls')
                config += ['ssl_cert_path = "/owned/server.crt"', 'ssl_key_path = "/owned/server.key"']
                if mode == 'mtls':
                    config += ['ssl_auth_path = "/owned/ca.crt"', 'ssl_require_auth = true']
            private_write(folder/'config.toml', '\n'.join(config)+'\n')
            try:
                subprocess.run(['docker', 'image', 'inspect', IMAGE], check=True, stdout=subprocess.DEVNULL)
                identifier = subprocess.check_output(['docker', 'create', '--name', 'nyadb-meili-it-'+nonce[:12], '--label', 'nyadb.fixture=meilisearch', '--label', 'nyadb.fixture.nonce='+nonce, '-p', '127.0.0.1::7700', '--mount', 'type=bind,src='+str(folder)+',dst=/owned,readonly', IMAGE, '/bin/meilisearch', '--config-file-path', '/owned/config.toml'], text=True).strip()
                evidence['id'] = identifier
                subprocess.run(['docker', 'start', identifier], check=True, stdout=subprocess.DEVNULL)
                inspected = json.loads(subprocess.check_output(['docker', 'inspect', identifier], text=True))[0]
                bindings = inspected['NetworkSettings']['Ports']['7700/tcp']
                if not inspected['State']['Running'] or len(bindings) != 1 or bindings[0]['HostIp'] != '127.0.0.1':
                    raise RuntimeError('Fixture did not publish a single loopback port')
                endpoint = ('https' if mode in ('tls', 'mtls') else 'http')+'://127.0.0.1:'+bindings[0]['HostPort']
                context = ssl.create_default_context(cafile=str(folder/'ca.crt')) if mode in ('tls', 'mtls') else None
                if mode == 'mtls':
                    context.load_cert_chain(str(folder/'client.crt'), str(folder/'client.key'))
                headers = {'Content-Type': 'application/json'}
                if key:
                    headers['Authorization'] = 'Bearer '+key
                deadline = time.monotonic()+90
                while True:
                    try:
                        with urllib.request.urlopen(urllib.request.Request(endpoint+'/version', headers=headers), context=context, timeout=5) as response:
                            identity = json.load(response)
                        if identity.get('pkgVersion') == VERSION and identity.get('commitSha') == COMMIT:
                            break
                    except Exception:
                        pass
                    if time.monotonic() >= deadline:
                        raise RuntimeError('Native readiness failed; inspect redacted owned server log')
                    time.sleep(0.5)
                probe = dict(endpoint=endpoint, auth='none' if mode == 'anonymous' else 'bearer', key_file='' if not key else str(folder/'api-key'), ca_file=str(folder/'ca.crt') if context else '', server_name='localhost' if context else '', client_cert_file=str(folder/'client.crt') if mode == 'mtls' else '', client_key_file=str(folder/'client.key') if mode == 'mtls' else '')
                private_write(folder/'probe.json', json.dumps(probe)+'\n')
                env = dict(os.environ, NYADB_MEILISEARCH_IT='1', NYADB_MEILISEARCH_CONFIG=str(folder/'probe.json'))
                hashes = source_hashes()
                logfile = output/('native-'+nonce+'.log')
                command = test_command(mode, race)
                with logfile.open('w') as log:
                    result = subprocess.run(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=330)
                if hashes != source_hashes():
                    raise RuntimeError('Verification sources changed during acceptance; rerun on stable sources')
                if result.returncode:
                    raise RuntimeError('Native acceptance failed; inspect '+str(logfile.relative_to(ROOT)))
                check_acceptance(logfile.read_text(), mode)
                evidence.update(status='passed_with_race' if race else 'passed', observed_identity=identity, source_hashes=hashes, command=command, log=str(logfile.relative_to(ROOT)), log_sha256=hashlib.sha256(logfile.read_bytes()).hexdigest(), required_passes=required_passes(mode))
            finally:
                if identifier:
                    logs = subprocess.run(['docker', 'logs', identifier], capture_output=True, text=True)
                    serverlog = output/('server-'+nonce+'.log')
                    serverlog.write_text(redacted_server_log(logs.stdout+logs.stderr, key))
                    evidence['server_log'] = str(serverlog.relative_to(ROOT))
                    evidence['server_log_sha256'] = hashlib.sha256(serverlog.read_bytes()).hexdigest()
                    remove_owned(identifier, nonce)
                    evidence['fixture_cleaned'] = True
        evidence['private_files_cleaned'] = not folder.exists()
    finally:
        evidence['private_files_cleaned'] = 'folder' in locals() and not folder.exists()
        evidence_path.write_text(json.dumps(evidence, indent=2)+'\n')
    print('Native Meilisearch '+VERSION+' '+mode+' '+evidence['status']+'; owned fixture and private files cleaned', flush=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--mode', choices=MODES, default='tls')
    parser.add_argument('--race', action='store_true')
    options = parser.parse_args()
    verify(options.mode, options.race)
