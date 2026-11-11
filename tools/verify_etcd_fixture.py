#!/usr/bin/env python3
"""Run native etcd foundation acceptance in an owned official single-node fixture."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
IMAGES = {
    '3.7.3': 'gcr.io/etcd-development/etcd@sha256:0d27b9478551f431f82d1e0ddb6c87ad07ebae8b904c637f3a99213349523910',
    '3.5.35': 'gcr.io/etcd-development/etcd@sha256:a9a88f52af136ac171fda89952280f7ec1c79c48673bd029c9e69db06f3e8feb',
}
REQUIRED = ('AuthenticationIdentityAndTransport', 'BinaryMVCCPagingAndCompaction',
            'AtomicComparisonsRenameAndConflicts', 'LeaseAttachmentKeepAliveAndExpiration',
            'ActualCommittedReplyLossCancellationAndOriginalClose',
            'OriginalReadCancellationBusyAndPreparationOwnership')


def run(args, **kwargs):
    result = subprocess.run(args, cwd=ROOT, capture_output=True, text=True, **kwargs)
    if result.returncode:
        # Command arguments/output may contain disposable credentials.
        raise RuntimeError('Owned fixture command failed: ' + args[0])
    return result.stdout.strip()


def remove_owned(identifier, nonce):
    if not isinstance(identifier, str) or not re.fullmatch(r'[0-9a-f]{64}', identifier):
        raise RuntimeError('Cleanup requires the original immutable container ID')
    inspected = subprocess.run(['docker', 'container', 'inspect', identifier], capture_output=True, text=True)
    if inspected.returncode:
        if 'No such' in (inspected.stderr or ''):
            return
        raise RuntimeError('Cannot inspect owned container before cleanup')
    try:
        records = json.loads(inspected.stdout)
        record = records[0]
        labels = record['Config']['Labels']
        valid = (len(records) == 1 and record['Id'] == identifier and
                 labels.get('nyadb.fixture') == 'etcd' and labels.get('nyadb.fixture.nonce') == nonce)
    except (ValueError, KeyError, IndexError, TypeError, AttributeError):
        valid = False
    if not valid:
        raise RuntimeError('Refusing cleanup of foreign or replaced fixture ownership')
    run(['docker', 'rm', '-f', identifier])


def certificates(folder, access=False):
    def ssl(*args):
        subprocess.run(
            ['openssl', *args], cwd=folder, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    ssl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', 'ca.key', '-out', 'ca.crt', '-days', '2', '-subj', '/CN=NyaDB owned etcd CA')
    identities = [('server', 'server', 'serverAuth'), ('client', 'root', 'clientAuth')]
    if access:
        identities.append(('access-client', 'acl-cert', 'clientAuth'))
    for name, cn, usage in identities:
        ssl('req', '-newkey', 'rsa:2048', '-nodes', '-keyout', name+'.key', '-out', name+'.csr', '-subj', '/CN='+cn)
        (folder/(name+'.ext')).write_text('extendedKeyUsage='+usage+'\nsubjectAltName=DNS:localhost,IP:127.0.0.1\n')
        ssl('x509', '-req', '-in', name+'.csr', '-CA', 'ca.crt', '-CAkey', 'ca.key', '-CAcreateserial', '-out', name+'.crt', '-days', '2', '-extfile', name+'.ext')
    for path in folder.iterdir():
        path.chmod(0o644)


def verify(mode, race, version, profile=False, session=False, workspace=False, data=False, public=False, smoke=False, access=False, access_smoke=False, watch_smoke=False, history_smoke=False, events_smoke=False, events_workspace_smoke=False, public_events=False, renewal_native=False, renewal_session=False, public_renewal=False, lease_browser_native=False, lease_browser_session=False, public_lease_browser=False, public_lease_management=False, dependency_modfile=None, auth_native=False, public_auth=False, compaction_native=False, public_compaction=False, member_native=False, member_session=False, alarm_native=False, alarm_workspace=False, public_alarms=False, snapshot_native=False, snapshot_session=False, snapshot_artifact=False, snapshot_file=False):
    if snapshot_file and any((profile, session, workspace, data, public, smoke, access, access_smoke, watch_smoke, history_smoke, events_smoke, events_workspace_smoke, public_events, renewal_native, renewal_session, public_renewal, lease_browser_native, lease_browser_session, public_lease_browser, public_lease_management, auth_native, public_auth, compaction_native, public_compaction, member_native, member_session, alarm_native, alarm_workspace, public_alarms, snapshot_native, snapshot_session, snapshot_artifact)):
        raise ValueError('Snapshot file acceptance is a separate original publication scope')
    snapshot_artifact = snapshot_artifact or snapshot_file
    if snapshot_artifact and any((profile, session, workspace, data, public, smoke, access, access_smoke, watch_smoke, history_smoke, events_smoke, events_workspace_smoke, public_events, renewal_native, renewal_session, public_renewal, lease_browser_native, lease_browser_session, public_lease_browser, public_lease_management, auth_native, public_auth, compaction_native, public_compaction, member_native, member_session, alarm_native, alarm_workspace, public_alarms, snapshot_native, snapshot_session)):
        raise ValueError('Snapshot artifact acceptance is a separate original file scope')
    snapshot_session = snapshot_session or snapshot_artifact
    if snapshot_session and any((profile, session, workspace, data, public, smoke, access, access_smoke, watch_smoke, history_smoke, events_smoke, events_workspace_smoke, public_events, renewal_native, renewal_session, public_renewal, lease_browser_native, lease_browser_session, public_lease_browser, public_lease_management, auth_native, public_auth, compaction_native, public_compaction, member_native, member_session, alarm_native, alarm_workspace, public_alarms, snapshot_native)):
        raise ValueError('Snapshot session acceptance is a separate original fixture scope')
    snapshot_native = snapshot_native or snapshot_session
    if snapshot_native and any((profile, session, workspace, data, public, smoke, access, access_smoke, watch_smoke, history_smoke, events_smoke, events_workspace_smoke, public_events, renewal_native, renewal_session, public_renewal, lease_browser_native, lease_browser_session, public_lease_browser, public_lease_management, auth_native, public_auth, compaction_native, public_compaction, member_native, member_session, alarm_native, alarm_workspace, public_alarms)):
        raise ValueError('Snapshot native acceptance is a separate original stream scope')
    if public_alarms:
        alarm_workspace = True
    if alarm_workspace:
        alarm_native = True
    if alarm_native and any((profile, workspace, data, public, smoke, access, access_smoke, watch_smoke, history_smoke, events_smoke, events_workspace_smoke, public_events, renewal_native, renewal_session, public_renewal, lease_browser_native, lease_browser_session, public_lease_browser, public_lease_management, auth_native, public_auth, compaction_native, public_compaction, member_native, member_session)):
        raise ValueError('Alarm native acceptance is a separate original session scope')
    if alarm_native:
        session = True
    if member_native and any((profile, session, workspace, data, public, smoke, access, access_smoke, watch_smoke, history_smoke, events_smoke, events_workspace_smoke, public_events, renewal_native, renewal_session, public_renewal, lease_browser_native, lease_browser_session, public_lease_browser, public_lease_management, auth_native, public_auth, compaction_native, public_compaction)):
        raise ValueError('Member native acceptance is a separate fixture scope')
    if member_session:
        member_native = session = True
    if public_compaction:
        public = compaction_native = True
    if compaction_native:
        session = True
    if public_auth:
        public = auth_native = True
    if auth_native:
        session = True
    if public_lease_management:
        public_lease_browser = True
    if public_lease_browser:
        public = lease_browser_session = True
    if public_renewal:
        public = True
    if lease_browser_session:
        session = True
    if renewal_session:
        session = True
    if public_events:
        public = True
    if events_workspace_smoke:
        workspace = True
    if events_smoke:
        session = True
    if access:
        public = True
    if access_smoke:
        session = True
    if smoke:
        public = True
    if public:
        profile = workspace = data = True
    if workspace or data:
        session = True
    if snapshot_session:
        session = True
    image = IMAGES[version]
    nonce = uuid.uuid4().hex
    build = ROOT/'build/etcd'
    build.mkdir(parents=True, exist_ok=True)
    report_path = build/('owned-fixture-'+nonce+'.json')
    log_path = report_path.with_suffix('.log')
    sources = sorted((ROOT/'internal/services/etcd').glob('*.go')) + [ROOT/'go.mod', ROOT/'go.sum', Path(__file__).resolve(), ROOT/'tests/test_etcd_fixture.py']
    if profile:
        sources += [p for folder in ('internal/services/connections', 'internal/storage/localdb', 'internal/ui/connectioneditor', 'locales') for p in (ROOT/folder).glob('*.go')]
        sources += [ROOT/'locales/en.json', ROOT/'locales/zh-Hans.json', ROOT/'internal/ui/shell/connection_engine_files.go', ROOT/'internal/services/dbruntime/providers.go', ROOT/'internal/services/dbruntime/runtime.go', ROOT/'internal/ui/connectioncatalog/catalog.go', ROOT/'internal/services/connections/uri.go']
    if session:
        sources += [p for folder in ('internal/db/contract', 'internal/services/dbruntime', 'internal/services/connections') for p in (ROOT/folder).glob('*.go')]
    if data:
        sources += [p for folder in ('internal/ui/dataworkspace', 'locales') for p in (ROOT/folder).glob('*.go')]
        sources += [ROOT/'locales/en.json', ROOT/'locales/zh-Hans.json']
    if workspace or alarm_workspace:
        sources += [p for folder in ('internal/ui/kvworkspace', 'internal/ui/shell', 'internal/bootstrap', 'locales') for p in (ROOT/folder).glob('*.go')]
        sources += [ROOT/'locales/en.json', ROOT/'locales/zh-Hans.json']
    if public or public_alarms:
        sources += [p for folder in ('internal/ui/connectioncatalog', 'internal/ui/navigation', 'internal/ui/sessionbrowser', 'internal/ui/objectworkspace', 'internal/ui/theme', 'internal/storage/localdb') for p in (ROOT/folder).glob('*.go')]
    if public_alarms:
        sources += [p for folder in ('internal/ui/dataworkspace', 'internal/ui/sqlworkspace') for p in (ROOT/folder).glob('*.go')]
    if snapshot_artifact:
        sources += sorted((ROOT/'internal/services/snapshotfiles').glob('*.go'))
    dependency_sources = [ROOT/'go.mod', ROOT/'go.sum']
    if dependency_modfile:
        selected_modfile = Path(dependency_modfile).resolve()
        selected_sumfile = selected_modfile.with_suffix('.sum')
        if not selected_modfile.is_file() or not selected_sumfile.is_file() or ROOT/'build' not in selected_modfile.parents:
            raise RuntimeError('Dependency snapshot must be an existing owned build modfile and sumfile')
        dependency_sources = [selected_modfile, selected_sumfile]
        sources = [p for p in sources if p not in (ROOT/'go.mod', ROOT/'go.sum')] + dependency_sources
    sources = sorted(set(sources))
    report = dict(nonce=nonce, scope='etcd-history-smoke' if history_smoke else ('etcd-watch-smoke' if watch_smoke else ('etcd-access-smoke' if access_smoke else ('public-etcd-access-smoke' if access and smoke else ('public-etcd-access' if access else ('public-etcd-smoke' if smoke else ('public-etcd-workbench' if public else ('kv-data-adapter-regression' if data else ('private-kv-request-workspace' if workspace else ('private-kv-session-runtime' if session else ('private-profile-editor' if profile else 'private-native-v3-foundation')))))))))), mode=mode, version=version,
                  image=image, race=race, passed=False,
                  source_hashes={str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in sources})
    if snapshot_native:
        report['scope'] = 'private-etcd-original-snapshot-stream-journal'
    if snapshot_session:
        report['scope'] = 'private-etcd-original-snapshot-session-runtime'
    identifier = None
    if snapshot_artifact:
        report['scope'] = 'private-etcd-original-snapshot-disk-artifacts'
    if snapshot_file:
        report['scope'] = 'private-etcd-original-snapshot-file-publication'
    if events_smoke:
        report['scope'] = 'private-etcd-events-session-smoke'
    if events_workspace_smoke:
        report['scope'] = 'private-etcd-events-workspace-smoke'
    if public_events:
        report['scope'] = 'public-etcd-original-key-events-history'
    if renewal_native:
        report['scope'] = 'private-etcd-original-lease-renewal'
    if renewal_session:
        report['scope'] = 'private-etcd-original-renewal-session'
    if public_renewal:
        report['scope'] = 'public-etcd-continuous-renewal-workbench'
    if lease_browser_native:
        report['scope'] = 'private-etcd-original-lease-browser'
    if lease_browser_session:
        report['scope'] = 'private-etcd-original-lease-browser-session'
    if public_lease_browser:
        report['scope'] = 'public-etcd-original-lease-browser-workbench'
    report['dependency_sources'] = [str(p.relative_to(ROOT)) for p in dependency_sources]
    if public_lease_management:
        report['scope'] = 'public-etcd-original-lease-management-workbench'
    if auth_native:
        report['scope'] = 'private-etcd-original-auth-rbac'
    if public_auth:
        report['scope'] = 'public-etcd-original-auth-management-workbench'
    if compaction_native:
        report['scope'] = 'private-etcd-original-compaction-session'
    if public_compaction:
        report['scope'] = 'public-etcd-original-compaction-workbench'
    if member_native:
        report['scope'] = 'private-etcd-original-member-native-protocol'
    if member_session:
        report['scope'] = 'private-etcd-original-member-session-runtime'
    if alarm_native:
        report['scope'] = 'private-etcd-original-alarm-list-exact-disarm-session'
    if alarm_workspace:
        report['scope'] = 'private-etcd-original-alarm-checked-batch-workspace'
    if public_alarms:
        report['scope'] = 'public-etcd-original-alarm-checked-disarm-workbench'
    private_path = None
    try:
        with tempfile.TemporaryDirectory(prefix='nyadb-etcd-'+nonce+'-', dir=build) as raw:
            private = Path(raw)
            private_path = private
            private.chmod(0o755)
            tls = mode in ('tls', 'mtls')
            secret = 'Nya_'+secrets.token_urlsafe(24)
            username = ' root '+nonce[:6]  # Exercise exact, untrimmed native names.
            if tls:
                certificates(private, access or access_smoke or auth_native or alarm_native or snapshot_native)
            scheme = 'https' if tls else 'http'
            env = ['ETCDCTL_API=3', 'ETCDCTL_ENDPOINTS='+scheme+'://localhost:2379']
            if tls:
                env += ['ETCDCTL_CACERT=/fixture/ca.crt']
            if mode == 'mtls':
                env += ['ETCDCTL_CERT=/fixture/client.crt', 'ETCDCTL_KEY=/fixture/client.key']
            (private/'environment').write_text('\n'.join(env)+'\n')
            (private/'environment').chmod(0o600)
            args = ['docker', 'run', '-d', '--name', 'nyadb-etcd-'+nonce, '--label', 'nyadb.fixture=etcd',
                    '--label', 'nyadb.fixture.nonce='+nonce,
                    '--env-file', str(private/'environment'), '-p', '127.0.0.1::2379',
                    '--mount', 'type=bind,src='+str(private)+',dst=/fixture,readonly', image,
                    '/usr/local/bin/etcd', '--name=owned', '--data-dir=/tmp/etcd-data',
                    '--listen-client-urls='+scheme+'://0.0.0.0:2379',
                    '--advertise-client-urls='+scheme+'://localhost:2379',
                    '--listen-peer-urls=http://127.0.0.1:2380', '--initial-advertise-peer-urls=http://127.0.0.1:2380',
                    '--initial-cluster=owned=http://127.0.0.1:2380', '--initial-cluster-token='+nonce,
                    '--log-level=error']
            if tls:
                args += ['--cert-file=/fixture/server.crt', '--key-file=/fixture/server.key']
            if mode == 'mtls':
                args += ['--client-cert-auth=true', '--trusted-ca-file=/fixture/ca.crt']
            identifier = run(args)
            report['container_id'] = identifier
            for _ in range(30):
                result = subprocess.run(['docker', 'exec', identifier, '/usr/local/bin/etcdctl', '--dial-timeout=1s', '--command-timeout=2s', 'endpoint', 'health'], capture_output=True, timeout=5)
                if result.returncode == 0:
                    break
                time.sleep(0.5)
            else:
                raise RuntimeError('Owned etcd did not become ready')
            def ctl(*args, authenticated=False, input=None):
                cmd = ['docker', 'exec', '-i']
                if authenticated:
                    cmd += ['-e', 'ETCDCTL_USER=root:'+secret]
                return run(cmd+[identifier, '/usr/local/bin/etcdctl', *args], input=input)
            if mode != 'anonymous':
                ctl('user', 'add', 'root', '--interactive=false', input=secret+'\n')
                ctl('user', 'grant-role', 'root', 'root')
                ctl('user', 'add', username, '--interactive=false', input=secret+'\n')
                ctl('user', 'grant-role', username, 'root')
                ctl('user', 'add', 'restricted', '--interactive=false', input=secret+'\n')
                ctl('role', 'add', 'restricted')
                ctl('role', 'grant-permission', 'restricted', 'read', '/allowed/', '--prefix=true')
                ctl('user', 'grant-role', 'restricted', 'restricted')
                if alarm_native or snapshot_native:
                    ctl('user', 'add', 'acl-cert', '--interactive=false', input=secret+'\n')
                    ctl('user', 'grant-role', 'acl-cert', 'restricted')
                ctl('auth', 'enable')
            port = run(['docker', 'port', identifier, '2379/tcp']).split(':')[-1]
            environment = dict(os.environ, GOWORK='off', GOFLAGS='-mod=readonly', CGO_ENABLED='1' if race else '0', NYADB_ETCD_NATIVE_IT='1',
                               NYADB_ETCD_ENDPOINT=scheme+'://localhost:'+port,
                               NYADB_ETCD_VERSION=version, NYADB_ETCD_MODE=mode,
                               NYADB_ETCD_USERNAME=username, NYADB_ETCD_SECRET=secret)
            if dependency_modfile:
                environment['GOFLAGS'] = '-mod=readonly -modfile='+str(selected_modfile)
            if tls:
                environment['NYADB_ETCD_CA'] = str(private/'ca.crt')
            if mode == 'mtls':
                environment.update(NYADB_ETCD_CERT=str(private/'client.crt'), NYADB_ETCD_KEY=str(private/'client.key'))
            if tls and (access or access_smoke or auth_native or alarm_native or snapshot_native):
                environment.update(NYADB_ETCD_ACCESS_CERT=str(private/'access-client.crt'), NYADB_ETCD_ACCESS_KEY=str(private/'access-client.key'),
                                   NYADB_ETCD_ROOT_CERT=str(private/'client.crt'), NYADB_ETCD_ROOT_KEY=str(private/'client.key'))
            packages = ['./internal/services/etcd']
            if profile:
                packages.append('./internal/ui/connectioneditor')
                environment.update(NYADB_ETCD_PROFILE_IT='1', NYADB_ETCD_PROFILE_IMAGES=str(report_path.with_suffix(''))+'-images')
            if session:
                packages.append('./internal/services/dbruntime')
                environment['NYADB_ETCD_SESSION_IT'] = '1'
            if workspace:
                packages += ['./internal/ui/kvworkspace', './internal/ui/shell']
                environment.update(NYADB_ETCD_WORKSPACE_IT='1', NYADB_ETCD_WORKSPACE_IMAGES=str(report_path.with_suffix(''))+'-workspace-images')
            if data:
                packages.append('./internal/ui/dataworkspace')
                environment.update(NYADB_ETCD_DATA_IT='1', NYADB_ETCD_DATA_IMAGES=str(report_path.with_suffix(''))+'-data-images')
            required = REQUIRED + (('ProfileURIReopenAndOriginalEditorCheck', 'ProfileVaultFailurePreservesOriginalConnection', 'ProfileOriginalCanceledHandshakeAndLateCheck', 'ProfileParkedSuccessDoesNotEnterReplacementDraft', 'EditorLocalizedFormEvidence') if profile else ())
            if session:
                required += ('SessionOriginalScopePurePlansAndExactReplies', 'SessionMVCCAndNativePermissionBoundary', 'SessionActualProcessedReplyFaultsAndRecovery', 'SessionOriginalReadBusyCancellationAndClose', 'SessionLateHandshakeClosesOnlyOriginalHandle', 'SessionLeaseLimitsAndRevokedTokenRecovery', 'RuntimeOriginalPublicationSnapshotsAndExplicitRecovery', 'RuntimeActualProcessedPutFaultAndGenerationReplacement', 'RuntimeLateOriginalHandshakeCannotCloseReplacement')
            if workspace:
                required += ('WorkspaceMethodsPureConfirmationAndExactReceipts', 'WorkspaceMVCCAndDeniedRequestRemainExplicit', 'WorkspaceLargeClipboardFilesAndCapturedSave', 'WorkspaceActualProcessedFaultRecoveryReadbackAndLeave', 'WorkspaceParkedOriginalCompletionAndStalePreview', 'WorkspaceLocalizedNativeControlsAndCompletePreview', 'ShellPublicKVDocumentsParkDisconnectRecoverAndOriginalClose')
            if data:
                required += ('DataOriginalBinaryMVCCCountAndFieldScopes', 'DataCompactionRetiresOriginalRangeAndExplicitReopen', 'DataAtomicEditsExactReceiptsAndPreservedLease', 'DataPreviewConflictsRollBackWholeOriginalBatch', 'DataFixedRevisionLosslessExportAndMaximumKey', 'DataActualTxnReceiptFaultCancelAndOriginalClose', 'DataAuxiliaryCancellationKeepsOriginalPageScope', 'DataSharedGridOriginalPreviewParkingRefreshAndGeneration', 'DataLargePagesUseBoundedRPCsAndOriginalRevision', 'DataPartialPageCancelAndClosePublishNoRows', 'DataLargeOmittedFieldsPreserveNativeValueAndLease')
            if public:
                environment.update(NYADB_ETCD_PUBLIC_IT='1', NYADB_ETCD_PUBLIC_IMAGES=str(report_path.with_suffix(''))+'-public-images')
                required += ('ShellPublicChooserReopenQualifiedGridAtomicMutationsAndGuards', 'ShellPublicGridConflictKeepsOriginalAtomicBatch', 'ShellPublicGridOneMiBLeaseOnlyPreservesCompleteBytes', 'ShellPublicGridActualTxnLossCancelAndOriginalCloseNeverReplay',)
            if smoke:
                packages = ['./internal/ui/shell', './internal/ui/dataworkspace']
                required = ('ShellPublicKVDocumentsParkDisconnectRecoverAndOriginalClose', 'ShellPublicChooserReopenQualifiedGridAtomicMutationsAndGuards', 'ShellPublicGridConflictKeepsOriginalAtomicBatch', 'ShellPublicGridOneMiBLeaseOnlyPreservesCompleteBytes', 'ShellPublicGridActualTxnLossCancelAndOriginalCloseNeverReplay', 'DataSharedGridOriginalPreviewParkingRefreshAndGeneration')
            if access or access_smoke:
                if './internal/services/etcd' not in packages:
                    packages.append('./internal/services/etcd')
                environment['NYADB_ETCD_ACCESS_IT'] = '1'
                access_cases = ('AccessOriginalBinaryRangesCountPagingExportAndWritePermissions','AccessActualGrantChangeAndOriginalMetadataCancelClose', 'AccessBasicTokenAndCertificateActualIdentity', 'AccessMaximumPrefixesRejectLongKeysAndRetainCompleteAuthorizedCounts')
                required += access_cases
                if access:
                    environment['NYADB_ETCD_ACCESS_IMAGES'] = str(report_path.with_suffix(''))+'-access-images'
                    required += ('ShellPublicAccessReadOnlyScopeCountExportSaveAndExplicitReopen',)
                if access_smoke:
                    packages = ['./internal/services/etcd']
                    required = access_cases
            if watch_smoke:
                environment.update(NYADB_ETCD_WATCH_IT='1', NYADB_ETCD_ACCESS_IT='1')
                packages = ['./internal/services/etcd']
                required = ('WatchOriginalBinaryEventsCompleteReceiptsProgressAndStop',
                            'WatchOriginalCompactionAndRestrictedCreationReceipts',
                            'WatchOriginalCancellationCloseAndBufferBounds',
                            'WatchActualOriginalCreationAndEventReceiptLossNeverReconnect')
            if history_smoke:
                environment.update(NYADB_ETCD_HISTORY_IT='1', NYADB_ETCD_ACCESS_IT='1')
                packages = ['./internal/services/etcd']
                required = ('HistoryCapturedWindowDeleteRecreateFutureAndEmptyKeys',
                            'HistoryNativeReceiptsKeepCompleteWindowBeyondProjectionLimit',
                            'HistoryActualCompactionDoesNotMoveRequestedEndRevision',
                            'HistoryOriginalReadOnlyIdentityAndDeniedRange',
                            'HistoryActualProgressLossTimeoutCancelAndOriginalClose',
                            'HistoryCompleteLargeValuesAndExplicitNativeCaptureBounds',
                            'HistoryActualRangeCreationAndEventReceiptLossNeverReplay')
            if events_smoke:
                environment.update(NYADB_ETCD_EVENTS_IT='1')
                packages = ['./internal/services/etcd', './internal/services/dbruntime']
                required = ('EventsSessionOriginalStreamsFixedHistoryAndIndependentRequests',
                            'EventsSessionOriginalRetirementRecoveryDoesNotSubscribe',
                            'EventsHistoryDoesNotReplaceActualUnknownWriteReceipt',
                            'EventsTransferredReceiptsExhaustSharedBudgetWithoutStoppingOtherRuns',
                            'EventsActualOriginalWatchLossAndCanceledCreationNeverReconnect',
                            'EventsActualProgressLossKeepsOriginalHistoryOnCancelAndRetirement',
                            'EventsRuntimeOriginalPublicationAndExplicitRecovery')
            if events_workspace_smoke:
                environment.update(NYADB_ETCD_WORKSPACE_IT='1', NYADB_ETCD_EVENTS_WORKSPACE_IT='1')
                packages = ['./internal/ui/kvworkspace']
                required = ('EventsWorkspaceOriginalStreamsHistoryAndCompleteArchives',
                            'EventsWorkspaceParkedStopPublicationRecoveryAndRequestFiles',
                            'EventsWorkspaceHistoryKeepsActualUnknownWriteReceipt')
            if public_events:
                environment.update(NYADB_ETCD_EVENTS_WORKSPACE_IT='1', NYADB_ETCD_ACCESS_IT='1')
                packages = ['./internal/services/etcd', './internal/ui/kvworkspace', './internal/ui/shell']
                required = ('DataKeyReadKeepsOriginalPageRevisionAndCompleteKey',
                            'DataKeyReadRejectsActualRevokedReadGrant',
                            'EventsWorkspaceOriginalStreamsHistoryAndCompleteArchives',
                            'EventsWorkspaceParkedStopPublicationRecoveryAndRequestFiles',
                            'EventsWorkspaceHistoryKeepsActualUnknownWriteReceipt',
                            'ShellPublicEventsOriginalKeyMenuFixedWindowAndArchives',
                            'ShellPublicEventsParkedPublicationRecoveryNeverResubscribes')
            if renewal_native:
                environment.update(NYADB_ETCD_RENEWAL_IT='1', NYADB_ETCD_ACCESS_IT='1')
                packages = ['./internal/services/etcd']
                required = ('RenewalOriginalExactLeaseSurvivesThenExpiresAfterExplicitStop',
                            'RenewalActualProcessedFirstAndLaterReplyLossNeverReconnects',
                            'RenewalActualRevocationMissingLeaseAndBoundedReceipts',
                            'RenewalActualPermissionRevocationStopsOnlyOriginalRun',
                            'LeaseAttachmentKeepAliveAndExpiration',
                            'OriginalReadCancellationBusyAndPreparationOwnership')
            if renewal_session:
                environment.update(NYADB_ETCD_RENEWAL_IT='1', NYADB_ETCD_RENEWAL_SESSION_IT='1', NYADB_ETCD_EVENTS_IT='1', NYADB_ETCD_ACCESS_IT='1')
                packages = ['./internal/services/etcd', './internal/services/dbruntime']
                required = ('RenewalOriginalExactLeaseSurvivesThenExpiresAfterExplicitStop',
                            'RenewalActualProcessedFirstAndLaterReplyLossNeverReconnects',
                            'RenewalActualRevocationMissingLeaseAndBoundedReceipts',
                            'RenewalActualPermissionRevocationStopsOnlyOriginalRun',
                            'LeaseAttachmentKeepAliveAndExpiration',
                            'OriginalReadCancellationBusyAndPreparationOwnership',
                            'RenewalSessionOriginalAuthorizationIndependentKVAndRecoveryBudget',
                            'RenewalSessionActualProcessedReceiptsRetirementAndUnknownRecovery',
                            'RenewalSessionActualRetainedDocumentOverflowStopsOriginalRun',
                            'RenewalRuntimeOriginalPublicationParkedReceiptsAndExplicitRecovery',
                            'EventsSessionOriginalRetirementRecoveryDoesNotSubscribe',
                            'EventsTransferredReceiptsExhaustSharedBudgetWithoutStoppingOtherRuns')
            if public_renewal:
                environment.update(NYADB_ETCD_RENEWAL_IT='1', NYADB_ETCD_RENEWAL_SESSION_IT='1', NYADB_ETCD_RENEWAL_WORKSPACE_IT='1',
                                   NYADB_ETCD_EVENTS_IT='1', NYADB_ETCD_EVENTS_WORKSPACE_IT='1', NYADB_ETCD_ACCESS_IT='1')
                packages = ['./internal/services/etcd', './internal/services/dbruntime', './internal/ui/kvworkspace', './internal/ui/shell']
                required = ('RenewalOriginalExactLeaseSurvivesThenExpiresAfterExplicitStop',
                            'RenewalActualProcessedFirstAndLaterReplyLossNeverReconnects',
                            'RenewalActualRevocationMissingLeaseAndBoundedReceipts',
                            'RenewalActualPermissionRevocationStopsOnlyOriginalRun',
                            'LeaseAttachmentKeepAliveAndExpiration',
                            'OriginalReadCancellationBusyAndPreparationOwnership',
                            'RenewalSessionOriginalAuthorizationIndependentKVAndRecoveryBudget',
                            'RenewalSessionActualProcessedReceiptsRetirementAndUnknownRecovery',
                            'RenewalSessionActualRetainedDocumentOverflowStopsOriginalRun',
                            'RenewalRuntimeOriginalPublicationParkedReceiptsAndExplicitRecovery',
                            'EventsSessionOriginalRetirementRecoveryDoesNotSubscribe',
                            'EventsTransferredReceiptsExhaustSharedBudgetWithoutStoppingOtherRuns',
                            'RenewalWorkspaceOriginalConfirmationContinuousTTLStopAndArchives',
                            'RenewalWorkspaceActualProcessedFirstAndLaterReceiptsAndRecovery',
                            'RenewalWorkspaceActualOneShotUnknownBlocksContinuousAuthorization',
                            'ShellPublicRenewalOriginalConfirmationControlsAndCompleteArchives',
                            'ShellPublicRenewalParkedPublicationRecoveryNeverRestarts',
                            'DataKeyReadKeepsOriginalPageRevisionAndCompleteKey',
                            'DataKeyReadRejectsActualRevokedReadGrant',
                            'EventsWorkspaceOriginalStreamsHistoryAndCompleteArchives',
                            'EventsWorkspaceParkedStopPublicationRecoveryAndRequestFiles',
                            'EventsWorkspaceHistoryKeepsActualUnknownWriteReceipt',
                            'ShellPublicEventsOriginalKeyMenuFixedWindowAndArchives',
                            'ShellPublicEventsParkedPublicationRecoveryNeverResubscribes')
            if lease_browser_native:
                environment.update(NYADB_ETCD_LEASE_BROWSER_IT='1', NYADB_ETCD_ACCESS_IT='1', NYADB_ETCD_RENEWAL_IT='1')
                packages = ['./internal/services/etcd']
                required = ('LeaseBrowserActualListWindowsAndCompleteAttachedKeys',
                            'LeaseBrowserActualProcessedListAndTTLFailureNeverRetries',
                            'LeaseBrowserActualForeignReceiptsAndUnknownFields',
                            'LeaseBrowserActualPermissionLossKeepsIndependentReads',
                            'LeaseBrowserAggregateBudgetKeepsCompleteOriginalReceipts',
                            'LeaseAttachmentKeepAliveAndExpiration',
                            'OriginalReadCancellationBusyAndPreparationOwnership',
                            'RenewalOriginalExactLeaseSurvivesThenExpiresAfterExplicitStop',
                            'RenewalActualProcessedFirstAndLaterReplyLossNeverReconnects')
            if lease_browser_session:
                environment.update(NYADB_ETCD_LEASE_BROWSER_IT='1', NYADB_ETCD_LEASE_BROWSER_SESSION_IT='1',
                                   NYADB_ETCD_SESSION_IT='1', NYADB_ETCD_ACCESS_IT='1', NYADB_ETCD_RENEWAL_IT='1')
                packages = ['./internal/services/etcd', './internal/services/dbruntime']
                required = ('LeaseBrowserActualListWindowsAndCompleteAttachedKeys',
                            'LeaseBrowserActualProcessedListAndTTLFailureNeverRetries',
                            'LeaseBrowserActualForeignReceiptsAndUnknownFields',
                            'LeaseBrowserActualPermissionLossKeepsIndependentReads',
                            'LeaseBrowserAggregateBudgetKeepsCompleteOriginalReceipts',
                            'LeaseAttachmentKeepAliveAndExpiration',
                            'OriginalReadCancellationBusyAndPreparationOwnership',
                            'RenewalOriginalExactLeaseSurvivesThenExpiresAfterExplicitStop',
                            'RenewalActualProcessedFirstAndLaterReplyLossNeverReconnects',
                            'LeaseBrowserSessionOriginalDocumentAndIndependentOneShotReceipts',
                            'LeaseBrowserSessionActualPartialReadCancelAndClose',
                            'LeaseBrowserSessionActualUnknownWriteRecoveryKeepsOriginalReceipt',
                            'LeaseBrowserRuntimeOriginalPublicationAndPureRecovery',
                            'RuntimeOriginalPublicationSnapshotsAndExplicitRecovery',
                            'RuntimeActualProcessedPutFaultAndGenerationReplacement',
                            'RuntimeLateOriginalHandshakeCannotCloseReplacement')
            if public_lease_browser:
                environment['NYADB_ETCD_LEASE_BROWSER_WORKSPACE_IT'] = '1'
                packages += ['./internal/ui/kvworkspace', './internal/ui/shell']
                required += ('LeaseBrowserWorkspaceOriginalListDetailCompleteKeysAndArchives',
                             'LeaseBrowserWorkspaceActualProcessedReadFaultsAndParkedOriginalRecovery',
                             'LeaseBrowserWorkspaceActualUnknownPutPureRecoveryAndReadIsolation',
                             'LeaseBrowserWorkspaceActualUnknownRenewalReadIsolation',
                             'ShellPublicLeaseBrowserOriginalListSelectionDetailAndCompleteArchives',
                             'ShellPublicLeaseBrowserParkedPublicationRecoveryRetainsOriginalPage')
            if public_lease_management:
                environment['NYADB_ETCD_LEASE_MANAGEMENT_IT'] = '1'
                required += ('LeaseManagementWorkspaceActualGrantRevokeAndChangingObservation',
                             'LeaseManagementWorkspaceBilingualCompletePreviewAndRenewalShortcut',
                             'LeaseManagementWorkspaceActualProcessedFaultsOriginalRecoveryAndClose',
                             'LeaseManagementWorkspaceNaturalExpirationBeforeConfirmation',
                             'LeaseManagementWorkspaceActualPermissionChangeRejectsRevoke',
                             'LeaseManagementWorkspaceForeignGrantReplyKeepsUnknownNativeEvidence',
                             'ShellPublicLeaseManagementOriginalFormsConfirmationAndCompleteArchives')
            if auth_native:
                environment.update(NYADB_ETCD_AUTH_IT='1', NYADB_ETCD_ACCESS_IT='1')
                packages = ['./internal/services/etcd']
                required = ('AuthOriginalFourteenRPCsExactNamesPermissionsAndRevision',
                            'AuthActualPasswordChangeAndNoPasswordCertificateIdentity',
                            'AuthActualRestrictedIdentityAndDefiniteDenials',
                            'AuthActualProcessedWritesLostCancelCloseAndForeignReceiptNeverReplay',
                            'AuthActualReadFaultsAndCompleteNativeExtensions',
                            'AuthOriginalAccessCacheInvalidatedAfterAcknowledgedWrite')
            if public_auth:
                environment['NYADB_ETCD_PUBLIC_AUTH_IT'] = '1'
                packages = ['./internal/services/etcd', './internal/ui/kvworkspace', './internal/ui/shell']
                required += ('AuthWorkspaceBilingualOriginalFormsAndCompletePermissions',
                             'AuthWorkspaceDefinitePartialStepsAndNativeNameReuse',
                             'AuthWorkspaceActualProcessedPartialBatchCancelCloseParkAndRecovery',
                             'AuthWorkspaceOriginalReadParkingRevisionAndNativeExtensions',
                             'AuthWorkspaceActualPasswordRestrictedAndCertificateIdentities',
                             'ShellPublicAuthOriginalFormsConfirmationAndCompleteArchives',
                             'ShellPublicAuthParkedPublicationRecoveryNeverReplays')
            if compaction_native:
                environment.update(NYADB_ETCD_COMPACTION_IT='1', NYADB_ETCD_ACCESS_IT='1',
                                   NYADB_ETCD_WATCH_IT='1', NYADB_ETCD_HISTORY_IT='1', NYADB_ETCD_DATA_IT='1',
                                   NYADB_ETCD_AUTH_IT='1', NYADB_ETCD_LEASE_BROWSER_SESSION_IT='1', NYADB_ETCD_EVENTS_IT='1')
                packages = ['./internal/services/etcd', './internal/services/dbruntime']
                required = ('CompactionOriginalLogicalPhysicalWholeClusterAndCompleteReceipts',
                            'CompactionActualFutureCompactedAndRestrictedIdentityDefiniteErrors',
                            'CompactionActualProcessedLostCancelCloseAndInvalidReceiptsNeverReplay',
                            'CompactionRuntimeOriginalPublicationInventoryAndPureRecovery',
                            'CompactionRuntimeActualProcessedFaultsOriginalPublicationAndRecoveryNeverReplay',
                            'BinaryMVCCPagingAndCompaction',
                            'WatchOriginalCompactionAndRestrictedCreationReceipts',
                            'HistoryActualCompactionDoesNotMoveRequestedEndRevision',
                            'DataCompactionRetiresOriginalRangeAndExplicitReopen',
                            'AuthOriginalFourteenRPCsExactNamesPermissionsAndRevision',
                            'AuthActualRestrictedIdentityAndDefiniteDenials',
                            'LeaseBrowserRuntimeOriginalPublicationAndPureRecovery',
                            'RuntimeOriginalPublicationSnapshotsAndExplicitRecovery',
                            'RuntimeActualProcessedPutFaultAndGenerationReplacement',
                            'RuntimeLateOriginalHandshakeCannotCloseReplacement',
                            'EventsSessionOriginalRetirementRecoveryDoesNotSubscribe')
            if public_compaction:
                environment.update(NYADB_ETCD_PUBLIC_COMPACTION_IT='1', NYADB_ETCD_EVENTS_WORKSPACE_IT='1',
                                   NYADB_ETCD_PUBLIC_AUTH_IT='1', NYADB_ETCD_LEASE_MANAGEMENT_IT='1',
                                   NYADB_ETCD_RENEWAL_WORKSPACE_IT='1', NYADB_ETCD_RENEWAL_IT='1')
                packages += ['./internal/ui/kvworkspace', './internal/ui/shell']
                required += ('CompactionWorkspaceBilingualLogicalPhysicalOriginalControlsAndArchives',
                             'CompactionWorkspaceActualProcessedFaultsParkingRecoveryAndIndependentReads',
                             'CompactionWorkspaceActualFutureRepeatedAndRestrictedDenials',
                             'ShellPublicCompactionOriginalFormsCheckedConfirmationAndCompleteArchives',
                             'ShellPublicCompactionParkedPublicationAndRecoveryNeverExecutes',
                             'AuthWorkspaceDefinitePartialStepsAndNativeNameReuse',
                             'LeaseManagementWorkspaceActualGrantRevokeAndChangingObservation',
                             'RenewalWorkspaceActualOneShotUnknownBlocksContinuousAuthorization',
                             'EventsWorkspaceHistoryKeepsActualUnknownWriteReceipt')
            if member_native:
                environment.update(NYADB_ETCD_MEMBER_NATIVE_IT='1', NYADB_ETCD_ACCESS_IT='1')
                required = REQUIRED + ('MemberDirectoryZeroRevisionAndOriginalEmptyDefragmentAck',
                                       'MemberDefragmentActualProcessedFaultsNeverReplayOrSettleByLaterStatus',
                                       'MemberDefragmentActualRestrictedTokenAndCertificateIdentity',
                                       'MemberDefragmentBeforeDispatchCancellationAndOriginalPlanOwnership')
            if member_session:
                environment.update(NYADB_ETCD_MEMBER_SESSION_IT='1', NYADB_ETCD_SESSION_IT='1')
                packages = ['./internal/services/etcd', './internal/services/dbruntime']
                required += ('MemberSessionOriginalDirectoryTransportAndEmptyAck',
                             'MemberSessionActualProcessedFaultsAndPureRecovery',
                             'MemberSessionRestrictedIdentityAndCertificateTransport',
                             'MemberRuntimeOriginalPublicationAndEvidenceRecovery',
                             'MemberRuntimeActualProcessedPublicationFaults',
                             'SessionOriginalScopePurePlansAndExactReplies',
                             'SessionActualProcessedReplyFaultsAndRecovery',
                             'SessionOriginalReadBusyCancellationAndClose',
                             'RuntimeActualProcessedPutFaultAndGenerationReplacement',
                             'RuntimeLateOriginalHandshakeCannotCloseReplacement')
            if alarm_native:
                environment.update(NYADB_ETCD_ALARM_IT='1', NYADB_ETCD_SESSION_IT='1', NYADB_ETCD_COMPACTION_IT='1', NYADB_ETCD_MEMBER_NATIVE_IT='1')
                packages = ['./internal/services/etcd', './internal/services/dbruntime']
                required = REQUIRED + ('AlarmOriginalListTypeFilterExactDisarmAndCompleteReceipts',
                                       'AlarmRestrictedTokenAndCertificateOriginalPermissions',
                                       'AlarmActualProcessedReadAndDisarmFaultsRetainOriginalsNeverReplay',
                                       'AlarmOriginalCancellationBusyOwnershipAndUnknownWriteBarrier',
                                       'AlarmRuntimeOriginalInventoryPublicationAndPureRecovery',
                                       'AlarmRuntimeActualProcessedFaultsOriginalPublicationAndRecoveryNeverReplay',
                                       'CompactionOriginalLogicalPhysicalWholeClusterAndCompleteReceipts',
                                       'CompactionRuntimeOriginalPublicationInventoryAndPureRecovery',
                                       'MemberDirectoryZeroRevisionAndOriginalEmptyDefragmentAck',
                                       'MemberDefragmentBeforeDispatchCancellationAndOriginalPlanOwnership',
                                       'RuntimeActualProcessedPutFaultAndGenerationReplacement',
                                       'RuntimeLateOriginalHandshakeCannotCloseReplacement')
            if alarm_workspace:
                environment.update(NYADB_ETCD_ALARM_WORKSPACE_IT='1', NYADB_ETCD_WORKSPACE_IT='1')
                packages += ['./internal/ui/kvworkspace']
                required += ('AlarmWorkspaceOriginalCheckedPairsAndCompleteArchives',
                             'AlarmWorkspaceStalePairStopsBeforeSendAndTypeWindowsRemainEvidence',
                             'AlarmWorkspaceActualProcessedPartialFaultsParkingRecoveryAndClose',
                             'AlarmWorkspaceOriginalRestrictedReadAndExactWriteDenial')
            if public_alarms:
                environment.update(NYADB_ETCD_PUBLIC_ALARMS_IT='1', NYADB_ETCD_PUBLIC_IT='1',
                                   NYADB_ETCD_WORKSPACE_IMAGES=str(report_path.with_suffix(''))+'-alarm-workspace-images',
                                   NYADB_ETCD_PUBLIC_IMAGES=str(report_path.with_suffix(''))+'-alarm-public-images')
                packages += ['./internal/ui/shell']
                required += ('AlarmsWorkspacePublicBilingualFormsCheckedBatchFilesAndCompleteRecords',
                             'AlarmsWorkspacePublicUnknownRecoveryParkingAndOriginalClose',
                             'AlarmsWorkspacePublicRestrictedDenialAndUnknownTypeInspection',
                             'AlarmsShellPublicCheckedOriginalFormsFilesAndCompleteArchives',
                             'AlarmsShellPublicUnknownParkingAndPureRecoveryRetainOriginals')
            if snapshot_native:
                environment['NYADB_ETCD_SNAPSHOT_IT'] = '1'
                packages = ['./internal/services/etcd']
                required = REQUIRED + ('SnapshotCompleteMultiFrameDiskOutputsRetainOriginalNativeEvidence',
                                       'SnapshotOriginalRestrictedTokenAndCertificatePermissions',
                                       'SnapshotActualOriginalStreamFaultsCancellationAndCloseNeverResume')
            if snapshot_session:
                environment['NYADB_ETCD_SNAPSHOT_SESSION_IT'] = '1'
                packages += ['./internal/services/dbruntime']
                required += ('SnapshotSessionOriginalScopeSameLeaseFullFramesAndPureRecovery',
                             'SnapshotSessionActualSourceFaultsOriginalCloseAndOutputAcknowledgement',
                             'SnapshotSessionReadAfterActualUnknownPutKeepsOriginalWriteBarrier',
                             'SnapshotSessionRestrictedIdentityRetainsOriginalNativeDenial',
                             'SnapshotRuntimeOriginalAllFacetsExactPublicationAndPureRecovery',
                             'SnapshotRuntimeActualSourceFaultsOriginalPublicationParkingAndRecovery',
                             'SnapshotRuntimeCompleteReadNeverSettlesEarlierActualUnknownWrite',
                             'SessionOriginalScopePurePlansAndExactReplies',
                             'SessionOriginalReadBusyCancellationAndClose',
                             'RuntimeActualProcessedPutFaultAndGenerationReplacement',
                             'RuntimeLateOriginalHandshakeCannotCloseReplacement')
            if snapshot_artifact:
                environment['NYADB_ETCD_SNAPSHOT_ARTIFACT_IT'] = '1'
                required += ('SnapshotArtifactOriginalCompleteFilesPureRecoveryAndExactCleanup',
                             'SnapshotArtifactActualSourceFaultsCancellationParkingAndRetainedEvidence',
                             'SnapshotArtifactCompleteFilesNeverSettleEarlierActualUnknownWrite',
                             'SnapshotArtifactRestrictedIdentityRetainsExactNativeDenialJournal')
            if snapshot_file:
                environment['NYADB_ETCD_SNAPSHOT_FILE_IT'] = '1'
                required += ('SnapshotFileOriginalBackendJournalNewAndReplacementAfterDisconnect',
                             'SnapshotFileActualUnknownPutBarrierSurvivesOriginalFilePublication',
                             'SnapshotFileIncompleteBackendRejectedCompleteFailureJournalExported')
            # Foundation cases revoke authentication on this same owned node.
            # Package acceptance therefore runs sequentially.
            command = ['go', 'test', '-p=1']+(['-race'] if race else [])+packages+['-run', '^('+'|'.join('TestEtcdNative'+case for case in required)+')$', '-count=1', '-v', '-timeout=600s']
            with log_path.open('w') as out:
                result = subprocess.run(command, cwd=ROOT, env=environment, stdout=out, stderr=subprocess.STDOUT)
            log = log_path.read_text()
            if result.returncode or re.search(r'^\s*--- (FAIL|SKIP):|WARNING: DATA RACE', log, re.M):
                raise RuntimeError('Native acceptance failed; inspect '+str(log_path.relative_to(ROOT)))
            accepted = re.findall(r'^--- PASS: TestEtcdNative(\w+) \(', log, re.M)
            if sorted(accepted) != sorted(required):
                raise RuntimeError('Native acceptance has missing or unexpected cases')
            for name, expected in report['source_hashes'].items():
                if hashlib.sha256((ROOT/name).read_bytes()).hexdigest() != expected:
                    raise RuntimeError('Native source changed during acceptance')
            if profile and not (public_auth or public_compaction) and not smoke and not (public_events or public_renewal or public_lease_browser):
                pictures = sorted(Path(environment['NYADB_ETCD_PROFILE_IMAGES']).glob('*.png'))
                if len(pictures) != 72:
                    raise RuntimeError('Native profile acceptance is missing localized images')
                report['profile_images'] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in pictures}
            if workspace and not (public_auth or public_compaction) and not events_workspace_smoke and not (public_events or public_renewal or public_lease_browser):
                pictures = sorted(Path(environment['NYADB_ETCD_WORKSPACE_IMAGES']).glob('*.png'))
                if len(pictures) != (4 if smoke else 40):
                    raise RuntimeError('Native workspace acceptance is missing localized images')
                report['workspace_images'] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in pictures}
            if data and not (public_auth or public_compaction) and not (public_events or public_renewal or public_lease_browser):
                pictures = sorted(Path(environment['NYADB_ETCD_DATA_IMAGES']).glob('*.png'))
                if len(pictures) != 16:
                    raise RuntimeError('Native data acceptance is missing localized images')
                report['data_images'] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in pictures}
            if public and not (public_auth or public_compaction) and not (public_events or public_renewal or public_lease_browser):
                pictures = sorted(Path(environment['NYADB_ETCD_PUBLIC_IMAGES']).glob('*.png'))
                if len(pictures) != 44:
                    raise RuntimeError('Public acceptance is missing localized images')
                report['public_images'] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in pictures}
            if public_events:
                pictures = sorted(Path(environment['NYADB_ETCD_PUBLIC_IMAGES']).glob('*.png'))
                if len(pictures) != 28:
                    raise RuntimeError('Public event/history acceptance is missing localized images')
                report['public_images'] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in pictures}
            if public_renewal:
                for key, env_key, expected in (('renewal_workspace_images', 'NYADB_ETCD_WORKSPACE_IMAGES', 20), ('public_images', 'NYADB_ETCD_PUBLIC_IMAGES', 60)):
                    pictures = sorted(Path(environment[env_key]).glob('*.png'))
                    if len(pictures) != expected:
                        raise RuntimeError('Continuous renewal acceptance is missing localized images: '+key)
                    report[key] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in pictures}
            if public_lease_browser:
                for key, env_key, expected in (('lease_browser_workspace_images', 'NYADB_ETCD_WORKSPACE_IMAGES', 48 if public_lease_management else 24), ('public_images', 'NYADB_ETCD_PUBLIC_IMAGES', 68 if public_lease_management else 32)):
                    pictures = sorted(Path(environment[env_key]).glob('*.png'))
                    if len(pictures) != expected:
                        raise RuntimeError('Lease browser acceptance is missing localized images: '+key)
                    report[key] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in pictures}
            if access:
                pictures = sorted(Path(environment['NYADB_ETCD_ACCESS_IMAGES']).glob('*.png'))
                if len(pictures) != 24:
                    raise RuntimeError('Public access acceptance is missing localized images')
                report['access_images'] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in pictures}
            if public_auth:
                for key, env_key, expected in (('auth_workspace_images', 'NYADB_ETCD_WORKSPACE_IMAGES', 36), ('auth_public_images', 'NYADB_ETCD_PUBLIC_IMAGES', 44)):
                    pictures = sorted(Path(environment[env_key]).glob('*.png'))
                    if len(pictures) != expected:
                        raise RuntimeError('Public Auth acceptance is missing localized images: '+key)
                    report[key] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in pictures}
            if public_compaction:
                for key, env_key, expected in (('compaction_workspace_images', 'NYADB_ETCD_WORKSPACE_IMAGES', 48), ('compaction_public_images', 'NYADB_ETCD_PUBLIC_IMAGES', 48)):
                    pictures = sorted(Path(environment[env_key]).glob('*.png'))
                    if len(pictures) != expected:
                        raise RuntimeError('Public Compact acceptance is missing localized images: '+key+' got '+str(len(pictures)))
                    report[key] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in pictures}
            if public_alarms:
                for key, env_key, expected in (('alarms_workspace_images', 'NYADB_ETCD_WORKSPACE_IMAGES', 68), ('alarms_public_images', 'NYADB_ETCD_PUBLIC_IMAGES', 68)):
                    pictures = sorted(Path(environment[env_key]).glob('*.png'))
                    if len(pictures) != expected:
                        raise RuntimeError('Public Alarm acceptance is missing localized images: '+key+' got '+str(len(pictures)))
                    report[key] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in pictures}
            report.update(passed=True, accepted=accepted, log_sha256=hashlib.sha256(log_path.read_bytes()).hexdigest())
    finally:
        try:
            if identifier:
                remove_owned(identifier, nonce)
                result = subprocess.run(['docker', 'inspect', identifier], capture_output=True)
                report['container_removed'] = result.returncode != 0
        finally:
            report['private_files_removed'] = private_path is not None and not private_path.exists()
            report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n')
    print(report_path.relative_to(ROOT))


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--mode', choices=('anonymous', 'basic', 'tls', 'mtls'), default='basic')
    parser.add_argument('--history-smoke', action='store_true')
    parser.add_argument('--events-smoke', action='store_true')
    parser.add_argument('--events-workspace-smoke', action='store_true')
    parser.add_argument('--public-events', action='store_true')
    parser.add_argument('--renewal-native', action='store_true')
    parser.add_argument('--renewal-session', action='store_true')
    parser.add_argument('--public-renewal', action='store_true')
    parser.add_argument('--lease-browser-native', action='store_true')
    parser.add_argument('--lease-browser-session', action='store_true')
    parser.add_argument('--public-lease-browser', action='store_true')
    parser.add_argument('--public-lease-management', action='store_true')
    parser.add_argument('--auth-native', action='store_true')
    parser.add_argument('--public-auth', action='store_true')
    parser.add_argument('--compaction-native', action='store_true')
    parser.add_argument('--public-compaction', action='store_true')
    parser.add_argument('--snapshot-native', action='store_true')
    parser.add_argument('--snapshot-session', action='store_true')
    parser.add_argument('--snapshot-artifact', action='store_true')
    parser.add_argument('--snapshot-file', action='store_true')
    parser.add_argument('--alarm-native', action='store_true')
    parser.add_argument('--alarm-workspace', action='store_true')
    parser.add_argument('--public-alarms', action='store_true')
    parser.add_argument('--member-native', action='store_true')
    parser.add_argument('--member-session', action='store_true')
    parser.add_argument('--dependency-modfile')
    parser.add_argument('--watch-smoke', action='store_true')
    parser.add_argument('--access', action='store_true')
    parser.add_argument('--access-smoke', action='store_true')
    parser.add_argument('--public', action='store_true')
    parser.add_argument('--public-smoke', action='store_true')
    parser.add_argument('--profile', action='store_true')
    parser.add_argument('--session', action='store_true')
    parser.add_argument('--workspace', action='store_true')
    parser.add_argument('--data-adapter', action='store_true')
    parser.add_argument('--race', action='store_true')
    parser.add_argument('--version', choices=tuple(IMAGES), default='3.7.3')
    args = parser.parse_args()
    verify(args.mode, args.race, args.version, args.profile, args.session, args.workspace, args.data_adapter, args.public, args.public_smoke, args.access, args.access_smoke, args.watch_smoke, args.history_smoke, args.events_smoke, args.events_workspace_smoke, args.public_events, args.renewal_native, args.renewal_session, args.public_renewal, args.lease_browser_native, args.lease_browser_session, args.public_lease_browser, args.public_lease_management, args.dependency_modfile, args.auth_native, args.public_auth, args.compaction_native, args.public_compaction, args.member_native, args.member_session, args.alarm_native, args.alarm_workspace, args.public_alarms, args.snapshot_native, args.snapshot_session, args.snapshot_artifact, args.snapshot_file)
