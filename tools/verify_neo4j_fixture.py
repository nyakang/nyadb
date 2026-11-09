#!/usr/bin/env python3
"""Accept native Neo4j stages against a uniquely owned official Neo4j node."""
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
    'PropertyListsStorageTypesAndNativeComparison',
    'PropertyExtendedStorageTypesAndNativeComparison',
)
PROFILE_REQUIRED = ('ProfileURIReopenAndOriginalEditorCheck', 'ProfileOriginalCanceledHandshake', 'EditorLocalizedFormEvidence')
SESSION_REQUIRED = ('SessionQualifiedMetadataAndOriginalDatabase', 'SessionTypedQueriesCompletionAndExplicitRecovery', 'SessionLostCanceledAndCloseNeverReplay', 'SessionBlockedCancellationAcknowledgesOriginalExecutor', 'PropertyWritesTypedLocksConflictsAndNativeReceipts', 'PropertyPresenceWritesAbsentLocksTypedAdditionAndRemoval', 'PropertyListsTypedLocksReceiptsAndEmptyPresence', 'PropertyExtendedTypedLocksReceiptsAndComponents')

PUBLIC_REQUIRED = ("PublicRuntimeQualifiedGraphAndGeneration", "PublicCypherLostCanceledAndOriginalCloseNeverReplay", "PublicChooserReopenNavigationCypherAndLocalizedResults", "PublicCypherNodePropertiesOriginalRowsSnapshotsAndExport", "PublicCypherGraphViewOriginalRecordsSnapshotsImagesAndCleanup", "PublicGraphNeighborsOriginalRecordsSnapshotsBoundsAndGeneration", "PublicGraphNeighborsLostCanceledOriginalCloseNeverReplay", "PublicGraphPropertyWritesOriginalRecordsSnapshotsConflictsAndVerification", "PublicGraphPropertyLostCanceledOriginalCloseAndRecoveryNeverReplay", "PublicGraphPropertyNeighborObservationOrderAndContinuedEditing", "PublicGraphPropertyPresenceOriginalUIConflictsSnapshotsAndGeneration", "PublicGraphPropertyListsOriginalUIReceiptsSnapshotsAndGeneration", "PublicGraphPropertyExtendedOriginalUIComponentsSnapshotsAndGeneration", "PublicGraphPropertyExtendedLostCanceledOriginalCloseAndRecoveryNeverReplay")
DATA_REQUIRED = ('DataQualifiedNodesRelationshipsAndCursor', 'DataOriginalFingerprintsFieldsAndExport', 'DataCancellationOriginalCloseAndNoReplay', 'DataWriteExplicitTransactionsLockComparisonRollbackAndCommit', 'DataWritePropertiesTypedAtomicReceiptsAndOldRows', 'DataWriteBlockedOriginalComparisonCancellationAndErrors')
ENTITY_REQUIRED = ('DataEntityCreationDeletionAndDetachEvidence', 'DataEntityAtomicRollbackAndBoundedDetach', 'DataEntityLostCanceledOriginalCloseNeverReplay', 'DataEntityLocksAndConcurrentChanges')
DATA_REQUIRED += ENTITY_REQUIRED
ENTITY_PREVIEW_REQUIRED = ('DataEntityPreviewCapturedTypesEdgesAndOneAttempt', 'DataEntityPreviewChangedObservationRollsBackCapturedBatch', 'DataEntityPreviewBoundsLocksCancellationAndClose', 'DataEntityPreviewLostCanceledOriginalCloseNeverReplay')
DATA_REQUIRED += ENTITY_PREVIEW_REQUIRED
WRITE_REQUIRED = ('DataWriteExplicitTransactionsLockComparisonRollbackAndCommit', 'DataWritePropertiesTypedAtomicReceiptsAndOldRows', 'DataWriteBlockedOriginalComparisonCancellationAndErrors')
GRID_REQUIRED = ('PublicDataSharedGridPagingInspectorExportParkGenerationAndClose', 'PublicDataLostCanceledOriginalCloseAndExplicitReopen', 'PublicDataPropertyColumnsAndOriginalFields', 'PublicDataPropertyEditingTypedReceiptsParkConflictsAndContinuedEdits', 'PublicDataCommitReplyLostCanceledOriginalCloseNeverReplay')

