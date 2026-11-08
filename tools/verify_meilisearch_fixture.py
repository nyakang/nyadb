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
PROFILE_REQUIRED = ['ProfileURIReopenAndOriginalEditorCheck', 'ProfileProxyContextAndCanceledOriginalCheck', 'EditorLocalizedFormEvidence']
SESSION_REQUIRED = ['SessionQualifiedPaginationAndDefinitions', 'SessionRESTAdmissionFailureAndSynchronous', 'SessionLostCancelReleaseCloseAndRecovery', 'SessionMetadataCloseAcknowledged']
SESSION_SCOPED = 'SessionScopedPermissionsAndRevocation'
PUBLIC_REQUIRED = ['PublicProviderMetadataAndPublishedGenerations', 'RESTLostWriteCancelAndExplicitRecovery', 'PublicChooserURINavigationDefinitionsRESTAndRecovery']
PUBLIC_SCOPED = 'PublicScopedWorkbenchPermissions'
DOCUMENT_REQUIRED = ['DocumentsEnumerationBeyondSearchLimitAndExactIDs', 'DocumentsReadInterruptionsCancelCloseAndNoReplay']
DOCUMENT_SCOPED = 'DocumentsScopedKeyPermissions'
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


def source_hashes(profile=False, session=False, public=False, documents=False):
    paths = sorted((ROOT/'internal/services/meilisearch').glob('*.go')) + sorted((ROOT/'internal/services/connections').glob('*.go')) + [ROOT/'go.mod', ROOT/'go.sum', Path(__file__).resolve(), ROOT/'tests/test_meilisearch_fixture.py']
    if profile or session or public:
        paths = sorted((ROOT/'internal').rglob('*.go')) + sorted((ROOT/'locales').glob('*.go')) + sorted((ROOT/'locales').glob('*.json')) + [ROOT/'go.mod', ROOT/'go.sum', Path(__file__).resolve(), ROOT/'tests/test_meilisearch_fixture.py']
    return {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in paths}


def required_passes(mode, profile=False, session=False, public=False, documents=False):
    if mode not in MODES:
        raise ValueError('Invalid native fixture mode')
    profile, session = profile or public, session or public
    return REQUIRED + ([] if mode == 'anonymous' else [SCOPED]) + (PROFILE_REQUIRED if profile else []) + ((SESSION_REQUIRED + ([] if mode == 'anonymous' else [SESSION_SCOPED])) if session else []) + ((PUBLIC_REQUIRED + ([] if mode == 'anonymous' else [PUBLIC_SCOPED])) if public else []) + ((DOCUMENT_REQUIRED + ([] if mode == 'anonymous' else [DOCUMENT_SCOPED])) if documents else [])


def test_command(mode, race, profile=False, session=False, public=False, documents=False):
    selected = '^TestMeilisearchNative(' + '|'.join(required_passes(mode, profile, session, public, documents)) + ')$'
    return ['go', 'test', '-count=1', '-timeout=10m', '-v', *(['-race'] if race else []), '-run', selected, './internal/services/meilisearch', *(['./internal/ui/connectioneditor'] if profile or public else []), *(['./internal/services/dbruntime', './internal/ui/sqlworkspace', './internal/ui/shell'] if public else [])]


def check_acceptance(log, mode, profile=False, session=False, public=False, documents=False):
    for name in required_passes(mode, profile, session, public, documents):
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


