#!/usr/bin/env python3
"""Verify Qdrant REST on a uniquely owned official Docker node, with native TLS."""
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
IMAGE = 'qdrant/qdrant@sha256:9bfc89f9914fc14876a88c05270685921a55b8877b254a98bcc1cc2f0a424cd2'
VERSION = '1.20.0'
MODES = ('anonymous', 'api-key', 'tls', 'mtls')
REQUIRED = (
    'AuthenticationVersionAndPermissions',
    'NamedSparseMultiVectorAndAliases',
    'ExactIDsScrollQueryCountAndCursor',
    'AdmissionCompletionAndNativeFailure',
    'LostResponseNeverReplays',
    'CanceledOriginalReadAndClose',
    'PointBatchExactModesNativeFiltersAndMixedOperations',
    'PointBatchNormalizationAndNativePartialFailure',
    'PointBatchReadOnlyDenial',
    'PointBatchLostCanceledAndOriginalCloseNeverReplays',
)
PROFILE_REQUIRED = (
    'ProfileURIReopenAndOriginalEditorCheck',
    'ProfileProxyContextAndCanceledOriginalCheck',
    'EditorLocalizedFormEvidence',
)
SESSION_REQUIRED = (
    'SessionMetadataAndDefinitions',
    'SessionRequestOutcomesAndExactResponse',
    'SessionLostResponseExplicitLeaseRecovery',
    'SessionCanceledOriginalLeaseAndSessionClose',
    'SessionBinaryObservationDriftRejectsDispatch',
    'SessionNonJSONAndSinkFailureDoNotReplay',
    'SessionTypedPointsAndExactRawEnvelopes',
    'SessionTypedPostReadLossIsInterruptedWithoutReplay',
)
PUBLIC_REQUIRED = (
    'PublicProviderMetadataAndPublishedGenerations',
    'PublicReadOnlyProviderDenialAndOriginalRecovery',
    'RESTLostWriteCancelAndExplicitRecovery',
    'PublicChooserURINavigationDefinitionsRESTAndRecovery',
)

DATA_REQUIRED = (
    'DataExactTypedPointsCursorFilterAndOriginalFields',
    'DataCompleteExportBeyondUIPageLimit',
    'DataLargePointsExplicitBoundsAndFullExport',
    'DataDeletedCursorBoundaryAndObservationChanges',
    'DataReadPermissionsAndDeniedWrites',
    'DataWritesExactMixedReplacementDeletionAndSnapshots',
    'DataWritesVectorDatatypesNormalizationAndPayloadClearing',
    'DataWritesCompletedBatchSkippedUpdateRetainsDrafts',
    'DataWritesLostCanceledAndCloseNeverReplays',
    'DataLostCanceledReadAndOriginalClose',
    'DataSharedGridPagingSelectorInspectorParkGenerationAndClose',
    'DataSharedGridFullPointCRUDParkedSaveVerificationEvidenceAndGeneration',
    'DataImportExactModesLargeSourceNativeRetrievalAndReplacement',
    'DataImportRacedModeSkipAndReadOnlyRetainOriginalEvidence',
    'DataImportLostCanceledCloseAndReadbackFaultsNeverReplay',
    'DataPublicPointImportWizardJSONJSONLModesEvidenceParkAndGeneration',
)