ENTITY_GRID_REQUIRED = ('PublicDataEntityCreationTypedOmittedAndPropertylessConfirmedIdentity', 'PublicDataEntityDeleteChoiceBlockedAndAdjacentBatchConfirmed', 'PublicDataEntityPreviewChangedObservationRollsBackOriginalBatch', 'PublicDataEntityCommitReplyLostCanceledOriginalCloseNeverReplay', 'PublicDataEntityPreviewParkGenerationCancelAndOriginalClose')
ENTITY_GRID_IMAGE_STAGES = ('entity-create-typed-draft', 'entity-create-typed-preview', 'entity-create-continued-receipt', 'entity-create-propertyless-draft', 'entity-delete-choice', 'entity-delete-plain-blocked', 'entity-delete-detach-preview', 'entity-delete-detach-receipt', 'entity-preview-conflict', 'entity-commit-unknown', 'entity-commit-unknown-close', 'entity-preview-parked')

RELATIONSHIP_REQUIRED = ('DataRelationshipCreationTypedDirectedParallelAndSelfLoop', 'DataRelationshipEndpointChangesBoundsAndAtomicRollback', 'DataRelationshipCreationLostCanceledOriginalCloseNeverReplay', 'DataRelationshipEndpointReadAndLockCancellationOriginalClose')
RELATIONSHIP_GRID_REQUIRED = ('PublicDataRelationshipCreationTypedDirectedParallelAndSelfLoop', 'PublicDataRelationshipEndpointChangesAndAtomicRollback', 'PublicDataRelationshipCommitReplyLostCanceledOriginalCloseNeverReplay', 'PublicDataRelationshipEndpointReadPreviewParkGenerationCancelAndClose')
RELATIONSHIP_GRID_IMAGE_STAGES = ('relationship-endpoint-input', 'relationship-endpoint-observed', 'relationship-typed-draft', 'relationship-typed-preview', 'relationship-continued-receipt', 'relationship-propertyless-draft', 'relationship-endpoint-conflict', 'relationship-commit-unknown', 'relationship-commit-unknown-close', 'relationship-endpoint-parked')


EXTENDED_LIST_KINDS = ('date', 'local_time', 'local_datetime', 'time', 'datetime', 'duration', 'point2d', 'point3d')
EXTENDED_LIST_REQUIRED = (
    'PropertyListsStorageTypesAndNativeComparison', 'PropertyListsTypedLocksReceiptsAndEmptyPresence',
    'PropertyExtendedStorageTypesAndNativeComparison', 'PropertyExtendedTypedLocksReceiptsAndComponents',
    'PropertyExtendedListsStorageTypesAndNativeComparison', 'PropertyExtendedListsTypedLocksReceiptsAndComponents',
) + WRITE_REQUIRED + (
    'DataEntityCreationDeletionAndDetachEvidence', 'DataRelationshipCreationTypedDirectedParallelAndSelfLoop',
    'PublicGraphPropertyExtendedOriginalUIComponentsSnapshotsAndGeneration',
    'PublicGraphPropertyExtendedLostCanceledOriginalCloseAndRecoveryNeverReplay',
    'PublicGraphPropertyExtendedListsOriginalUIComponentsSnapshotsAndGeneration',
    'PublicGraphPropertyExtendedListsLostCanceledOriginalCloseAndRecoveryNeverReplay',
) + GRID_REQUIRED + (
    'PublicDataExtendedListsTypedTransactionsCreationConflictsAndOriginalLease',
    'PublicDataExtendedListsCommitReplyLostCanceledOriginalCloseNeverReplay',
)
EXTENDED_LIST_PUBLIC_STAGES = tuple(prefix+'-'+kind for prefix in ('extended', 'extended-list') for kind in EXTENDED_LIST_KINDS) + ('extended-conflict', 'extended-retired', 'extended-list-conflict', 'extended-list-retired')
EXTENDED_LIST_GRID_STAGES = tuple('extended-list-'+object_kind+'-'+kind+'-editor' for object_kind in ('label', 'relationship') for kind in EXTENDED_LIST_KINDS) + tuple('extended-list-'+object_kind+'-'+stage for object_kind in ('label', 'relationship') for stage in ('draft', 'receipt', 'creation-preview', 'conflict')) + ('extended-list-uncertain-commit', 'extended-list-uncertain-close')

