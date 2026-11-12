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

def source_hashes():
 # Track neutral contracts, runtime and UI dependencies as well as the adapter.
 names=command(['git','ls-files','--cached','--others','--exclude-standard']).splitlines()
 names=set(n for n in names if n.endswith(('.go','.json')) or n in ('go.mod','go.sum'))
 names.add(str(Path(__file__).resolve().relative_to(ROOT)))
 return {n:hashlib.sha256((ROOT/n).read_bytes()).hexdigest() for n in sorted(names) if (ROOT/n).is_file()}

def accepted_cases(text,required):
 accepted=re.findall(r'^--- PASS: TestConsulNative(\w+) \(',text,re.M)
 if sorted(accepted)!=sorted(required) or re.search(r'^\s*--- (FAIL|SKIP):|WARNING: DATA RACE|^FAIL(?:\s|$)|^panic:',text,re.M):
  raise RuntimeError('Native Consul mandatory PASS set failed')
 return accepted

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

def verify(mode='anonymous',race=False,version='2.0.2',probe=False,session=False,public=False,tools=False,data_session=False,public_grid=False,data_mutations=False,txn_max_request=524288,mutation_limit_probe=False,public_grid_writes=False,grid_write_probe=False,key_copy=False,copy_probe=False,copy_limit_probe=False,public_key_copy=False,public_copy_probe=False,prefix_delete=False,prefix_delete_probe=False,public_prefix_delete=False,public_prefix_probe=False,prefix_delete_set=False,prefix_set_probe=False,test_timeout=600,memory_mib=768,public_prefix_set=False,public_prefix_set_probe=False):
 if not isinstance(test_timeout,int) or not 60<=test_timeout<=3600 or memory_mib not in (768,2048):raise ValueError('Invalid original fixture capacity or test deadline')
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
 if public_prefix_set_probe:required=PUBLIC_PREFIX_SET_REQUIRED
 if prefix_set_probe:required=PREFIX_SET_REQUIRED
 if prefix_delete_probe:required=PREFIX_DELETE_REQUIRED
 if public_prefix_probe:required=PUBLIC_PREFIX_DELETE_REQUIRED
 if public_copy_probe:required=PUBLIC_COPY_REQUIRED
 if copy_probe:required=COPY_REQUIRED
 if copy_limit_probe:required=(COPY_REQUIRED[-1],)
 if mutation_limit_probe:required=('DataMutationNativeRequestLimitRejectionNeverSplitsOrReplays',)
 if grid_write_probe:required=WRITE_GRID_REQUIRED
 packages=['./internal/services/consul']+(['./internal/services/dbruntime'] if session else [])+(['./internal/ui/connectioneditor','./internal/ui/kvworkspace','./internal/ui/shell'] if public else [])+(['./internal/ui/dataworkspace'] if public_grid else [])
 if grid_write_probe:packages=['./internal/services/dbruntime','./internal/ui/shell','./internal/ui/dataworkspace']
 if copy_probe or copy_limit_probe or prefix_delete_probe:packages=['./internal/services/consul']
 if public_prefix_probe:packages=['./internal/services/dbruntime','./internal/ui/shell','./internal/ui/dataworkspace']
 if prefix_set_probe:packages=['./internal/services/consul']
 if public_prefix_set_probe:packages=['./internal/services/dbruntime','./internal/ui/shell','./internal/ui/dataworkspace']
 if public_copy_probe:packages=['./internal/services/dbruntime','./internal/ui/shell','./internal/ui/dataworkspace']
 report={'nonce':nonce,'scope':'public-consul-original-prefix-explicit-batches' if public_prefix_set else 'diagnostic-consul-public-prefix-explicit-batches' if public_prefix_set_probe else 'private-consul-original-prefix-explicit-batches-with-public-prefix-regression' if prefix_delete_set else 'diagnostic-consul-original-prefix-explicit-batch-foundation' if prefix_set_probe else 'public-consul-original-captured-prefix-candidate-delete' if public_prefix_delete else 'diagnostic-consul-public-prefix-candidate-delete' if public_prefix_probe else 'private-consul-original-prefix-candidate-delete-with-public-copy-regression' if prefix_delete else 'diagnostic-consul-prefix-candidate-delete' if prefix_delete_probe else 'public-consul-original-conditional-copy' if public_key_copy else 'diagnostic-consul-public-key-copy-probe' if public_copy_probe else 'private-consul-original-conditional-copy-with-public-write-regression' if key_copy else 'diagnostic-consul-conditional-copy-probe' if copy_probe else 'private-consul-original-copy-request-limit' if copy_limit_probe else 'diagnostic-consul-public-grid-write-probe' if grid_write_probe else 'public-consul-original-grid-mutations' if public_grid_writes else 'private-consul-original-data-mutations-with-public-read-regression' if data_mutations and public_grid else 'private-consul-original-data-request-limit' if mutation_limit_probe else 'diagnostic-consul-probe' if probe else 'public-consul-read-only-shared-grid' if public_grid else 'private-consul-original-data-mutations' if data_mutations else 'private-consul-original-live-data-documents' if data_session else 'public-consul-structured-kv-tools' if tools else 'public-consul-original-http-workspace' if public else 'original-consul-native-document-runtime' if session else 'original-consul-native-foundation','txn_max_request':txn_max_request,'test_timeout':test_timeout,'memory_mib':memory_mib,'public_prefix_set':public_prefix_set,'public_prefix_set_probe':public_prefix_set_probe,'prefix_delete_set':prefix_delete_set,'prefix_set_probe':prefix_set_probe,'mutation_limit_probe':mutation_limit_probe,'public_grid_writes':public_grid_writes,'grid_write_probe':grid_write_probe,'key_copy':key_copy,'public_key_copy':public_key_copy,'public_copy_probe':public_copy_probe,'public_prefix_delete':public_prefix_delete,'public_prefix_probe':public_prefix_probe,'prefix_delete':prefix_delete,'prefix_delete_probe':prefix_delete_probe,'copy_probe':copy_probe,'copy_limit_probe':copy_limit_probe,'mode':mode,'version':version,'image':IMAGES[version],'race':race,'passed':False,'required':required,'packages':packages,'source_hashes':source_hashes()}
 identifier=None;private=None
 try:
  with tempfile.TemporaryDirectory(prefix='nyadb-consul-'+nonce+'-',dir=build) as rawfolder:
   private=Path(rawfolder);secure=mode in ('tls','mtls');acl=mode!='anonymous';token=str(uuid.uuid4()) if acl else ''
   node_id=str(uuid.uuid4());node='nyadb-'+nonce
   config={'node_id':node_id,'datacenter':'dc1','primary_datacenter':'dc1','ui_config':{'enabled':False},'ports':{'http':-1 if secure else 8500,'https':8501 if secure else -1}}
   if txn_max_request!=524288:config['limits']={'txn_max_req_len':txn_max_request}
   if acl:config['acl']={'enabled':True,'default_policy':'deny','enable_token_persistence':False,'tokens':{'initial_management':token}}
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
   readonly=writer=''
   if acl:
    for name,rules in [('kv-read','key_prefix "allowed/" { policy = "read" }'),('kv-write','key_prefix "allowed/" { policy = "write" } session_prefix "" { policy = "write" }')]:
     policy=http('PUT','/v1/acl/policy',{'Name':'owned-'+name,'Rules':rules})
     result=http('PUT','/v1/acl/token',{'Description':'owned '+name,'Policies':[{'ID':policy['ID']}]})
     if name=='kv-read':readonly=result['SecretID']
     else:writer=result['SecretID']
   if probe:
    report['passed']=True
   else:
    env={k:v for k,v in os.environ.items() if not k.startswith('NYADB_CONSUL_')};env.update(GOWORK='off',GOFLAGS='-mod=readonly',NYADB_CONSUL_IT='1',NYADB_CONSUL_TXN_MAX_REQUEST=str(txn_max_request),NYADB_CONSUL_ENDPOINT=endpoint,NYADB_CONSUL_AUTH='token' if acl else 'none',NYADB_CONSUL_TOKEN=token,NYADB_CONSUL_READ_TOKEN=readonly,NYADB_CONSUL_WRITE_TOKEN=writer,NYADB_CONSUL_VERSION=version,NYADB_CONSUL_NODE_ID=node_id)
    images=path.with_name(path.stem+'-public-images')
    if public:env.update(NYADB_CONSUL_PUBLIC_IMAGES=str(images))
    grid_images=path.with_name(path.stem+'-grid-images')
    if public_grid:env.update(NYADB_CONSUL_GRID_IMAGES=str(grid_images))
    if secure:env.update(NYADB_CONSUL_CA=str(private/'ca.crt'))
    if mode=='mtls':env.update(NYADB_CONSUL_CERT=str(private/'client.crt'),NYADB_CONSUL_KEY=str(private/'client.key'))
    args=['go','test','-p=1']+(['-race'] if race else [])+packages+['-run','^('+'|'.join('TestConsulNative'+c for c in required)+')$','-count=1','-v','-timeout='+str(test_timeout)+'s']
    with log.open('w') as out:r=subprocess.run(args,cwd=ROOT,env=env,stdout=out,stderr=subprocess.STDOUT)
    text=log.read_text()
    if r.returncode:raise RuntimeError('Native Consul acceptance failed; inspect '+str(log.relative_to(ROOT)))
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
    report.update(grid_images=grid_pictures,passed=True,accepted=accepted,public_images=pictures,log_sha256=hashlib.sha256(log.read_bytes()).hexdigest())
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
 p=argparse.ArgumentParser();p.add_argument('--mode',choices=('anonymous','token','tls','mtls'),default='anonymous');p.add_argument('--version',choices=tuple(IMAGES),default='2.0.2');p.add_argument('--race',action='store_true');p.add_argument('--probe',action='store_true');p.add_argument('--session',action='store_true');p.add_argument('--public',action='store_true');p.add_argument('--tools',action='store_true');p.add_argument('--data-session',action='store_true');p.add_argument('--public-grid',action='store_true');p.add_argument('--data-mutations',action='store_true');p.add_argument('--txn-max-request',type=int,choices=(524288,8388608),default=524288);p.add_argument('--mutation-limit-probe',action='store_true');p.add_argument('--public-grid-writes',action='store_true');p.add_argument('--grid-write-probe',action='store_true');p.add_argument('--key-copy',action='store_true');p.add_argument('--copy-probe',action='store_true');p.add_argument('--copy-limit-probe',action='store_true');p.add_argument('--public-key-copy',action='store_true');p.add_argument('--public-copy-probe',action='store_true');p.add_argument('--prefix-delete',action='store_true');p.add_argument('--prefix-delete-probe',action='store_true');p.add_argument('--public-prefix-delete',action='store_true');p.add_argument('--public-prefix-probe',action='store_true');p.add_argument('--prefix-delete-set',action='store_true');p.add_argument('--prefix-set-probe',action='store_true');p.add_argument('--public-prefix-set',action='store_true');p.add_argument('--public-prefix-set-probe',action='store_true');p.add_argument('--test-timeout',type=int,default=600);p.add_argument('--memory-mib',type=int,choices=(768,2048),default=768);a=p.parse_args()
 verify(a.mode,a.race,a.version,a.probe,a.session,a.public,a.tools,a.data_session,a.public_grid,a.data_mutations,a.txn_max_request,a.mutation_limit_probe,a.public_grid_writes,a.grid_write_probe,a.key_copy,a.copy_probe,a.copy_limit_probe,a.public_key_copy,a.public_copy_probe,a.prefix_delete,a.prefix_delete_probe,a.public_prefix_delete,a.public_prefix_probe,a.prefix_delete_set,a.prefix_set_probe,a.test_timeout,a.memory_mib,a.public_prefix_set,a.public_prefix_set_probe)
