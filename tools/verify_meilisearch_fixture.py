#!/usr/bin/env python3
"""Verify Meilisearch native task/search transport on a unique owned Docker node."""
import argparse
import hashlib
import ipaddress
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
PUBLIC_REQUIRED = ['PublicProviderMetadataAndPublishedGenerations', 'RESTLostWriteCancelAndExplicitRecovery', 'PublicChooserURINavigationDefinitionsRESTAndRecovery', 'PublicOriginalDocumentParkedReadAndClose']
PUBLIC_SCOPED = 'PublicScopedWorkbenchPermissions'
DOCUMENT_REQUIRED = ['DocumentsEnumerationBeyondSearchLimitAndExactIDs', 'DocumentsReadInterruptionsCancelCloseAndNoReplay']
DOCUMENT_SCOPED = 'DocumentsScopedKeyPermissions'
DATA_REQUIRED = ['DataQualifiedOffsetPagingFingerprintsAndCompleteExport', 'DataReadCancelCloseIndependentSlotsAndSessionLifecycle', 'DataExportInterruptionsAndObservedConcurrentChanges']
DATA_SCOPED = 'DataScopedPermissionsAndIndexObservationChanges'
SEARCH_REQUIRED = ['SearchOriginalSourcesFacetsSortingAndBoundedExport', 'SearchErrorsAndOriginalRequestLifecycle', 'PublicNativeSearchGridHistoryAndCompleteExport']
SEARCH_SCOPED = 'SearchScopedPermissionsWithOriginalDocuments'
TASK_REQUIRED = ['TaskCatalogPagingFiltersAndOriginalEvidence', 'TaskManagement200AdmissionAndCompletedCounts', 'TaskREST200AdmissionAndSynchronous', 'TaskCatalogReadCancelCloseAndMutationNoReplay']
TASK_SCOPED = 'TaskCatalogScopedPermissionsAndRevocation'
TASK_PANEL_REQUIRED = ['TaskDocumentLeasesCursorsOriginalRecordsAndSessionClose', 'TaskDocumentReadInterruptionsAndNoReplay', 'PublicTaskPanelNavigationFiltersJSONAndWorkspace', 'PublicTaskPanelParkedReadsCancelCloseAndRecovery']
TASK_PANEL_SCOPED = 'PublicTaskPanelScopedPermissions'
SETTINGS_REQUIRED = ['SettingsDeltaAdmissionObserveResetAndRecovery', 'SettingsConflictFailureAndIndexReplacement', 'SettingsLostCancelCloseNoReplay', 'PublicSettingsEditingPreviewAdmissionResetAndWorkspace', 'PublicSettingsParkedCancellationAndRecovery']
SETTINGS_SCOPED = ['SettingsScopedPermissions', 'PublicSettingsScopedPermissions']
TASK_MANAGEMENT_REQUIRED = ['TaskDocumentManagementAdmissionCountsAndRecovery', 'TaskDocumentManagementLostCancelCloseNoReplay', 'PublicTaskManagementPreviewAdmissionObservationAndWorkspace', 'PublicTaskManagementParkedReceiptCancelAndRecovery']
TASK_MANAGEMENT_SCOPED = 'TaskDocumentManagementScopedPermissions'
KEYS_COMMON = ['KeysConfiguredIndexScopeAndLeaseLimit', 'KeysPublishedGenerationsScopeAndRecovery']
KEYS_ANONYMOUS = ['KeysAnonymousMissingMaster']
KEYS_AUTHENTICATED = ['KeysPagingRedactionAndSynchronousCRUD', 'KeysNativePolicyRefusalsAndDelegatedPermissions', 'KeysLostCancelCloseAndRecoveryNoReplay', 'KeysReadLossCancelCloseAndReplacement']
KEY_PANEL_COMMON = ['PublicKeysReadParkingCancelAndRecovery', 'PublicKeysWorkspaceRestorationIsExplicit']
KEY_PANEL_ANONYMOUS = ['PublicKeysAnonymousRefusalAndScope']
KEY_PANEL_AUTHENTICATED = ['PublicKeysSynchronousCRUDSecretAndSnapshot', 'PublicKeysUnknownWriteParkingCancelAndRecovery', 'PublicKeysDelegatedPermissionsAndConfiguredScope']
DATA_WRITES_REQUIRED = ['DataWritesAdmissionExactReplacementAndDeletion', 'DataWritesTaskFailureRetainsOriginalReceipt', 'DataWritesLostCancelCloseNoReplay', 'DataWritesObservationCancelAndOriginalClose', 'PublicDataWriteGridAdmissionObservationAndEvidence']
DATA_WRITES_SCOPED = 'DataWritesScopedPermissionsAndObservation'
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