def remove_owned(identifier, nonce):
    if not isinstance(identifier, str) or not re.fullmatch('[0-9a-f]{64}', identifier):
        raise RuntimeError('Invalid immutable fixture ID')
    result = subprocess.run(['docker', 'inspect', identifier], capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError('Cannot inspect owned Qdrant fixture')
    records = json.loads(result.stdout)
    if len(records) != 1:
        raise RuntimeError('Ambiguous fixture inspection')
    record = records[0]
    labels = record['Config'].get('Labels') or {}
    if record['Id'] != identifier or labels.get('nyadb.fixture') != 'qdrant' or labels.get('nyadb.fixture.nonce') != nonce:
        raise RuntimeError('Fixture ownership changed; refuse cleanup')
    subprocess.run(['docker', 'rm', '-f', '-v', identifier], check=True, stdout=subprocess.DEVNULL)


def source_hashes(profile=False):
    paths = list((ROOT/'internal/services/qdrant').glob('*.go'))
    paths += list((ROOT/'internal/db/jsondocument').glob('*.go'))
    paths += list((ROOT/'internal/services/connections').glob('qdrant*.go'))
    paths += [ROOT/'go.mod', ROOT/'go.sum', ROOT/'tools/verify_qdrant_fixture.py', ROOT/'tests/test_qdrant_fixture.py']
    if profile:
        paths += list((ROOT/'internal').rglob('*.go'))
        paths += list((ROOT/'locales').glob('*.go')) + list((ROOT/'locales').glob('*.json'))
    return {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(paths)}


def check_acceptance(log, profile=False, session=False, public=False, data=False):
    session = session or public or data
    for name in REQUIRED + (PROFILE_REQUIRED if profile or session else ()) + (SESSION_REQUIRED if session else ()) + (PUBLIC_REQUIRED if public else ()) + (DATA_REQUIRED if data else ()):
        if not re.search(r'^--- PASS: TestQdrantNative'+name+r' \(', log, re.M):
            raise RuntimeError('Missing required native PASS: '+name)
    if re.search(r'^\s*--- (SKIP|FAIL):', log, re.M) or 'WARNING: DATA RACE' in log:
        raise RuntimeError('Unexpected skipped, failed or racing native test')


def native_test_command(race, profile=False, session=False, public=False, data=False):
    session = session or public or data
    profile = profile or session
    exclusions = [name for name, enabled in [('Session',session),('Public',public),('Data',data)] if not enabled]
    skip = '^TestQdrantNative'+(exclusions[0] if len(exclusions)==1 else '('+'|'.join(exclusions)+')') if exclusions else None
    return ['go', 'test', '-count=1', '-timeout=5m', '-v', *(['-race'] if race else []),
            *(['-skip', skip] if skip else []),
            '-run', '^TestQdrant' if profile else '^TestQdrantNative', './internal/services/qdrant',
            *(['./internal/services/connections', './internal/storage/localdb', './internal/ui/connectioneditor'] if profile else []),
            *(['./internal/services/dbruntime', './internal/ui/sqlworkspace', './internal/ui/shell'] if public else []),
            *(['./internal/ui/dataworkspace'] if data else [])]


def verify(mode='tls', race=False, profile=False, session=False, public=False, data=False):
    session = session or public or data
    profile = profile or session
    if mode not in MODES:
        raise ValueError('Invalid native fixture mode')
    output = ROOT/'build/qdrant'
    output.mkdir(parents=True, exist_ok=True)
    nonce = uuid.uuid4().hex
    identifier = None
    evidence_path = output/('owned-fixture-'+nonce+'.json')
    evidence = dict(nonce=nonce, image=IMAGE, requested_version=VERSION, mode=mode,
                    scope='original_shared_point_grid_crud_json_jsonl_import_explicit_native_modes_signed_snapshots_receipts_bounded_all_source_readback_no_replay_cursor_filter_count_fields_and_export' if data else 'public_chooser_uri_original_sessions_collections_definitions_rest_typed_points_and_recovery' if public else 'private_original_session_metadata_rest_leases_and_typed_native_point_results' if session else 'private_native_profile_uri_storage_vault_editor_and_original_connection_checks' if profile else 'private_native_rest_transport_and_collection_point_codecs',
                    status='failed', fixture_cleaned=False, private_files_cleaned=False)
    try:
        with tempfile.TemporaryDirectory(prefix='owned-'+nonce+'-', dir=output) as private:
            folder = Path(private)
            tls = mode in ('tls', 'mtls')
            admin_key, readonly_key = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
            for name, value in [('admin-key', admin_key), ('readonly-key', readonly_key)]:
                path = folder/name
                path.write_text(value)
                path.chmod(0o600)
            server = dict(telemetry_disabled=True, service=dict(host='0.0.0.0', http_port=6333,
                          grpc_port=None, enable_tls=tls, verify_https_client_certificate=mode == 'mtls'))
            if mode != 'anonymous':
                server['service'].update(api_key=admin_key, read_only_api_key=readonly_key)
            if tls:
                def openssl(*args):
                    subprocess.run(['openssl', *args], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                openssl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', str(folder/'ca.key'),
                        '-out', str(folder/'ca.crt'), '-days', '2', '-subj', '/CN=NyaDB owned Qdrant CA',
                        '-addext', 'basicConstraints=critical,CA:TRUE', '-addext', 'keyUsage=critical,keyCertSign,cRLSign')
                for name, usage in [('server', 'serverAuth'), ('client', 'clientAuth')]:
                    openssl('req', '-newkey', 'rsa:2048', '-nodes', '-keyout', str(folder/(name+'.key')),
                            '-out', str(folder/(name+'.csr')), '-subj', '/CN='+('localhost' if name == 'server' else 'owned-client'))
                    extension = folder/(name+'.ext')
                    extension.write_text('basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\n'
                                         'extendedKeyUsage='+usage+'\n'+('subjectAltName=DNS:localhost,IP:127.0.0.1\n' if name == 'server' else ''))
                    openssl('x509', '-req', '-in', str(folder/(name+'.csr')), '-CA', str(folder/'ca.crt'),
                            '-CAkey', str(folder/'ca.key'), '-CAcreateserial', '-CAserial', str(folder/'ca.srl'),
                            '-days', '2', '-sha256', '-extfile', str(extension), '-out', str(folder/(name+'.crt')))
                server['tls'] = dict(cert='/owned/server.crt', key='/owned/server.key', ca_cert='/owned/ca.crt')
            # JSON is valid YAML. Mount private configuration read-only; secrets
            # are never command-line/environment arguments or application profiles.
            config_path = folder/'server.yaml'
            config_path.write_text(json.dumps(server)+'\n')
            for path in folder.iterdir():
                path.chmod(0o600)
            subprocess.run(['docker', 'image', 'inspect', IMAGE], check=True, stdout=subprocess.DEVNULL)
            identifier = subprocess.check_output([
                'docker', 'create', '--name', 'nyadb-qdrant-it-'+nonce[:12],
                '--label', 'nyadb.fixture=qdrant', '--label', 'nyadb.fixture.nonce='+nonce,
                '-p', '127.0.0.1::6333', '--mount', 'type=bind,src='+str(folder)+',dst=/owned,readonly',
                IMAGE, './qdrant', '--config-path', '/owned/server.yaml'], text=True).strip()
            evidence['id'] = identifier
            try:
                subprocess.run(['docker', 'start', identifier], check=True, stdout=subprocess.DEVNULL)
                inspected = json.loads(subprocess.check_output(['docker', 'inspect', identifier], text=True))[0]
                bindings = inspected['NetworkSettings']['Ports']['6333/tcp']
                if not inspected['State']['Running'] or len(bindings) != 1 or bindings[0]['HostIp'] != '127.0.0.1':
                    raise RuntimeError('Fixture did not publish one loopback port')
                endpoint = ('https' if tls else 'http')+'://127.0.0.1:'+bindings[0]['HostPort']
                context = ssl.create_default_context(cafile=str(folder/'ca.crt')) if tls else None
                if mode == 'mtls':
                    context.load_cert_chain(str(folder/'client.crt'), str(folder/'client.key'))
                headers = {} if mode == 'anonymous' else {'api-key': admin_key}
                deadline = time.monotonic()+90
                while True:
                    try:
                        request = urllib.request.Request(endpoint+'/', headers=headers)
                        with urllib.request.urlopen(request, context=context, timeout=5) as response:
                            identity = json.load(response)
                        if identity.get('version') == VERSION:
                            break
                    except Exception:
                        pass
                    if time.monotonic() >= deadline:
                        raise RuntimeError('Native Qdrant readiness failed; inspect captured server log')
                    time.sleep(0.5)
                evidence['observed_identity'] = identity
                if profile:
                    def seed(method, path, body):
                        raw = json.dumps(body, ensure_ascii=False).encode()
                        request = urllib.request.Request(endpoint+path, data=raw, method=method,
                                                         headers=dict(headers, **{'Content-Type': 'application/json'}))
                        with urllib.request.urlopen(request, context=context, timeout=10) as response:
                            result = json.load(response)
                        if result.get('status') != 'ok':
                            raise RuntimeError('Native profile seed did not complete')
                    collection = 'owned_profile_'+nonce[:12]
                    seed('PUT', '/collections/'+collection, {'vectors': {'size': 2, 'distance': 'Dot'}})
                    seed('PUT', '/collections/'+collection+'/points?wait=true',
                         {'points': [{'id': 9007199254740993, 'vector': [1, 0],
                                      'payload': {'integer': 9007199254740993, 'text': '原生配置🐱'}}]})
                probe = dict(endpoint=endpoint, auth='none' if mode == 'anonymous' else 'api_key',
                             key_file='' if mode == 'anonymous' else str(folder/'admin-key'),
                             readonly_key_file='' if mode == 'anonymous' else str(folder/'readonly-key'),
                             ca_file=str(folder/'ca.crt') if tls else '', server_name='localhost' if tls else '',
                             client_cert_file=str(folder/'client.crt') if mode == 'mtls' else '',
                             client_key_file=str(folder/'client.key') if mode == 'mtls' else '')
                if profile:
                    probe.update(fixture_collection=collection, connect_timeout_seconds=12, request_timeout_seconds=45)
                probe_path = folder/'probe.json'
                probe_path.write_text(json.dumps(probe)+'\n')
                probe_path.chmod(0o600)
                env = dict(os.environ, NYADB_QDRANT_IT='1', NYADB_QDRANT_CONFIG=str(probe_path), CGO_ENABLED='1' if race else '0')
                if profile:
                    env.update(NYADB_QDRANT_PROFILE_IT='1', NYADB_QDRANT_PROFILE_IMAGES=str(output/('profile-images-'+nonce)))
                    readonly_probe = dict(probe, key_file=str(folder/'readonly-key'))
                    readonly_path = folder/'readonly-probe.json'
                    readonly_path.write_text(json.dumps(readonly_probe)+'\n')
                    readonly_path.chmod(0o600)
                    env['NYADB_QDRANT_READONLY_CONFIG'] = str(readonly_path)
                if session:
                    env['NYADB_QDRANT_SESSION_IT'] = '1'
                if public:
                    env.update(NYADB_QDRANT_PUBLIC_IT='1', NYADB_QDRANT_PUBLIC_IMAGES=str(output/('public-images-'+nonce)))
                if data:
                    env.update(NYADB_QDRANT_DATA_IT='1', NYADB_QDRANT_DATA_IMAGES=str(output/('data-images-'+nonce)), NYADB_QDRANT_IMPORT_IMAGES=str(output/('import-images-'+nonce)))
                hashes = source_hashes(profile)
                logfile = output/('native-'+nonce+'.log')
                command = native_test_command(race, profile, session, public, data)
                with logfile.open('w') as log:
                    result = subprocess.run(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=330)
                if hashes != source_hashes(profile):
                    raise RuntimeError('Acceptance sources changed during verification')
                if result.returncode:
                    raise RuntimeError('Native Qdrant acceptance failed; inspect '+str(logfile.relative_to(ROOT)))
                check_acceptance(logfile.read_text(), profile, session, public, data)
                if profile:
                    image_root = Path(env['NYADB_QDRANT_PROFILE_IMAGES'])
                    images = sorted(image_root.glob('*.png'))
                    expected = {language+'-'+str(width)+'-'+auth+'-'+page+'.png' for language in ['zh-Hans', 'en']
                                for width in [900, 720] for auth in ['none', 'api_key'] for page in ['general', 'advanced', 'ssl']}
                    if {p.name for p in images} != expected:
                        raise RuntimeError('Missing localized native Qdrant profile form evidence')
                    evidence['headless_profile_images'] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in images}
                if public:
                    image_root = Path(env['NYADB_QDRANT_PUBLIC_IMAGES'])
                    images = sorted(image_root.rglob('*.png'))
                    expected = {language+'/'+panel+'-'+str(width)+'x'+str(height)+'.png'
                                for language in ['zh-Hans', 'en'] for width,height in [(1280,800),(720,520)]
                                for panel in ['definition','aliases','points','rest-admission']}
                    if {str(p.relative_to(image_root)) for p in images} != expected:
                        raise RuntimeError('Missing localized native Qdrant workbench evidence')
                    evidence['headless_workbench_images'] = {str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in images}
                if data:
                    image_root = Path(env['NYADB_QDRANT_DATA_IMAGES'])
                    images = sorted(image_root.glob('*.png'))
                    expected = {language+'-'+str(width)+'x'+str(height)+'-'+panel+'.png'
                                for language in ['zh-Hans','en'] for width,height in [(1280,800),(720,520)]
                                for panel in ['grid','filter','write','write-evidence']}
                    if {p.name for p in images} != expected:
                        raise RuntimeError('Missing localized original Qdrant shared point grid evidence')
                    evidence['headless_data_images'] = {str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in images}
                    imports=sorted(Path(env['NYADB_QDRANT_IMPORT_IMAGES']).glob('*.png'))
                    expected_imports={language+'-'+str(width)+'x'+str(height)+'-import-'+phase+'.png' for language in ['zh-Hans','en'] for width,height in [(1280,800),(720,520)] for phase in ['source','mode','review','receipt','evidence','verified']}
                    if {p.name for p in imports} != expected_imports:
                        raise RuntimeError('Missing localized original point-import wizard evidence')
                    evidence['headless_import_images']={str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in imports}

                evidence.update(status='passed_with_race' if race else 'passed', source_hashes=hashes,
                                required_passes=list(REQUIRED + (PROFILE_REQUIRED if profile else ()) + (SESSION_REQUIRED if session else ()) + (PUBLIC_REQUIRED if public else ()) + (DATA_REQUIRED if data else ())), command=command, log=str(logfile.relative_to(ROOT)),
                                log_sha256=hashlib.sha256(logfile.read_bytes()).hexdigest())
            finally:
                logs = subprocess.run(['docker', 'logs', identifier], capture_output=True, text=True)
                # Configured keys and private file paths are not public diagnostics.
                text = (logs.stdout+logs.stderr).replace(admin_key, '[redacted]').replace(readonly_key, '[redacted]').replace(str(folder), '[private]')
                (output/('server-'+nonce+'.log')).write_text(text)
                remove_owned(identifier, nonce)
                evidence['fixture_cleaned'] = True
        evidence['private_files_cleaned'] = True
    finally:
        evidence['private_files_cleaned'] = not ('folder' in locals() and folder.exists())
        evidence_path.write_text(json.dumps(evidence, ensure_ascii=False, indent=2)+'\n')
    print(json.dumps(dict(report=str(evidence_path.relative_to(ROOT)), status=evidence['status'], mode=mode)))
    return evidence_path


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--mode', choices=MODES, default='tls')
    parser.add_argument('--race', action='store_true')
    parser.add_argument('--profile', action='store_true', help='Verify URI, SQLite reopen, separate vault, native connection checks and localized editor forms')
    parser.add_argument('--session', action='store_true', help='Verify original session, physical collection metadata and REST lease outcomes/cancellation/recovery; includes profile acceptance')
    parser.add_argument('--public', action='store_true', help='Verify public chooser/URI, original runtime generations, qualified navigation/definitions and REST workbench/recovery; includes sessions/profiles')
    parser.add_argument('--data', action='store_true', help='Verify original shared point data lease, cursor/filter/count/bounded fields/complete export and localized grid; includes sessions/profiles')
    options = parser.parse_args()
    verify(options.mode, options.race, options.profile, options.session, options.public, options.data)
