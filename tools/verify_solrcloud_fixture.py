#!/usr/bin/env python3
"""Verify private SolrCloud sessions on owned Solr 9.9.0/ZooKeeper 3.9.4 containers.

One node, two primary collection shards, RF=1; HTTP none and default open ZK ACL.
This fixture provides no HA, cross-replica consensus, TLS or ZK ACL claim.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import tempfile
import time
import urllib.request
import uuid

from verify_solr_fixture import IMAGE, ROOT, remove_owned

ZK_IMAGE = 'zookeeper@sha256:dfa9ba46d14bd52bcb3c1dac3ba91f179cf4cebf7ee576e1c2df09c68fc1d30d'


def remove_owned_network(identifier, nonce):
    if not isinstance(identifier, str) or not re.fullmatch('[0-9a-f]{64}', identifier):
        raise RuntimeError('Invalid immutable network ID')
    result = subprocess.run(['docker', 'network', 'inspect', identifier], capture_output=True, text=True)
    if result.returncode:
        return
    current = json.loads(result.stdout)[0]
    labels = current.get('Labels') or {}
    if current['Id'] != identifier or labels.get('nyadb.fixture') != 'solr' or labels.get('nyadb.fixture.nonce') != nonce or current.get('Containers'):
        raise RuntimeError('Network identity/ownership changed or still attached; refuse cleanup')
    subprocess.run(['docker', 'network', 'rm', identifier], check=True, stdout=subprocess.DEVNULL)


def verify(race=False, data_scope=False):
    base, output = ROOT/'temp/solr', ROOT/'build/solr'
    base.mkdir(parents=True, exist_ok=True)
    output.mkdir(parents=True, exist_ok=True)
    nonce, ids, network = uuid.uuid4().hex, [], None
    record = {'nonce': nonce, 'scope': 'private_solrcloud_data_documents' if data_scope else 'private_solrcloud_sessions_metadata_rest', 'solr_image': IMAGE, 'zk_image': ZK_IMAGE,
              'deployment': 'one node; two primary shards; RF=1; HTTP none; default open ZK ACL; private mounts/isolated network'}

    def shutdown(signum, _frame):
        raise SystemExit(128+signum)
    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGINT, shutdown)

    def docker(*args):
        return subprocess.check_output(['docker', *args], text=True).strip()

    with tempfile.TemporaryDirectory(prefix='cloud-', dir=base) as temporary:
        folder = Path(temporary)
        folder.chmod(0o700)
        for name, owner in [('solr', 8983), ('zkdata', 1000), ('zklog', 1000)]:
            path = folder/name
            path.mkdir(mode=0o700)
            if os.getuid() == 0:
                os.chown(path, owner, owner)
        try:
            for img in [IMAGE, ZK_IMAGE]:
                subprocess.run(['docker', 'image', 'inspect', img], check=True, stdout=subprocess.DEVNULL)
            network = docker('network', 'create', '--label', 'nyadb.fixture=solr', '--label', 'nyadb.fixture.nonce='+nonce, 'nyadb-solr-cloud-'+nonce[:12])
            record['network_id'] = network
            zk = docker('create', '--name', 'nyadb-solr-zk-'+nonce[:12], '--network', network, '--network-alias', 'owned-zookeeper', '--label', 'nyadb.fixture=solr', '--label', 'nyadb.fixture.nonce='+nonce, '--memory=1g', '--cpus=1', '--mount', 'type=bind,src='+str(folder/'zkdata')+',dst=/data', '--mount', 'type=bind,src='+str(folder/'zklog')+',dst=/datalog', ZK_IMAGE)
            ids.append(zk)
            docker('start', zk)
            solr = docker('create', '--name', 'nyadb-solr-cloud-node-'+nonce[:12], '--network', network, '--label', 'nyadb.fixture=solr', '--label', 'nyadb.fixture.nonce='+nonce, '--memory=2g', '--cpus=2', '-p', '127.0.0.1::8983', '-e', 'SOLR_HEAP=512m', '--mount', 'type=bind,src='+str(folder/'solr')+',dst=/var/solr', IMAGE, 'solr-foreground', '-c', '-z', 'owned-zookeeper:2181')
            ids.append(solr)
            record['containers'] = ids.copy()
            docker('start', solr)
            bindings = json.loads(docker('inspect', solr))[0]['NetworkSettings']['Ports']['8983/tcp']
            if len(bindings) != 1 or bindings[0]['HostIp'] != '127.0.0.1':
                raise RuntimeError('Fixture must bind exactly one loopback port')
            endpoint = 'http://127.0.0.1:'+bindings[0]['HostPort']+'/solr'

            def request(path, data=None):
                req = urllib.request.Request(endpoint+path, data=None if data is None else json.dumps(data).encode(), headers={'Content-Type': 'application/json'})
                with urllib.request.urlopen(req, timeout=20) as response:
                    return json.load(response)

            def await_read(read, timeout=180):
                end = time.monotonic()+timeout
                while True:
                    try:
                        if read():
                            return
                    except Exception:
                        pass
                    if time.monotonic() >= end:
                        raise RuntimeError('Native cloud read readiness failed')
                    time.sleep(1)

            await_read(lambda: request('/admin/info/system?wt=json').get('mode') == 'solrcloud' and request('/admin/collections?action=LIST&wt=json').get('collections') == [])
            docker('exec', solr, '/opt/solr/bin/solr', 'zk', 'upconfig', '-z', 'owned-zookeeper:2181', '-n', 'owned_default', '-d', '/opt/solr/server/solr/configsets/_default/conf')
            docker('exec', solr, '/opt/solr/bin/solr', 'zk', 'upconfig', '-z', 'owned-zookeeper:2181', '-n', 'owned_second', '-d', '/opt/solr/server/solr/configsets/_default/conf')
            for name, shards, config in [('nyadb_cloud', 2, 'owned_default'), ('nyadb_second', 1, 'owned_second')]:
                # Native mutation is submitted exactly once, never readiness-retried.
                request('/admin/collections?action=CREATE&name='+name+'&numShards='+str(shards)+'&replicationFactor=1&collection.configName='+config+'&wt=json')
                await_read(lambda: request('/'+name+'/schema?wt=json').get('schema', {}).get('uniqueKey') == 'id' and 'config' in request('/'+name+'/config?wt=json'))
            request('/nyadb_second/schema?wt=json', {'add-field': {'name': 'owned_extra_s', 'type': 'string', 'stored': True, 'indexed': True}})
            await_read(lambda: any(f.get('name') == 'owned_extra_s' for f in request('/nyadb_second/schema?wt=json')['schema']['fields']))
            request('/admin/collections?action=CREATEALIAS&name=CloudAlias&collections=nyadb_cloud,nyadb_second&wt=json')
            request('/admin/collections?action=CREATEALIAS&name=cloudalias&collections=nyadb_cloud&wt=json')
            password = folder/'password'
            password.write_text('')
            password.chmod(0o600)
            probe = folder/'probe.json'
            probe.write_text(json.dumps({'endpoint': endpoint, 'auth': 'none', 'password_file': str(password)}))
            probe.chmod(0o600)
            sources = sorted((ROOT/'internal/services/solr').glob('*.go'))+sorted((ROOT/'internal/services/connections').glob('*.go'))+sorted((ROOT/'internal/db/contract').glob('*.go'))+[Path(__file__).resolve(), ROOT/'tools/verify_solr_fixture.py', ROOT/'tests/test_solr_fixture.py', ROOT/'go.mod', ROOT/'go.sum']
            hashes = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in sources}
            record['source_sha256'] = hashes
            env = os.environ.copy()
            env.pop('NYADB_SOLR_IT', None)
            env.update({'NYADB_SOLR_CLOUD_IT': '1', 'NYADB_SOLR_CONFIG': str(probe)})
            if data_scope:
                env['NYADB_SOLR_DATA_IT'] = '1'
            logfile = output/('cloud-acceptance-'+nonce+'.log')
            command = ['go', 'test', '-count=1', '-v', *(['-race'] if race else []), './internal/services/solr']
            print('Native SolrCloud ready; fixture nonce: '+nonce, flush=True)
            with logfile.open('w') as log:
                result = subprocess.run(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=600)
            text = logfile.read_text()
            if result.returncode or '--- PASS: TestSolrNativeCloudSessionAliasesSchemasAndREST' not in text or (data_scope and '--- PASS: TestSolrNativeCloudDataMemberShardDuplicateIdentityAndAliasRevocation' not in text):
                raise RuntimeError('Cloud native acceptance failed; inspect captured log')
            if any(hashlib.sha256((ROOT/p).read_bytes()).hexdigest() != digest for p, digest in hashes.items()):
                raise RuntimeError('Sources changed during cloud acceptance; rerun on stable sources')
            record.update({'status': 'passed_with_race' if race else 'passed', 'log': str(logfile.relative_to(ROOT)), 'log_sha256': hashlib.sha256(logfile.read_bytes()).hexdigest()})
            print('Native SolrCloud acceptance passed', flush=True)
        finally:
            for identifier in reversed(ids):
                logs = subprocess.run(['docker', 'logs', identifier], capture_output=True, text=True)
                lines = [('[redacted sensitive startup line]' if re.search(r'password|token|authorization|credential|secret|basicauth|commandLineArgs', line, re.I) else line) for line in (logs.stdout+logs.stderr).splitlines()]
                (output/('cloud-server-'+identifier[:12]+'-'+nonce+'.log')).write_text('\n'.join(lines)+'\n')
                remove_owned(identifier, nonce)
            if network:
                remove_owned_network(network, nonce)
            record['fixture_cleaned'] = True
            (output/('owned-cloud-fixture-'+nonce+'.json')).write_text(json.dumps(record, indent=2)+'\n')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--race', action='store_true')
    parser.add_argument('--data', action='store_true')
    args = parser.parse_args()
    verify(args.race, args.data)