def verify(mode='tls', race=False, profile=False, session=False, public=False, documents=False):
    profile, session = profile or public, session or public
    required_passes(mode, profile, session, public, documents)
    output = ROOT/'build/meilisearch'
    output.mkdir(parents=True, exist_ok=True)
    nonce = uuid.uuid4().hex
    identifier = None
    evidence_path = output/('owned-fixture-'+nonce+'.json')
    evidence = dict(nonce=nonce, image=IMAGE, requested_version=VERSION, mode=mode, scope='public_workbench_and_private_document_read_codecs' if public and documents else 'private_native_document_listing_and_id_reads' if documents else 'public_chooser_uri_sessions_indexes_definitions_rest_recovery' if public else 'private_original_session_metadata_rest_leases' if session else ('private_profile_uri_vault_editor' if profile else 'private_native_transport_tasks_search'), status='failed', fixture_cleaned=False, private_files_cleaned=False)
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
                if profile:
                    probe.update(connect_timeout_seconds=12, request_timeout_seconds=45)
                    def request(method, path, body=None):
                        data = None if body is None else json.dumps(body, ensure_ascii=False).encode()
                        with urllib.request.urlopen(urllib.request.Request(endpoint+path, data=data, method=method, headers=headers), context=context, timeout=10) as response:
                            return response.status, json.load(response)
                    def wait(receipt):
                        deadline = time.monotonic()+30
                        while time.monotonic() < deadline:
                            status, task = request('GET', '/tasks/'+str(receipt['taskUid']))
                            if status != 200 or task['uid'] != receipt['taskUid']:
                                raise RuntimeError('Seed task observation changed')
                            if task['status'] == 'succeeded':
                                return
                            if task['status'] in ('failed', 'canceled'):
                                raise RuntimeError('Seed native task failed')
                            time.sleep(0.05)
                        raise RuntimeError('Seed task did not settle')
                    index_uid = 'owned_profile_'+nonce
                    status, receipt = request('POST', '/indexes', {'uid':index_uid, 'primaryKey':'id'})
                    if status != 202:
                        raise RuntimeError('Seed index admission failed')
                    wait(receipt)
                    status, receipt = request('POST', '/indexes/'+index_uid+'/documents', [{'id':'one', 'integer':9007199254740993, 'text':'原生🐱'}])
                    if status != 202:
                        raise RuntimeError('Seed document admission failed')
                    wait(receipt)
                    probe['fixture_index_uid'] = index_uid
                    if key:
                        status, scoped_key = request('POST', '/keys', {'name':'owned-profile-scope', 'actions':['indexes.get','documents.get','search','tasks.get'], 'indexes':[index_uid], 'expiresAt':None})
                        if status != 201 or not isinstance(scoped_key.get('key'), str):
                            raise RuntimeError('Seed index-scoped API key failed')
                        private_write(folder/'scoped-api-key', scoped_key['key'])
                        private_write(folder/'scoped-probe.json', json.dumps(dict(probe, key_file=str(folder/'scoped-api-key'), index_uid=index_uid))+'\n')
                private_write(folder/'probe.json', json.dumps(probe)+'\n')
                env = dict(os.environ, NYADB_MEILISEARCH_IT='1', NYADB_MEILISEARCH_CONFIG=str(folder/'probe.json'))
                if profile:
                    env.update(NYADB_MEILISEARCH_PROFILE_IT='1', NYADB_MEILISEARCH_SCOPED_CONFIG=str(folder/'scoped-probe.json') if key else '', NYADB_MEILISEARCH_PROFILE_IMAGES=str(output/('profile-images-'+nonce)))
                if session:
                    env['NYADB_MEILISEARCH_SESSION_IT'] = '1'
                if public:
                    env.update(NYADB_MEILISEARCH_PUBLIC_IT='1', NYADB_MEILISEARCH_PUBLIC_IMAGES=str(output/('public-images-'+nonce)))
                if documents:
                    env['NYADB_MEILISEARCH_DOCUMENT_IT'] = '1'
                hashes = source_hashes(profile, session, public, documents)
                logfile = output/('native-'+nonce+'.log')
                command = test_command(mode, race, profile, session, public, documents)
                with logfile.open('w') as log:
                    result = subprocess.run(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=630)
                if hashes != source_hashes(profile, session, public, documents):
                    raise RuntimeError('Verification sources changed during acceptance; rerun on stable sources')
                if result.returncode:
                    raise RuntimeError('Native acceptance failed; inspect '+str(logfile.relative_to(ROOT)))
                check_acceptance(logfile.read_text(), mode, profile, session, public, documents)
                if profile:
                    images = sorted(Path(env['NYADB_MEILISEARCH_PROFILE_IMAGES']).glob('*.png'))
                    expected = {language+'-'+str(width)+'-'+auth+'-'+tab+'.png' for language in ['zh-Hans','en'] for width in [900,720] for auth in ['none','bearer'] for tab in ['general','advanced','ssl']}
                    if {p.name for p in images} != expected:
                        raise RuntimeError('Missing localized general/advanced/SSL form evidence')
                    evidence['headless_form_images'] = {str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in images}
                if public:
                    images = sorted(Path(env['NYADB_MEILISEARCH_PUBLIC_IMAGES']).rglob('*.png'))
                    expected = {language+'/'+view+'-'+str(width)+'x'+str(height)+'.png' for language in ['zh-Hans','en'] for width,height in [(1280,800),(720,520)] for view in ['definition','rest-admission']}
                    if {str(p.relative_to(Path(env['NYADB_MEILISEARCH_PUBLIC_IMAGES']))) for p in images} != expected:
                        raise RuntimeError('Missing localized native public workbench images')
                    evidence['headless_workbench_images'] = {str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in images}
                evidence.update(status='passed_with_race' if race else 'passed', observed_identity=identity, source_hashes=hashes, command=command, log=str(logfile.relative_to(ROOT)), log_sha256=hashlib.sha256(logfile.read_bytes()).hexdigest(), required_passes=required_passes(mode, profile, session, public, documents))
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
    parser.add_argument('--profile', action='store_true', help='Verify private URI/options/vault/editor/reopen and original checks')
    parser.add_argument('--session', action='store_true', help='Verify original sessions, bounded index metadata, REST admission, cancellation and explicit recovery')
    parser.add_argument('--public', action='store_true', help='Verify public chooser, saved profiles, qualified metadata, REST admission and explicit recovery; implies profile and session')
    parser.add_argument('--documents', action='store_true', help='Verify bounded native document enumeration, exact IDs, readonly errors and original read cancellation/recovery; no public grid claims')
    options = parser.parse_args()
    verify(options.mode, options.race, options.profile, options.session, options.public, options.documents)