GRAPH_IMPORT_NATIVE_REQUIRED = ('GraphImportCompleteTypedNodesDirectedRelationshipsAndExchange', 'GraphImportAnchorConflictAndOriginalPlanOwnership', 'GraphImportLaterChunkFailureRollsBackCompleteBatch', 'GraphImportActualCommitReplyLostCancelAndOriginalCloseNeverReplay')

GRAPH_IMPORT_REQUIRED = GRAPH_IMPORT_NATIVE_REQUIRED + WRITE_REQUIRED + GRID_REQUIRED + ('PublicGraphImportFileMappingTypedPreviewCommitAndDatabaseExchange', 'PublicGraphImportActualCommitFaultsRetainSourcePreviewAndNeverReplay', 'PublicGraphImportPreviewParkGenerationCancelAndOriginalClose')
GRAPH_IMPORT_IMAGE_STAGES = ('graph-import-source', 'graph-import-mapping', 'graph-import-preview', 'graph-import-receipt', 'graph-export-database', 'graph-import-unknown', 'graph-import-unknown-close', 'graph-import-preview-parked')

GRAPH_NAMESPACE_REQUIRED = ('GraphNamespaceEmptyExchangeImportAndOriginalRuntime', 'PublicGraphNamespaceEmptyDatabaseEntryImportExchangeTabsAndOriginalGeneration', 'PublicGraphNamespaceCommitFaultsAndOriginalCloseNeverReplay', 'PublicGraphNamespacePreviewParkGenerationCancelAndOriginalClose') + GRAPH_IMPORT_REQUIRED
GRAPH_NAMESPACE_GRID_STAGES = ('namespace-unknown', 'namespace-unknown-close', 'namespace-preview-parked')
GRAPH_NAMESPACE_SHELL_STAGES = ('namespace-entry', 'namespace-empty-export', 'namespace-import-source', 'namespace-import-preview', 'namespace-import-receipt', 'namespace-export', 'namespace-restored', 'namespace-stale')


STRUCTURED_PLAN_REQUIRED = ('AuthenticationIdentityAndTransport', 'StructuredPlansExplainProfileArgumentsAndOriginalReceipt', 'StructuredPlansBoundsAndCompletionOwnership', 'PublicStructuredPlansLostCanceledOriginalCloseAndRecoveryNeverReplay', 'PublicStructuredPlansOriginalUIRowsSnapshotsExportAndGeneration', 'PublicStructuredPlansWorkbenchEntryAndLocalizedViews')
STRUCTURED_PLAN_STAGES = ('plan-explain', 'plan-profile', 'plan-native', 'plan-query-results', 'plan-snapshot', 'plan-stale', 'plan-workbench', 'plan-workbench-native')


def required_cases(profile=False, session=False, public=False, data=False, grid=False, grid_writes=False, entity_native=False, entity_preview=False, entity_grid=False, relationship_native=False, relationship_grid=False, extended_lists=False, graph_import_native=False, graph_import=False, graph_namespace=False, structured_plans=False):
    if structured_plans:
        return STRUCTURED_PLAN_REQUIRED
    if graph_namespace:
        return GRAPH_NAMESPACE_REQUIRED
    if graph_import:
        return GRAPH_IMPORT_REQUIRED
    if graph_import_native:
        return GRAPH_IMPORT_NATIVE_REQUIRED
    if extended_lists:
        return EXTENDED_LIST_REQUIRED
    public = public or grid
    data = data or grid
    session = session or public or data
    profile = profile or session
    return RELATIONSHIP_REQUIRED + WRITE_REQUIRED + ENTITY_REQUIRED + ENTITY_PREVIEW_REQUIRED + GRID_REQUIRED + ENTITY_GRID_REQUIRED + RELATIONSHIP_GRID_REQUIRED if relationship_grid else RELATIONSHIP_REQUIRED + WRITE_REQUIRED + ENTITY_REQUIRED + ENTITY_PREVIEW_REQUIRED if relationship_native else WRITE_REQUIRED + ENTITY_REQUIRED + ENTITY_PREVIEW_REQUIRED + GRID_REQUIRED + ENTITY_GRID_REQUIRED if entity_grid else WRITE_REQUIRED + ENTITY_REQUIRED + ENTITY_PREVIEW_REQUIRED if entity_preview else ENTITY_REQUIRED if entity_native else WRITE_REQUIRED + GRID_REQUIRED if grid_writes else REQUIRED + (PROFILE_REQUIRED if profile else ()) + (SESSION_REQUIRED if session else ()) + (PUBLIC_REQUIRED if public else ()) + (DATA_REQUIRED if data else ()) + (GRID_REQUIRED if grid else ())


