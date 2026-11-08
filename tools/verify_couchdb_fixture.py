#!/usr/bin/env python3
"""Verify native CouchDB on a unique owned Docker node, including native TLS."""
import argparse
import base64
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
import urllib.parse
import uuid

ROOT = Path(__file__).resolve().parents[1]
IMAGE = 'couchdb@sha256:a4be8d5fe6862766844672d3b660bce2ccc203852838d08b2f3385b910e5cae2'
VERSION = '3.5.3'
REQUIRED = [
    'AuthenticationNodeObservationAndExactJSON',
    'ExactPaddedPrincipal',
    'RevisionConflictAndPartialBulkReceipt',
    'DatabaseRecreationCanAliasOldRevision',
    'MangoOpaqueBookmarksAndChangesLifetime',
    'LostWriteResponseNeverReplays',
    'CancellationSettlesOriginalWorker',
    'DocumentAllDocsMangoExactJSONAndDeletedBoundary',
    'DocumentMangoLostReadCancellationNeverReplays',
]
WORKBENCH_REQUIRED = ['PublicProviderMetadataAndPublishedGenerations', 'RESTLostWriteCancelAndExplicitRecovery', 'PublicChooserURINavigationDefinitionsRESTAndRecovery']
SESSION_REQUIRED = ['SessionMetadataPermissionsAndQualifiedDefinitions', 'SessionRESTReceiptsAndRecoveryWithoutReplay', 'SessionLostReceiptCancellationAndOriginalClose', 'SessionRejectsChangedNodeAndPrincipalBeforeDispatch']
PROFILE_REQUIRED = ['ProfileURIReopenAndOriginalEditorCheck', 'ProfileProxyContextAndCanceledOriginalCheck', 'EditorLocalizedFormEvidence']


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
    if record['Id'] != identifier or labels.get('nyadb.fixture') != 'couchdb' or labels.get('nyadb.fixture.nonce') != nonce:
        raise RuntimeError('Fixture ownership changed; refuse cleanup')
    subprocess.run(['docker', 'rm', '-f', '-v', identifier], check=True, stdout=subprocess.DEVNULL)


def source_hashes():
    paths = sorted((ROOT/'internal').rglob('*.go')) + sorted((ROOT/'locales').glob('*.go')) + sorted((ROOT/'locales').glob('*.json')) + [ROOT/'go.mod', ROOT/'go.sum', ROOT/'tools/verify_couchdb_fixture.py', ROOT/'tests/test_couchdb_fixture.py']
    return {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in paths}


def check_acceptance(log, profile=False, session=False, workbench=False):
    for name in REQUIRED + (PROFILE_REQUIRED if profile else []) + (SESSION_REQUIRED if session else []) + (WORKBENCH_REQUIRED if workbench else []):
        if not re.search(r'^--- PASS: TestCouchDBNative'+name+r' \(', log, re.M):
            raise RuntimeError('Missing required native PASS: '+name)
    if re.search(r'^\s*--- SKIP:', log, re.M):
        raise RuntimeError('Unexpected skipped test in native acceptance')


def native_test_command(profile=False, race=False, workbench=False):
    packages = ['./internal/services/connections', './internal/services/couchdb']
    if profile:
        packages += ['./internal/storage/localdb', './internal/ui/connectioneditor']
    if workbench:
        packages += ['./internal/services/dbruntime', './internal/ui/connectioncatalog', './internal/ui/navigation', './internal/ui/objectworkspace', './internal/ui/sqlworkspace', './internal/ui/shell']
    return ['go', 'test', '-count=1', '-timeout=20m', '-v', *(['-race'] if race else []), '-run', '^TestCouchDB', *packages]


