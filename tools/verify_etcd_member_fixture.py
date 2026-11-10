#!/usr/bin/env python3
"""Verify native member protocol in an owned three-voter/one-learner cluster."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import tempfile
import subprocess
import time
import uuid

from verify_etcd_fixture import IMAGES, ROOT, certificates, remove_owned, run


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


def verify(version, race, mode="anonymous", member_session=False, public_members=False):
    if public_members:
        member_session = True
    nonce = uuid.uuid4().hex
    build = ROOT/'build/etcd'
    build.mkdir(parents=True, exist_ok=True)
    path = build/('owned-member-fixture-'+nonce+'.json')
    log_path = path.with_suffix('.log')
    sources = sorted((ROOT/'internal/services/etcd').glob('*.go')) + [ROOT/'go.mod', ROOT/'go.sum', Path(__file__).resolve(), ROOT/'tools/verify_etcd_fixture.py', ROOT/'tests/test_etcd_fixture.py', ROOT/'tests/test_etcd_member_fixture.py']
    if member_session:
        sources += [p for folder in ('internal/db/contract', 'internal/services/dbruntime', 'internal/services/connections') for p in (ROOT/folder).glob('*.go')]
    if public_members:
        sources += [p for folder in ('internal/ui/kvworkspace', 'internal/ui/shell', 'internal/ui/theme',
                                    'internal/ui/connectioncatalog', 'internal/ui/connectioneditor', 'internal/ui/navigation',
                                    'internal/ui/sessionbrowser', 'internal/ui/dataworkspace', 'internal/ui/objectworkspace',
                                    'internal/storage/localdb', 'internal/bootstrap', 'locales') for p in (ROOT/folder).glob('*.go')]
        sources += [ROOT/'locales/en.json', ROOT/'locales/zh-Hans.json']
    sources = sorted(set(sources))
    report = dict(nonce=nonce, version=version, mode=mode, image=IMAGES[version], race=race, passed=False,
                  scope='native-etcd-explicit-member-transports-three-voters-one-learner',
                  source_hashes={str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in sources})
    if member_session:
        report['scope'] = 'original-etcd-member-session-runtime-three-voters-one-learner'
    if public_members:
        report['scope'] = 'public-original-etcd-member-checked-batches-three-voters-one-learner'
    tls = mode in ('tls', 'mtls')
    scheme = 'https' if tls else 'http'
    secret = secrets.token_hex(24) if mode != 'anonymous' else ''
    private_owner = tempfile.TemporaryDirectory(prefix='nyadb-etcd-member-'+nonce+'-', dir=build)
    private = Path(private_owner.name)
    private.chmod(0o755)
    report['private_path'] = str(private.relative_to(ROOT))
    authenticated = False
    network = None
    identifiers = []
    try:
        if tls:
            certificates(private)
            # etcdctl discovers advertised internal URLs for cluster health;
            # validate those names as well as the explicit localhost aliases.
            (private/'server.ext').write_text('extendedKeyUsage=serverAuth\nsubjectAltName=DNS:localhost,IP:127.0.0.1,DNS:node0,DNS:node1,DNS:node2,DNS:node3\n')
            subprocess.run(['openssl', 'x509', '-req', '-in', str(private/'server.csr'),
                            '-CA', str(private/'ca.crt'), '-CAkey', str(private/'ca.key'),
                            '-CAcreateserial', '-out', str(private/'server.crt'), '-days', '2',
                            '-extfile', str(private/'server.ext')], check=True,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        network = run(['docker', 'network', 'create', '--label', 'nyadb.fixture=etcd-member',
                       '--label', 'nyadb.fixture.nonce='+nonce, 'nyadb-etcd-members-'+nonce])
        report['network_id'] = network
        def start(index, initial, existing=False):
            args = ['docker', 'run', '-d', '--name', 'nyadb-etcd-member-'+nonce+'-'+str(index),
                    '--label', 'nyadb.fixture=etcd', '--label', 'nyadb.fixture.nonce='+nonce,
                    '--network', network, '--network-alias', 'node'+str(index), '-p', '127.0.0.1::2379', IMAGES[version],
                    '/usr/local/bin/etcd', '--name=node'+str(index), '--data-dir=/tmp/etcd-data',
                    '--listen-client-urls='+scheme+'://0.0.0.0:2379', '--advertise-client-urls='+scheme+'://node'+str(index)+':2379',
                    '--listen-peer-urls=http://0.0.0.0:2380', '--initial-advertise-peer-urls=http://node'+str(index)+':2380',
                    '--initial-cluster='+initial, '--initial-cluster-token='+nonce,
                    '--initial-cluster-state='+('existing' if existing else 'new'), '--log-level=error']
            if tls:
                image_pos = args.index(IMAGES[version])
                args[image_pos:image_pos] = ['-v', str(private)+':/certs:ro']
                args += ['--cert-file=/certs/server.crt', '--key-file=/certs/server.key']
                if mode == 'mtls':
                    args += ['--client-cert-auth=true', '--trusted-ca-file=/certs/ca.crt']
            identifier = run(args)
            identifiers.append(identifier)
            return identifier
        initial = ','.join('node'+str(i)+'=http://node'+str(i)+':2380' for i in range(3))
        for i in range(3):
            start(i, initial)
        def ctl(*args, input=None):
            command = ['docker', 'exec', '-i']
            environment = dict(os.environ)
            if authenticated:
                environment['ETCDCTL_USER'] = 'root:'+secret
                command += ['-e', 'ETCDCTL_USER']
            command += [identifiers[0], '/usr/local/bin/etcdctl', '--endpoints='+scheme+'://localhost:2379',
                        '--dial-timeout=2s', '--command-timeout=5s']
            if tls:
                command += ['--cacert=/certs/ca.crt']
            if mode == 'mtls':
                command += ['--cert=/certs/client.crt', '--key=/certs/client.key']
            result = subprocess.run(command+list(args), env=environment, input=input, capture_output=True, text=True, timeout=10)
            if result.returncode:
                raise RuntimeError('Owned member control failed')
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
        if mode != 'anonymous':
            ctl('user', 'add', 'root', '--interactive=false', input=secret+'\n')
            ctl('user', 'grant-role', 'root', 'root')
            ctl('user', 'add', 'restricted', '--interactive=false', input=secret+'\n')
            ctl('role', 'add', 'restricted')
            ctl('role', 'grant-permission', 'restricted', 'read', '/allowed/', '--prefix=true')
            ctl('user', 'grant-role', 'restricted', 'restricted')
            ctl('auth', 'enable')
            authenticated = True
        report['container_ids'] = list(identifiers)
        targets = [dict(Name='node'+str(i), Endpoint=scheme+'://localhost:'+run(['docker', 'port', identifier, '2379/tcp']).split(':')[-1])
                   for i, identifier in enumerate(identifiers)]
        environment = dict(os.environ, GOWORK='off', GOFLAGS='-mod=readonly', CGO_ENABLED='1' if race else '0',
                           NYADB_ETCD_NATIVE_IT='1', NYADB_ETCD_MEMBER_CLUSTER_IT='1', NYADB_ETCD_MODE=mode,
                           NYADB_ETCD_VERSION=version, NYADB_ETCD_ENDPOINT=targets[0]['Endpoint'], NYADB_ETCD_MEMBER_TARGETS=json.dumps(targets),
                           NYADB_ETCD_USERNAME='root', NYADB_ETCD_SECRET=secret)
        if tls:
            environment['NYADB_ETCD_CA'] = str(private/'ca.crt')
        if mode == 'mtls':
            environment.update(NYADB_ETCD_CERT=str(private/'client.crt'), NYADB_ETCD_KEY=str(private/'client.key'))
        required = ['TestEtcdNativeMemberClusterDirectoryExplicitAliasesLearnerAndFreshLeaderObservation']
        packages = ['./internal/services/etcd']
        if member_session:
            environment.update(NYADB_ETCD_SESSION_IT='1', NYADB_ETCD_MEMBER_SESSION_IT='1')
            required += ['TestEtcdNativeMemberSessionClusterFourTargetsAndActualMetadataChanges',
                         'TestEtcdNativeMemberRuntimeOriginalPublicationAndEvidenceRecovery',
                         'TestEtcdNativeMemberRuntimeActualProcessedPublicationFaults']
            packages += ['./internal/services/dbruntime']
        if public_members:
            images = build/('owned-member-fixture-'+nonce+'-images')
            environment.update(NYADB_ETCD_PUBLIC_MEMBERS_IT='1', NYADB_ETCD_WORKSPACE_IT='1', NYADB_ETCD_PUBLIC_IT='1',
                               NYADB_ETCD_WORKSPACE_IMAGES=str(images/'workspace'), NYADB_ETCD_PUBLIC_IMAGES=str(images/'shell'))
            required += ['TestEtcdNativeMembersWorkspaceOriginalSelectedVotersCheckedBatchAndCompleteFiles',
                         'TestEtcdNativeMembersWorkspaceActualProcessedFaultsParkingAndOriginalRecovery',
                         'TestEtcdNativeMembersWorkspacePartialBatchRealAckLossAndRestrictedLearner',
                         'TestEtcdNativeShellPublicMembersCheckedOriginalBatchFilesAndCompleteArchives',
                         'TestEtcdNativeShellPublicMembersUnknownParkingPublicationAndRecoveryRetainOriginals']
            # The final native session case removes an owned follower. Run
            # public four-member workbenches before that independent control.
            packages = ['./internal/ui/kvworkspace', './internal/ui/shell'] + packages
        command = ['go', 'test', '-p=1']+(['-race'] if race else [])+packages+['-run', '^('+'|'.join(required)+')$', '-count=1', '-v', '-timeout=600s']
        with log_path.open('w') as out:
            result = subprocess.run(command, cwd=ROOT, env=environment, stdout=out, stderr=subprocess.STDOUT)
        log = log_path.read_text()
        accepted = re.findall(r'^--- PASS: (TestEtcdNative\w+) \(', log, re.M)
        if result.returncode or re.search(r'^\s*--- (FAIL|SKIP):|WARNING: DATA RACE', log, re.M) or sorted(accepted) != sorted(required):
            raise RuntimeError('Member cluster acceptance failed; inspect '+str(log_path.relative_to(ROOT)))
        for name, expected in report['source_hashes'].items():
            if hashlib.sha256((ROOT/name).read_bytes()).hexdigest() != expected:
                raise RuntimeError('Native member source changed during acceptance')
        if public_members:
            for key, directory, expected in [('workspace_images', images/'workspace', 36), ('shell_images', images/'shell', 36)]:
                pictures = sorted(directory.glob('*.png'))
                if len(pictures) != expected:
                    raise RuntimeError('Missing public member images: '+key+' got '+str(len(pictures)))
                report[key] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in pictures}
        report.update(passed=True, accepted=accepted, log_sha256=hashlib.sha256(log_path.read_bytes()).hexdigest())
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
        try:
            private_owner.cleanup()
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
    parser.add_argument('--mode', choices=('anonymous', 'basic', 'tls', 'mtls'), default='anonymous')
    parser.add_argument('--member-session', action='store_true')
    parser.add_argument('--public-members', action='store_true')
    args = parser.parse_args()
    verify(args.version, args.race, args.mode, args.member_session, args.public_members)