def source_hashes(profile=False, session=False, public=False, documents=False, data=False, search=False, tasks=False, task_panel=False, settings=False, task_management=False, keys=False, key_panel=False, data_writes=False):
    keys = keys or key_panel
    task_panel = task_panel or task_management
    public = public or search or task_panel or settings or key_panel or data_writes
    tasks = tasks or task_panel or settings
    paths = sorted((ROOT/'internal/services/meilisearch').glob('*.go')) + sorted((ROOT/'internal/services/connections').glob('*.go')) + [ROOT/'go.mod', ROOT/'go.sum', Path(__file__).resolve(), ROOT/'tests/test_meilisearch_fixture.py']
    if profile or session or public or keys:
        paths = sorted((ROOT/'internal').rglob('*.go')) + sorted((ROOT/'locales').glob('*.go')) + sorted((ROOT/'locales').glob('*.json')) + [ROOT/'go.mod', ROOT/'go.sum', Path(__file__).resolve(), ROOT/'tests/test_meilisearch_fixture.py']
    return {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in paths}


def required_passes(mode, profile=False, session=False, public=False, documents=False, data=False, search=False, tasks=False, task_panel=False, settings=False, task_management=False, keys=False, key_panel=False, data_writes=False):
    if mode not in MODES:
        raise ValueError('Invalid native fixture mode')
    keys = keys or key_panel
    task_panel = task_panel or task_management
    public = public or search or task_panel or settings or key_panel or data_writes
    tasks = tasks or task_panel or settings
    data = data or public
    profile, session, documents = profile or public, session or public or data, documents or data
    return REQUIRED + ([] if mode == 'anonymous' else [SCOPED]) + (PROFILE_REQUIRED if profile else []) + ((SESSION_REQUIRED + ([] if mode == 'anonymous' else [SESSION_SCOPED])) if session else []) + ((PUBLIC_REQUIRED + ([] if mode == 'anonymous' else [PUBLIC_SCOPED])) if public else []) + ((DOCUMENT_REQUIRED + ([] if mode == 'anonymous' else [DOCUMENT_SCOPED])) if documents else []) + ((DATA_REQUIRED + ([] if mode == 'anonymous' else [DATA_SCOPED])) if data else []) + ((SEARCH_REQUIRED + ([] if mode == 'anonymous' else [SEARCH_SCOPED])) if search else []) + ((TASK_REQUIRED + ([] if mode == 'anonymous' else [TASK_SCOPED])) if tasks else []) + ((TASK_PANEL_REQUIRED + ([] if mode == 'anonymous' else [TASK_PANEL_SCOPED])) if task_panel else []) + ((SETTINGS_REQUIRED + ([] if mode == 'anonymous' else SETTINGS_SCOPED)) if settings else []) + ((TASK_MANAGEMENT_REQUIRED + ([] if mode == 'anonymous' else [TASK_MANAGEMENT_SCOPED])) if task_management else []) + ((KEYS_COMMON + (KEYS_ANONYMOUS if mode == 'anonymous' else KEYS_AUTHENTICATED)) if keys else []) + ((KEY_PANEL_COMMON + (KEY_PANEL_ANONYMOUS if mode == 'anonymous' else KEY_PANEL_AUTHENTICATED)) if key_panel else []) + ((DATA_WRITES_REQUIRED + ([] if mode == 'anonymous' else [DATA_WRITES_SCOPED])) if data_writes else [])