def verify(plain=False, race=False, profile=False, session=False, workbench=False):
    profile, session = profile or workbench, session or workbench
    output = ROOT/'build/couchdb'
    output.mkdir(parents=True, exist_ok=True)
    nonce = uuid.uuid4().hex
    identifier = None
    evidence_path = output/('owned-fixture-'+nonce+'.json')
    evidence = {'nonce': nonce, 'image': IMAGE, 'requested_version': VERSION, 'mode': 'plain-none' if plain else 'tls-basic', 'scope': 'public_workbench' if workbench else 'private_session_metadata_query' if session else 'private_profile' if profile else 'private_native_transport', 'status': 'failed', 'fixture_cleaned': False}
    with tempfile.TemporaryDirectory(prefix='owned-'+nonce+'-', dir=output) as private:
        folder = Path(private)
        # Directory traversal for the native CouchDB UID; secrets stay mode 600
        # and container-readable files belong only to its service UID.
        folder.chmod(0o755)
        tls = folder/'tls'
        tls.mkdir(mode=0o755)
        config = folder/'config'
        config.mkdir(mode=0o755)
        password = secrets.token_urlsafe(32)
        padded_password = ' 密码🐱 '+secrets.token_urlsafe(24)+' '
        password_file = folder/'password'
        password_file.write_text(password)
        password_file.chmod(0o600)
        padded_file = folder/'padded-password'
        padded_file.write_text(padded_password)
        padded_file.chmod(0o600)
        ini = '[couchdb]\nsingle_node = true\n[admins]\nnative-admin = '+password+'\n[chttpd]\nrequire_valid_user = '+('false' if plain else 'true')+'\n'
        port = '5984' if plain else '6984'
        if not plain:
            def openssl(*args):
                subprocess.run(['openssl', *args], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            openssl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', str(folder/'ca.key'), '-out', str(tls/'ca.crt'), '-days', '2', '-subj', '/CN=NyaDB owned CouchDB CA', '-addext', 'basicConstraints=critical,CA:TRUE', '-addext', 'keyUsage=critical,keyCertSign,cRLSign')
            openssl('req', '-newkey', 'rsa:2048', '-nodes', '-keyout', str(tls/'server.key'), '-out', str(folder/'server.csr'), '-subj', '/CN=localhost')
            ext = folder/'extensions'
            ext.write_text('basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:localhost,IP:127.0.0.1\nauthorityKeyIdentifier=keyid,issuer\nsubjectKeyIdentifier=hash\n')
            openssl('x509', '-req', '-in', str(folder/'server.csr'), '-CA', str(tls/'ca.crt'), '-CAkey', str(folder/'ca.key'), '-CAcreateserial', '-CAserial', str(folder/'ca.srl'), '-days', '2', '-sha256', '-extfile', str(ext), '-out', str(tls/'server.crt'))
            ini += '[ssl]\nenable = true\ncert_file = /owned-tls/server.crt\nkey_file = /owned-tls/server.key\n'
            for leaf in tls.iterdir():
                leaf.chmod(0o444 if leaf.suffix == '.crt' else 0o400)
                if os.getuid() == 0:
                    os.chown(leaf, 5984, 5984)
        ini_path = config/'10-owned.ini'
        ini_path.write_text(ini)
        ini_path.chmod(0o600)
        if os.getuid() == 0:
            os.chown(ini_path, 5984, 5984)
            os.chown(config, 5984, 5984)
        args = ['--mount', 'type=bind,src='+str(config)+',dst=/opt/couchdb/etc/local.d']
        if not plain:
            args += ['--mount', 'type=bind,src='+str(tls)+',dst=/owned-tls,readonly']
        try:
            subprocess.run(['docker', 'image', 'inspect', IMAGE], check=True, stdout=subprocess.DEVNULL)
            identifier = subprocess.check_output(['docker', 'create', '--name', 'nyadb-couchdb-it-'+nonce[:12], '--label', 'nyadb.fixture=couchdb', '--label', 'nyadb.fixture.nonce='+nonce, '-p', '127.0.0.1::'+port, *args, IMAGE], text=True).strip()
            evidence['id'] = identifier
            subprocess.run(['docker', 'start', identifier], check=True, stdout=subprocess.DEVNULL)
            inspected = json.loads(subprocess.check_output(['docker', 'inspect', identifier], text=True))[0]
            bindings = inspected['NetworkSettings']['Ports'][port+'/tcp']
            if not inspected['State']['Running'] or len(bindings) != 1 or bindings[0]['HostIp'] != '127.0.0.1':
                raise RuntimeError('Fixture did not publish a single loopback port')
            endpoint = ('http' if plain else 'https')+'://127.0.0.1:'+bindings[0]['HostPort']
            context = None if plain else ssl.create_default_context(cafile=str(tls/'ca.crt'))
            headers = {'Authorization': 'Basic '+base64.b64encode(('native-admin:'+password).encode()).decode(), 'Content-Type': 'application/json'}
            def request(method, path, body=None):
                raw = None if body is None else json.dumps(body, ensure_ascii=False).encode()
                with urllib.request.urlopen(urllib.request.Request(endpoint+path, data=raw, method=method, headers=headers), context=context, timeout=10) as response:
                    return json.load(response)
            deadline = time.monotonic()+120
            while True:
                try:
                    identity = request('GET', '/')
                    session = request('GET', '/_session')
                    if identity.get('version') == VERSION and session.get('userCtx', {}).get('name') == 'native-admin':
                        break
                except Exception:
                    pass
                if time.monotonic() >= deadline:
                    raise RuntimeError('Native CouchDB readiness failed; inspect captured server log')
                time.sleep(1)
            request('PUT', '/nyadb_fixture')
            request('PUT', '/nyadb_fixture/_security', {'admins': {'names': [], 'roles': []}, 'members': {'names': [], 'roles': []}})
            padded_name = ' 原生用户🐱 '
            user_id = 'org.couchdb.user:'+padded_name
            request('PUT', '/_users/'+urllib.parse.quote(user_id, safe=''), {'_id': user_id, 'name': padded_name, 'type': 'user', 'roles': [], 'password': padded_password})
            admin = {'endpoint': endpoint, 'auth': 'basic', 'username': 'native-admin', 'password_file': str(password_file), 'ca_file': '' if plain else str(tls/'ca.crt'), 'server_name': '' if plain else 'localhost'}
            probe = dict(admin)
            if plain:
                probe.update(auth='none', username='', password_file='')
            padded = dict(admin, username=padded_name, password_file=str(padded_file))
            for name, value in [('probe', probe), ('admin', admin), ('padded', padded)]:
                path = folder/(name+'.json')
                path.write_text(json.dumps(value)+'\n')
                path.chmod(0o600)
            env = dict(os.environ, NYADB_COUCHDB_IT='1', NYADB_COUCHDB_CONFIG=str(folder/'probe.json'), NYADB_COUCHDB_ADMIN_CONFIG=str(folder/'admin.json'), NYADB_COUCHDB_PADDED_CONFIG=str(folder/'padded.json'))
            if profile:
                env.update(NYADB_COUCHDB_PROFILE_IT='1', NYADB_COUCHDB_PROFILE_IMAGES=str(output/('profile-images-'+nonce)))
            if workbench:
                env.update(NYADB_COUCHDB_WORKBENCH_IT='1', NYADB_COUCHDB_WORKBENCH_IMAGES=str(output/('workbench-images-'+nonce)))
            hashes = source_hashes()
            logfile = output/('native-'+nonce+'.log')
            command = native_test_command(profile, race, workbench)
            with logfile.open('w') as log:
                result = subprocess.run(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=1260)
            if hashes != source_hashes():
                raise RuntimeError('Verification sources changed during acceptance; rerun on stable sources')
            if result.returncode:
                raise RuntimeError('Native CouchDB acceptance failed; inspect '+str(logfile.relative_to(ROOT)))
            check_acceptance(logfile.read_text(), profile, session, workbench)
            if profile:
                images = sorted(Path(env['NYADB_COUCHDB_PROFILE_IMAGES']).glob('*.png'))
                expected = {language+'-'+str(width)+'-'+auth+'-'+page+'.png' for language in ['zh-Hans', 'en'] for width in [900, 720] for auth in ['none', 'basic'] for page in ['general', 'ssl']}
                if {p.name for p in images} != expected:
                    raise RuntimeError('Expected all 16 localized connection form images')
                evidence['headless_form_images'] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in images}
            if workbench:
                images = sorted(Path(env['NYADB_COUCHDB_WORKBENCH_IMAGES']).rglob('*.png'))
                expected = {language+'/'+view+'-'+str(width)+'x'+str(height)+'.png' for language in ['zh-Hans', 'en'] for width, height in [(1280, 800), (720, 520)] for view in ['definition', 'rest']}
                base = Path(env['NYADB_COUCHDB_WORKBENCH_IMAGES'])
                if {str(p.relative_to(base)) for p in images} != expected:
                    raise RuntimeError('Expected all 8 localized native workbench images')
                evidence['headless_workbench_images'] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in images}
            evidence.update(status='passed_with_race' if race else 'passed', observed_identity=identity, source_hashes=hashes, log=str(logfile.relative_to(ROOT)), log_sha256=hashlib.sha256(logfile.read_bytes()).hexdigest(), required_passes=REQUIRED+(PROFILE_REQUIRED if profile else [])+(SESSION_REQUIRED if session else [])+(WORKBENCH_REQUIRED if workbench else []))
            print('Native CouchDB '+VERSION+' '+evidence['mode']+' acceptance passed', flush=True)
        finally:
            if identifier:
                logs = subprocess.run(['docker', 'logs', identifier], capture_output=True, text=True)
                text = logs.stdout+logs.stderr
                for secret in [password, padded_password]:
                    text = text.replace(secret, '[redacted]')
                lines = [('[redacted sensitive startup line]' if re.search(r'password|token|authorization|credential|secret', line, re.I) else line) for line in text.splitlines()]
                (output/('server-'+nonce+'.log')).write_text('\n'.join(lines)+'\n')
                remove_owned(identifier, nonce)
                evidence['fixture_cleaned'] = True
            evidence_path.write_text(json.dumps(evidence, indent=2)+'\n')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--plain', action='store_true')
    parser.add_argument('--race', action='store_true')
    parser.add_argument('--profile', action='store_true', help='Verify private URI/storage/vault/editor and native original checks')
    parser.add_argument('--session', action='store_true', help='Require native session, qualified metadata, REST lease and recovery acceptance')
    parser.add_argument('--workbench', action='store_true', help='Verify public provider, URI, chooser, metadata definitions, REST workflow and recovery (includes profile and session)')
    options = parser.parse_args()
    verify(options.plain, options.race, options.profile, options.session, options.workbench)
