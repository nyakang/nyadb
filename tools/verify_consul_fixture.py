#!/usr/bin/env python3
"""Verify original Consul requests in isolated owned official fixtures."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid

PUBLIC_AGENT_MAINTENANCE_REQUIRED=('PublicAgentMaintenanceFormsOriginalReasonsLocalACKAndFiles','PublicAgentMaintenanceKnownDenialsPreserveOriginalState','PublicAgentMaintenanceProcessedWritesParkingRecoveryAndNoReplay','PublicShellAgentMaintenanceOriginalTabsLocalACKParkingAndRecovery')

AGENT_MAINTENANCE_REQUIRED=('AgentMaintenanceOriginalReasonsDefaultsNoopsAndPreservation','AgentMaintenanceOriginalScopesAndKnownDenials','AgentMaintenanceCatalogConvergenceUsesOriginalAgentToken','AgentMaintenanceProcessedRepliesOriginalLeaseRecoveryAndNoReplay','RuntimeAgentMaintenanceOriginalPublishedGenerationsAndRecovery','RuntimeAgentMaintenanceProcessedReplacementKeepsOriginalUnknown')
AGENT_MAINTENANCE_NO_AGENT_TOKEN_REQUIRED=('AgentMaintenanceLocalDisableACKKeepsIndependentStaleCatalog',)

PUBLIC_AGENT_TTL_REQUIRED=('PublicAgentTTLFormsOriginalStatesNotesReceiptsAndFiles','PublicAgentTTLProcessedWritesParkingACKRecoveryAndNoReplay','PublicAgentTTLOriginalKnownDenialsAndNonTTLUnknown','PublicShellAgentTTLOriginalTabsConfirmationsParkingAndRecovery')

AGENT_BROWSER_REQUIRED=('PublicAgentBrowserOriginalFormsMapsDetailsAndFiles','PublicAgentBrowserParkingUnknownWriteAndOriginalRecovery','PublicShellAgentBrowserOriginalTabsMapsParkingDisconnectAndRecovery')
AGENT_SERVICE_EVIDENCE_REQUIRED=('AgentServiceEvidenceOriginalUpdatesReplacementAndEmptyReceipts','AgentServiceEvidenceOriginalEscapedIDsAndACLDenial')
AGENT_SERVICE_REQUIRED=('AgentServiceOriginalPlansDefinitionsReplacementAndKnownDenials','AgentServiceProcessedRepliesOriginalLeaseRecoveryAndNoReplay','AgentServiceOriginalPartialSidecarFailureRetainsCompleteUnknown','AgentServiceOriginalIDsScopeBoundsAndForeignPreparation','AgentServiceOriginalKVUnknownBlocksAgentWritesWithoutReplay')
AGENT_SERVICE_RUNTIME_REQUIRED=('RuntimeAgentServiceOriginalPublishedGenerationsAndRecovery','RuntimeAgentServiceProcessedReplacementKeepsOriginalUnknown')
AGENT_CHECK_REQUIRED=('AgentCheckOriginalDefinitionsDefaultsUpdatesAndKnownDenials','AgentCheckServiceBindingAndRejectedOriginalDefinitions','AgentCheckProcessedRepliesOriginalLeaseRecoveryAndNoReplay','RuntimeAgentCheckOriginalPublishedGenerationsAndRecovery','RuntimeAgentCheckProcessedReplacementKeepsOriginalUnknown')

PUBLIC_AGENT_SERVICE_REQUIRED=('PublicAgentServiceFormsOriginalDefinitionsChecksReceiptsAndFiles','PublicAgentServiceProcessedWritesParkingACKRecoveryAndNoReplay','PublicAgentServiceOriginalPartialSidecarAndACLDenial','PublicShellAgentServiceOriginalTabsConfirmationsParkingAndRecovery','PublicSessionToolsProcessedWritesParkingACKRecoveryAndNoReplay')

AGENT_TTL_REQUIRED=('AgentTTLOriginalStatesNotesOutputAndReset','AgentTTLOriginalDefinitionsKnownDenialsAndNativeErrors','AgentTTLProcessedRepliesOriginalLeaseRecoveryAndNoReplay','RuntimeAgentTTLOriginalPublishedGenerationsAndRecovery','RuntimeAgentTTLProcessedReplacementKeepsOriginalUnknown')

PUBLIC_AGENT_CHECK_REQUIRED=('PublicAgentCheckFormsOriginalDefinitionsDefaultsReceiptsAndFiles','PublicAgentCheckProcessedWritesParkingACKRecoveryAndNoReplay','PublicAgentCheckOriginalRejectedDefinitionsAndACLDenial','PublicShellAgentCheckOriginalTabsConfirmationsParkingAndRecovery')

def agent_check_image_names():
 stages=('register-form',*(f'register-{suffix}-{stage}' for suffix in ('original','updated','omitted','empty','null','service') for stage in ('confirmation','receipt')),'deregister-form','deregister-confirmation','deregister-receipt','missing-id-receipt','service-preserved-receipt',*(f'{kind}-{stage}-{mode}' for kind in ('http-agent-check-register','http-agent-check-deregister') for stage in ('unknown','recovered') for mode in ('lost','partial','http400','http500','http408','invalid','cancel','disconnect','replace','parked')),*(f'rejected-{kind}-{stage}' for kind in ('unknown-field','disabled-script','missing-service') for stage in ('unknown','after-read')),*(f'{kind}-acl-or-boundary' for kind in ('http-agent-check-register','http-agent-check-deregister')))
 return {f'{lang}-{size}-workspace-agent-check-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in stages}|{f'{lang}-{size}-shell-agent-check-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in ('register-form','register-confirmation','register-receipt','deregister-form','deregister-confirmation','deregister-receipt','recovered')}

def agent_service_image_names():
 stages=('register-form',*(f'register-{suffix}-{stage}' for suffix in ('a','b','c','d','e') for stage in ('confirmation','receipt')),'deregister-form','deregister-confirmation','deregister-receipt','missing-id-receipt',*(f'{kind}-{stage}-{mode}' for kind in ('http-agent-service-register','http-agent-service-deregister') for stage in ('unknown','recovered') for mode in ('lost','partial','http400','http500','http408','invalid','cancel','disconnect','replace','parked')),*(f'{kind}-acl-or-boundary' for kind in ('http-agent-service-register','http-agent-service-deregister')),'partial-sidecar-unknown','partial-sidecar-after-read')
 return {f'{lang}-{size}-workspace-agent-service-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in stages}|{f'{lang}-{size}-shell-agent-service-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in ('register-form','register-confirmation','register-receipt','deregister-form','deregister-confirmation','deregister-receipt','recovered')}

def agent_browser_image_names():
 stages=tuple(kind+'-native-'+stage for kind in ('http-agent-self','http-agent-services','http-agent-checks') for stage in ('form','receipt'))+tuple(kind+'-native-'+stage for kind in ('http-agent-services','http-agent-checks') for stage in ('list','detail'))+('native-agent-after-unknown','native-agent-recovered')+tuple('shell-agent-'+kind+'-form' for kind in ('http-agent-self','http-agent-services','http-agent-checks'))+('shell-agent-list','shell-agent-recovered')
 return {f'{lang}-{size}-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in stages}

HEALTH_WATCH_UI_REQUIRED=('PublicHealthWatchOriginalFormsSnapshotsDetailsAndFiles','PublicHealthWatchParkingUnknownWriteAndOriginalRecovery','PublicShellHealthWatchOriginalTabsRecordsParkingDisconnectAndRecovery')

AGENT_READ_REQUIRED=('AgentLocalReadsOriginalMapsFiltersAndConfiguredScope','AgentLocalReadReceiptFaultsAndCancellationNeverRetry','AgentLocalReadsKeepOriginalUnknownWriteAndEvidenceOnlyRecovery')

def health_watch_ui_image_names():
 stages=tuple(kind+'-watch-'+stage for kind in ('http-health-state','http-health-node','http-health-checks','http-health-service') for stage in ('form','request','selected','detail'))+('health-watch-after-unknown','health-watch-recovered')+tuple('shell-health-watch-'+stage for stage in ('observing','stopped','recovered'))
 return {f'{lang}-{size}-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in stages}

HEALTH_WATCH_REQUIRED=('HealthWatchOriginalEndpointsBlockingChangeAndCompleteSnapshots','HealthWatchProcessedReadFaultsOriginalAcknowledgementAndRecovery','HealthWatchACLFilteredSnapshotsUnknownWriteAndTerminalBudget','RuntimeHealthWatchOriginalGenerationReplacementAndEvidenceRecovery','RuntimeHealthWatchRejectsForeignOriginalConfiguration')

HEALTH_BROWSER_REQUIRED=('PublicHealthBrowserOriginalFormsPagesDetailsAndExport','PublicHealthBrowserParkedReadPreservesUnknownWriteAndRecovery','PublicShellHealthBrowserMenusParkingDisconnectAndOriginalRecovery')

def health_browser_image_names():
 stages=tuple(kind+'-native-'+stage for kind in ('http-health-state','http-health-node','http-health-checks','http-health-service') for stage in ('form','list','detail'))+('native-health-after-unknown','native-health-recovered')+tuple('shell-health-'+kind+'-form' for kind in ('http-health-state','http-health-node','http-health-checks','http-health-service'))+('shell-health-list','shell-health-recovered')
 return {f'{lang}-{size}-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in stages}

HEALTH_PROJECTION_REQUIRED=('HealthProjectionOriginalEndpointsFieldsAndReadOptions','HealthProjectionACLAndOriginalUnknownWriteRecovery','HealthProjectionProcessedReadFaultsCancellationRetirementAndRecovery')

PUBLIC_SESSION_DESTROY_REQUIRED=('PublicSessionDestroyOriginalReleaseDeleteConfirmationAndCompleteRecords','PublicSessionDestroyChangedKeysUnknownCancellationAndRecovery','PublicShellSessionDestroyOriginalConfirmationParkingAndReceipts')

def session_destroy_image_names():
 stages=tuple('native-'+behavior+'-'+stage for behavior in ('release','delete') for stage in ('confirm','last-page','receipt'))+('native-changed-refused','native-lost-unknown','native-cancel-unknown','shell-destroy-confirm','shell-destroy-put')
 return {f'{lang}-{size}-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in stages}

SESSION_DESTROY_PLAN_REQUIRED=('SessionDestroyPlanOriginalReleaseDeleteAndCompleteReceipts','SessionDestroyPlanChangedKeysACLAndRecoveryCannotAuthorizePUT','SessionDestroyPlanProcessedUnknownCancellationAndOriginalRecovery')

SESSION_DESTROY_IMPACT_REQUIRED=('SessionDestroyImpactOriginalWholeRootBehaviorAndChangedKeyVersions','SessionDestroyImpactACLAndOriginalUnknownWriteRecovery','SessionDestroyImpactProcessedRootFaultsAndOriginalAcknowledgement')

ROOT=Path(__file__).resolve().parents[1]
IMAGES={'2.0.2':'hashicorp/consul@sha256:7dcf35d6b2682831094f1680aa58be214134969505acce0a9b280249581aa7d2','1.22.3':'hashicorp/consul@sha256:43b5ffb7756ac1cacc775aa6fc6f79f5c635072cd854eab7dc2cc624f5d66025'}
REQUIRED=('IdentityScopeTransportAndNativeHeaders','BinaryValuesIndicesRawAndConditionalWrites','AtomicTransactionsComparisonsAndOriginalResults','SessionLocksRenewalAndNativeRejections','ActualCommittedReplyLossCancellationAndOriginalClose','RestrictedKVOnlyPermissionsAndOriginalDenial','SessionAndTransactionReplyLossNeverReplay')
DOCUMENT_REQUIRED=('DocumentOriginalScopesBytesAndCompleteReceipts','DocumentTransactionsAndGeneratedSessionIDsRemainNative','DocumentOriginalProcessedWritesNeverReplayAfterLostReply','DocumentReceiptBudgetAndCrossSessionTransfer','DocumentCapturedTLSAndUnavailableRestrictedIdentity','DocumentOriginalBlockingReadRetirementAndExplicitRecovery')
PUBLIC_REQUIRED=('PublicBuiltinProfileMetadataCapabilitiesAndRestrictedToken','PublicWorkspaceOriginalHTTPConfirmationReceiptsFilesAndRecovery','PublicWorkspaceActualProcessedReplyFaultsNeverPromoteUnknownOrReplay','PublicProfileURIStorageVaultEditorChecksAndOriginalTLS','PublicShellChooserHTTPDocumentsMenusParkingDisconnectAndClose')
RUNTIME_REQUIRED=('RuntimeGenerationsRequestsAndOriginalRecovery','RuntimeOriginalCommittedReplyLossAndRetirement','RuntimeLateConnectionClosesOnlyCapturedOriginalSession','RuntimeForeignScopeRejectedBeforeOriginalRecovery')
TOOLS_REQUIRED=('PublicRuntimeStructuredToolsCapturedOriginalProvider','PublicWorkspaceStructuredToolsOriginalRequestsBytesScopesAndLeave','PublicWorkspaceStructuredToolsProcessedReplyFaultsNeverReplay')
GRID_REQUIRED=('PublicDataOriginalLiveRuntimeAndRestrictedScope','PublicDataProcessedReadFaultsKeepOriginalRuntimeAndRequireReopen','PublicDataSharedGridLivePagingInspectorExportsParkingAndOriginalClose','PublicDataSharedGridProcessedReplyRetirementParkingAndExplicitReopen','PublicShellQualifiedGridNavigationMenusSearchStructureAndDisconnect','PublicShellProcessedGridCancelParkingOriginalReopenAndInactiveClose')
WRITE_GRID_REQUIRED=('PublicGridMutationBoundsOverlapAndDestinationConflictsNeverPartiallyWrite','PublicDataMutationQualifiedOriginalPlansAndGenerationReplacement','PublicDataMutationProcessedRepliesPreserveOriginalUnknownAndNeverReplay','PublicShellGridMutationConfirmationParkingReceiptAndDisconnect','PublicShellGridMutationProcessedReplyUnknownLeaveAndExplicitClose','PublicGridMutationOriginalConfirmationBytesFlagsRenameInsertDeleteAndParking','PublicGridMutationConflictKeepsOriginalDraftsAndLockedRowsReadOnly','PublicGridMutationProcessedReplyFaultsParkingGenerationLeaveAndNoReplay','PublicGridMutationOptionalMetadataWarningsKeepVerifiedOriginalCommit')
MUTATION_REQUIRED=('DataMutationOriginalAtomicInsertUpdateRenameDeleteCompleteEvidence','DataMutationOriginalObservationDestinationAndSessionConflictsRollbackBatch','DataMutationProcessedReplyFaultsCancellationAndOriginalCloseNeverReplay','DataMutationOriginalLeaseForeignPlanRevocationAndBounds','DataMutationRestrictedTokenOriginalWriteDenialAndExplicitNewPlan','DataMutationNativeRequestLimitRejectionNeverSplitsOrReplays')
PUBLIC_COPY_REQUIRED=('PublicDataKeyCopyQualifiedOriginalPlansAndGenerationReplacement','PublicGridKeyCopyBothPayloadsCompleteSourceDestinationConfirmationsAndContinuedEdits','PublicGridKeyCopySourceDestinationLockAndPreviewConflictsRetainOriginalDrafts','PublicGridKeyCopyProcessedReplyParkingGenerationLeaveAndNoReplay','PublicGridKeyCopyOriginalInputParkingCancelCloseAndLatePreview','PublicShellGridKeyCopyConfirmationReceiptAndDisconnect')
COPY_REQUIRED=('DataKeyCopyOriginalBytesFlagsBothPayloadsSourcePreservationAndContinuedEdits','DataKeyCopyOriginalSourceDestinationAndLockConflictsNeverPartiallyWrite','DataKeyCopyProcessedOriginalReplyFaultsCancellationCloseAndNoReplay','DataKeyCopyForeignOriginalPlanRevocationAndLockedSource','DataKeyCopyRestrictedOriginalTokenDenialRetainsCompleteReceipt','DataKeyCopyOriginalServerRequestLimitNeverSplitsOrReplays')
PUBLIC_PREFIX_DELETE_REQUIRED=('PublicDataKeyPrefixDeleteQualifiedOriginalPlansAndGenerationReplacement','PublicGridKeyPrefixDeleteCompleteOriginalCandidatesOutsidePageAndLaterKeys','PublicGridKeyPrefixDeleteConflictsBoundsAndLocksRetainOriginalEvidence','PublicGridKeyPrefixDeleteProcessedReplyParkingGenerationAndNoReplay','PublicGridKeyPrefixDeleteOriginalInputParkingCancelCloseAndLatePreview','PublicShellGridKeyPrefixDeleteConfirmationEvidenceAndDisconnect')
PREFIX_DELETE_REQUIRED=('DataKeyPrefixDeleteCompleteOriginalSetAndLaterKeysPreserved','DataKeyPrefixDeleteOriginalConflictsAndSingleTransactionBounds','DataKeyPrefixDeleteProcessedOriginalRepliesNeverReplay','DataKeyPrefixDeleteLocksOriginalOwnershipAndCaptureBudget','DataKeyPrefixDeleteRestrictedACLFilteringAndOriginalWriteDenial')
PREFIX_SET_REQUIRED=('DataKeyPrefixDeleteSetCompleteOriginalCaptureExplicitBatchesAndLaterKeys','DataKeyPrefixDeleteSetLaterConflictsPreserveOriginalPartialProgress','DataKeyPrefixDeleteSetProcessedOriginalRepliesStopFurtherBatchesAndReplay','DataKeyPrefixDeleteSetCaptureBoundsAndOriginalLockedCandidates','DataKeyPrefixDeleteSetACLFilteredCaptureAndDeniedOriginalBatch')
PUBLIC_PREFIX_SET_REQUIRED=('PublicDataPrefixSetQualifiedCapturedPlansPartialProgressAndGenerationReplacement','PublicGridPrefixSetCompleteExplicitBatchesOriginalReceiptsAndLaterKeys','PublicGridPrefixSetLateConflictsKeepEarlierCommitsAndUnstartedKeys','PublicGridPrefixSetProcessedLateRepliesCancellationParkingCloseAndNoReplay','PublicGridPrefixSetOriginalInputParkingLateCaptureAndExplicitStop','PublicShellPrefixSetExplicitConfirmationsOriginalEvidenceAndDisconnect')
DATA_REQUIRED=('DataOriginalLiveBoundariesBytesUnsignedScopeAndReceipts','DataIndependentCountFieldExportAndOriginalRestrictedScope','DataActualProcessedReadReplyLossCancellationOriginalCloseAndReopen','DataCompleteValueBudgetRejectsCountAndExportBeforeEmission')

HTTP_WATCH_REQUIRED=('HTTPWatchOriginalBlockingAncestorTargetsAndValues','HTTPWatchNativeTimeoutUnrelatedChangeAndMissingTarget','HTTPWatchOriginalProcessedReplyFailuresNeverReconnect','HTTPWatchNativeBoundsCompleteTerminalAndUnsignedHeaders','HTTPWatchRestrictedACLPositiveObservationsAndOriginalDenial','HTTPWatchOriginalBlockingRetirementAndEvidenceOnlyRecovery','RuntimeHTTPWatchOriginalGenerationsCancellationAndEvidenceOnlyRecovery','RuntimeHTTPWatchRejectsForeignIdentityBeforeRecovery')

SESSION_RENEWAL_REQUIRED=('HTTPSessionRenewalOriginalUUIDScopeTTLAndBoundedReceipts','HTTPSessionRenewalProcessedReplyFaultsNeverReplay','HTTPSessionRenewalMissingSessionAndOriginalACLDenials','HTTPSessionRenewalBudgetParkingRetirementAndEvidenceTransfer','HTTPSessionRenewalGoneBetweenObservationAndPUT','RuntimeHTTPSessionRenewalOriginalGenerationAndEvidenceRecovery','RuntimeHTTPSessionRenewalForeignScopeRejectedBeforeRecovery')

PUBLIC_SESSION_RENEWAL_REQUIRED=('PublicHTTPSessionRenewalOriginalTTLConfirmationAndCompleteRecords','PublicHTTPSessionRenewalProcessedWritesUnknownBarriersAndNoReplay','PublicHTTPSessionRenewalBoundsMissingSessionAndOriginalACL','PublicHTTPSessionRenewalOriginalParkingRetirementAndEvidenceRecovery','PublicShellHTTPSessionRenewalOriginalTabsParkingDisconnectAndRecovery')

SESSION_TOOLS_REQUIRED=('SessionToolsCreateInfoDestroyOriginalPresenceAndUUID','SessionToolsAcquireReleaseNativeBooleanBytesAndFlags','SessionToolsProcessedWritesUnknownAndOriginalRecovery','SessionToolsOriginalACLAndNaturalExpiry')

PUBLIC_SESSION_TOOLS_REQUIRED=('PublicSessionToolsFormsOriginalConfirmationUUIDLocksAndFiles','PublicSessionToolsOriginalACLFilteredReadsAndDeniedWrites','PublicSessionToolsProcessedWritesParkingACKRecoveryAndNoReplay','PublicSessionToolsRenewalUnknownBarrierAndOriginalReceiptSelection','PublicShellSessionToolsOriginalFormsTabsReceiptsAndRecovery')

SESSION_BROWSER_REQUIRED=('SessionBrowserCapturedListsNodesDetailsAndOriginalCopies','SessionBrowserReadAfterUnknownDestroyKeepsOriginalWriteReceipt','PublicShellSessionBrowserOriginalMenusParkingReceiptsAndRecovery')

PUBLIC_SESSION_HELD_KEYS_REQUIRED=('PublicSessionHeldKeysOriginalWholeRootPagesDetailsAndRecovery','PublicSessionHeldKeysOriginalUnknownDestroyAndACLBoundary','PublicShellSessionHeldKeysOriginalSelectionParkingReceiptsAndRecovery')
SESSION_HELD_KEYS_REQUIRED=('SessionHeldKeysWholeRootOriginalOrderBytesFlagsAndBounds','SessionHeldKeysOriginalACLAndUnknownDestroyRecovery','SessionHeldKeysProcessedReadFaultsCancellationRetirementAndClose')
SESSION_PROJECTION_REQUIRED=('SessionProjectionOriginalListNodeInfoAndOptionalPresence','SessionProjectionACLAndOriginalUnknownRecovery','SessionProjectionProcessedReadFaultsCancellationRetirementAndRecovery')

def session_tools_image_names():
 kinds=('http-session-create','http-session-info','http-acquire','http-release','http-session-destroy')
 stages=tuple(kind+'-'+stage for kind in kinds for stage in ('form','generated','receipt'))+tuple(kind+'-confirmation' for kind in kinds if kind!='http-session-info')+('http-acquire-false','http-release-false','recovered','retained-records')+tuple(kind+'-unknown-'+mode for kind in kinds if kind!='http-session-info' for mode in ('lost','partial','http500','http408','cancel','disconnect','replace'))
 return {f'{lang}-{size}-workspace-session-tool-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in stages} | {f'{lang}-{size}-shell-session-tool-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in ('form','confirmation','receipt','recovered')}

def session_held_keys_image_names():
 stages=('native-selected-session','native-held-keys-list','native-held-last-page','native-held-key-detail','native-original-held-receipt','native-held-after-unknown','native-original-unknown-destroy','native-held-acl-or-original-boundary',*(f'shell-held-keys-{name}' for name in ('selected-session','list','detail','receipt','recovered')))
 return {f'{language}-{size}-{stage}.png' for language in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in stages}

def session_browser_image_names():
 stages={f'native-{kind}-{stage}' for kind in ('http-session-list','http-node-sessions') for stage in ('form','list','detail')}
 stages.update(f'shell-session-browser-{kind}-{stage}' for kind in ('http-session-list','http-node-sessions') for stage in ('form','list','detail'))
 stages.add('shell-session-browser-recovered')
 stages.update('native-unknown-write-'+kind+'-detail' for kind in ('http-session-list','http-node-sessions'))
 return {f'{lang}-{size}-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in stages}

def session_renewal_image_names():
 stages=tuple('workspace-session-renewal-'+stage for stage in ('form','confirmation','running','initial-read','stopped','cleared','overflow','missing','read-denied','write-denied','parked','recovered',*(prefix+mode for prefix in ('unknown-','after-read-') for mode in ('lost','partial','http500','http408','uuid','cancel','replace','parked'))))+tuple('shell-session-renewal-'+stage for stage in ('confirmation','running','stopped','recovered'))
 return {f'{lang}-{size}-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in stages}

def agent_maintenance_image_names():
 kinds=('http-agent-node-maintenance','http-agent-service-maintenance');enables=('true','false')
 stages={kind+'-'+enable+'-form' for kind in kinds for enable in enables}
 stages.update(kind+'-'+enable+'-'+reason+'-'+view for kind in kinds for enable in enables for reason in ('original','omitted','empty','long') for view in ('confirmation','receipt'))
 stages.update(kind+'-repeated-'+view for kind in kinds for view in ('confirmation','receipt'))
 stages.update(kind+'-'+enable+'-'+prefix+mode for kind in kinds for enable in enables for prefix in ('unknown-','recovered-') for mode in ('lost','partial','http400','http500','http408','invalid','cancel','disconnect','replace','parked'))
 stages.update(kind+'-acl-or-boundary' for kind in kinds);stages.add('http-agent-service-maintenance-missing')
 shell={target+'-'+operation+'-'+view for target in ('node','service') for operation in ('enable','disable') for view in ('form','confirmation','receipt')}|{'recovered'}
 return {f'{lang}-{size}-workspace-agent-maintenance-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in stages}|{f'{lang}-{size}-shell-agent-maintenance-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in shell}

def agent_ttl_image_names():
 kinds=('http-agent-check-pass','http-agent-check-warn','http-agent-check-fail','http-agent-check-update')
 stages={kind+'-form' for kind in kinds}
 stages.update(kind+'-'+value+'-'+view for kind in kinds for value in ('original','omitted','empty','null','long') if value!='null' or kind=='http-agent-check-update' for view in ('confirmation','receipt'))
 stages.update(kind+'-'+prefix+mode for kind in kinds for prefix in ('unknown-','recovered-') for mode in ('lost','partial','http400','http500','http408','invalid','cancel','disconnect','replace','parked'))
 stages.update(kind+'-'+value for kind in kinds for value in ('missing','non-ttl','acl-or-boundary'))
 stages.add('http-agent-check-update-unsupported-json')
 shell={value+'-'+view for value in ('pass','warn','fail','update') for view in ('form','confirmation','receipt')} | {'recovered'}
 return {f'{lang}-{size}-workspace-agent-ttl-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in stages} | {f'{lang}-{size}-shell-agent-ttl-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in shell}

def source_hashes():
 # Track neutral contracts, runtime and UI dependencies as well as the adapter.
 names=command(['git','ls-files','--cached','--others','--exclude-standard']).splitlines()
 names=set(n for n in names if n.endswith(('.go','.json','.py')) or n in ('go.mod','go.sum'))
 names.add(str(Path(__file__).resolve().relative_to(ROOT)))
 return {n:hashlib.sha256((ROOT/n).read_bytes()).hexdigest() for n in sorted(names) if (ROOT/n).is_file()}

def accepted_cases(text,required):
 accepted=re.findall(r'^--- PASS: TestConsulNative(\w+) \(',text,re.M)
 if sorted(accepted)!=sorted(required) or re.search(r'^\s*--- (FAIL|SKIP):|WARNING: DATA RACE|^FAIL(?:\s|$)|^panic:',text,re.M):
  raise RuntimeError('Native Consul mandatory PASS set failed')
 return accepted

CATALOG_READ_REQUIRED=('CatalogReadsOriginalFiltersNamesAndConfiguredScope','CatalogReadFaultsCancellationAndOriginalNoRetry','CatalogReadsRetainOriginalUnknownWriteAndPureRecovery')

CATALOG_PROJECTION_REQUIRED=('CatalogProjectionOriginalEndpointsMapsAndConfiguredScope','CatalogProjectionFaultsCancellationAndOriginalNoRetry','CatalogProjectionRetainsOriginalUnknownWriteAndPureRecovery')

PUBLIC_CATALOG_REQUIRED=('PublicCatalogBrowserOriginalFormsMapsDetailsAndFiles','PublicCatalogBrowserParkingUnknownWriteAndOriginalRecovery','PublicCatalogOriginalNullInvalidOptionsAndReadCancellation','PublicShellCatalogBrowserOriginalTabsMapsParkingDisconnectAndRecovery')

ACL_READ_REQUIRED=('ACLReadResourcesMembershipAndOriginalScope','ACLReadOriginalCredentialRedactionDenialAndAbsence','ACLReadFaultsCancellationAndRetirementNeverRetry')

ACL_DOCUMENT_REQUIRED=('ACLDocumentOriginalShapesAndPrivateRecovery','ACLDocumentSelfRedactionDenialAndAbsence','ACLDocumentFaultsCancelCloseAndRecoveryNeverReplay','ACLDocumentReadKeepsProcessedUnknownWriteIndependent')
PUBLIC_ACL_REQUIRED=('PrivateACLWorkspaceParkingRevealAndOriginalRecovery','PrivateACLWorkspaceReadKeepsProcessedUnknownWriteIndependent','PrivateACLBrowserRevealCopyExportOriginalReceipt','ACLReadFormSixResourcesAndOriginalRecovery','PublicShellACLOriginalFormsParkingFilesAndRecovery')

ACL_MUTATION_SERVICE_REQUIRED=('ACLMutationOriginalDefinitionsCloneAndEndpointScopes','ACLMutationProcessedRepliesRetainUnknownAndNeverReplay','ACLMutationDocumentOriginalReceiptsRecoveryAndProcessedFaults','ACLImpactOriginalReferencesAndUnchangedNativeIndices','ACLImpactOriginalFaultsCancellationAndNoReplay','ACLImpactAuthMethodActualIssuedTokensAndDeletion','ACLMutationPlanOriginalResourceWritesAndClone','ACLMutationPlanChangedReferenceBodiesStopOriginalWrite','ACLMutationPlanProcessedFaultsKeepOriginalPreviewAndRecheck','ACLCreationAllResourcesOriginalDependenciesAndAuthUpsert','ACLCreationCollisionsAndChangedDependenciesNeverDispatch','ACLCreationProcessedFaultsRetainOriginalUpsertAndGeneratedReceipts')
PUBLIC_ACL_MUTATION_REQUIRED=('PrivateACLMutationWorkspaceSecretsConfirmationAndPureRecovery','PrivateACLMutationWorkspaceProcessedFaultsParkingAndWriteBarrier','ACLMutationFormOriginalMenuConfirmationSecretReceiptAndFiles','ACLMutationWorkspaceOriginalPreviewFaultsAndParking','ACLMutationWorkspaceChangedTargetKeepsBothObservations','PublicShellACLMutationOriginalPlansParkingFilesAndRecovery','PublicACLMutationReadOnlyDenialKeepsOriginalPolicy')

def public_acl_mutation_acceptance_test_phases(packages,required,race=False,test_timeout=600):
 added=ACL_MUTATION_SERVICE_REQUIRED+PUBLIC_ACL_MUTATION_REQUIRED
 retained=tuple(name for name in required if name not in added)
 if len(set(required))!=len(required) or len(retained)+len(added)!=len(required) or any(name not in required for name in added):
  raise ValueError('Public ACL mutation acceptance is missing or duplicating a mandatory parent')
 return public_acl_acceptance_test_phases(packages,retained,race,test_timeout)+acceptance_test_phases(['./internal/services/consul'],ACL_MUTATION_SERVICE_REQUIRED,race,test_timeout)+acceptance_test_phases(['./internal/ui/kvworkspace','./internal/ui/shell'],PUBLIC_ACL_MUTATION_REQUIRED,race,test_timeout)

def acl_mutation_image_names(authenticated):
 workspace=('input','confirmation','impact','hidden-receipt','export-confirmation') if authenticated else ('input','failed-preview','export-confirmation')
 shell=('form','confirmation','impact','hidden','export-consent','recovered') if authenticated else ('form','failed-preview','export-consent','recovered')
 return {f'{language}-{size}-acl-write-{stage}.png' for language in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in workspace}|{f'{language}-{size}-shell-acl-write-{stage}.png' for language in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in shell}

def acl_mutation_image_evidence(folder,authenticated):
 from PIL import Image
 paths=sorted(folder.glob('*.png'))
 if {path.name for path in paths}!=acl_mutation_image_names(authenticated):
  raise RuntimeError('Original public ACL mutation/impact image set incomplete')
 evidence={}
 for path in paths:
  try:
   with Image.open(path) as image:
    image.load()
    expected=(1280,800) if '-1280x800-' in path.name else (720,520)
    if image.format!='PNG' or image.size!=expected:raise ValueError('ACL mutation image dimensions changed')
  except (OSError,ValueError) as error:
   raise RuntimeError('Original public ACL mutation image invalid: '+path.name) from error
  evidence[str(path.relative_to(ROOT))]=hashlib.sha256(path.read_bytes()).hexdigest()
 return evidence

def public_acl_acceptance_test_phases(packages,required,race=False,test_timeout=600):
 added=ACL_DOCUMENT_REQUIRED+PUBLIC_ACL_REQUIRED
 retained=tuple(name for name in required if name not in added)
 if len(set(required))!=len(required) or len(retained)+len(added)!=len(required) or any(name not in required for name in added):
  raise ValueError('Public ACL acceptance is missing or duplicating a mandatory parent')
 return acl_read_acceptance_test_phases(packages,retained,race,test_timeout)+acceptance_test_phases(['./internal/services/consul'],ACL_DOCUMENT_REQUIRED,race,test_timeout)+acceptance_test_phases(['./internal/ui/kvworkspace','./internal/ui/shell'],PUBLIC_ACL_REQUIRED,race,test_timeout)

def acl_browser_image_names():
 workspace=('hidden','export-consent')+tuple(resource+'-'+stage for resource in ('token','policy','role','auth-method','binding-rule','templated-policy') for stage in ('form','result'))
 return {f'{language}-{size}-workspace-acl-{stage}.png' for language in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in workspace}|{f'{language}-{size}-shell-acl-{stage}.png' for language in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in ('form','hidden','export-consent','recovered')}

def acl_browser_image_evidence(folder):
 from PIL import Image
 paths=sorted(folder.glob('*.png'))
 if {path.name for path in paths}!=acl_browser_image_names():
  raise RuntimeError('Original public ACL hidden/form/consent/recovery image set incomplete')
 evidence={}
 for path in paths:
  try:
   with Image.open(path) as image:
    image.load()
    expected=(1280,800) if '-1280x800-' in path.name else (720,520)
    if image.format!='PNG' or image.size!=expected:raise ValueError('ACL image dimensions changed')
  except (OSError,ValueError) as error:
   raise RuntimeError('Original public ACL image invalid: '+path.name) from error
  evidence[str(path.relative_to(ROOT))]=hashlib.sha256(path.read_bytes()).hexdigest()
 return evidence

def acl_read_acceptance_test_phases(packages,required,race=False,test_timeout=600):
 retained=tuple(name for name in required if name not in ACL_READ_REQUIRED)
 if len(retained)+len(ACL_READ_REQUIRED)!=len(required) or any(name not in required for name in ACL_READ_REQUIRED):
  raise ValueError('ACL read acceptance is missing a mandatory parent')
 return public_catalog_acceptance_test_phases(packages,retained,race,test_timeout)+acceptance_test_phases(['./internal/services/consul'],ACL_READ_REQUIRED,race,test_timeout)

def catalog_browser_image_names():
 stages=tuple(f'http-catalog-{kind}-native-{stage}' for kind in ('datacenters','nodes','services','service','node') for stage in ('form','receipt','list','detail'))+('native-catalog-after-unknown','native-catalog-recovered','native-catalog-node-null')+tuple('native-catalog-read-'+mode for mode in ('lost','partial','cancel'))+tuple('shell-catalog-http-catalog-'+kind+'-form' for kind in ('datacenters','nodes','services','service','node'))+('shell-catalog-list','shell-catalog-recovered')
 return {f'{language}-{size}-{stage}.png' for language in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in stages}

def public_catalog_acceptance_test_phases(packages,required,race=False,test_timeout=600):
 retained=tuple(name for name in required if name not in PUBLIC_CATALOG_REQUIRED)
 if len(retained)+len(PUBLIC_CATALOG_REQUIRED)!=len(required) or any(name not in required for name in PUBLIC_CATALOG_REQUIRED):
  raise ValueError('Public Catalog acceptance is missing a mandatory parent')
 return catalog_projection_acceptance_test_phases(packages,retained,race,test_timeout)+acceptance_test_phases(['./internal/ui/kvworkspace','./internal/ui/shell'],PUBLIC_CATALOG_REQUIRED,race,test_timeout)

def catalog_projection_acceptance_test_phases(packages,required,race=False,test_timeout=600):
 retained=tuple(name for name in required if name not in CATALOG_PROJECTION_REQUIRED)
 if len(retained)+len(CATALOG_PROJECTION_REQUIRED)!=len(required) or any(name not in required for name in CATALOG_PROJECTION_REQUIRED):
  raise ValueError('Catalog projection acceptance is missing a mandatory parent')
 return catalog_read_acceptance_test_phases(packages,retained,race,test_timeout)+acceptance_test_phases(['./internal/services/consul'],CATALOG_PROJECTION_REQUIRED,race,test_timeout)

def catalog_read_acceptance_test_phases(packages,required,race=False,test_timeout=600):
 retained=tuple(name for name in required if name not in CATALOG_READ_REQUIRED)
 if len(retained)+len(CATALOG_READ_REQUIRED)!=len(required) or any(name not in required for name in CATALOG_READ_REQUIRED):
  raise ValueError('Catalog read acceptance is missing a mandatory parent')
 return public_agent_maintenance_acceptance_test_phases(packages,retained,race,test_timeout)+acceptance_test_phases(['./internal/services/consul'],CATALOG_READ_REQUIRED,race,test_timeout)

def public_agent_maintenance_acceptance_test_phases(packages,required,race=False,test_timeout=600):
 retained=tuple(name for name in required if name not in PUBLIC_AGENT_MAINTENANCE_REQUIRED)
 if len(retained)+len(PUBLIC_AGENT_MAINTENANCE_REQUIRED)!=len(required) or any(name not in required for name in PUBLIC_AGENT_MAINTENANCE_REQUIRED):
  raise ValueError('Public Agent maintenance acceptance is missing a mandatory parent')
 return agent_maintenance_acceptance_test_phases(packages,retained,race,test_timeout)+acceptance_test_phases(['./internal/ui/kvworkspace','./internal/ui/shell'],PUBLIC_AGENT_MAINTENANCE_REQUIRED,race,test_timeout)

def agent_maintenance_acceptance_test_phases(packages,required,race=False,test_timeout=600):
 catalog='AgentMaintenanceCatalogConvergenceUsesOriginalAgentToken'
 retained=tuple(name for name in required if name not in AGENT_MAINTENANCE_REQUIRED)
 if len(retained)+len(AGENT_MAINTENANCE_REQUIRED)!=len(required) or any(name not in required for name in AGENT_MAINTENANCE_REQUIRED):
  raise ValueError('Agent maintenance acceptance is missing a mandatory parent')
 # Observe catalog restoration before prior TTL tests make the node critical.
 # Every accepted public TTL/check/service/browser parent still runs exactly once.
 return acceptance_test_phases(['./internal/services/consul'],(catalog,),race,test_timeout)+agent_ttl_acceptance_test_phases(packages,retained,race,test_timeout,True)+acceptance_test_phases(['./internal/services/consul','./internal/services/dbruntime'],tuple(name for name in AGENT_MAINTENANCE_REQUIRED if name!=catalog),race,test_timeout)

def agent_ttl_acceptance_test_phases(packages,required,race=False,test_timeout=600,public=False):
 if public:
  retained=tuple(c for c in required if c not in PUBLIC_AGENT_TTL_REQUIRED)
  if len(required)!=64 or len(set(required))!=64 or len(retained)!=60 or not set(PUBLIC_AGENT_TTL_REQUIRED).issubset(required):raise ValueError("Public TTL phases require all 60 retained parents and all four public parents exactly once")
  return agent_ttl_acceptance_test_phases(packages,retained,race,test_timeout)+acceptance_test_phases(["./internal/ui/kvworkspace","./internal/ui/shell"],PUBLIC_AGENT_TTL_REQUIRED,race,test_timeout)
 retained=tuple(c for c in required if c not in AGENT_TTL_REQUIRED)
 if len(required)!=60 or len(set(required))!=60 or len(retained)!=55 or not set(AGENT_TTL_REQUIRED).issubset(required):raise ValueError('TTL foundation phases require every accepted public/check and native TTL parent exactly once')
 return agent_check_acceptance_test_phases(packages,retained,race,test_timeout,True)+acceptance_test_phases(['./internal/services/consul','./internal/services/dbruntime'],AGENT_TTL_REQUIRED,race,test_timeout)

def agent_check_acceptance_test_phases(packages,required,race=False,test_timeout=600,public=False):
 # Critical node checks affect passing-only health reads. Retain all original
 # browser/service regressions first, then native check writes and public UI.
 new=AGENT_CHECK_REQUIRED+(PUBLIC_AGENT_CHECK_REQUIRED if public else ())
 retained=tuple(c for c in required if c not in new)
 if len(required)!=(55 if public else 51) or len(set(required))!=len(required) or len(retained)!=46 or not set(new).issubset(required):raise ValueError('Check phases require all original public and native parents exactly once')
 phases=acceptance_test_phases(packages,retained,race,test_timeout)+acceptance_test_phases(['./internal/services/consul','./internal/services/dbruntime'],AGENT_CHECK_REQUIRED,race,test_timeout)
 if public:phases+=acceptance_test_phases(['./internal/ui/kvworkspace','./internal/ui/shell'],PUBLIC_AGENT_CHECK_REQUIRED,race,test_timeout)
 return phases


def acceptance_test_phases(packages,required,race=False,test_timeout=600,session_held_keys=False,public_session_held_keys=False,session_destroy_impact=False,session_destroy_plan=False,public_session_destroy=False,health_projection=False):
 def arguments(phase_packages,cases):
  return ['go','test','-p=1']+(['-race'] if race else [])+list(phase_packages)+['-run','^('+'|'.join('TestConsulNative'+c for c in cases)+')$','-count=1','-v','-timeout='+str(test_timeout)+'s']
 if health_projection:
  retained=tuple(c for c in required if c not in HEALTH_PROJECTION_REQUIRED)
  if len(retained)+len(HEALTH_PROJECTION_REQUIRED)!=len(required) or not set(HEALTH_PROJECTION_REQUIRED).issubset(required):raise ValueError("Health projection phases require every original parent exactly once")
  return [arguments(["./internal/services/consul"],HEALTH_PROJECTION_REQUIRED)]+acceptance_test_phases(packages,retained,race,test_timeout,session_held_keys,public_session_held_keys,session_destroy_impact,session_destroy_plan,public_session_destroy)
 if public_session_destroy:
  retained=tuple(c for c in required if c not in PUBLIC_SESSION_DESTROY_REQUIRED)
  if len(retained)+len(PUBLIC_SESSION_DESTROY_REQUIRED)!=len(required) or not set(PUBLIC_SESSION_DESTROY_REQUIRED).issubset(required):raise ValueError("Public destroy phases require every original parent exactly once")
  return [arguments(["./internal/ui/kvworkspace","./internal/ui/shell"],PUBLIC_SESSION_DESTROY_REQUIRED)]+acceptance_test_phases(packages,retained,race,test_timeout,session_held_keys,public_session_held_keys,session_destroy_impact,session_destroy_plan)
 if session_destroy_plan:
  retained=tuple(c for c in required if c not in SESSION_DESTROY_PLAN_REQUIRED)
  if len(retained)+len(SESSION_DESTROY_PLAN_REQUIRED)!=len(required) or not set(SESSION_DESTROY_PLAN_REQUIRED).issubset(required):raise ValueError("Destroy plan phases require every original parent exactly once")
  return [arguments(["./internal/services/consul"],SESSION_DESTROY_PLAN_REQUIRED)]+acceptance_test_phases(packages,retained,race,test_timeout,session_held_keys,public_session_held_keys,session_destroy_impact)
 if session_destroy_impact:
  retained=tuple(c for c in required if c not in SESSION_DESTROY_IMPACT_REQUIRED)
  if len(retained)+len(SESSION_DESTROY_IMPACT_REQUIRED)!=len(required) or not set(SESSION_DESTROY_IMPACT_REQUIRED).issubset(required):raise ValueError("Impact phases require every original parent exactly once")
  return [arguments(["./internal/services/consul"],SESSION_DESTROY_IMPACT_REQUIRED)]+acceptance_test_phases(packages,retained,race,test_timeout,session_held_keys,public_session_held_keys)
 if not session_held_keys:return [arguments(packages,required)]
 public_cases=PUBLIC_SESSION_HELD_KEYS_REQUIRED if public_session_held_keys else ()
 early=SESSION_HELD_KEYS_REQUIRED+public_cases
 retained=tuple(c for c in required if c not in early)
 if len(retained)+len(early)!=len(required) or not set(early).issubset(required):raise ValueError('Held-key phases require each original parent exactly once')
 phases=[arguments(['./internal/services/consul'],SESSION_HELD_KEYS_REQUIRED)]
 if public_cases:phases.append(arguments(['./internal/ui/kvworkspace','./internal/ui/shell'],public_cases))
 return phases+[arguments(packages,retained)]

PUBLIC_HTTP_WATCH_REQUIRED=('PublicHTTPWatchOriginalRangesIndexedRecordsParkingAndExplicitStop','PublicHTTPWatchCompleteOverflowAndIndependentUnknownWrite','PublicHTTPWatchOriginalTerminalHeadersReadFailuresAndACL','PublicShellHTTPWatchOriginalTabsRecordsParkingDisconnectAndRecovery')

def command(args):
 r=subprocess.run(args,cwd=ROOT,capture_output=True,text=True)
 if r.returncode: raise RuntimeError('Owned Consul command failed: '+args[0])
 return r.stdout.strip()

def remove_owned(identifier,nonce):
 if not re.fullmatch(r'[0-9a-f]{64}',identifier):raise RuntimeError('Missing original immutable container ID')
 r=subprocess.run(['docker','inspect',identifier],capture_output=True,text=True)
 if r.returncode:
  if 'No such' in r.stderr:return
  raise RuntimeError('Cannot inspect original fixture')
 data=json.loads(r.stdout)
 if len(data)!=1 or data[0]['Id']!=identifier or data[0]['Config']['Labels'].get('nyadb.fixture')!='consul' or data[0]['Config']['Labels'].get('nyadb.fixture.nonce')!=nonce:raise RuntimeError('Foreign original fixture ownership')
 command(['docker','rm','-f',identifier])

def certs(folder):
 def ssl(*args):subprocess.run(['openssl',*args],cwd=folder,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,check=True)
 ssl('req','-x509','-newkey','rsa:2048','-nodes','-keyout','ca.key','-out','ca.crt','-days','2','-subj','/CN=NyaDB owned Consul CA','-addext','keyUsage=critical,keyCertSign,cRLSign')
 for name,usage in [('server','serverAuth,clientAuth'),('client','clientAuth')]:
  ssl('req','-newkey','rsa:2048','-nodes','-keyout',name+'.key','-out',name+'.csr','-subj','/CN='+('localhost' if name=='server' else 'owned-client'))
  (folder/(name+'.ext')).write_text('extendedKeyUsage='+usage+'\nsubjectAltName=DNS:localhost,IP:127.0.0.1\n')
  ssl('x509','-req','-in',name+'.csr','-CA','ca.crt','-CAkey','ca.key','-CAcreateserial','-out',name+'.crt','-days','2','-extfile',name+'.ext')
 for p in folder.iterdir():p.chmod(0o600)

def verify(mode='anonymous',race=False,version='2.0.2',probe=False,session=False,public=False,tools=False,data_session=False,public_grid=False,data_mutations=False,txn_max_request=524288,mutation_limit_probe=False,public_grid_writes=False,grid_write_probe=False,key_copy=False,copy_probe=False,copy_limit_probe=False,public_key_copy=False,public_copy_probe=False,prefix_delete=False,prefix_delete_probe=False,public_prefix_delete=False,public_prefix_probe=False,prefix_delete_set=False,prefix_set_probe=False,test_timeout=600,memory_mib=768,public_prefix_set=False,public_prefix_set_probe=False,http_watch=False,http_watch_probe=False,public_http_watch=False,public_http_watch_probe=False,session_renewal=False,session_renewal_probe=False,public_session_renewal=False,public_session_renewal_probe=False,session_tools=False,session_tools_probe=False,public_session_tools=False,public_session_tools_probe=False,session_projection=False,session_projection_probe=False,session_browser_probe=False,session_browser=False,session_held_keys=False,session_held_keys_probe=False,public_session_held_keys=False,public_session_held_keys_probe=False,session_destroy_impact=False,session_destroy_impact_probe=False,session_destroy_plan=False,session_destroy_plan_probe=False,public_session_destroy=False,public_session_destroy_probe=False,health_projection=False,health_projection_probe=False,health_browser_probe=False,health_watch_probe=False,health_watch_ui_probe=False,agent_read_probe=False,agent_projection_probe=False,agent_browser_probe=False,agent_service_evidence_probe=False,agent_service_probe=False,agent_service_native_probe=False,public_agent_service_probe=False,agent_check_probe=False,public_agent_check_probe=False,agent_ttl_probe=False,public_agent_ttl_probe=False,agent_maintenance_probe=False,agent_maintenance_no_agent_token_probe=False,public_agent_maintenance_probe=False,catalog_read_probe=False,catalog_projection_probe=False,public_catalog_probe=False,acl_read_probe=False,public_acl_probe=False,public_acl_mutation_probe=False):
 if public_acl_mutation_probe and any(value for name,value in locals().items() if isinstance(value,bool) and name not in ('race','public_acl_mutation_probe')):
  raise ValueError('Public ACL mutation acceptance requires its dedicated original scope')
 if public_acl_probe and any(value for name,value in locals().items() if isinstance(value,bool) and name not in ('race','public_acl_probe')):
  raise ValueError('Public ACL probe cannot claim other acceptance scopes')
 if acl_read_probe and any(value for name,value in locals().items() if isinstance(value,bool) and name not in ('race','acl_read_probe')):
  raise ValueError('ACL read foundation requires its dedicated original scope')
 if public_catalog_probe and any(value for name,value in locals().items() if isinstance(value,bool) and name not in ('race','public_catalog_probe')):
  raise ValueError('Public Catalog requires its dedicated original scope')
 if catalog_projection_probe and any(value for name,value in locals().items() if isinstance(value,bool) and name not in ('race','catalog_projection_probe')):
  raise ValueError('Catalog projection requires its dedicated original scope')
 if catalog_read_probe and any(value for name,value in locals().items() if isinstance(value,bool) and name not in ('race','catalog_read_probe')):
  raise ValueError('Catalog read foundation requires its dedicated original scope')
 if public_agent_maintenance_probe and any(value for name,value in locals().items() if isinstance(value,bool) and name not in ('race','public_agent_maintenance_probe')):
  raise ValueError('Public Agent maintenance requires its dedicated original scope')
 if agent_maintenance_probe and any(value for name,value in locals().items() if isinstance(value,bool) and name not in ('race','agent_maintenance_probe')):
  raise ValueError('Agent maintenance foundation requires its dedicated original scope')
 if agent_maintenance_no_agent_token_probe and (mode!='token' or any(value for name,value in locals().items() if isinstance(value,bool) and name not in ('race','agent_maintenance_no_agent_token_probe'))):
  raise ValueError('No-Agent-token maintenance evidence requires its separate token-only scope')
 if public_agent_ttl_probe and any(value for name,value in locals().items() if isinstance(value,bool) and name not in ('race','public_agent_ttl_probe')):
  raise ValueError('Public Agent TTL requires its dedicated original scope')
 if agent_ttl_probe and any(value for name,value in locals().items() if isinstance(value,bool) and name not in ('race','agent_ttl_probe')):
  raise ValueError('Agent TTL foundation requires its dedicated original scope')
 if public_agent_check_probe and any(value for name,value in locals().items() if isinstance(value,bool) and name not in ('race','public_agent_check_probe')):
  raise ValueError('Public Agent check acceptance requires its dedicated original scope')
 if agent_check_probe and any(value for name,value in locals().items() if isinstance(value,bool) and name not in ('race','agent_check_probe')):
  raise ValueError('Agent check foundation requires its dedicated original scope')
 if public_agent_service_probe and any(value for name,value in locals().items() if isinstance(value,bool) and name not in ('race','public_agent_service_probe')):
  raise ValueError('Public Agent service acceptance requires its dedicated original scope')
 if (agent_service_probe or agent_service_native_probe) and any(value for name,value in locals().items() if isinstance(value,bool) and name not in ('race','agent_service_probe' if agent_service_probe else 'agent_service_native_probe')):
  raise ValueError('Agent service foundation requires its original dedicated scope')
 if public_acl_mutation_probe:
  public_acl_probe=True
 if public_acl_probe:
  acl_read_probe=True
 if acl_read_probe:
  public_catalog_probe=True
 if public_catalog_probe:
  catalog_projection_probe=True
 if catalog_projection_probe:
  catalog_read_probe=True
 if catalog_read_probe:
  public_agent_maintenance_probe=True
 if public_agent_maintenance_probe:
  agent_maintenance_probe=True
 if agent_maintenance_probe:
  public_agent_ttl_probe=True
 if public_agent_ttl_probe:
  agent_ttl_probe=True
 if agent_ttl_probe:
  public_agent_check_probe=True
 if public_agent_check_probe:
  agent_check_probe=True
 if agent_check_probe:
  public_agent_service_probe=True
 if public_agent_service_probe:
  agent_service_probe=True
 if agent_service_probe:
  agent_browser_probe=True
 if agent_service_evidence_probe and any(value for name,value in locals().items() if isinstance(value,bool) and name not in ('race','agent_service_evidence_probe')):
  raise ValueError('Agent service evidence probe cannot claim other acceptance scopes')
 prior_flags=(probe,session,public,tools,data_session,public_grid,data_mutations,mutation_limit_probe,public_grid_writes,grid_write_probe,key_copy,copy_probe,copy_limit_probe,public_key_copy,public_copy_probe,prefix_delete,prefix_delete_probe,public_prefix_delete,public_prefix_probe,prefix_delete_set,prefix_set_probe,public_prefix_set,public_prefix_set_probe,http_watch,http_watch_probe,public_http_watch,public_http_watch_probe)
 public_keys_prior=(session_held_keys,session_held_keys_probe,session_browser,session_browser_probe,session_projection,session_projection_probe,public_session_tools,public_session_tools_probe,session_tools,session_tools_probe,public_session_renewal,public_session_renewal_probe,session_renewal,session_renewal_probe,*prior_flags)
 plan_prior=(session_destroy_impact,session_destroy_impact_probe,public_session_held_keys,public_session_held_keys_probe,*public_keys_prior)
 health_prior=(public_session_destroy,public_session_destroy_probe,session_destroy_plan,session_destroy_plan_probe,*plan_prior)
 if agent_browser_probe:
  if agent_projection_probe or agent_read_probe or health_watch_ui_probe or health_watch_probe or health_browser_probe or health_projection or health_projection_probe or any(health_prior):raise ValueError("Agent browser scope requires only original Agent and health regression parents")
  agent_projection_probe=True
 if agent_projection_probe:
  if agent_read_probe or health_watch_ui_probe or health_watch_probe or health_browser_probe or health_projection or health_projection_probe or any(health_prior):raise ValueError("Agent projection scope requires only original Agent and health regression parents")
  agent_read_probe=True
 if agent_read_probe:
  if health_watch_ui_probe or health_watch_probe or health_browser_probe or health_projection or health_projection_probe or any(health_prior):raise ValueError("Agent read scope requires only original Agent and health regression parents")
  health_watch_ui_probe=True
 if health_watch_ui_probe and (health_watch_probe or health_browser_probe or health_projection or health_projection_probe or any(health_prior)):raise ValueError("Health Watch UI focused scope cannot claim unrelated complete matrices")
 if health_watch_probe and (health_watch_ui_probe or health_browser_probe or health_projection or health_projection_probe or any(health_prior)):raise ValueError("Health watch focused scope cannot claim unrelated complete feature matrices")
 if health_browser_probe and (health_projection or health_projection_probe or any(health_prior)):raise ValueError("Health browser focused acceptance cannot claim unrelated full-stage regression")
 if health_projection_probe and (health_projection or any(health_prior)):raise ValueError("Health projection diagnostic cannot claim complete public destroy acceptance")
 if health_projection and any((probe,mutation_limit_probe,grid_write_probe,copy_probe,copy_limit_probe,public_copy_probe,prefix_delete_probe,public_prefix_probe,prefix_set_probe,public_prefix_set_probe,http_watch_probe,public_http_watch_probe,session_renewal_probe,public_session_renewal_probe,session_tools_probe,public_session_tools_probe,session_projection_probe,session_browser_probe,session_held_keys_probe,public_session_held_keys_probe,session_destroy_impact_probe,session_destroy_plan_probe,public_session_destroy_probe,health_projection_probe)):raise ValueError("Health projection requires all retained public destroy parents")
 public_session_destroy=public_session_destroy or health_projection
 if public_session_destroy_probe and (public_session_destroy or session_destroy_plan or session_destroy_plan_probe or any(plan_prior)):raise ValueError("Public destroy diagnostic cannot claim private or full acceptance")
 if public_session_destroy and (public_session_destroy_probe or session_destroy_plan_probe):raise ValueError("Public destroy requires every original private plan parent")
 session_destroy_plan=session_destroy_plan or public_session_destroy
 if session_destroy_plan_probe and (session_destroy_plan or any(plan_prior)):raise ValueError("Destroy plan diagnostic cannot claim full impact acceptance")
 if session_destroy_plan and (session_destroy_plan_probe or session_destroy_impact_probe or public_session_held_keys_probe or any((session_held_keys_probe,session_browser_probe,session_projection_probe,public_session_tools_probe,session_tools_probe,public_session_renewal_probe,session_renewal_probe,probe,mutation_limit_probe,grid_write_probe,copy_probe,copy_limit_probe,public_copy_probe,prefix_delete_probe,public_prefix_probe,prefix_set_probe,public_prefix_set_probe,http_watch_probe,public_http_watch_probe))):raise ValueError("Destroy plan requires every retained impact parent")
 session_destroy_impact=session_destroy_impact or session_destroy_plan
 if session_destroy_impact_probe and (session_destroy_impact or public_session_held_keys or public_session_held_keys_probe or any(public_keys_prior)):raise ValueError("Impact diagnostic cannot claim complete held-key acceptance")
 if session_destroy_impact and (session_destroy_impact_probe or public_session_held_keys_probe or any((session_held_keys_probe,session_browser_probe,session_projection_probe,public_session_tools_probe,session_tools_probe,public_session_renewal_probe,session_renewal_probe,probe,mutation_limit_probe,grid_write_probe,copy_probe,copy_limit_probe,public_copy_probe,prefix_delete_probe,public_prefix_probe,prefix_set_probe,public_prefix_set_probe,http_watch_probe,public_http_watch_probe))):raise ValueError("Impact requires every retained public held-key parent")
 public_session_held_keys=public_session_held_keys or session_destroy_impact
 if public_session_held_keys_probe and (public_session_held_keys or any(public_keys_prior)):raise ValueError('Public held-key diagnostic cannot claim complete original browser acceptance')
 if public_session_held_keys and (public_session_held_keys_probe or session_held_keys_probe):raise ValueError('Public held keys require every retained foundation and browser parent')
 session_held_keys=session_held_keys or public_session_held_keys
 held_conflicts=(session_browser_probe,session_projection_probe,public_session_tools_probe,session_tools_probe,public_session_renewal_probe,session_renewal_probe,probe,mutation_limit_probe,grid_write_probe,copy_probe,copy_limit_probe,public_copy_probe,prefix_delete_probe,public_prefix_probe,prefix_set_probe,public_prefix_set_probe,http_watch_probe,public_http_watch_probe)
 if session_held_keys_probe and (session_held_keys or session_browser or session_projection or session_projection_probe or public_session_tools or public_session_tools_probe or session_tools or session_tools_probe or public_session_renewal or public_session_renewal_probe or session_renewal or session_renewal_probe or any(prior_flags) or session_browser_probe):raise ValueError('Held-key diagnostic cannot claim complete browser acceptance')
 if session_held_keys and (session_held_keys_probe or any(held_conflicts)):raise ValueError('Held-key foundation requires every retained public Session browser parent')
 session_browser=session_browser or session_held_keys
 if session_browser and (session_browser_probe or session_projection_probe or public_session_tools_probe or session_tools_probe or public_session_renewal_probe or session_renewal_probe or any((probe,mutation_limit_probe,grid_write_probe,copy_probe,copy_limit_probe,public_copy_probe,prefix_delete_probe,public_prefix_probe,prefix_set_probe,public_prefix_set_probe,http_watch_probe,public_http_watch_probe))):raise ValueError('Session browser requires all retained projection and public tools parents')
 session_projection=session_projection or session_browser
 if session_browser_probe and (session_projection or session_projection_probe or public_session_tools or public_session_tools_probe or session_tools or session_tools_probe or public_session_renewal or public_session_renewal_probe or session_renewal or session_renewal_probe or any(prior_flags)):raise ValueError('Session browser diagnostic cannot claim complete acceptance')
 projection_conflicts=(public_session_tools_probe,session_tools_probe,public_session_renewal_probe,session_renewal_probe,probe,mutation_limit_probe,grid_write_probe,copy_probe,copy_limit_probe,public_copy_probe,prefix_delete_probe,public_prefix_probe,prefix_set_probe,public_prefix_set_probe,http_watch_probe,public_http_watch_probe)
 if session_projection_probe and (session_projection or public_session_tools or session_tools or public_session_renewal or session_renewal or any(prior_flags) or any(projection_conflicts)):raise ValueError('Session projection diagnostic cannot claim complete acceptance')
 if session_projection and any(projection_conflicts):raise ValueError('Session projection requires all retained public Session tools parents')
 public_session_tools=public_session_tools or session_projection
 if public_session_tools_probe and (public_session_tools or session_tools or session_tools_probe or public_session_renewal or public_session_renewal_probe or session_renewal or session_renewal_probe or any(prior_flags)):raise ValueError('Public Session tools diagnostic cannot claim complete acceptance')
 if public_session_tools and session_tools_probe:raise ValueError('Public Session tools require complete foundation acceptance')
 session_tools=session_tools or public_session_tools
 if session_tools_probe and (session_tools or public_session_renewal or public_session_renewal_probe or session_renewal or session_renewal_probe or any(prior_flags)):raise ValueError('Session tools diagnostic cannot claim complete acceptance')
 if session_tools and any((probe,public_session_renewal_probe,session_renewal_probe,mutation_limit_probe,grid_write_probe,copy_probe,copy_limit_probe,public_copy_probe,prefix_delete_probe,public_prefix_probe,prefix_set_probe,public_prefix_set_probe,http_watch_probe,public_http_watch_probe)):raise ValueError('Session tools foundation requires every retained public renewal parent')
 public_session_renewal=public_session_renewal or session_tools
 if public_session_renewal_probe and (public_session_renewal or session_renewal or session_renewal_probe or any(prior_flags)):raise ValueError('Public Session renewal diagnostic cannot claim complete acceptance')
 if public_session_renewal and session_renewal_probe:raise ValueError('Public Session renewal requires complete foundation acceptance')
 session_renewal=session_renewal or public_session_renewal
 if session_renewal_probe and (session_renewal or any(prior_flags)):raise ValueError('Session renewal diagnostic cannot claim complete acceptance')
 if session_renewal and any((probe,mutation_limit_probe,grid_write_probe,copy_probe,copy_limit_probe,public_copy_probe,prefix_delete_probe,public_prefix_probe,prefix_set_probe,public_prefix_set_probe,http_watch_probe,public_http_watch_probe)):raise ValueError('Session renewal foundation requires retained public Watch acceptance')
 public_http_watch=public_http_watch or session_renewal
 if not isinstance(test_timeout,int) or not 60<=test_timeout<=3600 or memory_mib not in (768,2048):raise ValueError('Invalid original fixture capacity or test deadline')
 if public_http_watch_probe and (public_http_watch or http_watch or http_watch_probe or any((probe,session,public,tools,data_session,public_grid,data_mutations,mutation_limit_probe,public_grid_writes,grid_write_probe,key_copy,copy_probe,copy_limit_probe,public_key_copy,public_copy_probe,prefix_delete,prefix_delete_probe,public_prefix_delete,public_prefix_probe,prefix_delete_set,prefix_set_probe,public_prefix_set,public_prefix_set_probe))):raise ValueError('Public HTTP Watch diagnostic cannot claim complete acceptance')
 http_watch=http_watch or public_http_watch
 all_prior=(probe,session,public,tools,data_session,public_grid,data_mutations,mutation_limit_probe,public_grid_writes,grid_write_probe,key_copy,copy_probe,copy_limit_probe,public_key_copy,public_copy_probe,prefix_delete,prefix_delete_probe,public_prefix_delete,public_prefix_probe,prefix_delete_set,prefix_set_probe,public_prefix_set,public_prefix_set_probe)
 if http_watch_probe and (http_watch or any(all_prior)):raise ValueError('HTTP Watch diagnostic cannot claim complete acceptance')
 if http_watch and any((probe,mutation_limit_probe,grid_write_probe,copy_probe,copy_limit_probe,public_copy_probe,prefix_delete_probe,public_prefix_probe,prefix_set_probe,public_prefix_set_probe)):raise ValueError('HTTP Watch foundation requires all retained public parents')
 public_prefix_set=public_prefix_set or http_watch
 previous_flags=(probe,session,public,tools,data_session,public_grid,data_mutations,mutation_limit_probe,public_grid_writes,grid_write_probe,key_copy,copy_probe,copy_limit_probe,public_key_copy,public_copy_probe,prefix_delete,prefix_delete_probe,public_prefix_delete,public_prefix_probe)
 if public_prefix_set_probe and (public_prefix_set or prefix_delete_set or prefix_set_probe or any(previous_flags)):raise ValueError('Public explicit batch diagnostic cannot claim complete acceptance')
 if public_prefix_set and any((probe,mutation_limit_probe,grid_write_probe,copy_probe,copy_limit_probe,public_copy_probe,prefix_delete_probe,public_prefix_probe,prefix_set_probe)):raise ValueError('Public explicit batches require every retained original parent')
 prefix_delete_set=prefix_delete_set or public_prefix_set
 public_grid=public_grid or public_prefix_set_probe

 if prefix_set_probe and (prefix_delete_set or any(previous_flags)):raise ValueError('Explicit batch diagnostic cannot claim complete acceptance')
 if prefix_delete_set and any((probe,mutation_limit_probe,grid_write_probe,copy_probe,copy_limit_probe,public_copy_probe,prefix_delete_probe,public_prefix_probe)):raise ValueError('Explicit batch acceptance requires all retained public prefix parents')
 public_prefix_delete=public_prefix_delete or prefix_delete_set
 if public_prefix_probe and any((probe,session,public,tools,data_session,public_grid,data_mutations,mutation_limit_probe,public_grid_writes,grid_write_probe,key_copy,copy_probe,copy_limit_probe,public_key_copy,public_copy_probe,prefix_delete,prefix_delete_probe,public_prefix_delete)):raise ValueError('Public prefix diagnostic cannot claim full acceptance')
 if public_prefix_delete and any((probe,mutation_limit_probe,grid_write_probe,copy_probe,copy_limit_probe,public_copy_probe,prefix_delete_probe)):raise ValueError('Public prefix acceptance requires all retained original parents')
 prefix_delete=prefix_delete or public_prefix_delete
 if prefix_delete_probe and any((probe,session,public,tools,data_session,public_grid,data_mutations,mutation_limit_probe,public_grid_writes,grid_write_probe,key_copy,copy_probe,copy_limit_probe,public_key_copy,public_copy_probe,prefix_delete)):raise ValueError('Prefix candidate diagnostic cannot claim complete acceptance')
 if prefix_delete and any((probe,mutation_limit_probe,grid_write_probe,copy_probe,copy_limit_probe,public_copy_probe)):raise ValueError('Prefix candidate acceptance requires every retained public copy parent')
 public_key_copy=public_key_copy or prefix_delete
 if public_copy_probe and any((probe,session,public,tools,data_session,public_grid,data_mutations,mutation_limit_probe,public_grid_writes,grid_write_probe,key_copy,copy_probe,copy_limit_probe,public_key_copy)):raise ValueError('Public copy diagnostic cannot claim complete public acceptance')
 if public_key_copy and any((probe,mutation_limit_probe,grid_write_probe,copy_probe,copy_limit_probe)):raise ValueError('Public copy acceptance requires every retained original parent')
 key_copy=key_copy or public_key_copy
 public_grid_writes=public_grid_writes or public_copy_probe or public_prefix_probe
 if sum((key_copy,copy_probe,copy_limit_probe))>1:raise ValueError('Conditional copy acceptance and diagnostics are distinct scopes')
 if (copy_probe or copy_limit_probe) and any((probe,session,public,tools,data_session,public_grid,data_mutations,mutation_limit_probe,public_grid_writes,grid_write_probe)):raise ValueError('Conditional copy diagnostic cannot claim public regression acceptance')
 if key_copy and (probe or mutation_limit_probe or grid_write_probe):raise ValueError('Conditional copy acceptance requires the complete public regression')
 if copy_limit_probe and txn_max_request!=524288:raise ValueError('Conditional copy limit probe requires the original server default')
 public_grid_writes=public_grid_writes or key_copy
 public_grid=public_grid or public_grid_writes or grid_write_probe
 data_mutations=data_mutations or public_grid_writes or grid_write_probe
 data_session=data_session or public_grid or data_mutations
 tools=tools or data_session
 public=public or tools
 if probe and (session or public):raise ValueError('Diagnostic probe cannot claim session or public acceptance')
 session=session or public
 build=ROOT/'build/consul';build.mkdir(parents=True,exist_ok=True)
 nonce=uuid.uuid4().hex;path=build/('owned-fixture-'+nonce+'.json');log=path.with_suffix('.log')
 required=REQUIRED+(DOCUMENT_REQUIRED+RUNTIME_REQUIRED if session else ())+(PUBLIC_REQUIRED if public else ())+(TOOLS_REQUIRED if tools else ())+(DATA_REQUIRED if data_session else ())+(MUTATION_REQUIRED if data_mutations else ())+(GRID_REQUIRED if public_grid else ())+(WRITE_GRID_REQUIRED if public_grid_writes or grid_write_probe else ())
 if key_copy:required+=COPY_REQUIRED
 if public_key_copy:required+=PUBLIC_COPY_REQUIRED
 if prefix_delete:required+=PREFIX_DELETE_REQUIRED
 if public_prefix_delete:required+=PUBLIC_PREFIX_DELETE_REQUIRED
 if prefix_delete_set:required+=PREFIX_SET_REQUIRED
 if public_prefix_set:required+=PUBLIC_PREFIX_SET_REQUIRED
 if http_watch:required+=HTTP_WATCH_REQUIRED
 if public_http_watch:required+=PUBLIC_HTTP_WATCH_REQUIRED
 if session_renewal:required+=SESSION_RENEWAL_REQUIRED
 if public_session_renewal:required+=PUBLIC_SESSION_RENEWAL_REQUIRED
 if session_tools:required+=SESSION_TOOLS_REQUIRED
 if public_session_tools:required+=PUBLIC_SESSION_TOOLS_REQUIRED
 if session_projection:required+=SESSION_PROJECTION_REQUIRED
 if session_projection_probe:required=SESSION_PROJECTION_REQUIRED
 if session_browser:required+=SESSION_BROWSER_REQUIRED
 if session_browser_probe:required=SESSION_BROWSER_REQUIRED
 if session_held_keys:required+=SESSION_HELD_KEYS_REQUIRED
 if session_held_keys_probe:required=SESSION_HELD_KEYS_REQUIRED
 if public_session_held_keys:required+=PUBLIC_SESSION_HELD_KEYS_REQUIRED
 if session_destroy_impact:required+=SESSION_DESTROY_IMPACT_REQUIRED
 if session_destroy_plan:required+=SESSION_DESTROY_PLAN_REQUIRED
 if public_session_destroy:required+=PUBLIC_SESSION_DESTROY_REQUIRED
 if health_projection:required+=HEALTH_PROJECTION_REQUIRED
 if public_session_held_keys_probe:required=PUBLIC_SESSION_HELD_KEYS_REQUIRED
 if session_destroy_impact_probe:required=SESSION_DESTROY_IMPACT_REQUIRED
 if session_destroy_plan_probe:required=SESSION_DESTROY_PLAN_REQUIRED
 if public_session_tools_probe:required=PUBLIC_SESSION_TOOLS_REQUIRED
 if session_tools_probe:required=SESSION_TOOLS_REQUIRED
 if public_session_renewal_probe:required=PUBLIC_SESSION_RENEWAL_REQUIRED
 if session_renewal_probe:required=SESSION_RENEWAL_REQUIRED
 if public_http_watch_probe:required=PUBLIC_HTTP_WATCH_REQUIRED
 if public_prefix_set_probe:required=PUBLIC_PREFIX_SET_REQUIRED
 if prefix_set_probe:required=PREFIX_SET_REQUIRED
 if prefix_delete_probe:required=PREFIX_DELETE_REQUIRED
 if public_prefix_probe:required=PUBLIC_PREFIX_DELETE_REQUIRED
 if public_copy_probe:required=PUBLIC_COPY_REQUIRED
 if copy_probe:required=COPY_REQUIRED
 if copy_limit_probe:required=(COPY_REQUIRED[-1],)
 if mutation_limit_probe:required=('DataMutationNativeRequestLimitRejectionNeverSplitsOrReplays',)
 if grid_write_probe:required=WRITE_GRID_REQUIRED
 if http_watch_probe:required=HTTP_WATCH_REQUIRED
 packages=['./internal/services/consul']+(['./internal/services/dbruntime'] if session else [])+(['./internal/ui/connectioneditor','./internal/ui/kvworkspace','./internal/ui/shell'] if public else [])+(['./internal/ui/dataworkspace'] if public_grid else [])
 if grid_write_probe:packages=['./internal/services/dbruntime','./internal/ui/shell','./internal/ui/dataworkspace']
 if public_session_renewal_probe:packages=['./internal/ui/kvworkspace','./internal/ui/shell']
 if public_session_tools_probe:packages=['./internal/ui/kvworkspace','./internal/ui/shell']
 if session_projection_probe:packages=['./internal/services/consul']
 if session_browser_probe:packages=['./internal/ui/kvworkspace','./internal/ui/shell']
 if session_held_keys_probe:packages=['./internal/services/consul']
 if public_session_held_keys_probe:packages=['./internal/ui/kvworkspace','./internal/ui/shell']
 if session_destroy_impact_probe or session_destroy_plan_probe:packages=['./internal/services/consul']
 if public_session_destroy_probe:
  required=PUBLIC_SESSION_DESTROY_REQUIRED
  packages=['./internal/ui/kvworkspace','./internal/ui/shell']
 if health_projection_probe:
  required=HEALTH_PROJECTION_REQUIRED
  packages=["./internal/services/consul"]
 if health_browser_probe or health_watch_ui_probe:
  required=HEALTH_PROJECTION_REQUIRED+HEALTH_BROWSER_REQUIRED
  packages=["./internal/services/consul","./internal/ui/kvworkspace","./internal/ui/shell"]
 if health_watch_probe:
  required=HEALTH_WATCH_REQUIRED+HEALTH_PROJECTION_REQUIRED+HTTP_WATCH_REQUIRED+PUBLIC_HTTP_WATCH_REQUIRED
  packages=["./internal/services/consul","./internal/services/dbruntime","./internal/ui/kvworkspace","./internal/ui/shell"]
 if health_watch_ui_probe:
  required=HEALTH_WATCH_UI_REQUIRED+HEALTH_WATCH_REQUIRED+HEALTH_PROJECTION_REQUIRED+HEALTH_BROWSER_REQUIRED+HTTP_WATCH_REQUIRED+PUBLIC_HTTP_WATCH_REQUIRED
  packages=["./internal/services/consul","./internal/services/dbruntime","./internal/ui/kvworkspace","./internal/ui/shell"]
 if session_tools_probe:packages=['./internal/services/consul']
 if session_renewal_probe:packages=['./internal/services/consul','./internal/services/dbruntime']
 if http_watch_probe:packages=['./internal/services/consul','./internal/services/dbruntime']
 if public_http_watch_probe:packages=['./internal/ui/kvworkspace','./internal/ui/shell']
 if copy_probe or copy_limit_probe or prefix_delete_probe:packages=['./internal/services/consul']
 if public_prefix_probe:packages=['./internal/services/dbruntime','./internal/ui/shell','./internal/ui/dataworkspace']
 if prefix_set_probe:packages=['./internal/services/consul']
 if public_prefix_set_probe:packages=['./internal/services/dbruntime','./internal/ui/shell','./internal/ui/dataworkspace']
 if public_copy_probe:packages=['./internal/services/dbruntime','./internal/ui/shell','./internal/ui/dataworkspace']
 if agent_read_probe:required=AGENT_READ_REQUIRED+required
 if agent_browser_probe:required=AGENT_BROWSER_REQUIRED+required
 if agent_service_evidence_probe:
  required=AGENT_SERVICE_EVIDENCE_REQUIRED
  packages=['./internal/services/consul']
 if agent_service_probe:
  required=AGENT_SERVICE_REQUIRED+AGENT_SERVICE_RUNTIME_REQUIRED+AGENT_SERVICE_EVIDENCE_REQUIRED+required
 if public_agent_service_probe:
  required=PUBLIC_AGENT_SERVICE_REQUIRED+required
 if agent_check_probe:
  required=AGENT_CHECK_REQUIRED+required
 if public_agent_check_probe:
  required=PUBLIC_AGENT_CHECK_REQUIRED+required
 if agent_ttl_probe:
  required=AGENT_TTL_REQUIRED+required
 if public_agent_ttl_probe:
  required=PUBLIC_AGENT_TTL_REQUIRED+required
 if agent_service_native_probe:
  required=AGENT_SERVICE_REQUIRED+AGENT_SERVICE_RUNTIME_REQUIRED+AGENT_SERVICE_EVIDENCE_REQUIRED
  packages=['./internal/services/consul','./internal/services/dbruntime']
 if agent_maintenance_probe:
  required=AGENT_MAINTENANCE_REQUIRED+required
 if public_agent_maintenance_probe:
  required=PUBLIC_AGENT_MAINTENANCE_REQUIRED+required
 if catalog_read_probe:
  required=CATALOG_READ_REQUIRED+required
 if catalog_projection_probe:
  required=CATALOG_PROJECTION_REQUIRED+required
 if public_catalog_probe:
  required=PUBLIC_CATALOG_REQUIRED+required
 if acl_read_probe:
  required=ACL_READ_REQUIRED+required
 if public_acl_probe:
  required=ACL_DOCUMENT_REQUIRED+PUBLIC_ACL_REQUIRED+required
 if public_acl_mutation_probe:
  required=ACL_MUTATION_SERVICE_REQUIRED+PUBLIC_ACL_MUTATION_REQUIRED+required
 if agent_maintenance_no_agent_token_probe:
  required=AGENT_MAINTENANCE_NO_AGENT_TOKEN_REQUIRED
  packages=['./internal/services/consul']
 report={'agent_browser_probe':agent_browser_probe,'agent_projection_probe':agent_projection_probe,'agent_read_probe':agent_read_probe,'health_watch_ui_probe':health_watch_ui_probe,'health_watch_probe':health_watch_probe,'health_browser_probe':health_browser_probe,'health_projection':health_projection,'health_projection_probe':health_projection_probe,'public_session_destroy':public_session_destroy,'public_session_destroy_probe':public_session_destroy_probe,'session_destroy_plan':session_destroy_plan,'session_destroy_plan_probe':session_destroy_plan_probe,'session_destroy_impact':session_destroy_impact,'session_destroy_impact_probe':session_destroy_impact_probe,'public_session_held_keys':public_session_held_keys,'public_session_held_keys_probe':public_session_held_keys_probe,'session_held_keys':session_held_keys,'session_held_keys_probe':session_held_keys_probe,'session_browser':session_browser,'session_browser_probe':session_browser_probe,'session_projection':session_projection,'session_projection_probe':session_projection_probe,'public_session_tools':public_session_tools,'public_session_tools_probe':public_session_tools_probe,'nonce':nonce,'session_tools':session_tools,'session_tools_probe':session_tools_probe,'public_session_renewal':public_session_renewal,'public_session_renewal_probe':public_session_renewal_probe,'session_renewal':session_renewal,'session_renewal_probe':session_renewal_probe,'public_http_watch':public_http_watch,'public_http_watch_probe':public_http_watch_probe,'scope':'public-consul-original-agent-browser-with-health-regression' if agent_browser_probe else 'focused-consul-original-agent-projection-with-health-regression' if agent_projection_probe else 'focused-consul-original-agent-reads-with-health-regression' if agent_read_probe else 'focused-consul-public-health-watch' if health_watch_ui_probe else 'focused-consul-native-health-watch' if health_watch_probe else 'focused-consul-public-health-browser' if health_browser_probe else 'private-consul-health-projection-with-public-destroy-regression' if health_projection else 'diagnostic-consul-health-projection' if health_projection_probe else 'public-consul-session-destroy' if public_session_destroy else 'diagnostic-consul-public-session-destroy' if public_session_destroy_probe else 'private-consul-session-destroy-plan-with-public-held-keys-regression' if session_destroy_plan else 'diagnostic-consul-session-destroy-plan' if session_destroy_plan_probe else 'private-consul-session-destroy-impact-with-public-held-keys-regression' if session_destroy_impact else 'diagnostic-consul-session-destroy-impact' if session_destroy_impact_probe else 'public-consul-session-held-keys' if public_session_held_keys else 'diagnostic-consul-session-held-keys-public' if public_session_held_keys_probe else 'private-consul-session-held-keys-with-public-browser-regression' if session_held_keys else 'diagnostic-consul-session-held-keys' if session_held_keys_probe else 'public-consul-session-browser' if session_browser else 'diagnostic-consul-session-browser' if session_browser_probe else 'private-consul-session-projection-with-public-tools-regression' if session_projection else 'diagnostic-consul-session-projection' if session_projection_probe else 'public-consul-session-tools' if public_session_tools else 'diagnostic-consul-public-session-tools' if public_session_tools_probe else 'private-consul-session-tools-with-public-renewal-regression' if session_tools else 'diagnostic-consul-session-tools' if session_tools_probe else 'public-consul-original-session-renewal' if public_session_renewal else 'diagnostic-consul-public-session-renewal' if public_session_renewal_probe else 'private-consul-original-session-renewal-with-public-watch-regression' if session_renewal else 'diagnostic-consul-original-session-renewal' if session_renewal_probe else 'public-consul-original-http-watch' if public_http_watch else 'diagnostic-consul-public-http-watch' if public_http_watch_probe else 'private-consul-original-http-watch-with-public-batch-regression' if http_watch else 'diagnostic-consul-original-http-watch-foundation' if http_watch_probe else 'public-consul-original-prefix-explicit-batches' if public_prefix_set else 'diagnostic-consul-public-prefix-explicit-batches' if public_prefix_set_probe else 'private-consul-original-prefix-explicit-batches-with-public-prefix-regression' if prefix_delete_set else 'diagnostic-consul-original-prefix-explicit-batch-foundation' if prefix_set_probe else 'public-consul-original-captured-prefix-candidate-delete' if public_prefix_delete else 'diagnostic-consul-public-prefix-candidate-delete' if public_prefix_probe else 'private-consul-original-prefix-candidate-delete-with-public-copy-regression' if prefix_delete else 'diagnostic-consul-prefix-candidate-delete' if prefix_delete_probe else 'public-consul-original-conditional-copy' if public_key_copy else 'diagnostic-consul-public-key-copy-probe' if public_copy_probe else 'private-consul-original-conditional-copy-with-public-write-regression' if key_copy else 'diagnostic-consul-conditional-copy-probe' if copy_probe else 'private-consul-original-copy-request-limit' if copy_limit_probe else 'diagnostic-consul-public-grid-write-probe' if grid_write_probe else 'public-consul-original-grid-mutations' if public_grid_writes else 'private-consul-original-data-mutations-with-public-read-regression' if data_mutations and public_grid else 'private-consul-original-data-request-limit' if mutation_limit_probe else 'diagnostic-consul-probe' if probe else 'public-consul-read-only-shared-grid' if public_grid else 'private-consul-original-data-mutations' if data_mutations else 'private-consul-original-live-data-documents' if data_session else 'public-consul-structured-kv-tools' if tools else 'public-consul-original-http-workspace' if public else 'original-consul-native-document-runtime' if session else 'original-consul-native-foundation','http_watch':http_watch,'http_watch_probe':http_watch_probe,'txn_max_request':txn_max_request,'test_timeout':test_timeout,'memory_mib':memory_mib,'public_prefix_set':public_prefix_set,'public_prefix_set_probe':public_prefix_set_probe,'prefix_delete_set':prefix_delete_set,'prefix_set_probe':prefix_set_probe,'mutation_limit_probe':mutation_limit_probe,'public_grid_writes':public_grid_writes,'grid_write_probe':grid_write_probe,'key_copy':key_copy,'public_key_copy':public_key_copy,'public_copy_probe':public_copy_probe,'public_prefix_delete':public_prefix_delete,'public_prefix_probe':public_prefix_probe,'prefix_delete':prefix_delete,'prefix_delete_probe':prefix_delete_probe,'copy_probe':copy_probe,'copy_limit_probe':copy_limit_probe,'mode':mode,'version':version,'image':IMAGES[version],'race':race,'passed':False,'required':required,'packages':packages,'source_hashes':source_hashes()}
 if agent_service_evidence_probe:
  report.update(agent_service_evidence_probe=True,scope='diagnostic-consul-original-agent-service-evidence-only',production_agent_writes_published=False)
 if agent_service_probe or agent_service_native_probe:
  report.update(agent_service_probe=agent_service_probe,agent_service_native_probe=agent_service_native_probe,scope='private-consul-native-agent-service-foundation-with-agent-health-watch-regression' if agent_service_probe else 'diagnostic-consul-native-agent-service-foundation',production_agent_writes_published=False)
 if public_agent_service_probe:
  report.update(public_agent_service_probe=True,scope='public-consul-original-agent-service-tools-with-browser-regression',production_agent_writes_published=True)
 if agent_check_probe:
  report.update(agent_check_probe=True,scope='private-consul-native-agent-check-foundation-with-public-service-browser-regression',production_agent_check_tools_published=False,production_agent_service_tools_published=True)
 if public_agent_check_probe:
  report.update(public_agent_check_probe=True,scope='public-consul-original-agent-check-tools-with-service-browser-regression',production_agent_check_tools_published=True)
 if agent_ttl_probe:
  report.update(agent_ttl_probe=True,scope='private-consul-native-agent-ttl-foundation-with-public-check-service-browser-regression',production_agent_ttl_tools_published=False)
 if public_agent_ttl_probe:
  report.update(public_agent_ttl_probe=True,scope='public-consul-original-agent-ttl-tools-with-check-service-browser-regression',production_agent_ttl_tools_published=True)
 if agent_maintenance_probe:
  report.update(agent_maintenance_probe=True,scope='private-consul-native-agent-maintenance-foundation-with-public-ttl-check-service-browser-regression',production_agent_maintenance_tools_published=False,agent_token_configured=mode!='anonymous')
 if agent_maintenance_no_agent_token_probe:
  report.update(agent_maintenance_no_agent_token_probe=True,scope='native-consul-original-maintenance-no-agent-token-evidence',production_agent_maintenance_tools_published=False,agent_token_configured=False,application_acceptance=False)
 if public_agent_maintenance_probe:
  report.update(public_agent_maintenance_probe=True,scope='public-consul-original-agent-maintenance-tools-with-ttl-check-service-browser-regression',production_agent_maintenance_tools_published=True)
 if catalog_read_probe:
  report.update(catalog_read_probe=True,scope='private-consul-native-catalog-reads-with-public-maintenance-ttl-check-service-browser-regression',production_catalog_tools_published=False)
 if catalog_projection_probe:
  report.update(catalog_projection_probe=True,scope='private-consul-original-catalog-projection-with-all-catalog-read-and-public-maintenance-regressions',production_catalog_tools_published=False)
 if public_catalog_probe:
  report.update(public_catalog_probe=True,scope='public-consul-original-catalog-tools-browser-with-all-catalog-and-maintenance-regressions',production_catalog_tools_published=True)
 if acl_read_probe:
  report.update(acl_read_probe=True,scope='private-consul-native-acl-reads-with-all-public-catalog-regressions',production_acl_tools_published=False,generic_acl_documents_enabled=False)
 if public_acl_probe:
  report.update(public_acl_probe=True,scope='public-consul-original-acl-forms-private-receipts-with-all-catalog-regressions',production_acl_tools_published=True,generic_acl_documents_enabled=False)
 if public_acl_mutation_probe:
  report.update(public_acl_mutation_probe=True,scope='public-consul-original-acl-mutations-impact-revalidation-and-private-receipts',production_acl_mutation_tools_published=True,generic_acl_documents_enabled=False)
 identifier=None;private=None
 try:
  with tempfile.TemporaryDirectory(prefix='nyadb-consul-'+nonce+'-',dir=build) as rawfolder:
   private=Path(rawfolder);secure=mode in ('tls','mtls');acl=mode!='anonymous';token=str(uuid.uuid4()) if acl else ''
   node_id=str(uuid.uuid4());node='nyadb-'+nonce
   config={'node_id':node_id,'datacenter':'dc1','primary_datacenter':'dc1','ui_config':{'enabled':False},'ports':{'http':-1 if secure else 8500,'https':8501 if secure else -1}}
   if txn_max_request!=524288:config['limits']={'txn_max_req_len':txn_max_request}
   if acl:config['acl']={'enabled':True,'default_policy':'deny','enable_token_persistence':False,'tokens':{'initial_management':token}}
   if acl and agent_maintenance_probe:config['acl']['tokens']['agent']=token
   if secure:
    config['ports'].update(grpc=-1,grpc_tls=8503)
    certs(private);config['tls']={'defaults':{'ca_file':'/owned/ca.crt','cert_file':'/owned/server.crt','key_file':'/owned/server.key','verify_incoming':mode=='mtls','verify_outgoing':True}}
   configfile=private/'config.json';configfile.write_text(json.dumps(config));configfile.chmod(0o600)
   port=8501 if secure else 8500
   identifier=command(['docker','run','-d','--name','nyadb-consul-'+nonce,'--label','nyadb.fixture=consul','--label','nyadb.fixture.nonce='+nonce,'--user','0:0','--memory',str(memory_mib)+'m','-p','127.0.0.1::'+str(port),'-v',str(private)+':/owned:ro','--entrypoint','consul',IMAGES[version],'agent','-dev','-client=0.0.0.0','-bind=127.0.0.1','-config-file=/owned/config.json','-node='+node])
   report['container_id']=identifier
   mapping=command(['docker','port',identifier,str(port)+'/tcp']);hostport=mapping.split(':')[-1]
   endpoint=('https' if secure else 'http')+'://127.0.0.1:'+hostport
   import ssl
   context=ssl.create_default_context(cafile=str(private/'ca.crt')) if secure else None
   if mode=='mtls':context.load_cert_chain(str(private/'client.crt'),str(private/'client.key'))
   opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPSHandler(context=context))
   def http(method,route,body=None,key=token):
    headers={'X-Consul-Token':key,'Content-Type':'application/json'} if key else {'Content-Type':'application/json'}
    data=json.dumps(body).encode() if body is not None else None
    req=urllib.request.Request(endpoint+route,data=data,headers=headers,method=method)
    with opener.open(req,timeout=3) as response:return json.loads(response.read() or b'null')
   deadline=time.monotonic()+60
   while True:
    try:
     if http('GET','/v1/status/leader'):break
    except (OSError,ValueError) as error:
     report['readiness_error_type']=type(error).__name__
    if time.monotonic()>deadline:
     startup=subprocess.run(['docker','logs',identifier],capture_output=True,text=True)
     diagnostic=startup.stdout+startup.stderr
     if token:diagnostic=diagnostic.replace(token,'[omitted token]')
     log.write_text(diagnostic[-200000:])
     raise RuntimeError('Original Consul fixture did not become ready; inspect '+str(log.relative_to(ROOT)))
    time.sleep(.2)
   self=http('GET','/v1/agent/self')
   report['native_identity']={k:self['Config'].get(k) for k in ('Version','Revision','NodeID','NodeName','Datacenter')}
   if self['Config'].get('Version')!=version or self['Config'].get('NodeID')!=node_id:raise RuntimeError('Official original Consul identity mismatch')
   readonly=writer=sessionreadonly=''
   if acl:
    for name,rules in [('kv-read','key_prefix "allowed/" { policy = "read" }'),('kv-write','key_prefix "allowed/" { policy = "write" } session_prefix "" { policy = "write" }')]:
     policy=http('PUT','/v1/acl/policy',{'Name':'owned-'+name,'Rules':rules})
     result=http('PUT','/v1/acl/token',{'Description':'owned '+name,'Policies':[{'ID':policy['ID']}]})
     if name=='kv-read':readonly=result['SecretID']
     else:writer=result['SecretID']
   if acl and (session_projection_probe or session_renewal or session_renewal_probe or public_session_renewal_probe or session_tools_probe or public_session_tools_probe):
    policy=http('PUT','/v1/acl/policy',{'Name':'owned-session-read','Rules':'key_prefix "" { policy = "read" } session_prefix "" { policy = "read" }'})
    sessionreadonly=http('PUT','/v1/acl/token',{'Description':'owned session read','Policies':[{'ID':policy['ID']}]})['SecretID']
   if probe:
    report['passed']=True
   else:
    env={k:v for k,v in os.environ.items() if not k.startswith('NYADB_CONSUL_')};env.update(GOWORK='off',GOFLAGS='-mod=readonly',NYADB_CONSUL_IT='1',NYADB_CONSUL_TXN_MAX_REQUEST=str(txn_max_request),NYADB_CONSUL_ENDPOINT=endpoint,NYADB_CONSUL_AUTH='token' if acl else 'none',NYADB_CONSUL_TOKEN=token,NYADB_CONSUL_READ_TOKEN=readonly,NYADB_CONSUL_SESSION_READ_TOKEN=sessionreadonly,NYADB_CONSUL_WRITE_TOKEN=writer,NYADB_CONSUL_VERSION=version,NYADB_CONSUL_NODE_ID=node_id)
    if agent_maintenance_probe or agent_maintenance_no_agent_token_probe:env.update(NYADB_CONSUL_AGENT_TOKEN_CONFIGURED='1' if acl and agent_maintenance_probe else '0')
    images=path.with_name(path.stem+'-public-images')
    if public:env.update(NYADB_CONSUL_PUBLIC_IMAGES=str(images))
    agent_check_images=path.with_name(path.stem+'-agent-check-images')
    if public_agent_check_probe:env.update(NYADB_CONSUL_AGENT_CHECK_IMAGES=str(agent_check_images))
    agent_service_images=path.with_name(path.stem+'-agent-service-images')
    if public_agent_service_probe:env.update(NYADB_CONSUL_AGENT_SERVICE_IMAGES=str(agent_service_images))
    agent_browser_images=path.with_name(path.stem+"-agent-browser-images")
    if agent_browser_probe:env.update(NYADB_CONSUL_AGENT_UNIT_IMAGES=str(agent_browser_images))
    health_browser_images=path.with_name(path.stem+"-health-browser-images")
    if health_browser_probe or health_watch_ui_probe:env.update(NYADB_CONSUL_HEALTH_UNIT_IMAGES=str(health_browser_images))
    session_destroy_images=path.with_name(path.stem+'-session-destroy-images')
    if public_session_destroy or public_session_destroy_probe:env.update(NYADB_CONSUL_SESSION_DESTROY_IMAGES=str(session_destroy_images))
    session_held_keys_images=path.with_name(path.stem+'-session-held-keys-images')
    if public_session_held_keys or public_session_held_keys_probe:env.update(NYADB_CONSUL_SESSION_HELD_KEYS_IMAGES=str(session_held_keys_images))
    session_browser_images=path.with_name(path.stem+'-session-browser-images')
    if session_browser or session_browser_probe:env.update(NYADB_CONSUL_SESSION_BROWSER_IMAGES=str(session_browser_images))
    session_tools_images=path.with_name(path.stem+'-session-tools-images')
    if public_session_tools or public_session_tools_probe:env.update(NYADB_CONSUL_SESSION_TOOLS_IMAGES=str(session_tools_images))
    renewal_images=path.with_name(path.stem+'-session-renewal-images')
    if public_session_renewal or public_session_renewal_probe:env.update(NYADB_CONSUL_SESSION_RENEWAL_IMAGES=str(renewal_images))
    watch_images=path.with_name(path.stem+'-watch-images')
    if public_http_watch or public_http_watch_probe or health_watch_probe or health_watch_ui_probe:env.update(NYADB_CONSUL_WATCH_IMAGES=str(watch_images))
    grid_images=path.with_name(path.stem+'-grid-images')
    if public_grid:env.update(NYADB_CONSUL_GRID_IMAGES=str(grid_images))
    agent_ttl_images=path.with_name(path.stem+'-agent-ttl-images')
    if public_agent_ttl_probe:env.update(NYADB_CONSUL_AGENT_TTL_IMAGES=str(agent_ttl_images))
    acl_mutation_images=path.with_name(path.stem+'-acl-mutation-images')
    if public_acl_mutation_probe:env.update(NYADB_CONSUL_ACL_MUTATION_NATIVE_IMAGES=str(acl_mutation_images))
    acl_browser_images=path.with_name(path.stem+'-acl-browser-images')
    if public_acl_probe:env.update(NYADB_CONSUL_ACL_NATIVE_IMAGES=str(acl_browser_images))
    catalog_browser_images=path.with_name(path.stem+'-catalog-browser-images')
    if public_catalog_probe:env.update(NYADB_CONSUL_CATALOG_UNIT_IMAGES=str(catalog_browser_images))
    agent_maintenance_images=path.with_name(path.stem+'-agent-maintenance-images')
    if public_agent_maintenance_probe:env.update(NYADB_CONSUL_AGENT_MAINTENANCE_IMAGES=str(agent_maintenance_images))
    if secure:env.update(NYADB_CONSUL_CA=str(private/'ca.crt'))
    if mode=='mtls':env.update(NYADB_CONSUL_CERT=str(private/'client.crt'),NYADB_CONSUL_KEY=str(private/'client.key'))
    args=['go','test','-p=1']+(['-race'] if race else [])+packages+['-run','^('+'|'.join('TestConsulNative'+c for c in required)+')$','-count=1','-v','-timeout='+str(test_timeout)+'s']
    # Held-key observations intentionally read the entire root. Run their
    # controlled fixtures before legacy response-limit pressure tests populate
    # that root; keep every retained parent in the same owned configuration.
    phases=public_acl_mutation_acceptance_test_phases(packages,required,race,test_timeout) if public_acl_mutation_probe else public_acl_acceptance_test_phases(packages,required,race,test_timeout) if public_acl_probe else acl_read_acceptance_test_phases(packages,required,race,test_timeout) if acl_read_probe else public_catalog_acceptance_test_phases(packages,required,race,test_timeout) if public_catalog_probe else catalog_projection_acceptance_test_phases(packages,required,race,test_timeout) if catalog_projection_probe else catalog_read_acceptance_test_phases(packages,required,race,test_timeout) if catalog_read_probe else public_agent_maintenance_acceptance_test_phases(packages,required,race,test_timeout) if public_agent_maintenance_probe else agent_maintenance_acceptance_test_phases(packages,required,race,test_timeout) if agent_maintenance_probe else agent_ttl_acceptance_test_phases(packages,required,race,test_timeout,public_agent_ttl_probe) if agent_ttl_probe else agent_check_acceptance_test_phases(packages,required,race,test_timeout,public_agent_check_probe) if agent_check_probe else acceptance_test_phases(packages,required,race,test_timeout,session_held_keys,public_session_held_keys,session_destroy_impact,session_destroy_plan,public_session_destroy,health_projection)
    report['test_phases']=phases
    with log.open('w') as out:
     for phase in phases:
      r=subprocess.run(phase,cwd=ROOT,env=env,stdout=out,stderr=subprocess.STDOUT)
      if r.returncode:raise RuntimeError('Native Consul acceptance failed; inspect '+str(log.relative_to(ROOT)))
    text=log.read_text()
    try:accepted=accepted_cases(text,required)
    except RuntimeError as error:raise RuntimeError('Native Consul acceptance failed; inspect '+str(log.relative_to(ROOT))) from error
    if source_hashes()!=report['source_hashes']:raise RuntimeError('Native Consul source changed during acceptance')
    pictures={}
    if public and not grid_write_probe and not public_copy_probe and not public_prefix_probe and not public_prefix_set_probe:
     pictures={str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(images.glob('*.png'))}
     expected={f'{lang}-{size}-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in ('editor-native','shell-native','workspace-write-confirmation','workspace-complete-body','workspace-headers',*(f'workspace-{prefix}{mode}' for prefix in ('unknown-','unknown-after-read-') for mode in ('lost','partial','http500','http408','cancel','disconnect','replace')))}
     if tools:
      expected.update(f'{lang}-{size}-workspace-tool-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in (*(f'{view}-{kind}' for view in ('form','generated','receipt') for kind in ('put','read','list','raw','delete','transaction')),*(f'{prefix}{mode}' for prefix in ('unknown-','unknown-after-read-') for mode in ('lost','partial','http500','http408','cancel','disconnect','replace'))))
     if {Path(name).name for name in pictures}!=expected:raise RuntimeError('Mandatory original public Consul image set failed')
    grid_pictures={}
    if public_grid:
     grid_pictures={str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(grid_images.glob('*.png'))}
     expected_grid={f'{lang}-{size}-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in (*(f'data-{s}' for s in ('live-grid','live-next','refresh','count','field','export','parked','empty','read-retired','reopened')),*(f'shell-grid-{s}' for s in ('keys','grid','structure','reopened')))}
     if public_grid_writes or grid_write_probe:
      write_stages=('bounds','destination-conflict','draft','confirmation','saved','renamed','inserted','delete-confirmation','deleted','conflict','session-locked',*(f'{prefix}{mode}' for prefix in ('unknown-','after-read-') for mode in ('lost','partial','missing','http500','http408','cancel','replace','parked')),*(f'warning-{mode}' for mode in ('bad-index','bad-leader')))
      write_images={f'{lang}-{size}-data-mutation-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in write_stages}
      write_images.update(f'{lang}-{size}-shell-grid-mutation-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in ('confirmation','receipt','unknown-lost','unknown-cancel','unknown-close-lost','unknown-close-cancel'))
      expected_grid=write_images if grid_write_probe else expected_grid|write_images
     if public_key_copy or public_copy_probe:
      success=tuple(f'key-copy-{mode}{empty}-{stage}' for mode in ('values','fingerprints') for empty in ('','-empty') for stage in ('form','confirmation','saved','source-preserved'))
      conflicts=tuple('key-copy-conflict-'+case for case in ('source-before-preview','destination-before-preview','source-after-preview','destination-after-preview','locked'))
      faults=tuple(f'key-copy-{prefix}{mode}' for prefix in ('unknown-','after-read-') for mode in ('lost','partial','missing','http500','http408','cancel','replace','parked'))
      copy_images={f'{lang}-{size}-data-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in success+conflicts+faults}
      copy_images.update(f'{lang}-{size}-shell-grid-key-copy-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in ('form','confirmation','receipt'))
      expected_grid=copy_images if public_copy_probe else expected_grid|copy_images
     if public_prefix_delete or public_prefix_probe:
      success=tuple(f'prefix-delete-{kind}-{stage}' for kind in ('empty','large') for stage in ('form','confirmation','observation','saved','later-preserved'))
      conflicts=tuple('prefix-delete-conflict-'+case for case in ('changed','deleted','too-many'))
      faults=tuple(f'prefix-delete-{prefix}{mode}' for prefix in ('unknown-','after-read-') for mode in ('lost','partial','missing','http500','http408','cancel','replace','parked'))
      prefix_images={f'{lang}-{size}-data-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in success+conflicts+faults}
      prefix_images.update(f'{lang}-{size}-shell-grid-prefix-delete-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in ('form','confirmation','receipt'))
      expected_grid=prefix_images if public_prefix_probe else expected_grid|prefix_images
     if public_prefix_set or public_prefix_set_probe:
      success=tuple(f'prefix-set-large-{kind}-{stage}' for kind in ('false','true') for stage in ('form','review','confirmation','ledger','receipt','observation'))
      conflicts=tuple(f'prefix-set-conflict-{kind}-ledger' for kind in ('value','flags','delete','recreate','lock'))
      faults=tuple(f'prefix-set-unknown-{kind}-ledger' for kind in ('lost','partial','missing','http500','http408','cancel','replace','parked'))
      batch_images={f'{lang}-{size}-data-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in success+conflicts+faults}
      batch_images.update(f'{lang}-{size}-shell-grid-prefix-set-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in ('form','review','confirmation','ledger','receipt'))
      expected_grid=batch_images if public_prefix_set_probe else expected_grid|batch_images
     if {Path(name).name for name in grid_pictures}!=expected_grid:raise RuntimeError('Mandatory original shared Consul grid image set failed')
    watch_pictures={}
    if public_http_watch or public_http_watch_probe or health_watch_probe or health_watch_ui_probe:
     watch_pictures={str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(watch_images.glob('*.png'))}
     stages=tuple('workspace-watch-'+stage for stage in ('template','observing','parked','selected','stopped','recovered','overflow','unknown',*("terminal-"+mode for mode in ('zero','missing','invalid','backward','lost','partial','http500','http408','acl'))))+tuple('shell-watch-'+stage for stage in ('observing','stopped','recovered'))
     expected_watch={f'{lang}-{size}-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in stages}
     if {Path(name).name for name in watch_pictures}!=expected_watch:raise RuntimeError('Mandatory original public Watch image set failed')
    renewal_pictures={}
    if public_session_renewal or public_session_renewal_probe:
     renewal_pictures={str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(renewal_images.glob('*.png'))}
     if {Path(name).name for name in renewal_pictures}!=session_renewal_image_names():raise RuntimeError('Mandatory original public Session renewal image set failed')
    session_tools_pictures={}
    if public_session_tools or public_session_tools_probe:
     session_tools_pictures={str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(session_tools_images.glob('*.png'))}
     if {Path(name).name for name in session_tools_pictures}!=session_tools_image_names():raise RuntimeError('Mandatory original public Session tools image set failed')
    session_browser_pictures={}
    if session_browser or session_browser_probe:
     session_browser_pictures={str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(session_browser_images.glob('*.png'))}
     expected_browser=session_browser_image_names()
     if {Path(name).name for name in session_browser_pictures}!=expected_browser:raise RuntimeError('Original candidate Session browser images incomplete')
    held_keys_pictures={}
    if public_session_held_keys or public_session_held_keys_probe:
     held_keys_pictures={str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(session_held_keys_images.glob('*.png'))}
     if {Path(name).name for name in held_keys_pictures}!=session_held_keys_image_names():raise RuntimeError('Original public Session held-key images incomplete')
    destroy_pictures={}
    if public_session_destroy or public_session_destroy_probe:
     destroy_pictures={str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(session_destroy_images.glob('*.png'))}
     if {Path(name).name for name in destroy_pictures}!=session_destroy_image_names():raise RuntimeError('Original public Session destroy image set incomplete')
    health_pictures={}
    if health_browser_probe or health_watch_ui_probe:
     health_pictures={str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(health_browser_images.glob("*.png"))}
     if {Path(name).name for name in health_pictures}!=(health_browser_image_names() | health_watch_ui_image_names() if health_watch_ui_probe else health_browser_image_names()):raise RuntimeError("Original public health browser image set incomplete")
    acl_mutation_pictures=acl_mutation_image_evidence(acl_mutation_images,acl) if public_acl_mutation_probe else {}
    acl_pictures=acl_browser_image_evidence(acl_browser_images) if public_acl_probe else {}
    catalog_pictures={}
    if public_catalog_probe:
     catalog_pictures={str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(catalog_browser_images.glob('*.png'))}
     if {Path(name).name for name in catalog_pictures}!=catalog_browser_image_names():raise RuntimeError('Original public Catalog form/value/null/fault/Shell image set incomplete')
    agent_pictures={}
    if agent_browser_probe:
     agent_pictures={str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(agent_browser_images.glob("*.png"))}
     if {Path(p).name for p in agent_pictures}!=agent_browser_image_names():raise RuntimeError("Agent browser images do not cover original forms/maps/details/recovery")
    agent_service_pictures={}
    if public_agent_service_probe:
     agent_service_pictures={str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(agent_service_images.glob('*.png'))}
     if {Path(name).name for name in agent_service_pictures}!=agent_service_image_names():raise RuntimeError('Original public Agent service confirmation/receipt/unknown/recovery images incomplete')
    agent_check_pictures={}
    if public_agent_check_probe:
     agent_check_pictures={str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(agent_check_images.glob('*.png'))}
     if {Path(name).name for name in agent_check_pictures}!=agent_check_image_names():raise RuntimeError('Original public Agent check confirmation/receipt/unknown/recovery images incomplete')
    agent_ttl_pictures={}
    if public_agent_ttl_probe:
     agent_ttl_pictures={str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(agent_ttl_images.glob('*.png'))}
     if {Path(name).name for name in agent_ttl_pictures}!=agent_ttl_image_names():raise RuntimeError('Original public Agent TTL confirmation/receipt/unknown/recovery images incomplete')
    agent_maintenance_pictures={}
    if public_agent_maintenance_probe:
     agent_maintenance_pictures={str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(agent_maintenance_images.glob('*.png'))}
     if {Path(name).name for name in agent_maintenance_pictures}!=agent_maintenance_image_names():raise RuntimeError('Original public Agent maintenance image set incomplete')
    report.update(acl_mutation_images=acl_mutation_pictures,acl_browser_images=acl_pictures,agent_maintenance_images=agent_maintenance_pictures,agent_ttl_images=agent_ttl_pictures,agent_check_images=agent_check_pictures,agent_service_images=agent_service_pictures,catalog_browser_images=catalog_pictures,agent_browser_images=agent_pictures,health_browser_images=health_pictures,session_destroy_images=destroy_pictures,session_held_keys_images=held_keys_pictures,session_browser_images=session_browser_pictures,session_tools_images=session_tools_pictures,session_renewal_images=renewal_pictures,watch_images=watch_pictures,grid_images=grid_pictures,passed=True,accepted=accepted,public_images=pictures,log_sha256=hashlib.sha256(log.read_bytes()).hexdigest())
 finally:
  try:
   if identifier:
    remove_owned(identifier,nonce)
    r=subprocess.run(['docker','inspect',identifier],capture_output=True);report['container_removed']=r.returncode!=0
  finally:
   report['private_files_removed']=private is not None and not private.exists();path.write_text(json.dumps(report,indent=2)+'\n')
 print(path.relative_to(ROOT))
 return report

if __name__=='__main__':
 p=argparse.ArgumentParser();p.add_argument('--mode',choices=('anonymous','token','tls','mtls'),default='anonymous');p.add_argument('--version',choices=tuple(IMAGES),default='2.0.2');p.add_argument('--race',action='store_true');p.add_argument('--probe',action='store_true');p.add_argument('--session',action='store_true');p.add_argument('--public',action='store_true');p.add_argument('--tools',action='store_true');p.add_argument('--data-session',action='store_true');p.add_argument('--public-grid',action='store_true');p.add_argument('--data-mutations',action='store_true');p.add_argument('--txn-max-request',type=int,choices=(524288,8388608),default=524288);p.add_argument('--mutation-limit-probe',action='store_true');p.add_argument('--public-grid-writes',action='store_true');p.add_argument('--grid-write-probe',action='store_true');p.add_argument('--key-copy',action='store_true');p.add_argument('--copy-probe',action='store_true');p.add_argument('--copy-limit-probe',action='store_true');p.add_argument('--public-key-copy',action='store_true');p.add_argument('--public-copy-probe',action='store_true');p.add_argument('--prefix-delete',action='store_true');p.add_argument('--prefix-delete-probe',action='store_true');p.add_argument('--public-prefix-delete',action='store_true');p.add_argument('--public-prefix-probe',action='store_true');p.add_argument('--prefix-delete-set',action='store_true');p.add_argument('--prefix-set-probe',action='store_true');p.add_argument('--public-prefix-set',action='store_true');p.add_argument('--public-prefix-set-probe',action='store_true');p.add_argument('--http-watch',action='store_true');p.add_argument('--http-watch-probe',action='store_true');p.add_argument('--public-http-watch',action='store_true');p.add_argument('--public-http-watch-probe',action='store_true');p.add_argument('--test-timeout',type=int,default=600);p.add_argument('--memory-mib',type=int,choices=(768,2048),default=768);p.add_argument('--session-renewal',action='store_true');p.add_argument('--session-renewal-probe',action='store_true');p.add_argument('--public-session-renewal',action='store_true');p.add_argument('--public-session-renewal-probe',action='store_true');p.add_argument('--session-tools',action='store_true');p.add_argument('--session-tools-probe',action='store_true');p.add_argument('--public-session-tools',action='store_true');p.add_argument('--public-session-tools-probe',action='store_true');p.add_argument('--session-projection',action='store_true');p.add_argument('--session-projection-probe',action='store_true');p.add_argument('--session-browser-probe',action='store_true');p.add_argument('--session-browser',action='store_true');p.add_argument('--session-held-keys',action='store_true');p.add_argument('--session-held-keys-probe',action='store_true');p.add_argument('--public-session-held-keys',action='store_true');p.add_argument('--public-session-held-keys-probe',action='store_true');p.add_argument('--session-destroy-impact',action='store_true');p.add_argument('--session-destroy-impact-probe',action='store_true');p.add_argument('--session-destroy-plan',action='store_true');p.add_argument('--session-destroy-plan-probe',action='store_true');p.add_argument('--public-session-destroy',action='store_true');p.add_argument('--public-session-destroy-probe',action='store_true');p.add_argument('--health-projection',action='store_true');p.add_argument('--health-projection-probe',action='store_true');p.add_argument('--health-browser-probe',action='store_true');p.add_argument('--health-watch-probe',action='store_true');p.add_argument('--health-watch-ui-probe',action='store_true');p.add_argument('--agent-read-probe',action='store_true');p.add_argument('--agent-projection-probe',action='store_true');p.add_argument('--agent-browser-probe',action='store_true');p.add_argument('--agent-service-evidence-probe',action='store_true');p.add_argument('--agent-service-probe',action='store_true');p.add_argument('--agent-service-native-probe',action='store_true');p.add_argument('--public-agent-service-probe',action='store_true');p.add_argument('--agent-check-probe',action='store_true');p.add_argument('--public-agent-check-probe',action='store_true');p.add_argument('--agent-ttl-probe',action='store_true');p.add_argument('--public-agent-ttl-probe',action='store_true');p.add_argument('--agent-maintenance-probe',action='store_true');p.add_argument('--agent-maintenance-no-agent-token-probe',action='store_true');p.add_argument('--public-agent-maintenance-probe',action='store_true');p.add_argument('--catalog-read-probe',action='store_true');p.add_argument('--catalog-projection-probe',action='store_true');p.add_argument('--public-catalog-probe',action='store_true');p.add_argument('--acl-read-probe',action='store_true');p.add_argument('--public-acl-probe',action='store_true');p.add_argument('--public-acl-mutation-probe',action='store_true');a=p.parse_args()
 verify(a.mode,a.race,a.version,a.probe,a.session,a.public,a.tools,a.data_session,a.public_grid,a.data_mutations,a.txn_max_request,a.mutation_limit_probe,a.public_grid_writes,a.grid_write_probe,a.key_copy,a.copy_probe,a.copy_limit_probe,a.public_key_copy,a.public_copy_probe,a.prefix_delete,a.prefix_delete_probe,a.public_prefix_delete,a.public_prefix_probe,a.prefix_delete_set,a.prefix_set_probe,a.test_timeout,a.memory_mib,a.public_prefix_set,a.public_prefix_set_probe,a.http_watch,a.http_watch_probe,a.public_http_watch,a.public_http_watch_probe,a.session_renewal,a.session_renewal_probe,a.public_session_renewal,a.public_session_renewal_probe,a.session_tools,a.session_tools_probe,a.public_session_tools,a.public_session_tools_probe,a.session_projection,a.session_projection_probe,a.session_browser_probe,a.session_browser,a.session_held_keys,a.session_held_keys_probe,a.public_session_held_keys,a.public_session_held_keys_probe,a.session_destroy_impact,a.session_destroy_impact_probe,a.session_destroy_plan,a.session_destroy_plan_probe,a.public_session_destroy,a.public_session_destroy_probe,a.health_projection,a.health_projection_probe,a.health_browser_probe,a.health_watch_probe,a.health_watch_ui_probe,a.agent_read_probe,a.agent_projection_probe,a.agent_browser_probe,a.agent_service_evidence_probe,a.agent_service_probe,a.agent_service_native_probe,a.public_agent_service_probe,a.agent_check_probe,a.public_agent_check_probe,a.agent_ttl_probe,a.public_agent_ttl_probe,a.agent_maintenance_probe,a.agent_maintenance_no_agent_token_probe,a.public_agent_maintenance_probe,a.catalog_read_probe,a.catalog_projection_probe,a.public_catalog_probe,a.acl_read_probe,a.public_acl_probe,a.public_acl_mutation_probe)