def test_command(mode, race, profile=False, session=False, public=False, documents=False, data=False, search=False, tasks=False, task_panel=False, settings=False, task_management=False, keys=False, key_panel=False, data_writes=False):
    keys = keys or key_panel
    task_panel = task_panel or task_management
    public = public or search or task_panel or settings or key_panel or data_writes
    tasks = tasks or task_panel or settings
    selected = '^TestMeilisearchNative(' + '|'.join(required_passes(mode, profile, session, public, documents, data, search, tasks, task_panel, settings, task_management, keys, key_panel, data_writes)) + ')$'
    return ['go', 'test', '-count=1', '-timeout=10m', '-v', *(['-race'] if race else []), '-run', selected, './internal/services/meilisearch', *(['./internal/ui/connectioneditor'] if profile or public else []), *(['./internal/services/dbruntime', './internal/ui/sqlworkspace', './internal/ui/shell'] if public else ['./internal/services/dbruntime'] if keys else [])]


def check_acceptance(log, mode, profile=False, session=False, public=False, documents=False, data=False, search=False, tasks=False, task_panel=False, settings=False, task_management=False, keys=False, key_panel=False, data_writes=False):
    for name in required_passes(mode, profile, session, public, documents, data, search, tasks, task_panel, settings, task_management, keys, key_panel, data_writes):
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


