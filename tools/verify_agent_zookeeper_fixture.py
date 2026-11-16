#!/usr/bin/env python3
"""Verify the original DBX v1 agent against an owned official ZooKeeper 3.9.4.

By default this accepts the managed native protocol foundation. --provider also
accepts injected provider/document/workspace workflows. Neither mode publishes
or accepts a public connection card, runtime provider, URI or editor.
Requires Linux, Docker, Go and the complete original source in temp/dbx.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import secrets
import signal
import socket
import subprocess
import time
import zipfile

ROOT = Path(__file__).resolve().parents[1]
DBX = ROOT / 'temp/dbx'
SOURCE = DBX / 'agents/drivers/zookeeper'
IMAGE = 'zookeeper@sha256:dfa9ba46d14bd52bcb3c1dac3ba91f179cf4cebf7ee576e1c2df09c68fc1d30d'
REQUIRED = [
    'TestZooKeeperAgentNativeManagedValuesStatListingAndNamespace',
    'TestZooKeeperAgentNativeEphemeralOwnershipAndExplicitProcessClose',
    'TestZooKeeperAgentNativeCommittedLostReplyCancellationQueueAndRecovery',
    'TestZooKeeperAgentNativeEndpointRoutesRequireCompleteCapturedMapping',
]
PROVIDER_REQUIRED = [
    'TestZooKeeperNativeProviderOriginalValuesNamespaceListingAndDocumentOwnership',
    'TestZooKeeperNativeWorkspaceConfirmationParkedReceiptFilesAndUnknownRecovery',
    'TestZooKeeperNativeWorkspaceCommittedLostReplyCancellationRecoveryAndNoReplay',
]


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def run(args, cwd=ROOT, env=None, timeout=600):
    completed = subprocess.run(args, cwd=cwd, env=env, stdout=subprocess.PIPE,
                               stderr=subprocess.STDOUT, timeout=timeout)
    if completed.returncode:
        raise RuntimeError('Command failed: ' + str(args[:3]) + '\n' + completed.stdout.decode(errors='replace')[-12000:])
    return completed.stdout


def source_snapshot():
    files = {}
    for directory in [SOURCE, DBX / 'agents/go-common/gosasl', DBX / 'agents/go-common/go-gssapi']:
        if not directory.is_dir():
            raise RuntimeError('Complete original source missing: ' + str(directory))
        for path in sorted(directory.rglob('*')):
            if path.is_file() and not any(part in ['.git', 'build'] for part in path.relative_to(directory).parts):
                files[str(path.relative_to(DBX))] = digest(path.read_bytes())
    files['LICENSE'] = digest((DBX / 'LICENSE').read_bytes())
    return files


def nyadb_snapshot():
    paths = sorted(set(run(['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard']).decode().split('\0')) - {''})
    return {name: digest((ROOT / name).read_bytes()) for name in paths}


def json_stream(raw):
    decoder = json.JSONDecoder()
    text, offset = raw.decode(), 0
    while offset < len(text):
        while offset < len(text) and text[offset].isspace():
            offset += 1
        if offset == len(text):
            return
        value, offset = decoder.raw_decode(text, offset)
        yield value


def verify_acceptance(output, required=REQUIRED):
    for name in required:
        if not re.search(r'^--- PASS: ' + re.escape(name) + r' \(', output, re.M):
            raise RuntimeError('Required native acceptance missing: ' + name)


def build_bundle(base, before):
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
    module_evidence = []
    for index, (name, module) in enumerate(sorted(modules.items())):
        location = module.get('Replace', module)
        directory = Path(location['Dir'])
        licenses = [p for p in sorted(directory.rglob('*')) if p.is_file() and re.match(r'^(license|licence|notice|copying|copyright)([._-]|$)', p.name, re.I)]
        if name != 'github.com/t8y2/dbx/agents/drivers/zookeeper' and not licenses:
            raise RuntimeError('Compiled module license missing: ' + name)
        names = []
        for number, path in enumerate(licenses):
            target = 'licenses/module-' + str(index) + '-' + str(number) + '.txt'
            payload[target] = path.read_bytes()
            names.append({'path': str(path.relative_to(directory)), 'payload': target, 'sha256': digest(payload[target])})
        module_evidence.append({'path': name, 'version': module.get('Version'),
                                'sum': module.get('Sum'), 'replace': module.get('Replace'), 'licenses': names})
    goroot = Path(run(['go', 'env', 'GOROOT']).decode().strip())
    payload['licenses/go-LICENSE'] = (goroot / 'LICENSE').read_bytes()
    payload['sources.json'] = json.dumps({'scope': 'unmodified original ZooKeeper v1 agent',
        'source_files': before, 'compiled_modules': module_evidence,
        'binary_sha256': digest(payload['bin/agent']), 'build_info': build_info.decode()}, indent=2).encode()
    version = 'dbx-v1-' + digest(payload['bin/agent'])[:16]
    manifest = {'format': 1, 'key': 'zookeeper', 'version': version,
        'platform': 'linux-amd64', 'runtime': 'native', 'executable': 'bin/agent',
        'profiles': ['zookeeper-v1'], 'files': [
            {'path': name, 'sha256': digest(raw), **({'executable': True} if name == 'bin/agent' else {})}
            for name, raw in sorted(payload.items())],
        'licenses': sorted(name for name in payload if name.startswith('licenses/')), 'provenance': 'sources.json'}
    archive = base / 'zookeeper-driver.zip'
    with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as bundle:
        bundle.writestr('manifest.json', json.dumps(manifest, indent=2))
        for name, raw in sorted(payload.items()):
            bundle.writestr(name, raw)
    return archive, digest(archive.read_bytes()), version


def process_state(record):
    pid = int(record['pid'])
    try:
        stat = Path('/proc/' + str(pid) + '/stat').read_text()
        fields = stat[stat.rfind(')') + 1:].split()
        executable = os.readlink('/proc/' + str(pid) + '/exe')
        return fields[19] == record['start_ticks'] and executable == record['executable']
    except (FileNotFoundError, ProcessLookupError):
        return False


def cleanup_processes(base):
    journal = base / 'processes.jsonl'
    records = [json.loads(line) for line in journal.read_text().splitlines()] if journal.exists() else []
    cleaned = []
    for record in records:
        if process_state(record):
            pid = int(record['pid'])
            if os.getpgid(pid) != pid:
                raise RuntimeError('Original process group ownership changed')
            os.killpg(pid, signal.SIGKILL)
            deadline = time.monotonic() + 5
            while process_state(record) and time.monotonic() < deadline:
                time.sleep(.02)
            if process_state(record):
                raise RuntimeError('Original captured process still alive after termination')
            cleaned.append(record['pid'])
    return {'recorded': len(records), 'forced_cleanup': cleaned,
            'all_original_processes_exited': not any(process_state(record) for record in records)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--race', action='store_true')
    parser.add_argument('--provider', action='store_true')
    args = parser.parse_args()
    if platform.system() != 'Linux' or platform.machine() not in ['x86_64', 'amd64']:
        raise RuntimeError('This pinned acceptance requires Linux amd64')
    token = secrets.token_hex(8)
    base = ROOT / 'build/zookeeper' / ('agent-' + token)
    base.mkdir(parents=True)
    print('Evidence: ' + str(base.relative_to(ROOT)), flush=True)
    nyadb_before = nyadb_snapshot()
    (base / 'nyadb-source-snapshot.json').write_text(json.dumps({'files': nyadb_before,
        'base_commit': run(['git', 'rev-parse', 'HEAD']).decode().strip()}, indent=2))
    before = source_snapshot()
    (base / 'accepted-original-source.json').write_text(json.dumps(before, indent=2))
    archive, archive_digest, version = build_bundle(base, before)
    required = REQUIRED + PROVIDER_REQUIRED if args.provider else REQUIRED
    result = {'scope': 'private-native-provider-workspace' if args.provider else 'managed-native-protocol-foundation', 'public_entry_accepted': False,
              'product': 'ZooKeeper', 'server_version': '3.9.4', 'image': IMAGE,
              'race': args.race, 'driver_sha256': archive_digest,
              'passed_tests': [], 'unverified': ['public runtime/editor/chooser/URI' if args.provider else 'public provider/editor/chooser/URI/KV workspace',
                  'TLS/mTLS', 'digest ACL enforcement', 'SASL authentication', 'ensemble failover', 'SSH routes']}
    container = None
    try:
        container = run(['docker', 'run', '-d', '--name', 'nyadb-zookeeper-' + token,
            '--label', 'nyadb.zookeeper.fixture=' + token, '-p', '127.0.0.1::2181',
            '-e', 'ZOO_TICK_TIME=500', '-e', 'ZOO_4LW_COMMANDS_WHITELIST=srvr,ruok',
            IMAGE]).decode().strip()
        inspect = json.loads(run(['docker', 'inspect', container]))[0]
        (base / 'container.json').write_text(json.dumps(inspect, indent=2))
        port = int(inspect['NetworkSettings']['Ports']['2181/tcp'][0]['HostPort'])
        endpoint = '127.0.0.1:' + str(port)
        deadline = time.monotonic() + 60
        while True:
            try:
                with socket.create_connection(('127.0.0.1', port), timeout=1) as conn:
                    conn.sendall(b'ruok')
                    if conn.recv(32) == b'imok':
                        break
            except OSError:
                pass
            if time.monotonic() >= deadline:
                raise RuntimeError('Owned ZooKeeper did not become ready')
            time.sleep(.2)
        fixture = base / 'fixture.json'
        fixture.write_text(json.dumps({'archive': str(archive), 'sha256': archive_digest,
            'version': version, 'directory': str(base), 'endpoint': endpoint}, indent=2))
        env = dict(os.environ, CGO_ENABLED='1' if args.race else '0',
            NYADB_ZOOKEEPER_PROVIDER_IT='1' if args.provider else '0',
            NYADB_AGENT_ZOOKEEPER_IT='1', NYADB_AGENT_ZOOKEEPER_FIXTURE_FILE=str(fixture))
        command = ['go', 'test', '-count=1', '-v', '-timeout=5m']
        if args.race:
            command += ['-race']
        command += ['./internal/services/agentbridge']
        if args.provider:
            command += ['./internal/ui/kvworkspace']
        command += ['-run', '^TestZooKeeper(AgentNative|Native)']
        completed = subprocess.run(command, cwd=ROOT, env=env, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, timeout=360)
        output = completed.stdout.decode(errors='replace')
        (base / 'acceptance.log').write_text(output)
        if completed.returncode:
            raise RuntimeError('Original ZooKeeper acceptance failed:\n' + output[-16000:])
        verify_acceptance(output, required)
        result['passed_tests'] = required
        if source_snapshot() != before:
            raise RuntimeError('Original source changed during acceptance')
        result['original_source_unchanged'] = True
        if nyadb_snapshot() != nyadb_before:
            raise RuntimeError('NyaDB source changed during native acceptance')
        result['nyadb_source_unchanged'] = True
        result['status'] = 'passed'
    except Exception as error:
        result['status'] = 'failed'
        result['error'] = str(error)
        raise
    finally:
        result['process_cleanup'] = cleanup_processes(base)
        if result['process_cleanup']['forced_cleanup']:
            result['status'] = 'failed'
            result['error'] = 'Acceptance required forced native process cleanup'
        if container:
            labels = json.loads(run(['docker', 'inspect', container]))[0]['Config']['Labels']
            if labels.get('nyadb.zookeeper.fixture') != token:
                raise RuntimeError('Fixture container ownership changed')
            (base / 'server.log').write_bytes(run(['docker', 'logs', container]))
            run(['docker', 'rm', '-f', container])
            remaining = run(['docker', 'ps', '-aq', '--filter', 'label=nyadb.zookeeper.fixture=' + token]).decode().strip()
            if remaining:
                raise RuntimeError('Owned ZooKeeper fixture remained after cleanup')
            result['container_removed'] = True
        (base / 'verification.json').write_text(json.dumps(result, indent=2))
    if result['process_cleanup']['forced_cleanup']:
        raise RuntimeError('Acceptance required forced native process cleanup')
    print(json.dumps(result, indent=2), flush=True)


if __name__ == '__main__':
    main()
