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


def certificates(folder):
    def ssl(*args):
        subprocess.run(
            ['openssl', *args], cwd=folder, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    ssl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', 'ca.key', '-out', 'ca.crt', '-days', '2', '-subj', '/CN=NyaDB owned etcd CA')
    for name, cn, usage in [('server', 'server', 'serverAuth'), ('client', 'root', 'clientAuth')]:
        ssl('req', '-newkey', 'rsa:2048', '-nodes', '-keyout', name+'.key', '-out', name+'.csr', '-subj', '/CN='+cn)
        (folder/(name+'.ext')).write_text('extendedKeyUsage='+usage+'\nsubjectAltName=DNS:localhost,IP:127.0.0.1\n')
        ssl('x509', '-req', '-in', name+'.csr', '-CA', 'ca.crt', '-CAkey', 'ca.key', '-CAcreateserial', '-out', name+'.crt', '-days', '2', '-extfile', name+'.ext')
    for path in folder.iterdir():
        path.chmod(0o644)


def verify(mode, race, version):
    image = IMAGES[version]
    nonce = uuid.uuid4().hex
    build = ROOT/'build/etcd'
    build.mkdir(parents=True, exist_ok=True)
    report_path = build/('owned-fixture-'+nonce+'.json')
    log_path = report_path.with_suffix('.log')
    sources = sorted((ROOT/'internal/services/etcd').glob('*.go')) + [ROOT/'go.mod', ROOT/'go.sum', Path(__file__).resolve(), ROOT/'tests/test_etcd_fixture.py']
    report = dict(nonce=nonce, scope='private-native-v3-foundation', mode=mode, version=version,
                  image=image, race=race, passed=False,
                  source_hashes={str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in sources})
    identifier = None
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
                certificates(private)
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
                ctl('auth', 'enable')
            port = run(['docker', 'port', identifier, '2379/tcp']).split(':')[-1]
            environment = dict(os.environ, GOWORK='off', NYADB_ETCD_NATIVE_IT='1',
                               NYADB_ETCD_ENDPOINT=scheme+'://localhost:'+port,
                               NYADB_ETCD_VERSION=version, NYADB_ETCD_MODE=mode,
                               NYADB_ETCD_USERNAME=username, NYADB_ETCD_SECRET=secret)
            if tls:
                environment['NYADB_ETCD_CA'] = str(private/'ca.crt')
            if mode == 'mtls':
                environment.update(NYADB_ETCD_CERT=str(private/'client.crt'), NYADB_ETCD_KEY=str(private/'client.key'))
            command = ['go', 'test']+(['-race'] if race else [])+['./internal/services/etcd', '-run', '^TestEtcdNative', '-count=1', '-v', '-timeout=180s']
            with log_path.open('w') as out:
                result = subprocess.run(command, cwd=ROOT, env=environment, stdout=out, stderr=subprocess.STDOUT)
            log = log_path.read_text()
            if result.returncode or re.search(r'^\s*--- (FAIL|SKIP):|WARNING: DATA RACE', log, re.M):
                raise RuntimeError('Native acceptance failed; inspect '+str(log_path.relative_to(ROOT)))
            accepted = re.findall(r'^--- PASS: TestEtcdNative(\w+) \(', log, re.M)
            if sorted(accepted) != sorted(REQUIRED):
                raise RuntimeError('Native acceptance has missing or unexpected cases')
            for name, expected in report['source_hashes'].items():
                if hashlib.sha256((ROOT/name).read_bytes()).hexdigest() != expected:
                    raise RuntimeError('Native source changed during acceptance')
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
    parser.add_argument('--race', action='store_true')
    parser.add_argument('--version', choices=tuple(IMAGES), default='3.7.3')
    args = parser.parse_args()
    verify(args.mode, args.race, args.version)