def verify(mode='tls', race=False, profile=False, session=False, public=False, documents=False, data=False, search=False, tasks=False, task_panel=False, settings=False, task_management=False, keys=False, key_panel=False, data_writes=False):
    keys = keys or key_panel
    task_panel = task_panel or task_management
    public = public or search or task_panel or settings or key_panel or data_writes
    tasks = tasks or task_panel or settings
    data = data or public
    profile, session, documents = profile or public, session or public or data, documents or data
    required_passes(mode, profile, session, public, documents, data, search, tasks, task_panel, settings, task_management, keys, key_panel, data_writes)
    output = ROOT/'build/meilisearch'
    output.mkdir(parents=True, exist_ok=True)
    nonce = uuid.uuid4().hex
    identifier = None
    evidence_path = output/('owned-fixture-'+nonce+'.json')
    evidence = dict(nonce=nonce, image=IMAGE, requested_version=VERSION, mode=mode, scope='public_original_shared_grid_native_async_admission_explicit_observation_actual_readback_and_no_replay' if data_writes else 'public_original_keys_pages_synchronous_crud_temporary_secrets_permissions_parked_results_and_recovery' if key_panel else 'private_native_keys_original_synchronous_receipts_permissions_secret_redaction_and_recovery' if keys else 'public_selected_original_uid_task_management_preview_admission_explicit_observation_and_recovery' if task_management else 'public_native_settings_delta_preview_original_admission_observation_and_reset' if settings else 'public_native_task_documents_pages_details_observation_and_original_recovery' if task_panel else 'private_native_task_catalog_original_observations_and_explicit_uid_admission' if tasks else 'public_native_search_original_documents_bounded_tabular_and_complete_jsonl' if search else 'public_readonly_document_grid_and_complete_export' if public else 'private_readonly_data_document_leases_and_complete_export' if data else 'public_workbench_and_private_document_read_codecs' if public and documents else 'private_native_document_listing_and_id_reads' if documents else 'public_chooser_uri_sessions_indexes_definitions_rest_recovery' if public else 'private_original_session_metadata_rest_leases' if session else ('private_profile_uri_vault_editor' if profile else 'private_native_transport_tasks_search'), status='failed', fixture_cleaned=False, private_files_cleaned=False)
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
                settings_options = []
                if settings:
                    network = json.loads(subprocess.check_output(['docker', 'network', 'inspect', 'bridge'], text=True))
                    if len(network) != 1 or len(network[0]['IPAM']['Config']) != 1:
                        raise RuntimeError('Owned model fixture requires a unique bridge gateway')
                    gateway = ipaddress.IPv4Address(network[0]['IPAM']['Config'][0]['Gateway'])
                    settings_options = ['--experimental-allowed-ip-networks', str(gateway)+'/32']
                    evidence['owned_model_allowed_network'] = str(gateway)+'/32'
                identifier = subprocess.check_output(['docker', 'create', '--name', 'nyadb-meili-it-'+nonce[:12], '--label', 'nyadb.fixture=meilisearch', '--label', 'nyadb.fixture.nonce='+nonce, '-p', '127.0.0.1::7700', '--mount', 'type=bind,src='+str(folder)+',dst=/owned,readonly', IMAGE, '/bin/meilisearch', '--config-file-path', '/owned/config.toml', *settings_options], text=True).strip()
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
                fixture_networks = list(inspected['NetworkSettings']['Networks'].values())
                if len(fixture_networks) != 1 or not fixture_networks[0].get('Gateway'):
                    raise RuntimeError('Owned fixture has no unique Docker bridge gateway')
                probe['fixture_gateway'] = fixture_networks[0]['Gateway']
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
                if data_writes:
                    env['NYADB_MEILISEARCH_WRITE_IMAGES'] = str(output/('data-write-images-'+nonce))
                if keys:
                    env['NYADB_MEILISEARCH_KEYS_IT'] = '1'
                if key_panel:
                    env['NYADB_MEILISEARCH_KEY_PANEL_IMAGES'] = str(output/('key-panel-images-'+nonce))
                if profile:
                    env.update(NYADB_MEILISEARCH_PROFILE_IT='1', NYADB_MEILISEARCH_SCOPED_CONFIG=str(folder/'scoped-probe.json') if key else '', NYADB_MEILISEARCH_PROFILE_IMAGES=str(output/('profile-images-'+nonce)))
                if session:
                    env['NYADB_MEILISEARCH_SESSION_IT'] = '1'
                if public:
                    env.update(NYADB_MEILISEARCH_PUBLIC_IT='1', NYADB_MEILISEARCH_PUBLIC_IMAGES=str(output/('public-images-'+nonce)))
                if documents:
                    env['NYADB_MEILISEARCH_DOCUMENT_IT'] = '1'
                if data:
                    env['NYADB_MEILISEARCH_DATA_IT'] = '1'
                if task_panel:
                    env['NYADB_MEILISEARCH_TASK_IMAGES'] = str(output/('task-images-'+nonce))
                if task_management:
                    env['NYADB_MEILISEARCH_TASK_MANAGEMENT_IMAGES'] = str(output/('task-management-images-'+nonce))
                if settings:
                    env['NYADB_MEILISEARCH_SETTINGS_IMAGES'] = str(output/('settings-images-'+nonce))
                if search:
                    env['NYADB_MEILISEARCH_SEARCH_IMAGES'] = str(output/('search-images-'+nonce))
                hashes = source_hashes(profile, session, public, documents, data, search, tasks, task_panel, settings, task_management, keys, key_panel, data_writes)
                logfile = output/('native-'+nonce+'.log')
                command = test_command(mode, race, profile, session, public, documents, data, search, tasks, task_panel, settings, task_management, keys, key_panel, data_writes)
                with logfile.open('w') as log:
                    result = subprocess.run(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=630)
                if hashes != source_hashes(profile, session, public, documents, data, search, tasks, task_panel, settings, task_management, keys, key_panel, data_writes):
                    raise RuntimeError('Verification sources changed during acceptance; rerun on stable sources')
                if result.returncode:
                    raise RuntimeError('Native acceptance failed; inspect '+str(logfile.relative_to(ROOT)))
                check_acceptance(logfile.read_text(), mode, profile, session, public, documents, data, search, tasks, task_panel, settings, task_management, keys, key_panel, data_writes)
                if profile:
                    images = sorted(Path(env['NYADB_MEILISEARCH_PROFILE_IMAGES']).glob('*.png'))
                    expected = {language+'-'+str(width)+'-'+auth+'-'+tab+'.png' for language in ['zh-Hans','en'] for width in [900,720] for auth in ['none','bearer'] for tab in ['general','advanced','ssl']}
                    if {p.name for p in images} != expected:
                        raise RuntimeError('Missing localized general/advanced/SSL form evidence')
                    evidence['headless_form_images'] = {str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in images}
                if public:
                    images = sorted(Path(env['NYADB_MEILISEARCH_PUBLIC_IMAGES']).rglob('*.png'))
                    expected = {language+'/'+view+'-'+str(width)+'x'+str(height)+'.png' for language in ['zh-Hans','en'] for width,height in [(1280,800),(720,520)] for view in ['definition','rest-admission','documents']}
                    if {str(p.relative_to(Path(env['NYADB_MEILISEARCH_PUBLIC_IMAGES']))) for p in images} != expected:
                        raise RuntimeError('Missing localized native public workbench images')
                    evidence['headless_workbench_images'] = {str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in images}
                if search:
                    images = sorted(Path(env['NYADB_MEILISEARCH_SEARCH_IMAGES']).rglob('*.png'))
                    expected = {language+'/'+view+'-'+str(width)+'x'+str(height)+'.png' for language in ['zh-Hans','en'] for width,height in [(1280,800),(720,520)] for view in ['search','search-response']}
                    if {str(p.relative_to(Path(env['NYADB_MEILISEARCH_SEARCH_IMAGES']))) for p in images} != expected:
                        raise RuntimeError('Missing localized native search/response workbench images')
                    evidence['headless_search_images'] = {str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in images}
                if task_panel:
                    images = sorted(Path(env['NYADB_MEILISEARCH_TASK_IMAGES']).rglob('*.png'))
                    expected = {language+'/'+view+'-'+str(width)+'x'+str(height)+'.png' for language in ['zh-Hans','en'] for width,height in [(1280,800),(720,520)] for view in ['tasks','task-detail','task-response','task-filters']}
                    if {str(p.relative_to(Path(env['NYADB_MEILISEARCH_TASK_IMAGES']))) for p in images} != expected:
                        raise RuntimeError('Missing localized native task panel evidence')
                    evidence['headless_task_images'] = {str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in images}
                if settings:
                    image_root = Path(env['NYADB_MEILISEARCH_SETTINGS_IMAGES'])
                    images = sorted(image_root.rglob('*.png'))
                    expected = {language+'/'+view+'-'+str(width)+'x'+str(height)+'.png' for language in ['zh-Hans','en'] for width,height in [(1280,800),(720,520)] for view in ['settings-current','settings-draft','settings-preview','settings-receipt','settings-reset']}
                    if {str(p.relative_to(image_root)) for p in images} != expected:
                        raise RuntimeError('Missing localized native settings preview/receipt/reset evidence')
                    evidence['headless_settings_images'] = {str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in images}
                if task_management:
                    image_root = Path(env['NYADB_MEILISEARCH_TASK_MANAGEMENT_IMAGES'])
                    images = sorted(image_root.rglob('*.png'))
                    expected = {language+'/'+view+'-'+str(width)+'x'+str(height)+'.png' for language in ['zh-Hans','en'] for width,height in [(1280,800),(720,520)] for view in ['task-management-preview','task-management-receipt','task-management-result','task-management-guard']}
                    if {str(p.relative_to(image_root)) for p in images} != expected:
                        raise RuntimeError('Missing localized native task management preview/receipt/result/leave evidence')
                    evidence['headless_task_management_images'] = {str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in images}
                if key_panel:
                    image_root = Path(env['NYADB_MEILISEARCH_KEY_PANEL_IMAGES'])
                    images = sorted(image_root.rglob('*.png'))
                    phases = ['list', 'create', 'preview', 'result', 'guard', 'recovered']
                    if mode != 'anonymous':
                        phases += ['details', 'delete']
                    expected = {language+'/keys-'+view+'-'+str(width)+'x'+str(height)+'.png' for language in ['zh-Hans', 'en'] for width,height in [(1280,800),(720,520)] for view in phases}
                    if {str(p.relative_to(image_root)) for p in images} != expected:
                        raise RuntimeError('Missing localized native key catalog/draft/preview/receipt/leave evidence')
                    evidence['headless_key_panel_images'] = {str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in images}
                if data_writes:
                    image_root = Path(env['NYADB_MEILISEARCH_WRITE_IMAGES'])
                    images = sorted(image_root.rglob('*.png'))
                    expected = {language+'/data-write-'+phase+'-'+str(width)+'x'+str(height)+'.png' for language in ['zh-Hans','en'] for width,height in [(1280,800),(720,520)] for phase in ['draft','admitted','evidence','saved','failed','guard']}
                    if {str(p.relative_to(image_root)) for p in images} != expected:
                        raise RuntimeError('Missing localized shared-grid async write evidence')
                    evidence['headless_data_write_images'] = {str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in images}
                evidence.update(status='passed_with_race' if race else 'passed', observed_identity=identity, source_hashes=hashes, command=command, log=str(logfile.relative_to(ROOT)), log_sha256=hashlib.sha256(logfile.read_bytes()).hexdigest(), required_passes=required_passes(mode, profile, session, public, documents, data, search, tasks, task_panel, settings, task_management, keys, key_panel, data_writes))
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
    parser.add_argument('--public', action='store_true', help='Verify public chooser, saved profiles, qualified metadata, REST admission and explicit recovery; implies profile, session and data')
    parser.add_argument('--documents', action='store_true', help='Verify bounded native document enumeration, exact IDs, readonly errors and original read cancellation/recovery; no public grid claims')
    parser.add_argument('--search', action='store_true', help='Verify native search options/response, original source grid, bounded filtered export and complete JSONL; implies public')
    parser.add_argument('--data', action='store_true', help='Verify private readonly document leases, signed original offsets/rows, independent reads and complete export; implies documents and session')
    parser.add_argument('--tasks', action='store_true', help='Verify native live task pages, complete original observations, HTTP200 cancellation/deletion admission, permissions and no replay; no public management panel claims')
    parser.add_argument('--task-panel', action='store_true', help='Verify public qualified/global task panel, full JSON, native cursors, explicit observation/recovery and localized views; implies public and tasks')
    parser.add_argument('--settings', action='store_true', help='Verify public native settings JSON editing, recursive previews, original task admission/observation, conflict, reset and explicit recovery; implies public and tasks')
    parser.add_argument('--task-management', action='store_true', help='Verify selected original UID cancellation/deletion previews, admission/counts, permissions, explicit observation, parked receipts and recovery; implies task panel and public')
    parser.add_argument('--keys', action='store_true', help='Verify private native key pagination/redaction, global scope, original synchronous CRUD/permissions and recovery without replay; no public key panel claims')
    parser.add_argument('--key-panel', action='store_true', help='Verify public global key pages, native synchronous CRUD, original UID previews, temporary secrets, permission refusals, parked results and explicit recovery; implies keys and public')
    parser.add_argument('--data-writes', action='store_true', help='Verify shared-grid native async write admission, explicit task observation/readback, original snapshots, scoped permissions and lost/canceled writes; implies public')
    options = parser.parse_args()
    verify(options.mode, options.race, options.profile, options.session, options.public, options.documents, options.data, options.search, options.tasks, options.task_panel, options.settings, options.task_management, options.keys, options.key_panel, options.data_writes)