def check_stage_images(pictures, stages, public=False):
    required = {language+'-'+size+'-'+stage+'.png' for language in ('zh-Hans', 'en') for size in ('1280x800', '720x520') for stage in stages}
    actual = {p.name for p in pictures}
    if public:
        required = {language+'/'+stage+'-'+size+'.png' for language in ('zh-Hans', 'en') for size in ('1280x800', '720x520') for stage in stages}
        actual = {p.parent.name+'/'+p.name for p in pictures}
    if not required.issubset(actual):
        raise RuntimeError('Missing required typed property list editor/preview/receipt/lifecycle images')


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


def check_acceptance(log, profile=False, session=False, public=False, data=False, grid=False, grid_writes=False, entity_native=False, entity_preview=False, entity_grid=False, relationship_native=False, relationship_grid=False, extended_lists=False, graph_import_native=False, graph_import=False, graph_namespace=False, structured_plans=False):
    public = public or grid
    data = data or grid
    session = session or public or data
    profile = profile or session
    names = required_cases(profile, session, public, data, grid, grid_writes, entity_native, entity_preview, entity_grid, relationship_native, relationship_grid, extended_lists, graph_import_native, graph_import, graph_namespace, structured_plans)
    for name in names:
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


def verify(mode, race, profile=False, session=False, public=False, data=False, grid=False, grid_writes=False, entity_native=False, entity_preview=False, entity_grid=False, relationship_native=False, relationship_grid=False, extended_lists=False, graph_import_native=False, graph_import=False, graph_namespace=False, structured_plans=False):
    if extended_lists:
        profile, session, public, data, grid, grid_writes = False, True, True, True, True, True
        entity_native, entity_preview, entity_grid, relationship_native, relationship_grid = (False,)*5
    if relationship_grid:
        entity_grid = True
        relationship_native, entity_native, entity_preview = False, False, False
    if entity_grid:
        grid_writes = True
    if relationship_native:
        profile, session, public, data, grid, grid_writes = False, True, False, True, False, False
    elif entity_native or entity_preview:
        profile, session, public, data, grid = False, True, False, True, False
    elif grid_writes and not extended_lists:
        profile, session, public, data, grid = False, True, False, True, True
    elif not extended_lists:
        public = public or grid
        data = data or grid
        session = session or public or data
        profile = profile or session
    if graph_namespace:
        graph_import = True
    if graph_import_native:
        profile, session, public, data, grid, grid_writes = False, True, False, True, False, False
    if graph_import:
        profile, session, public, data, grid, grid_writes = False, True, True, True, True, True
    if structured_plans:
        profile, session, public, data, grid, grid_writes = False, True, True, False, False, False
    nonce = uuid.uuid4().hex
    build = ROOT/'build/neo4j'
    build.mkdir(parents=True, exist_ok=True)
    report_path = build/('owned-fixture-'+nonce+'.json')
    log_path = build/('owned-fixture-'+nonce+'.log')
    sources = list((ROOT/'internal/services/neo4j').glob('*.go')) + list((ROOT/'internal/services/connections').glob('neo4j*.go')) + [ROOT/'go.mod', ROOT/'go.sum', ROOT/'tests/test_neo4j_fixture.py', Path(__file__).resolve()]
    if profile or grid_writes:
        sources += list((ROOT/'internal/services/connections').glob('*.go')) + list((ROOT/'internal/storage/localdb').glob('*.go')) + list((ROOT/'internal/ui/connectioneditor').glob('*.go'))
        sources += list((ROOT/'locales').glob('*.go')) + list((ROOT/'locales').glob('*.json')) + [ROOT/'internal/ui/shell/connection_engine_files.go']
    if graph_import_native or graph_import:
        sources += list((ROOT/'internal/db/graphimport').glob('*.go'))
    if structured_plans:
        sources += list((ROOT/'internal/db/queryplan').glob('*.go')) + list((ROOT/'locales').glob('*.go')) + list((ROOT/'locales').glob('*.json'))
    if session:
        sources += list((ROOT/'internal/db/contract').glob('*.go'))
    if relationship_native:
        sources += list((ROOT/'internal/services/dbruntime').glob('*.go'))
    if public or grid_writes:
        for package in ('internal/db/graphresult', 'internal/services/dbruntime', 'internal/ui/shell', 'internal/ui/navigation', 'internal/ui/connectioncatalog', 'internal/ui/sqlworkspace', 'internal/ui/objectworkspace', 'internal/ui/sessionbrowser'):
            sources += list((ROOT/package).glob('*.go'))
    if grid:
        for package in ('internal/ui/dataworkspace', 'internal/ui/gridview', 'internal/ui/theme'):
            sources += list((ROOT/package).glob('*.go'))
    report = {'nonce': nonce, 'mode': mode, 'version': VERSION, 'image': IMAGE,
              'scope': 'public-original-native-structured-plans' if structured_plans else 'public-empty-database-graph-namespace-exchange' if graph_namespace else 'public-complete-graph-exchange-and-import' if graph_import else 'private-native-complete-graph-import' if graph_import_native else 'public-temporal-spatial-property-lists' if extended_lists else 'public-shared-qualified-relationship-creation' if relationship_grid else 'private-native-qualified-relationship-creation' if relationship_native else 'public-shared-graph-entity-confirmations' if entity_grid else 'private-native-entity-previews-and-property-transactions' if entity_preview else 'private-native-entity-mutations' if entity_native else 'public-shared-graph-property-write-and-close-followup' if grid_writes else 'public-shared-graph-data-and-workbench' if grid else 'public-workbench-and-private-graph-data' if public and data else 'private-original-graph-data' if data else 'public-native-workbench' if public else 'private-native-session-and-graph-contract' if session else ('private-native-profile-and-editor' if profile else 'private-native-foundation'), 'race': race, 'passed': False,
              'source_hashes': {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(set(sources))}}
    identifier = None
    try:
        with tempfile.TemporaryDirectory(prefix='nyadb-neo4j-'+nonce+'-', dir=build) as raw:
            private = Path(raw)
            secret = 'Nya_'+secrets.token_urlsafe(24)
            if profile:
                # Keep whitespace inside the env-file value so Docker preserves
                # it; native Bolt authentication must receive these exact bytes.
                secret += '_ 原始密码🐱\t _end'
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
            if profile:
                env['NYADB_NEO4J_PROFILE_IT'] = '1'
                env['NYADB_NEO4J_PROFILE_IMAGES'] = str(build/('owned-fixture-'+nonce+'-profile-images'))
            if session:
                env['NYADB_NEO4J_SESSION_IT'] = '1'
            if public:
                env['NYADB_NEO4J_PUBLIC_IT'] = '1'
                env['NYADB_NEO4J_PUBLIC_IMAGES'] = str(build/('owned-fixture-'+nonce+'-public-images'))
            if data:
                env['NYADB_NEO4J_DATA_IT'] = '1'
            if grid:
                env['NYADB_NEO4J_GRID_IT'] = '1'
                env['NYADB_NEO4J_GRID_IMAGES'] = str(build/('owned-fixture-'+nonce+'-grid-images'))
                env['NYADB_NEO4J_SHELL_GRID_IMAGES'] = str(build/('owned-fixture-'+nonce+'-shell-grid-images'))
            if structured_plans:
                env['NYADB_NEO4J_PLAN_IMAGES'] = str(build/('owned-fixture-'+nonce+'-plan-images'))
            if graph_namespace:
                env['NYADB_NEO4J_NAMESPACE_IMAGES'] = str(build/('owned-fixture-'+nonce+'-namespace-images'))
            env['CGO_ENABLED'] = '1' if race else '0'
            packages = ['./internal/services/neo4j'] + (['./internal/ui/connectioneditor'] if profile else [])
            if public:
                packages += ['./internal/services/dbruntime', './internal/ui/sqlworkspace', './internal/ui/shell']
            if grid:
                packages += ['./internal/ui/dataworkspace']
            accepted = required_cases(profile, session, public, data, grid, grid_writes, entity_native, entity_preview, entity_grid, relationship_native, relationship_grid, extended_lists, graph_import_native, graph_import, graph_namespace, structured_plans)
            pattern = '^TestNeo4jNative(' + '|'.join(accepted) + ')$'
            command = ['go', 'test'] + (['-race'] if race else []) + (['-p=1'] if graph_import or graph_import_native else []) + packages + ['-run', pattern, '-count=1', '-timeout=8m', '-v']
            commands = [command]
            if graph_namespace:
                # Each empty-database parent must execute before other graph
                # fixtures; never clear arbitrary existing data to make it pass.
                prefix = ['go', 'test'] + (['-race'] if race else []) + ['-p=1']
                commands = [prefix + [package, '-run', '^TestNeo4jNative'+name+'$', '-count=1', '-timeout=8m', '-v']
                            for package, name in (('./internal/services/neo4j', GRAPH_NAMESPACE_REQUIRED[0]),
                                                  ('./internal/ui/shell', GRAPH_NAMESPACE_REQUIRED[1]))]
                rest = accepted[2:]
                commands.append(prefix + packages + ['-run', '^TestNeo4jNative('+'|'.join(rest)+')$', '-count=1', '-timeout=8m', '-v'])
            log = ''
            for command in commands:
                result = subprocess.run(command, cwd=ROOT, env=env, capture_output=True, text=True)
                log += result.stdout+result.stderr
                if result.returncode:
                    break
            # Preserve evidence without ever retaining generated credentials.
            log = log.replace(secret, '[redacted]')
            log_path.write_text(log)
            if result.returncode:
                raise RuntimeError('Native Neo4j verification failed; see '+str(log_path.relative_to(ROOT)))
            check_acceptance(log, profile, session, public, data, grid, grid_writes, entity_native, entity_preview, entity_grid, relationship_native, relationship_grid, extended_lists, graph_import_native, graph_import, graph_namespace, structured_plans)
            if profile:
                pictures = sorted(Path(env['NYADB_NEO4J_PROFILE_IMAGES']).glob('*.png'))
                if len(pictures) != 48:
                    raise RuntimeError('Missing localized Neo4j profile screenshots')
                report['images'] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in pictures}
            if public and not graph_import and not structured_plans:
                pictures = sorted(Path(env['NYADB_NEO4J_PUBLIC_IMAGES']).rglob('*.png'))
                if len(pictures) != (4*len(EXTENDED_LIST_PUBLIC_STAGES) if extended_lists else 224):
                    raise RuntimeError('Missing localized Neo4j workbench screenshots')
                if extended_lists:
                    check_stage_images(pictures, EXTENDED_LIST_PUBLIC_STAGES, public=True)
                report['public_images'] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in pictures}
            if grid:
                for field, variable, count in [('grid_images', 'NYADB_NEO4J_GRID_IMAGES', 64 + 4*(len(GRAPH_IMPORT_IMAGE_STAGES) + (len(GRAPH_NAMESPACE_GRID_STAGES) if graph_namespace else 0)) if graph_import else 64 + 4*len(EXTENDED_LIST_GRID_STAGES) if extended_lists else 64 + 4*len(ENTITY_GRID_IMAGE_STAGES) + (4*len(RELATIONSHIP_GRID_IMAGE_STAGES) if relationship_grid else 0) if entity_grid else 64)] + ([] if grid_writes else [('shell_grid_images', 'NYADB_NEO4J_SHELL_GRID_IMAGES', 8)]):
                    pictures = sorted(Path(env[variable]).rglob('*.png'))
                    if len(pictures) != count:
                        raise RuntimeError('Missing localized public graph data screenshots: '+field)
                    if extended_lists and field == 'grid_images':
                        check_stage_images(pictures, EXTENDED_LIST_GRID_STAGES)
                    if entity_grid and field == 'grid_images':
                        required_names = {language+'-'+size+'-'+stage+'.png' for language in ('zh-Hans', 'en') for size in ('1280x800', '720x520') for stage in ENTITY_GRID_IMAGE_STAGES + (RELATIONSHIP_GRID_IMAGE_STAGES if relationship_grid else ())}
                        if not required_names.issubset({p.name for p in pictures}):
                            raise RuntimeError('Missing required public entity creation/deletion/preview/receipt/lifecycle images')
                    report[field] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in pictures}
            if graph_import:
                check_stage_images(list((build/('owned-fixture-'+nonce+'-grid-images')).glob('*.png')), GRAPH_IMPORT_IMAGE_STAGES + (GRAPH_NAMESPACE_GRID_STAGES if graph_namespace else ()))
            if graph_namespace:
                pictures = sorted(Path(env['NYADB_NEO4J_NAMESPACE_IMAGES']).glob('*.png'))
                check_stage_images(pictures, GRAPH_NAMESPACE_SHELL_STAGES)
                if len(pictures) != 4*len(GRAPH_NAMESPACE_SHELL_STAGES):
                    raise RuntimeError('Missing original empty-database graph namespace screenshots')
                report['namespace_images'] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in pictures}
            if structured_plans:
                pictures = sorted(Path(env['NYADB_NEO4J_PLAN_IMAGES']).glob('*.png'))
                check_stage_images(pictures, STRUCTURED_PLAN_STAGES)
                if len(pictures) != 4*len(STRUCTURED_PLAN_STAGES):
                    raise RuntimeError('Missing original native plan screenshots')
                report['plan_images'] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in pictures}
            report['passed'] = True
            report['accepted'] = list(accepted)
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
    parser.add_argument('--profile', action='store_true', help='Include private profiles, native editor checks and localized forms')
    parser.add_argument('--session', action='store_true', help='Include original sessions, qualified graph metadata and Cypher leases')
    parser.add_argument('--public', action='store_true', help='Include public provider, workbench, native results and original recovery')
    parser.add_argument('--data', action='store_true', help='Include private original-session graph data reads, cursors, fields, export and cancellation')
    parser.add_argument('--grid', action='store_true', help='Include the public shared node/relationship grid, original read retirement and explicit reopen')
    parser.add_argument('--grid-writes', action='store_true', help='Run native property transactions and public grid read/write/close regression cases')
    parser.add_argument('--entity-native', action='store_true', help='Probe private native entity creation/deletion, bounded detach and original transactions')
    parser.add_argument('--entity-preview', action='store_true', help='Probe private one-attempt previews, exact affected-edge comparisons and original property transactions')
    parser.add_argument('--entity-grid', action='store_true', help='Probe public node creation, explicit deletion decisions, original previews and entity commit safety')
    parser.add_argument('--relationship-native', action='store_true', help='Probe private qualified endpoint reads, directed relationship creation, original locks and commit faults')
    parser.add_argument('--relationship-grid', action='store_true', help='Probe explicit public endpoint input, shared relationship drafts, original previews, commit faults and localized lifecycle evidence')
    parser.add_argument('--extended-lists', action='store_true', help='Verify temporal/spatial lists through native transactions, Cypher editing and the sole shared grid')
    parser.add_argument('--graph-import-native', action='store_true', help='Verify complete graph exchange, explicit anchor mapping and atomic native graph import')
    parser.add_argument('--graph-import', action='store_true', help='Verify complete graph import through the original public runtime and shared transfer wizard')
    parser.add_argument('--graph-namespace', action='store_true', help='Verify the complete original database graph exchange/import entry, including truly empty databases, namespace identity and original lifecycle')
    parser.add_argument('--structured-plans', action='store_true', help='Verify original native EXPLAIN/PROFILE trees, exact metrics, receipts, shared views, snapshots, export and generation/cancel/close safety')
    args = parser.parse_args()
    verify(args.mode, args.race, args.profile, args.session, args.public, args.data, args.grid, args.grid_writes, args.entity_native, args.entity_preview, args.entity_grid, args.relationship_native, args.relationship_grid, args.extended_lists, args.graph_import_native, args.graph_import, args.graph_namespace, args.structured_plans)


if __name__ == '__main__':
    main()
