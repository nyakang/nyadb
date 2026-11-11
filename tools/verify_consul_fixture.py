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
GRID_REQUIRED=('PublicDataOriginalLiveReadOnlyRuntimeAndRestrictedScope','PublicDataProcessedReadFaultsKeepOriginalRuntimeAndRequireReopen','PublicDataSharedGridLivePagingInspectorExportsParkingAndOriginalClose','PublicDataSharedGridProcessedReplyRetirementParkingAndExplicitReopen','PublicShellQualifiedGridNavigationMenusSearchStructureAndDisconnect','PublicShellProcessedGridCancelParkingOriginalReopenAndInactiveClose')
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

def verify(mode='anonymous',race=False,version='2.0.2',probe=False,session=False,public=False,tools=False,data_session=False,public_grid=False):
 data_session=data_session or public_grid
 tools=tools or data_session
 public=public or tools
 if probe and (session or public):raise ValueError('Diagnostic probe cannot claim session or public acceptance')
 session=session or public
 build=ROOT/'build/consul';build.mkdir(parents=True,exist_ok=True)
 nonce=uuid.uuid4().hex;path=build/('owned-fixture-'+nonce+'.json');log=path.with_suffix('.log')
 required=REQUIRED+(DOCUMENT_REQUIRED+RUNTIME_REQUIRED if session else ())+(PUBLIC_REQUIRED if public else ())+(TOOLS_REQUIRED if tools else ())+(DATA_REQUIRED if data_session else ())+(GRID_REQUIRED if public_grid else ())
 packages=['./internal/services/consul']+(['./internal/services/dbruntime'] if session else [])+(['./internal/ui/connectioneditor','./internal/ui/kvworkspace','./internal/ui/shell'] if public else [])+(['./internal/ui/dataworkspace'] if public_grid else [])
 report={'nonce':nonce,'scope':'diagnostic-consul-probe' if probe else 'public-consul-read-only-shared-grid' if public_grid else 'private-consul-original-live-data-documents' if data_session else 'public-consul-structured-kv-tools' if tools else 'public-consul-original-http-workspace' if public else 'original-consul-native-document-runtime' if session else 'original-consul-native-foundation','mode':mode,'version':version,'image':IMAGES[version],'race':race,'passed':False,'required':required,'packages':packages,'source_hashes':source_hashes()}
 identifier=None;private=None
 try:
  with tempfile.TemporaryDirectory(prefix='nyadb-consul-'+nonce+'-',dir=build) as rawfolder:
   private=Path(rawfolder);secure=mode in ('tls','mtls');acl=mode!='anonymous';token=str(uuid.uuid4()) if acl else ''
   node_id=str(uuid.uuid4());node='nyadb-'+nonce
   config={'node_id':node_id,'datacenter':'dc1','primary_datacenter':'dc1','ui_config':{'enabled':False},'ports':{'http':-1 if secure else 8500,'https':8501 if secure else -1}}
   if acl:config['acl']={'enabled':True,'default_policy':'deny','enable_token_persistence':False,'tokens':{'initial_management':token}}
   if secure:
    config['ports'].update(grpc=-1,grpc_tls=8503)
    certs(private);config['tls']={'defaults':{'ca_file':'/owned/ca.crt','cert_file':'/owned/server.crt','key_file':'/owned/server.key','verify_incoming':mode=='mtls','verify_outgoing':True}}
   configfile=private/'config.json';configfile.write_text(json.dumps(config));configfile.chmod(0o600)
   port=8501 if secure else 8500
   identifier=command(['docker','run','-d','--name','nyadb-consul-'+nonce,'--label','nyadb.fixture=consul','--label','nyadb.fixture.nonce='+nonce,'--user','0:0','--memory','768m','-p','127.0.0.1::'+str(port),'-v',str(private)+':/owned:ro','--entrypoint','consul',IMAGES[version],'agent','-dev','-client=0.0.0.0','-bind=127.0.0.1','-config-file=/owned/config.json','-node='+node])
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
    env={k:v for k,v in os.environ.items() if not k.startswith('NYADB_CONSUL_')};env.update(GOWORK='off',GOFLAGS='-mod=readonly',NYADB_CONSUL_IT='1',NYADB_CONSUL_ENDPOINT=endpoint,NYADB_CONSUL_AUTH='token' if acl else 'none',NYADB_CONSUL_TOKEN=token,NYADB_CONSUL_READ_TOKEN=readonly,NYADB_CONSUL_WRITE_TOKEN=writer,NYADB_CONSUL_VERSION=version,NYADB_CONSUL_NODE_ID=node_id)
    images=path.with_name(path.stem+'-public-images')
    if public:env.update(NYADB_CONSUL_PUBLIC_IMAGES=str(images))
    grid_images=path.with_name(path.stem+'-grid-images')
    if public_grid:env.update(NYADB_CONSUL_GRID_IMAGES=str(grid_images))
    if secure:env.update(NYADB_CONSUL_CA=str(private/'ca.crt'))
    if mode=='mtls':env.update(NYADB_CONSUL_CERT=str(private/'client.crt'),NYADB_CONSUL_KEY=str(private/'client.key'))
    args=['go','test','-p=1']+(['-race'] if race else [])+packages+['-run','^('+'|'.join('TestConsulNative'+c for c in required)+')$','-count=1','-v','-timeout=600s']
    with log.open('w') as out:r=subprocess.run(args,cwd=ROOT,env=env,stdout=out,stderr=subprocess.STDOUT)
    text=log.read_text()
    if r.returncode:raise RuntimeError('Native Consul acceptance failed; inspect '+str(log.relative_to(ROOT)))
    try:accepted=accepted_cases(text,required)
    except RuntimeError as error:raise RuntimeError('Native Consul acceptance failed; inspect '+str(log.relative_to(ROOT))) from error
    if source_hashes()!=report['source_hashes']:raise RuntimeError('Native Consul source changed during acceptance')
    pictures={}
    if public:
     pictures={str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(images.glob('*.png'))}
     expected={f'{lang}-{size}-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in ('editor-native','shell-native','workspace-write-confirmation','workspace-complete-body','workspace-headers',*(f'workspace-{prefix}{mode}' for prefix in ('unknown-','unknown-after-read-') for mode in ('lost','partial','http500','http408','cancel','disconnect','replace')))}
     if tools:
      expected.update(f'{lang}-{size}-workspace-tool-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in (*(f'{view}-{kind}' for view in ('form','generated','receipt') for kind in ('put','read','list','raw','delete','transaction')),*(f'{prefix}{mode}' for prefix in ('unknown-','unknown-after-read-') for mode in ('lost','partial','http500','http408','cancel','disconnect','replace'))))
     if {Path(name).name for name in pictures}!=expected:raise RuntimeError('Mandatory original public Consul image set failed')
    grid_pictures={}
    if public_grid:
     grid_pictures={str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(grid_images.glob('*.png'))}
     expected_grid={f'{lang}-{size}-{stage}.png' for lang in ('zh-Hans','en') for size in ('1280x800','720x520') for stage in (*(f'data-{s}' for s in ('live-grid','live-next','refresh','count','field','export','parked','empty','read-retired','reopened')),*(f'shell-grid-{s}' for s in ('keys','grid','structure','reopened')))}
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
 p=argparse.ArgumentParser();p.add_argument('--mode',choices=('anonymous','token','tls','mtls'),default='anonymous');p.add_argument('--version',choices=tuple(IMAGES),default='2.0.2');p.add_argument('--race',action='store_true');p.add_argument('--probe',action='store_true');p.add_argument('--session',action='store_true');p.add_argument('--public',action='store_true');p.add_argument('--tools',action='store_true');p.add_argument('--data-session',action='store_true');p.add_argument('--public-grid',action='store_true');a=p.parse_args()
 verify(a.mode,a.race,a.version,a.probe,a.session,a.public,a.tools,a.data_session,a.public_grid)
