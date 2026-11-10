#!/usr/bin/env python3
"""Verify native member protocol in an owned three-voter/one-learner cluster."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import time
import uuid

from verify_etcd_fixture import IMAGES, ROOT, remove_owned, run


def remove_network(identifier, nonce):
    if not isinstance(identifier, str) or not re.fullmatch(r'[0-9a-f]{64}', identifier):
        raise RuntimeError('Network cleanup requires its original immutable ID')
    result = subprocess.run(['docker', 'network', 'inspect', identifier], capture_output=True, text=True)
    if result.returncode:
        if 'No such' in result.stderr:
            return
        raise RuntimeError('Cannot inspect original owned network')
    try:
        records = json.loads(result.stdout)
        valid = (len(records) == 1 and records[0]['Id'] == identifier and
                 records[0]['Labels'].get('nyadb.fixture') == 'etcd-member' and
                 records[0]['Labels'].get('nyadb.fixture.nonce') == nonce and not records[0].get('Containers'))
    except (ValueError, TypeError, KeyError, IndexError, AttributeError):
        valid = False
    if not valid:
        raise RuntimeError('Refusing foreign, replaced or occupied network cleanup')
    run(['docker', 'network', 'rm', identifier])


def verify(version, race):
    nonce = uuid.uuid4().hex
    build = ROOT/'build/etcd'
    build.mkdir(parents=True, exist_ok=True)
    path = build/('owned-member-fixture-'+nonce+'.json')
    log_path = path.with_suffix('.log')
    sources = sorted((ROOT/'internal/services/etcd').glob('*.go')) + [ROOT/'go.mod', ROOT/'go.sum', Path(__file__).resolve(), ROOT/'tools/verify_etcd_fixture.py', ROOT/'tests/test_etcd_fixture.py', ROOT/'tests/test_etcd_member_fixture.py']
    report = dict(nonce=nonce, version=version, image=IMAGES[version], race=race, passed=False,
                  scope='native-etcd-explicit-member-transports-three-voters-one-learner',
                  source_hashes={str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in sources})
    network = None
    identifiers = []
    try:
        network = run(['docker', 'network', 'create', '--label', 'nyadb.fixture=etcd-member',
                       '--label', 'nyadb.fixture.nonce='+nonce, 'nyadb-etcd-members-'+nonce])
        report['network_id'] = network
        def start(index, initial, existing=False):
            args = ['docker', 'run', '-d', '--name', 'nyadb-etcd-member-'+nonce+'-'+str(index),
                    '--label', 'nyadb.fixture=etcd', '--label', 'nyadb.fixture.nonce='+nonce,
                    '--network', network, '--network-alias', 'node'+str(index), '-p', '127.0.0.1::2379', IMAGES[version],
                    '/usr/local/bin/etcd', '--name=node'+str(index), '--data-dir=/tmp/etcd-data',
                    '--listen-client-urls=http://0.0.0.0:2379', '--advertise-client-urls=http://node'+str(index)+':2379',
                    '--listen-peer-urls=http://0.0.0.0:2380', '--initial-advertise-peer-urls=http://node'+str(index)+':2380',
                    '--initial-cluster='+initial, '--initial-cluster-token='+nonce,
                    '--initial-cluster-state='+('existing' if existing else 'new'), '--log-level=error']
            identifier = run(args)
            identifiers.append(identifier)
            return identifier
        initial = ','.join('node'+str(i)+'=http://node'+str(i)+':2380' for i in range(3))
        for i in range(3):
            start(i, initial)
        def ctl(*args):
            command = ['docker', 'exec', identifiers[0], '/usr/local/bin/etcdctl', '--endpoints=http://node0:2379',
                       '--dial-timeout=2s', '--command-timeout=5s', *args]
            result = subprocess.run(command, capture_output=True, text=True, timeout=10)
            if result.returncode:
                # This fixture is anonymous and has no credential arguments.
                raise RuntimeError('Owned member control failed: '+result.stderr[-2000:])
            return result.stdout.strip()
        for _ in range(60):
            try:
                ctl('endpoint', 'health', '--cluster')
                break
            except (RuntimeError, subprocess.TimeoutExpired):
                time.sleep(0.5)
        else:
            raise RuntimeError('Original three voters did not become healthy')
        # etcd's reconfiguration health window requires sustained peer health,
        # beyond a single successful endpoint probe. Add the learner once;
        # never retry a membership write whose outcome might be unknown.
        time.sleep(6)
        ctl('member', 'add', 'node3', '--learner', '--peer-urls=http://node3:2380')
        start(3, initial+',node3=http://node3:2380', existing=True)
        for _ in range(60):
            directory = json.loads(ctl('member', 'list', '--write-out=json'))
            if len(directory.get('members', [])) == 4 and all(m.get('name') for m in directory['members']):
                break
            time.sleep(0.5)
        else:
            raise RuntimeError('Original learner did not join the directory')
        report['container_ids'] = list(identifiers)
        targets = [dict(Name='node'+str(i), Endpoint='http://localhost:'+run(['docker', 'port', identifier, '2379/tcp']).split(':')[-1])
                   for i, identifier in enumerate(identifiers)]
        environment = dict(os.environ, GOWORK='off', GOFLAGS='-mod=readonly', CGO_ENABLED='1' if race else '0',
                           NYADB_ETCD_NATIVE_IT='1', NYADB_ETCD_MEMBER_CLUSTER_IT='1', NYADB_ETCD_MODE='anonymous',
                           NYADB_ETCD_VERSION=version, NYADB_ETCD_ENDPOINT=targets[0]['Endpoint'], NYADB_ETCD_MEMBER_TARGETS=json.dumps(targets))
        test = 'TestEtcdNativeMemberClusterDirectoryExplicitAliasesLearnerAndFreshLeaderObservation'
        command = ['go', 'test', '-p=1']+(['-race'] if race else [])+['./internal/services/etcd', '-run', '^'+test+'$', '-count=1', '-v', '-timeout=120s']
        with log_path.open('w') as out:
            result = subprocess.run(command, cwd=ROOT, env=environment, stdout=out, stderr=subprocess.STDOUT)
        log = log_path.read_text()
        if result.returncode or re.search(r'^\s*--- (FAIL|SKIP):|WARNING: DATA RACE', log, re.M) or len(re.findall(r'^--- PASS: '+test+r' \(', log, re.M)) != 1:
            raise RuntimeError('Member cluster acceptance failed; inspect '+str(log_path.relative_to(ROOT)))
        for name, expected in report['source_hashes'].items():
            if hashlib.sha256((ROOT/name).read_bytes()).hexdigest() != expected:
                raise RuntimeError('Native member source changed during acceptance')
        report.update(passed=True, accepted=[test], log_sha256=hashlib.sha256(log_path.read_bytes()).hexdigest())
    finally:
        report['container_ids'] = list(identifiers)
        cleanup_errors = []
        for identifier in reversed(identifiers):
            try:
                remove_owned(identifier, nonce)
            except Exception as exc:
                cleanup_errors.append(exc)
        if network:
            try:
                remove_network(network, nonce)
            except Exception as exc:
                cleanup_errors.append(exc)
        report['owned_resources_removed'] = not cleanup_errors
        path.write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n')
        if cleanup_errors:
            raise RuntimeError('Original member fixture cleanup failed') from cleanup_errors[0]
    print(path.relative_to(ROOT))


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--version', choices=tuple(IMAGES), default='3.7.3')
    parser.add_argument('--race', action='store_true')
    args = parser.parse_args()
    verify(args.version, args.race)
