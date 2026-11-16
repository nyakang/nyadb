#!/usr/bin/env python3
"""Verify the unmodified DBX Trino agent/JDBC 471 against owned Trino 484.

Requires Linux, Docker, Go, an existing JDK 21+, and the pinned Trino image.
Builds a complete private driverstore bundle, including a linked JRE. This
acceptance supports bridge, adapter-only provider, and complete public workbench
scopes. Only workbench evidence validates public managed JDBC integration; the
shared Trino grid remains read-only.
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import platform
import re
import secrets
import shutil
import signal
import subprocess
import sys
import time
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location('h2_agent_fixture', ROOT / 'tools/verify_agent_h2_fixture.py')
HELPERS = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(HELPERS)
PINS = HELPERS.PINS[:4] + [
    ('io.trino', 'trino-jdbc', '471', '12e9095dfca58929e7b0cff4dc546b5fd4d67ba80fd1004a92b95a172de78aba', '9bd036a793a3f3b32521087289afe518877b3793c066653265a9d0213f063cd1'),
]
REQUIRED = [
    'TestTrinoAgentNativeManagedValuesMetadataAndOriginalPackage',
    'TestTrinoAgentNativePhysicalAffinityCursorsAndIndependentSessions',
    'TestTrinoAgentNativeConnectorReceiptsFailureAndExplicitRecovery',
    'TestTrinoAgentNativeCancelOriginalExecutorAndQueuedRequest',
    'TestTrinoAgentNativeOriginalProcessFailureKeepsUnknownWriteReceipt',
    'TestTrinoAgentNativeAuthenticationAndTrust',
]
PROVIDER_REQUIRED = [
    'TestTrinoManagedNativeProviderQualifiedReadsOriginalValuesAndSQL',
    'TestTrinoManagedNativePoolReserveCancellationAndOriginalReceipts',
    'TestTrinoManagedNativeAuthenticationAndExplicitRecovery',
    'TestTrinoNativeWorkspaceMetadataPagingTypesExportAndRecovery',
    'TestTrinoNativeAuthenticationTypesAndSessionState',
    'TestTrinoNativeCancellationAcknowledgement',
    'TestTrinoNativeDistributedEstimatedPlansFixture',
    'TestTrinoNativeCommittedWriteLostReplyNeverReplays',
    'TestTrinoNativeCloseReleasesOriginalTransaction',
]

def verify_named_acceptance(output, required):
    for name in required:
        if not re.search(r'^\s*--- PASS: ' + re.escape(name) + r' \(', output, re.MULTILINE):
            raise RuntimeError('Required native acceptance is missing: ' + name)


def verify_acceptance(output, tls=False):
    required = REQUIRED + (['TestTrinoAgentNativeAuthenticationAndTrust/' + case for case in ['wrong-password', 'untrusted-certificate']] if tls else [])
    verify_named_acceptance(output, required)


def verify_provider_acceptance(output, tls=False):
    verify_named_acceptance(output, PROVIDER_REQUIRED)
    if tls:
        verify_named_acceptance(output, ['TestTrinoManagedNativeAuthenticationAndExplicitRecovery/' + case for case in ['wrong-password', 'untrusted-certificate', 'wrong-hostname']])


EDITOR_CONTROLS = {
    'general': ['Trino 连接方式', 'Trino 驱动包', '导入驱动包', '刷新驱动包', '密码'],
    'advanced': ['连接超时（秒）', '查询超时（秒）', '连接池等待（毫秒）', '最大连接数', '元数据预留连接数', 'SQL 会话上限'],
    'ssl': ['Trino TLS 模式', 'Trino CA 证书', '选择 Trino CA 证书', '证书服务器名'],
}
UI_REQUIRED = {
    'editor': 'TestTrinoManagedNativeEditorImportPersistenceCredentialsAndRevocation',
    'shell': 'TestTrinoManagedNativePublicChooserQualifiedGridSQLAndRecovery',
}
SHELL_IMAGES = ['grid', 'structure', 'sql', 'plan', 'recovery']


def verify_workbench_images(base):
    import math
    from PIL import Image
    editor = base / 'editor-screenshots'
    controls = json.loads((editor / 'controls.json').read_text())
    images = []
    for language in ['zh-Hans', 'en']:
        for size, width in [('wide', 920), ('narrow', 720)]:
            for tab, labels in EDITOR_CONTROLS.items():
                name = language + '-' + size + '-' + tab + '.png'
                images.append((editor / name, (width, 780)))
                for label in labels:
                    box = controls.get(name, {}).get(label, {})
                    coordinates = [box.get(key) for key in ['X', 'Y', 'W', 'H']]
                    if any(not isinstance(n, (int, float)) or not math.isfinite(n) for n in coordinates):
                        raise RuntimeError('Localized Trino control evidence missing: ' + name + ':' + label)
                    x, y, w, h = coordinates
                    if x < 0 or y < 0 or w <= 0 or h <= 0 or x + w > width + 1 or y + h > 781:
                        raise RuntimeError('Localized Trino control clipped: ' + name + ':' + label)
    images += [(base / 'shell-screenshots' / ('trino-managed-' + name + '.png'), (1280, 800)) for name in SHELL_IMAGES]
    for path, dimensions in images:
        with Image.open(path) as img:
            if img.format != 'PNG' or img.size != dimensions:
                raise RuntimeError('Trino screenshot dimensions differ: ' + str(path))
            img.verify()
    return [str(path.relative_to(base)) for path, _ in images]


def verify_scope_evidence(base, scope, tls=False):
    if scope not in ['bridge', 'provider', 'workbench']:
        raise RuntimeError('Unknown Trino evidence scope')
    if scope != 'provider':
        verify_acceptance((base / 'acceptance.log').read_text(), tls)
    if scope in ['provider', 'workbench']:
        verify_provider_acceptance((base / 'provider-acceptance.log').read_text(), tls)
    if scope == 'workbench':
        for log, name in UI_REQUIRED.items():
            verify_named_acceptance((base / (log + '-acceptance.log')).read_text(), [name])
        return {'public_managed_provider': True, 'screenshots': verify_workbench_images(base)}
    return {'public_managed_provider': False, 'screenshots': []}


def build_bundle(dbx, java, base, nonce):
    dependencies, classpath = [], []
    for group, name, version, jar_hash, pom_hash in PINS:
        cache = ROOT / ('build/trino/dependencies' if name == 'trino-jdbc' else 'build/agentbridge/dependencies')
        cache.mkdir(parents=True, exist_ok=True)
        url = 'https://repo.maven.apache.org/maven2/' + group.replace('.', '/') + f'/{name}/{version}/'
        for extension, expected in [('jar', jar_hash), ('pom', pom_hash)]:
            target = cache / f'{name}-{version}.{extension}'
            if not target.exists():
                temporary = cache / (target.name + '.' + nonce + '.tmp')
                try:
                    with urllib.request.urlopen(url + target.name, timeout=30) as response:
                        temporary.write_bytes(response.read())
                    if HELPERS.sha(temporary) != expected:
                        raise RuntimeError('Downloaded dependency differs from its fixed SHA256 pin')
                    temporary.replace(target)
                finally:
                    temporary.unlink(missing_ok=True)
            if HELPERS.sha(target) != expected:
                raise RuntimeError('Retained dependency differs from its fixed SHA256 pin')
            dependencies.append({'coordinate': f'{group}:{name}:{version}', 'file': str(target), 'url': url + target.name, 'sha256': expected})
            if extension == 'jar':
                classpath.append(target)
    sources = sorted((dbx / 'agents/common/src/main/java').rglob('*.java')) + sorted((dbx / 'agents/drivers/trino/src/main/java').rglob('*.java'))
    if not sources or not any(path.name == 'TrinoAgent.java' for path in sources):
        raise RuntimeError('Original DBX Trino/common sources are missing')
    classes = base / 'classes'
    classes.mkdir()
    argfile = base / 'javac-sources.txt'
    argfile.write_text('\n'.join('"' + str(path).replace('\\', '\\\\').replace('"', '\\"') + '"' for path in sources) + '\n')
    with (base / 'compile.log').open('w') as log:
        subprocess.run([str(java.parent / 'javac'), '--release', '21', '-encoding', 'UTF-8', '-cp', os.pathsep.join(map(str, classpath)), '-d', str(classes), '@' + str(argfile)], stdout=log, stderr=subprocess.STDOUT, check=True, timeout=120)
    shutil.copytree(dbx / 'agents/common/src/main/resources', classes, dirs_exist_ok=True)
    for name in ['LICENSE', 'NOTICE']:
        shutil.copy2(dbx / name, classes / ('DBX-' + name))
    agent = base / 'dbx-agent-trino-fixture.jar'
    subprocess.run([str(java.parent / 'jar'), '--create', '--date=2026-11-16T00:00:00Z', '--file', str(agent), '--main-class', 'com.dbx.agent.trino.TrinoAgent', '-C', str(classes), '.'], check=True, timeout=60)
    provenance = {'source': str(dbx), 'head': subprocess.check_output(['git', '-C', str(dbx), 'rev-parse', 'HEAD'], text=True).strip(), 'source_files': [{'path': str(path.relative_to(dbx)), 'sha256': HELPERS.sha(path)} for path in sources], 'dependencies': dependencies, 'agent_sha256': HELPERS.sha(agent), 'java': str(java), 'java_version': subprocess.run([str(java), '-version'], capture_output=True, text=True, check=True).stderr.strip(), 'scope': 'unmodified original DBX Trino/common agent with pinned JDBC 471; fixture only'}
    (base / 'source-provenance.json').write_text(json.dumps(provenance, indent=2) + '\n')
    package = base / 'managed-package'
    package.mkdir()
    with (base / 'jlink.log').open('w') as log:
        subprocess.run([str(java.parent / 'jlink'), '--add-modules', 'java.se,jdk.crypto.ec,jdk.unsupported,jdk.security.auth', '--strip-debug', '--no-header-files', '--no-man-pages', '--output', str(package / 'jre')], stdout=log, stderr=subprocess.STDOUT, check=True, timeout=120)
    for target in [agent] + classpath:
        shutil.copy2(target, package / target.name)
    for name in ['LICENSE', 'NOTICE']:
        shutil.copy2(dbx / name, package / ('DBX-' + name))
    shutil.copy2(base / 'source-provenance.json', package / 'sources.json')
    # Original dependency jars retain their embedded notices. Include POMs and
    # extracted license/notice files as named manifest evidence as well.
    legal = package / 'legal'
    legal.mkdir()
    for dependency in dependencies:
        target = Path(dependency['file'])
        if target.suffix == '.pom':
            shutil.copy2(target, legal / target.name)
        else:
            with zipfile.ZipFile(target) as archive:
                for index, member in enumerate(archive.namelist()):
                    if not member.endswith('/') and any(term in Path(member).name.upper() for term in ['LICENSE', 'NOTICE', 'COPYING']):
                        (legal / (target.stem + '-' + str(index) + '.txt')).write_bytes(archive.read(member))
    files = [{'path': target.relative_to(package).as_posix(), 'sha256': HELPERS.sha(target), 'executable': bool(target.stat().st_mode & 0o111)} for target in sorted(package.rglob('*')) if target.is_file()]
    architecture = {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(platform.machine())
    if architecture is None:
        raise RuntimeError('Unsupported managed fixture architecture')
    manifest = {'format': 1, 'key': 'trino', 'version': 'jdbc-471-' + nonce, 'platform': 'linux-' + architecture, 'runtime': 'java', 'executable': 'jre/bin/java', 'main_class': 'com.dbx.agent.trino.TrinoAgent', 'classpath': [target.name for target in [agent] + classpath], 'profiles': ['trino'], 'files': files, 'licenses': ['DBX-LICENSE', 'DBX-NOTICE'] + [item['path'] for item in files if item['path'].startswith(('jre/legal/', 'legal/'))], 'provenance': 'sources.json'}
    (package / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
    bundle = base / 'managed-trino.zip'
    with zipfile.ZipFile(bundle, 'w', compression=zipfile.ZIP_DEFLATED) as archive:
        for target in sorted(package.rglob('*')):
            if target.is_file():
                archive.writestr(target.relative_to(package).as_posix(), target.read_bytes())
    return bundle


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dbx-source', type=Path, default=ROOT / 'temp/dbx')
    parser.add_argument('--java', default=shutil.which('java'))
    parser.add_argument('--plain', action='store_true')
    parser.add_argument('--race', action='store_true')
    parser.add_argument('--scope', choices=['bridge', 'provider', 'workbench'], default='bridge', help='provider is adapter-only evidence; workbench also requires public editor and shell acceptance')
    args = parser.parse_args()
    if sys.platform != 'linux' or not args.java:
        parser.error('Requires Linux and an existing JDK 21+')
    java = Path(args.java).resolve()
    if any(not (java.parent / tool).is_file() for tool in ['javac', 'jar', 'jlink', 'keytool']):
        parser.error('Selected JDK must include javac, jar, jlink and keytool')
    nonce = secrets.token_hex(8)
    base = ROOT / 'build/trino' / ('agent-' + nonce)
    private = ROOT / 'temp/trino' / ('agent-' + nonce)
    base.mkdir(parents=True)
    private.mkdir(mode=0o700, parents=True)
    owner = {'owner': nonce, 'purpose': 'nyadb-trino-native-agent-fixture'}
    (private / 'owner.json').write_text(json.dumps(owner))
    state = dict(owner, status='building', evidence=str(base), race=args.race, tls=not args.plain, scope=args.scope, public_managed_provider=False, grid_writes=False, full_goal_complete=False)
    state_file = base / 'current-fixture.json'
    state_file.write_text(json.dumps(state, indent=2) + '\n')
    server = None
    owned = None
    passed = False
    def terminate(signum, _frame):
        raise SystemExit(128 + signum)
    signal.signal(signal.SIGTERM, terminate)
    signal.signal(signal.SIGINT, terminate)
    try:
        bundle = build_bundle(args.dbx_source.resolve(), java, base, nonce)
        print('Original Trino/common sources and managed JDBC 471 bundle built; owner=' + nonce, flush=True)
        state['status'] = 'starting-owned-server'
        state_file.write_text(json.dumps(state, indent=2) + '\n')
        server_log = base / 'server-fixture.log'
        with server_log.open('w') as log:
            server = subprocess.Popen([sys.executable, str(ROOT / 'tools/verify_trino_fixture.py'), '--hold', *(['--plain'] if args.plain else [])], stdout=log, stderr=subprocess.STDOUT, cwd=ROOT, start_new_session=True)
        deadline = time.monotonic() + 270
        while True:
            output = server_log.read_text()
            match = re.search(r'^Native Trino 484 ready; private probe: (.+)$', output, re.MULTILINE)
            if match:
                probe = Path(match[1])
                break
            if server.poll() is not None:
                raise RuntimeError('Captured original Trino fixture stopped before readiness')
            if time.monotonic() >= deadline:
                raise RuntimeError('Captured original Trino fixture did not become ready within its deadline')
            time.sleep(.25)
        candidates = []
        for path in (ROOT / 'build/trino').glob('owned-fixture-*.json'):
            record = json.loads(path.read_text())
            if record.get('probe') == str(probe):
                candidates.append(record)
        if len(candidates) != 1:
            raise RuntimeError('Original server ownership record is ambiguous or missing')
        owned = candidates[0]
        inspected = json.loads(subprocess.check_output(['docker', 'inspect', owned['id']], text=True))[0]
        labels = inspected['Config']['Labels']
        if inspected['Id'] != owned['id'] or labels.get('nyadb.fixture') != 'trino' or labels.get('nyadb.fixture.nonce') != owned['nonce'] or not inspected['State']['Running']:
            raise RuntimeError('Captured original server ownership changed')
        native = json.loads(probe.read_text())
        config = dict(owner, archive=str(bundle), sha256=HELPERS.sha(bundle), driver_version='jdbc-471-' + nonce, directory=str(private), **native)
        if not args.plain:
            trust_password = private / 'trust-password'
            trust_password.write_text(secrets.token_urlsafe(24))
            trust_password.chmod(0o600)
            trust = private / 'trust.p12'
            with (base / 'keytool.log').open('w') as log:
                subprocess.run([str(java.parent / 'keytool'), '-importcert', '-noprompt', '-alias', 'owned-trino-ca', '-file', native['ca_file'], '-keystore', str(trust), '-storetype', 'PKCS12', '-storepass:file', str(trust_password)], stdout=log, stderr=subprocess.STDOUT, check=True, timeout=30)
            trust.chmod(0o600)
            config.update(trust_file=str(trust), trust_password_file=str(trust_password))
        config_path = private / 'probe.json'
        config_path.write_text(json.dumps(config, indent=2) + '\n')
        config_path.chmod(0o600)
        state.update(status='testing', managed_package_sha256=HELPERS.sha(bundle), server_id=owned['id'], server_image=owned['image'], server_version='484', jdbc_version='471')
        state_file.write_text(json.dumps(state, indent=2) + '\n')
        print('Owned official Trino 484 ready; verifying original JDBC sessions; owner=' + nonce, flush=True)
        env = dict(os.environ, NYADB_AGENT_TRINO_IT='1', NYADB_AGENT_TRINO_FIXTURE_FILE=str(config_path), CGO_ENABLED='1' if args.race else '0')
        paths = subprocess.check_output(['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard'], cwd=ROOT).decode().split('\0')
        snapshot = {'base_commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(), 'files': {name: HELPERS.sha(ROOT / name) for name in sorted(set(paths)) if name and (ROOT / name).is_file()}}
        (base / 'nyadb-source-snapshot.json').write_text(json.dumps(snapshot, indent=2) + '\n')
        command = ['go', 'test', '-count=1', '-v', '-timeout=15m', *(['-race'] if args.race else []), '-run=TestTrinoAgentNative', './internal/services/agentbridge']
        if args.scope != 'provider':
            with (base / 'acceptance.log').open('w') as log:
                subprocess.run(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, check=True, timeout=930)
            verify_acceptance((base / 'acceptance.log').read_text(), tls=not args.plain)
        if args.scope in ['provider', 'workbench']:
            native_env = dict(env, NYADB_TRINO_MANAGED_IT='1', NYADB_TRINO_IT='1', NYADB_TRINO_CONFIG=str(config_path))
            native_command = command[:-2] + ['-run=TestTrinoManagedNative|TestTrinoNative', './internal/services/trino']
            with (base / 'provider-acceptance.log').open('w') as log:
                subprocess.run(native_command, cwd=ROOT, env=native_env, stdout=log, stderr=subprocess.STDOUT, check=True, timeout=930)
            verify_provider_acceptance((base / 'provider-acceptance.log').read_text(), tls=not args.plain)
            state['managed_provider_verified'] = True
            if args.scope == 'workbench':
                ui_env = dict(native_env, NYADB_AGENT_TRINO_EDITOR_IT='1', NYADB_AGENT_TRINO_SHELL_IT='1', NYADB_TRINO_EDITOR_SCREENSHOTS=str(base / 'editor-screenshots'), NYADB_TRINO_MANAGED_SHELL_SCREENSHOTS=str(base / 'shell-screenshots'))
                for package, name, log_name in [('connectioneditor', 'TestTrinoManagedNativeEditorImportPersistenceCredentialsAndRevocation', 'editor'), ('shell', 'TestTrinoManagedNativePublicChooserQualifiedGridSQLAndRecovery', 'shell')]:
                    ui_command = command[:-2] + ['-run=^' + name + '$', './internal/ui/' + package]
                    with (base / (log_name + '-acceptance.log')).open('w') as log:
                        subprocess.run(ui_command, cwd=ROOT, env=ui_env, stdout=log, stderr=subprocess.STDOUT, check=True, timeout=930)
                    verify_named_acceptance((base / (log_name + '-acceptance.log')).read_text(), [name])
        state.update(verify_scope_evidence(base, args.scope, tls=not args.plain))
        passed = True
        print('Original Trino JDBC acceptance passed; scope=' + args.scope, flush=True)
    finally:
        journal = private / 'processes.jsonl'
        records = [json.loads(line) for line in journal.read_text().splitlines()] if journal.exists() else []
        (base / 'original-processes.json').write_text(json.dumps(records, indent=2) + '\n')
        cleanup_errors = []
        for record in records:
            try:
                HELPERS.cleanup_process(record)
            except Exception as error:
                cleanup_errors.append(str(error))
        if server is not None:
            if server.poll() is None:
                server.terminate()
            try:
                server.wait(timeout=45)
            except subprocess.TimeoutExpired:
                cleanup_errors.append('Original server child did not acknowledge termination')
                server.kill()
                server.wait(timeout=10)
        if owned is not None:
            if subprocess.run(['docker', 'inspect', owned['id']], capture_output=True).returncode == 0:
                cleanup_errors.append('Original server remains after child exit')
            if Path(owned['folder']).exists():
                cleanup_errors.append('Original server private configuration remains after child exit')
        if json.loads((private / 'owner.json').read_text()) != owner:
            cleanup_errors.append('Private agent fixture ownership changed')
        else:
            shutil.rmtree(private)
        state.update(status='closed' if not cleanup_errors else 'cleanup-failed', acceptance='passed' if passed else 'failed', original_processes_closed=len(records), cleanup_errors=cleanup_errors)
        state_file.write_text(json.dumps(state, indent=2) + '\n')
        if cleanup_errors:
            raise RuntimeError('; '.join(cleanup_errors))
        if passed:
            (base / 'fixture-verification.json').write_text(json.dumps(state, indent=2) + '\n')
            (ROOT / 'build/trino/latest-agent-fixture.json').write_text(json.dumps(state, indent=2) + '\n')
        print('Captured original agents/server closed and private configuration removed; owner=' + nonce, flush=True)


if __name__ == '__main__':
    main()
