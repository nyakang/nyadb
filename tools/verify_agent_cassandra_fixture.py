#!/usr/bin/env python3
"""Accept the managed Cassandra native-agent foundation on an owned 4.1.10 node.

The existing public protocol-v4 provider remains separate. This gate verifies
NyaDB's original managed processes/receipts/values/cursors/cancellation; it does
not accept public managed-driver configuration, editor or workspace publication.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import secrets
import select
import signal
import subprocess
import time
import zipfile

ROOT = Path(__file__).resolve().parents[1]
DBX = ROOT / 'temp/dbx'
SOURCE = DBX / 'agents/drivers/cassandra-go'
IMAGE = 'cassandra@sha256:5e620e10efaeaf936ae73963e594c98df88f0cd468582d7ceb324cbb864f0604'
REQUIRED = ['TestCassandraAgentManagedOriginalReceiptsValuesCursorAndClose',
            'TestCassandraAgentManagedDocumentIsolationAndOriginalCancellation']
LOCAL_REQUIRED = [name.replace('AgentManaged', 'AgentLocal') for name in REQUIRED]
ORIGINAL_REQUIRED = ['TestCassandraIntegration', 'TestCassandraTableDataCursorIntegration']

def digest(raw):
    return hashlib.sha256(raw).hexdigest()

def run(args, cwd=ROOT, env=None, timeout=600):
    result = subprocess.run(args, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout)
    if result.returncode:
        raise RuntimeError(str(args[:3]) + '\n' + result.stdout.decode(errors='replace')[-6000:])
    return result.stdout


def run_logged(args, logfile, cwd=ROOT, env=None, timeout=600):
    with logfile.open('wb') as log:
        result = subprocess.run(args, cwd=cwd, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=timeout)
    output = logfile.read_bytes()
    if result.returncode:
        raise RuntimeError('Command failed; inspect '+str(logfile)+'\n'+output.decode(errors='replace')[-6000:])
    return output

def source_snapshot():
    files = {}
    for directory in [SOURCE, DBX/'agents/go-common/gosasl', DBX/'agents/go-common/go-gssapi']:
        for path in sorted(directory.rglob('*')):
            if path.is_file() and '.git' not in path.parts:
                files[str(path.relative_to(DBX))] = digest(path.read_bytes())
    files['LICENSE'] = digest((DBX/'LICENSE').read_bytes())
    return files

def nyadb_snapshot():
    paths = run(['git','ls-files','-z','--cached','--others','--exclude-standard']).decode().split('\0')
    return {name:digest((ROOT/name).read_bytes()) for name in sorted(set(paths)-{''})}

def json_stream(raw):
    decoder = json.JSONDecoder()
    text, offset = raw.decode(), 0
    while offset < len(text):
        while offset < len(text) and text[offset].isspace(): offset += 1
        if offset == len(text): return
        value, offset = decoder.raw_decode(text, offset)
        yield value

def verify_acceptance(output, required=REQUIRED):
    for name in required:
        if not re.search(r'^--- PASS: ' + re.escape(name) + r' \(', output, re.M):
            raise RuntimeError('Required managed Cassandra acceptance missing: ' + name)

def build_original(base, before):
    env = dict(os.environ, GOWORK='off', CGO_ENABLED='0')
    binary = base / 'original-agent'
    run(['go', 'build', '-mod=readonly', '-o', str(binary), '.'], SOURCE, env)
    dependencies = run(['go', 'list', '-mod=readonly', '-deps', '-json', '.'], SOURCE, env)
    (base / 'original-dependencies.jsonl').write_bytes(dependencies)
    build_info = run(['go', 'version', '-m', str(binary)])
    (base / 'original-build-info.txt').write_bytes(build_info)
    if source_snapshot() != before:
        raise RuntimeError('Building modified the original source or dependency manifests')
    payload = {'bin/agent': binary.read_bytes(), 'licenses/dbx-LICENSE': (DBX / 'LICENSE').read_bytes()}
    modules = {}
    for package in json_stream(dependencies):
        module = package.get('Module')
        if module:
            modules[module['Path']] = module
    module_evidence, missing = [], []
    for index, (name, module) in enumerate(sorted(modules.items())):
        location = module.get('Replace', module)
        directory = Path(location['Dir'])
        licenses = [p for p in sorted(directory.rglob('*')) if p.is_file() and re.match(r'^(license|licence|notice|copying|copyright)([._-]|$)', p.name, re.I)]
        if name != 'github.com/t8y2/dbx/agents/drivers/cassandra-go' and not licenses:
            missing.append({'path': name, 'version': module.get('Version'), 'sum': module.get('Sum')})
        names = []
        for number, path in enumerate(licenses):
            target = 'licenses/module-' + str(index) + '-' + str(number) + '.txt'
            payload[target] = path.read_bytes()
            names.append({'path': str(path.relative_to(directory)), 'payload': target, 'sha256': digest(payload[target])})
        module_evidence.append({'path': name, 'version': module.get('Version'),
                                'sum': module.get('Sum'), 'replace': module.get('Replace'), 'licenses': names})
    goroot = Path(run(['go', 'env', 'GOROOT']).decode().strip())
    payload['licenses/go-LICENSE'] = (goroot / 'LICENSE').read_bytes()
    payload['sources.json'] = json.dumps({'scope': 'unmodified original Cassandra v2 agent',
        'source_files': before, 'compiled_modules': module_evidence,
        'unresolved_module_licenses': missing,
        'binary_sha256': digest(payload['bin/agent']), 'build_info': build_info.decode()}, indent=2).encode()
    (base/'sources.json').write_bytes(payload['sources.json'])
    version = 'dbx-v2-' + digest(payload['bin/agent'])[:16]
    return payload, version, missing


def build_bundle(base, before):
    payload, version, missing = build_original(base, before)
    require_module_licenses(missing)
    manifest = {'format': 1, 'key': 'cassandra', 'version': version,
        'platform': 'linux-amd64', 'runtime': 'native', 'executable': 'bin/agent',
        'profiles': ['cassandra-v2'], 'files': [
            {'path': name, 'sha256': digest(raw), **({'executable': True} if name == 'bin/agent' else {})}
            for name, raw in sorted(payload.items())],
        'licenses': sorted(name for name in payload if name.startswith('licenses/')), 'provenance': 'sources.json'}
    archive = base / 'cassandra-driver.zip'
    with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as bundle:
        bundle.writestr('manifest.json', json.dumps(manifest, indent=2))
        for name, raw in sorted(payload.items()):
            bundle.writestr(name, raw)
    return archive, digest(archive.read_bytes()), version


def require_module_licenses(missing):
    if missing:
        raise RuntimeError('Compiled module license missing; managed package acceptance blocked: ' +
                           ', '.join(item['path']+'@'+item['version'] for item in missing))


def verify_receipt(record):
    raw = base64.b64decode(record['native_bytes'], validate=True)
    if digest(raw) != record['native_sha256']:
        raise RuntimeError('Managed original receipt bytes changed')
    original = json.loads(raw)
    receipt = record['receipt']
    if receipt.get('ExecutorAcknowledged') is not True or receipt.get('Dispatched') is not True or original.get('jsonrpc') != '2.0' or type(original.get('id')) is not int or original['id'] <= 0:
        raise RuntimeError('Original managed executor acknowledgement missing')
    if ('result' in original) == ('error' in original):
        raise RuntimeError('Original managed receipt is incomplete or contradictory')
    if 'result' in original and original['result'] != receipt['Result']:
        raise RuntimeError('Original managed result projection changed')
    if 'error' in original and (receipt.get('RemoteError') is None or original['error'].get('code') != receipt['RemoteError'].get('Code')):
        raise RuntimeError('Original managed error projection changed')
    return original


def verify_receipts(base):
    values = json.loads((base/'managed-values-cursors.json').read_text())
    cancellation = json.loads((base/'managed-cancellation.json').read_text())
    records = [*values['opening'],values['query_receipt'],values['syntax_receipt'],values['metadata_receipt'],*values['page_receipts'],values['bounded_receipt'],values['empty_receipt']]
    if len(values['opening']) != 3 or len(values['page_receipts']) != 3:
        raise RuntimeError('Managed original opening/page receipts missing')
    for record in records: verify_receipt(record)
    syntax=verify_receipt(values['syntax_receipt'])
    native_code=values['syntax_receipt']['receipt']['RemoteError']
    if syntax.get('error',{}).get('data',{}).get('sqlState')!='0x2000' or native_code.get('NativeCode')!='0x2000' or native_code.get('SQLState')!='':
        raise RuntimeError('Original CQL error code was lost or misrepresented as SQLSTATE')
    canceled = verify_receipt(cancellation['original_receipt'])
    r = cancellation['original_receipt']['receipt']
    if r.get('CancellationRequested') is not True or 'error' not in canceled or any(type(cancellation[k]) is not int or cancellation[k] <= 0 for k in ['original_pid','independent_pid']) or cancellation['original_pid'] == cancellation['independent_pid']:
        raise RuntimeError('Original managed cancellation/process isolation missing')
    if [len(page['SQL']['Sets'][0]['Rows']) for page in values['pages']] != [100,100,5] or not values['bounded_terminal']['SQL']['Truncated']:
        raise RuntimeError('Original managed paging boundary changed')
    if values.get('early_close_acknowledged') is not True or values['empty_page']['HasMore'] is not False or values['empty_page']['SQL']['Sets'][0]['Rows'] not in [[], None]:
        raise RuntimeError('Original cursor early-close/empty-page evidence missing')
    return len(records)+1


def cleanup_owned_container(base, container, token):
    observed=json.loads(run(['docker','inspect',container]))[0]
    if observed['Id']!=container or observed['Config']['Labels'].get('nyadb.cassandra.agent.fixture')!=token:
        raise RuntimeError('Original fixture ownership changed')
    (base/'server.log').write_bytes(run(['docker','logs',container]))
    volumes={m['Name'] for m in observed['Mounts'] if m['Type']=='volume'}
    run(['docker','rm','-f','-v',container])
    if volumes.intersection(run(['docker','volume','ls','-q']).decode().splitlines()):
        raise RuntimeError('Owned anonymous volume remained')
    return len(volumes)


def probe_original(base, endpoint, keyspace):
    host, port = endpoint.rsplit(':', 1)
    receipts = []
    with (base/'original-probe-stderr.log').open('wb') as stderr:
        p = subprocess.Popen([str(base/'original-agent')], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=stderr)
        try:
            def read():
                if not select.select([p.stdout], [], [], 35)[0]:
                    raise RuntimeError('Original source probe response timed out')
                raw = p.stdout.readline()
                if not raw:
                    raise RuntimeError('Original source probe exited before acknowledgement')
                receipts.append({'native_bytes':base64.b64encode(raw).decode(), 'native_sha256':digest(raw)})
                return json.loads(raw)
            if read() != {'ready':True}:
                raise RuntimeError('Original source probe readiness changed')
            params = {'host':host, 'port':int(port), 'database':keyspace,
                      'url_params':'retry=FallthroughRetryPolicy&connecttimeout=15000ms&requesttimeout=11000ms',
                      'agentSessionId':'original-source-probe'}
            for i, method in enumerate(['handshake','open_session','connection_info','close_session','shutdown'], 1):
                p.stdin.write((json.dumps({'jsonrpc':'2.0','id':i,'method':method,'params':params})+'\n').encode())
                p.stdin.flush()
                response = read()
                if response.get('id') != i or 'result' not in response:
                    raise RuntimeError('Original source probe failed at '+method)
            p.stdin.close()
            p.wait(timeout=10)
            if p.returncode:
                raise RuntimeError('Original source probe did not exit normally')
        finally:
            if p.poll() is None:
                p.kill()
                p.wait()
            (base/'original-probe-receipts.json').write_text(json.dumps(receipts,indent=2))


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--race',action='store_true')
    parser.add_argument('--local-original-diagnostic', action='store_true',
        help='Run a local pinned original binary without packaging or accepting managed publication')
    args=parser.parse_args()
    def shutdown(signum, _frame):
        raise SystemExit(128+signum)
    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGINT, shutdown)
    if platform.system()!='Linux' or platform.machine() not in ['x86_64','amd64']:
        raise RuntimeError('Pinned managed acceptance requires Linux amd64')
    token=secrets.token_hex(8)
    base=ROOT/'build/cassandra'/('managed-'+token)
    base.mkdir(parents=True)
    print('Evidence: '+str(base.relative_to(ROOT)),flush=True)
    before,nyadb=source_snapshot(),nyadb_snapshot()
    (base/'original-source.json').write_text(json.dumps(before,indent=2))
    (base/'nyadb-source.json').write_text(json.dumps(nyadb,indent=2))
    container=None
    result={'scope':'local_original_agent_diagnostic' if args.local_original_diagnostic else 'managed_native_agent_foundation',
        'managed_package_accepted':False,'public_managed_entry_accepted':False,
        'existing_native_provider_replaced':False,'server_version':'4.1.10','image':IMAGE,'race':args.race,'status':'running'}
    try:
        if args.local_original_diagnostic:
            payload,version,missing=build_original(base,before)
            sha=digest(payload['bin/agent'])
            driver={'binary':str(base/'original-agent'),'sha256':sha,'version':version}
            result['unresolved_module_licenses']=missing
        else:
            archive,sha,version=build_bundle(base,before)
            driver={'archive':str(archive),'sha256':sha,'version':version}
        container=run(['docker','run','-d','--name','nyadb-cassandra-agent-'+token,
            '--label','nyadb.cassandra.agent.fixture='+token,'-p','127.0.0.1::9042',
            '-e','MAX_HEAP_SIZE=512M','-e','HEAP_NEWSIZE=128M','-e','CASSANDRA_NUM_TOKENS=16',IMAGE]).decode().strip()
        observed=json.loads(run(['docker','inspect',container]))[0]
        (base/'original-container.json').write_text(json.dumps(observed,indent=2))
        bindings=observed['NetworkSettings']['Ports']['9042/tcp']
        if observed['Id']!=container or observed['Config']['Image']!=IMAGE or len(bindings)!=1 or bindings[0]['HostIp']!='127.0.0.1':
            raise RuntimeError('Pinned original fixture identity or loopback binding changed')
        port=bindings[0]['HostPort']
        deadline=time.monotonic()+180
        while time.monotonic()<deadline:
            ready=subprocess.run(['docker','exec',container,'cqlsh','-e','SELECT release_version FROM system.local;'],stdout=subprocess.PIPE,stderr=subprocess.STDOUT,timeout=15)
            if ready.returncode==0 and b'4.1.10' in ready.stdout: break
            time.sleep(1)
        else: raise RuntimeError('Owned original Cassandra did not become ready')
        keyspace='nyadb_agent_'+token
        seed="CREATE KEYSPACE "+keyspace+" WITH replication={'class':'SimpleStrategy','replication_factor':1};"
        seed+='CREATE TABLE '+keyspace+'.original_values (id int PRIMARY KEY,big bigint,exact decimal,binary blob,huge varint,absent text,pair frozen<tuple<int,text>>);'
        seed+='INSERT INTO '+keyspace+'.original_values (id,big,exact,binary,huge,pair) VALUES (1,9007199254740993,9007199254740993.12345678901234567890,0x00ff80,1234567890123456789012345678901234567890,(7,\'seven\'));'
        seed+='CREATE TABLE '+keyspace+'.pages (bucket int,id int,payload blob,PRIMARY KEY(bucket,id));BEGIN UNLOGGED BATCH '
        seed+=''.join('INSERT INTO '+keyspace+'.pages (bucket,id,payload) VALUES (1,'+str(i)+',0x00ff);' for i in range(205))+'APPLY BATCH;'
        (base/'independent-seed.log').write_bytes(run(['docker','exec',container,'cqlsh','-e',seed]))
        fixture=base/'fixture.json'
        fixture.write_text(json.dumps({**driver,'endpoint':'127.0.0.1:'+port,'keyspace':keyspace,'directory':str(base)},indent=2))
        fixture.chmod(0o600)
        probe_original(base,'127.0.0.1:'+port,keyspace)
        local=args.local_original_diagnostic
        gate='NYADB_CASSANDRA_AGENT_LOCAL_IT' if local else 'NYADB_CASSANDRA_AGENT_IT'
        env=dict(os.environ,CGO_ENABLED='1' if args.race else '0',NYADB_CASSANDRA_AGENT_FIXTURE=str(fixture))
        env.pop('NYADB_CASSANDRA_AGENT_IT',None)
        env.pop('NYADB_CASSANDRA_AGENT_LOCAL_IT',None)
        env[gate]='1'
        required=LOCAL_REQUIRED if local else REQUIRED
        command=['go','test','-count=1','-timeout=5m','-v']+(['-race'] if args.race else [])+['./internal/services/cassandra','-run','^TestCassandraAgent'+('Local' if local else 'Managed')]
        output=run_logged(command,base/'acceptance.log',env=env)
        verify_acceptance(output.decode(),required)
        result['original_receipts']=verify_receipts(base)
        original_env=dict(os.environ,GOWORK='off',CASSANDRA_TEST_HOST='127.0.0.1',CASSANDRA_TEST_PORT=port)
        original_command=['go','test','-mod=readonly','-count=1','-timeout=5m','-v']+(['-race'] if args.race else [])+['-run','^TestCassandra(Integration|TableDataCursorIntegration)$','.']
        output=run_logged(original_command,base/'original-integration.log',cwd=SOURCE,env=original_env)
        verify_acceptance(output.decode(),ORIGINAL_REQUIRED)
        if source_snapshot()!=before or nyadb_snapshot()!=nyadb:
            raise RuntimeError('Original or NyaDB source changed during managed acceptance')
        result.update(status='diagnostic_passed' if local else 'passed',passed_tests=required,
            original_source_tests=ORIGINAL_REQUIRED,managed_package_accepted=not local,
            original_source_unchanged=True,nyadb_source_unchanged=True,
            unverified=['public managed-driver options/editor/provider/workspaces','TLS/mTLS/password/Kerberos/Astra/routes/other versions','committed lost-write outcomes','external new-keyspace metadata refresh'])
    except Exception as error:
        result.update(status='failed',error=str(error))
        raise
    finally:
        cleanup=[]
        records=[json.loads(line) for line in (base/'processes.jsonl').read_text().splitlines()] if (base/'processes.jsonl').exists() else []
        for record in records:
            try:
                stat=Path('/proc/'+str(record['pid'])+'/stat').read_text()
                fields=stat[stat.rfind(')')+1:].split()
                exe=os.readlink('/proc/'+str(record['pid'])+'/exe')
                if fields[19]==record['start_ticks'] and exe==record['executable']:
                    os.kill(record['pid'],signal.SIGKILL)
                    cleanup.append('Original managed process required forced cleanup')
            except (FileNotFoundError,ProcessLookupError):pass
        result['original_processes']=len(records)
        if container:
            try:
                volumes=cleanup_owned_container(base,container,token)
                result.update(original_container_removed=True,anonymous_volumes_removed=volumes)
            except Exception as error:
                cleanup.append(str(error))
        if cleanup:result.update(status='failed',cleanup_errors=cleanup)
        (base/'verification.json').write_text(json.dumps(result,indent=2))
        if cleanup:raise RuntimeError('; '.join(cleanup))
    print(json.dumps(result,indent=2),flush=True)


if __name__=='__main__':main()
