#!/usr/bin/env python3
"""Verify private Cassandra v4 transport against an owned 5.0.6 Docker node."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import signal
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
IMAGE = 'cassandra@sha256:b494afb347bfd20765d80817d89d9e6e31ae77a339bfb34e82c6ea868a51c9c1'
DATA_TESTS = ['DataPagingRowsAndExport', 'DataUUIDStaticFieldsAndSchema', 'DataCancellationAndOriginalClose', 'SharedGridPagingInspectorParkGenerationAndClose']
SESSION_TESTS = ['QualifiedSchemaAndIndependentCQL', 'CQLPagingStreamingAndAcknowledgement', 'LeaseLostCounterReplyExplicitRecovery', 'MetadataChangeQueueAndOriginalClose', 'OriginalPreconnectAndMetadataCancellation']



def source_paths(profile=False, session=False, data=False):
    sources = sorted((ROOT/'internal/services/cassandra').glob('*.go')) + [ROOT/'internal/services/connections/cassandra_config.go', ROOT/'internal/services/connections/cassandra_config_test.go', ROOT/'tools/verify_cassandra_fixture.py', ROOT/'tools/cassandra_fixture_probe/main.go', ROOT/'tests/test_cassandra_fixture.py']
    if session:
        sources += [ROOT/'internal/db/sqltext/cassandra.go', ROOT/'internal/db/sqltext/cassandra_test.go']
    if data:
        sources += sorted((ROOT/'internal/ui/dataworkspace').glob('*.go')) + [ROOT/'internal/db/contract/data_models.go'] + sorted((ROOT/'locales').glob('*.json')) + [ROOT/'locales/catalog_gen.go']
    if profile:
        sources += sorted((ROOT/'internal/services/connections').glob('*.go')) + sorted((ROOT/'internal/storage/localdb').glob('*.go')) + sorted((ROOT/'internal/ui/connectioneditor').glob('*.go')) + sorted((ROOT/'locales').glob('*.json')) + [ROOT/'locales/catalog_gen.go']
    return sorted(set(sources))


def source_snapshot(sources):
    return {str(path.relative_to(ROOT)): hashlib.sha256(path.read_bytes()).hexdigest() for path in sources}

def remove_owned(identifier, nonce):
    if not isinstance(identifier, str) or not re.fullmatch('[0-9a-f]{64}', identifier):
        raise RuntimeError('Invalid immutable fixture ID')
    result = subprocess.run(['docker', 'inspect', identifier], capture_output=True, text=True)
    if result.returncode:
        return
    record = json.loads(result.stdout)[0]
    labels = record['Config'].get('Labels') or {}
    if record['Id'] != identifier or labels.get('nyadb.fixture') != 'cassandra' or labels.get('nyadb.fixture.nonce') != nonce:
        raise RuntimeError('Fixture ownership changed; refuse cleanup')
    subprocess.run(['docker', 'rm', '-f', '-v', identifier], check=True, stdout=subprocess.DEVNULL)


def verify(plain=False, race=False, profile=False, session=False, data=False):
    session = session or data
    base, output = ROOT/'temp/cassandra', ROOT/'build/cassandra'
    base.mkdir(parents=True, exist_ok=True)
    output.mkdir(parents=True, exist_ok=True)
    nonce, identifier = uuid.uuid4().hex, None
    def shutdown(signum, _frame):
        raise SystemExit(128+signum)
    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGINT, shutdown)
    evidence = {'image': IMAGE, 'nonce': nonce, 'tls': not plain, 'race': race, 'scope': 'private_native_data_documents' if data else 'private_native_sessions_cql_metadata' if session else 'private_native_profiles_and_editor' if profile else 'private_native_transport_foundation'}
    with tempfile.TemporaryDirectory(prefix='fixture-', dir=base) as temporary:
        folder = Path(temporary)
        folder.chmod(0o700)
        server = folder/'server'
        server.mkdir(mode=0o755)
        password = folder/'password'
        password.write_text('' if plain else 'Nya'+secrets.token_hex(24)+'@9')
        password.chmod(0o600)
        bootstrap_password = folder/'bootstrap-password'
        bootstrap_password.write_text('' if plain else 'cassandra')
        bootstrap_password.chmod(0o600)
        yaml = subprocess.check_output(['docker', 'run', '--rm', '--entrypoint', 'cat', IMAGE, '/etc/cassandra/cassandra.yaml'], text=True)
        yaml = yaml.replace('cluster_name: \'Test Cluster\'', 'cluster_name: \'NyaDB owned Cassandra '+nonce+'\'')
        if session:
            # Official 5.0.6 disables MV creation by default. Enable the native
            # feature only in this disposable metadata acceptance fixture.
            yaml = yaml.replace('materialized_views_enabled: false', 'materialized_views_enabled: true')
            evidence['materialized_views_enabled'] = True
        args = ['--memory=3g', '--cpus=3', '-e', 'MAX_HEAP_SIZE=768M', '-e', 'HEAP_NEWSIZE=128M']
        if not plain:
            yaml = yaml.replace('authenticator: AllowAllAuthenticator', 'authenticator: PasswordAuthenticator')
            yaml = yaml.replace('authorizer: AllowAllAuthorizer', 'authorizer: CassandraAuthorizer')
            def openssl(*arguments):
                subprocess.run(['openssl', *arguments], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            openssl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2', '-subj', '/CN=NyaDB owned Cassandra CA', '-keyout', str(folder/'ca.key'), '-out', str(folder/'ca.crt'), '-addext', 'basicConstraints=critical,CA:TRUE', '-addext', 'keyUsage=critical,keyCertSign,cRLSign')
            openssl('req', '-new', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN=localhost', '-keyout', str(folder/'server.key'), '-out', str(folder/'server.csr'))
            ext = folder/'extensions'
            ext.write_text('basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:localhost,IP:127.0.0.1\n')
            openssl('x509', '-req', '-in', str(folder/'server.csr'), '-CA', str(folder/'ca.crt'), '-CAkey', str(folder/'ca.key'), '-CAcreateserial', '-days', '2', '-sha256', '-extfile', str(ext), '-out', str(folder/'server.crt'))
            store_password = folder/'store-password'
            store_password.write_text(secrets.token_hex(24))
            store_password.chmod(0o600)
            openssl('pkcs12', '-export', '-in', str(folder/'server.crt'), '-inkey', str(folder/'server.key'), '-certfile', str(folder/'ca.crt'), '-out', str(server/'server.p12'), '-name', 'owned-cassandra', '-passout', 'file:'+str(store_password))
            yaml = re.sub(r'(?ms)^client_encryption_options:\n.*?(?=^\S)', 'client_encryption_options:\n  enabled: true\n  optional: false\n  keystore: /owned/server.p12\n  keystore_password: '+store_password.read_text()+'\n  store_type: PKCS12\n  require_client_auth: false\n\n', yaml, count=1)
        (server/'cassandra.yaml').write_text(yaml)
        for leaf in server.iterdir():
            leaf.chmod(0o400)
            if os.getuid() == 0:
                os.chown(leaf, 999, 999)
        args += ['--mount', 'type=bind,src='+str(server)+',dst=/owned,readonly', '--entrypoint', '/bin/bash']
        cfg = {'Auth': 'none' if plain else 'password', 'Username': '' if plain else 'cassandra', 'TLSMode': 'disable' if plain else 'verify-full', 'ca_file': '' if plain else str(folder/'ca.crt'), 'password_file': str(bootstrap_password)}
        if not plain:
            cfg['fault_cert_file'] = str(folder/'server.crt')
            cfg['fault_key_file'] = str(folder/'server.key')
        config = folder/'config.json'
        logfile = output/(('data-' if data else 'session-' if session else 'profile-' if profile else '')+('plain-transport.log' if plain else 'race-transport.log' if race else 'tls-transport.log'))
        probe = folder/'probe'
        subprocess.run(['go', 'build', '-o', str(probe), './tools/cassandra_fixture_probe'], cwd=ROOT, check=True)
        try:
            identifier = subprocess.check_output(['docker', 'create', '--name', 'nyadb-cassandra-it-'+nonce[:12], '--label', 'nyadb.fixture=cassandra', '--label', 'nyadb.fixture.nonce='+nonce, '-p', '127.0.0.1::9042', *args, IMAGE, '-c', 'cp /owned/cassandra.yaml /etc/cassandra/cassandra.yaml; exec /usr/local/bin/docker-entrypoint.sh cassandra -f'], text=True).strip()
            evidence['id'] = identifier
            subprocess.run(['docker', 'start', identifier], check=True, stdout=subprocess.DEVNULL)
            inspected = json.loads(subprocess.check_output(['docker', 'inspect', identifier], text=True))[0]
            bindings = inspected['NetworkSettings']['Ports']['9042/tcp']
            if len(bindings) != 1 or bindings[0]['HostIp'] != '127.0.0.1':
                raise RuntimeError('Fixture must publish a single loopback port')
            cfg['Endpoint'] = '127.0.0.1:'+bindings[0]['HostPort']
            config.write_text(json.dumps(cfg))
            config.chmod(0o600)
            end = time.monotonic()+180
            while True:
                result = subprocess.run([str(probe), '-config', str(config)], capture_output=True, text=True, timeout=35)
                if result.returncode == 0 and result.stdout.strip() == '5.0.6':
                    break
                if time.monotonic() >= end:
                    raise RuntimeError('Native Cassandra readiness failed; inspect private server log')
                time.sleep(1)
            if not plain:
                subprocess.run([str(probe), '-config', str(config), '-new-password-file', str(password)], check=True, stdout=subprocess.DEVNULL)
            cfg['password_file'] = str(password)
            config.write_text(json.dumps(cfg))
            # Default authentication caches can still hold the bootstrap password.
            time.sleep(3)
            subprocess.run([str(probe), '-config', str(config)], check=True, stdout=subprocess.DEVNULL)
            env = dict(os.environ, NYADB_CASSANDRA_IT='1', NYADB_CASSANDRA_CONFIG=str(config))
            packages = ['./internal/services/cassandra', './internal/services/connections']
            if session:
                env['NYADB_CASSANDRA_SESSION_IT'] = '1'
                packages += ['./internal/db/sqltext']
            if data:
                packages += ['./internal/ui/dataworkspace']
                env['NYADB_CASSANDRA_DATA_IT'] = '1'
                env['NYADB_CASSANDRA_DATA_IMAGES'] = str(output/('data-images-plain' if plain else 'data-images-race' if race else 'data-images-tls'))
            if profile:
                packages += ['./internal/storage/localdb', './internal/ui/connectioneditor']
                env['NYADB_CASSANDRA_PROFILE_IT'] = '1'
                env['NYADB_CASSANDRA_PROFILE_IMAGES'] = str(output/('profile-images-plain' if plain else 'profile-images-race' if race else 'profile-images-tls'))
            # Profile/editor acceptance also creates and drops keyspaces. Keep
            # packages sequential when sharing the session fixture; the intended
            # in-test concurrent ALTER still proves schema-version rejection.
            command = ['go', 'test', '-count=1', '-timeout=5m', '-v', *(['-p', '1'] if data or session and profile else []), *(['-race'] if race else []), *(['-run', 'Cassandra|^TestData(Native|Identity|Reservation)'] if profile or data else []), *packages]
            sources = source_paths(profile, session, data)
            source_before = source_snapshot(sources)
            evidence['command'] = command
            with logfile.open('w') as log:
                result = subprocess.run(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=360)
            if result.returncode:
                raise RuntimeError('Native Cassandra acceptance failed; inspect '+str(logfile))
            required = ['IdentityTypesAndParameters', 'PagingBoundToPhysicalRequest', 'AuthenticationAndTLS', 'LostCounterReplyAndCancellation']
            if profile:
                required += ['ProfileURIReopenAndOriginalEditorCheck', 'EditorCanceledOriginalHandshake', 'EditorLocalizedFormEvidence']
            if session:
                required += SESSION_TESTS
            if data:
                required += DATA_TESTS
            text = logfile.read_text()
            if any('--- PASS: TestCassandraNative'+name not in text for name in required):
                raise RuntimeError('Missing native Cassandra PASS evidence')
            allowed_skips = set() if session else {'TestCassandraNative'+name for name in SESSION_TESTS}
            if not data:
                allowed_skips |= {'TestCassandraNative'+name for name in DATA_TESTS[:3]}
            skipped = set(re.findall(r'--- SKIP: (\S+)', text))
            if skipped - allowed_skips:
                raise RuntimeError('Unexpected native Cassandra SKIP evidence')
            evidence['version'] = '5.0.6'
            evidence['required_tests'] = required
            evidence['log_sha256'] = hashlib.sha256(logfile.read_bytes()).hexdigest()
            if data:
                image_folder = Path(env['NYADB_CASSANDRA_DATA_IMAGES'])
                expected = {lang+'-'+str(width)+'-native-shared-grid.png' for lang in ['zh-Hans','en'] for width in [1200,900]}
                images = sorted(image_folder.glob('*.png'))
                if {path.name for path in images} != expected:
                    raise RuntimeError('Missing localized Cassandra shared grid evidence')
                evidence['data_image_sha256'] = {str(path.relative_to(ROOT)): hashlib.sha256(path.read_bytes()).hexdigest() for path in images}
            if profile:
                image_folder = Path(env['NYADB_CASSANDRA_PROFILE_IMAGES'])
                expected = {language+'-'+str(width)+'-'+auth+'-'+page+'.png' for language in ['zh-Hans','en'] for width in [900,720] for auth in ['none','password'] for page in ['general','ssl']}
                images = sorted(image_folder.glob('*.png'))
                if {path.name for path in images} != expected:
                    raise RuntimeError('Missing localized Cassandra form evidence')
                evidence['image_sha256'] = {str(path.relative_to(ROOT)): hashlib.sha256(path.read_bytes()).hexdigest() for path in images}
            source_after = source_snapshot(source_paths(profile, session, data))
            if source_before != source_after:
                raise RuntimeError('Fixture sources changed during acceptance; rerun finalized sources')
            evidence['source_sha256'] = source_after
            evidence['source_unchanged_during_run'] = True
            print('Cassandra 5.0.6 '+('plain' if plain else 'TLS/password')+' native acceptance passed')
        finally:
            if identifier:
                with (output/('server-'+nonce+'.log')).open('w') as log:
                    subprocess.run(['docker', 'logs', identifier], stdout=log, stderr=subprocess.STDOUT)
                remove_owned(identifier, nonce)
                evidence['removed'] = True
            (output/('owned-fixture-'+nonce+'.json')).write_text(json.dumps(evidence, indent=2)+'\n')


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--plain', action='store_true')
    parser.add_argument('--race', action='store_true')
    parser.add_argument('--profile', action='store_true')
    parser.add_argument('--session', action='store_true')
    parser.add_argument('--data', action='store_true')
    options = parser.parse_args()
    verify(options.plain, options.race, options.profile, options.session, options.data)
