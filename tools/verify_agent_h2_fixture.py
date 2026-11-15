#!/usr/bin/env python3
"""Build the original local DBX H2 agent and verify NyaDB's owned v2 bridge.

Requires Linux, an existing JDK 21+, Go and network access to Maven Central.
Managed bundles and databases belong to this private fixture. The optional
workbench check verifies the H2 adapter, connection editor and public shell
using actual native H2 files and the builtin provider.
Original DBX sources are not modified.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import subprocess
import sys
import time
import urllib.request
import platform
import zipfile

ROOT = Path(__file__).resolve().parent.parent
PINS = [
    ('com.google.code.gson', 'gson', '2.15.0', '96d3dd843dc2911de01cdabcd50064cb9e860353102a0a78c96fd833d396fc7d', '426bc968a4e521865a58dd17a60389fed6195a1c357e3eb8bcb7cae31387bcae'),
    ('com.zaxxer', 'HikariCP', '7.1.0', '2be45c641b841e81df87292e181f2c203500c5f0e9a1676f135927387f3cf40b', '5b8df2699933ea898aa50d4def4fc2226b15e6f4d993e373a16070f7ed480216'),
    ('org.slf4j', 'slf4j-api', '2.0.20', '7e1446499b359675d8aabe6af2de86b85e72ae8a2a932c0b40d0e5ba57097439', '2e625c3f553cddd49f8554cea9926cbf70673b3d6833464f0c5a02f4d9130594'),
    ('org.slf4j', 'slf4j-nop', '2.0.20', '6522080396fb339e99bae40d329a06d18d3190dc864020ac4ca943519d0fd85b', '3cee6394352190ba1cdde7bf3704532434d5563d5343e4f3016c977acd0162ee'),
    ('com.h2database', 'h2', '1.4.200', '3ad9ac4b6aae9cd9d3ac1c447465e1ed06019b851b893dd6a8d76ddb6d85bca6', '2259dcfe014254244489e59e1576d3d24591abf8dd9846c7ff070987cd204aa3'),
    ('com.h2database', 'h2', '2.1.214', 'd623cdc0f61d218cf549a8d09f1c391ff91096116b22e2475475fce4fbe72bd0', '517e1a05729949521230bf56265bdac5a239fdb90ead6caa85f0c6a69cd7f803'),
    ('com.h2database', 'h2', '2.4.240', '29b70e427cc1c40cdc376283adbb0cc62853073797bb5fe5761f81fe73d57ce0', '27af4b7805856b3170d587e0a02caac93d421f875cfc9497dbf5ffd7f3247a4a'),
]
REQUIRED = [
    'TestH2AgentNativeVersionsValuesMetadataAndIndependentSessions',
    'TestH2AgentNativePinnedStateCursorAndOriginalRuntimeFailure',
    'TestH2AgentNativeOwnedFileRestartWithoutReplay',
    'TestH2AgentNativeCancelWaitsForExecutorAndPreservesOtherSession',
    'TestH2AgentNativeWorkbenchQueryLeasesAndCompleteReceipts',
    'TestH2AgentNativeWorkbenchQueuedCancellationAndOriginalReceipt',
    'TestH2AgentNativeTypedCursorsKeepOriginalIdentityAndCloseScopes',
    'TestCancellationRetainsOriginalExecutorReceiptAndOtherRuntime',
    'TestQueuedCancellationNeverCancelsActiveSessionRequest',
    'TestProtocolFaultsRetireOriginalProcessWithoutReplay',
    'TestCancelTimeoutKeepsUnknownOutcomeAndWaitsForProcess',
]


def verify_provider_output(output):
    required = {
        'TestH2ManagedProviderQualifiedMetadataAndQueryLeases': ['', '/1.4.200', '/2.1.214', '/2.4.240', '/2.1.214#01'],
        'TestH2NativePoolCapacityMetadataReserveAndCanceledAdmission': ['', '/h2-v1', '/h2-v2', '/h2-v3', '/h2-legacy'],
        'TestH2ProviderFileCreateReopenRequireExistingAndReadOnly': ['', '/h2-v1', '/h2-v2', '/h2-v3', '/h2-legacy'],
        'TestH2NativeSharedFileConnectionsPreserveOriginalSession': ['', '/h2-v1', '/h2-v2', '/h2-v3', '/h2-legacy'],
    }
    for name, suffixes in required.items():
        for suffix in suffixes:
            if '--- PASS: ' + name + suffix + ' ' not in output:
                raise RuntimeError('H2 provider acceptance is missing: ' + name + suffix)


def verify_editor_output(output):
    name = 'TestH2NativeEditorDriverImportPersistenceCredentialsAndIsolatedTesting'
    for suffix in ['', '/h2-v1', '/h2-v2', '/h2-v3', '/h2-legacy']:
        if '--- PASS: ' + name + suffix + ' ' not in output:
            raise RuntimeError('H2 editor acceptance is missing: ' + name + suffix)


def verify_shell_output(output):
    name = 'TestH2NativePublicChooserURISharedGridSQLStructureAndRecovery'
    for suffix in ['', '/h2-v1', '/h2-v2', '/h2-v3', '/h2-legacy']:
        if '--- PASS: ' + name + suffix + ' ' not in output:
            raise RuntimeError('H2 public shell acceptance is missing: ' + name + suffix)


def sha(path):
    with path.open('rb') as source:
        return hashlib.file_digest(source, 'sha256').hexdigest()


def process_state(pid):
    try:
        stat = Path(f'/proc/{pid}/stat').read_text()
    except FileNotFoundError:
        return None
    fields = stat[stat.rfind(')') + 1:].split()
    return {'state': fields[0], 'start_ticks': fields[19]}


def cleanup_process(record):
    pid = record['pid']
    state = process_state(pid)
    if state is None or state['start_ticks'] != record['start_ticks'] or state['state'] == 'Z':
        return
    args = Path(f'/proc/{pid}/cmdline').read_bytes().split(b'\0')
    decoded = [part.decode() for part in args if part]
    if '-cp' not in decoded or record['agent'] not in decoded[decoded.index('-cp') + 1].split(os.pathsep):
        raise RuntimeError('Captured agent no longer has its original fixture identity')
    if os.getpgid(pid) != pid:
        raise RuntimeError('Captured agent no longer owns its original process group')
    os.killpg(pid, signal.SIGTERM)
    deadline = time.monotonic() + 3
    while time.monotonic() < deadline:
        state = process_state(pid)
        if state is None or state['start_ticks'] != record['start_ticks'] or state['state'] == 'Z':
            return
        time.sleep(.05)
    state = process_state(pid)
    if state and state['start_ticks'] == record['start_ticks'] and state['state'] != 'Z':
        os.killpg(pid, signal.SIGKILL)
    deadline = time.monotonic() + 3
    while time.monotonic() < deadline:
        state = process_state(pid)
        if state is None or state['start_ticks'] != record['start_ticks'] or state['state'] == 'Z':
            return
        time.sleep(.05)
    raise RuntimeError('Captured original agent process did not terminate')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dbx-source', type=Path, default=ROOT / 'temp/dbx')
    parser.add_argument('--race', action='store_true')
    parser.add_argument('--workbench', action='store_true', help='also verify the managed H2 provider and connection editor')
    parser.add_argument('--managed', action='store_true', help='also verify an offline bundle containing a linked JRE and the original agent')
    parser.add_argument('--java', default=shutil.which('java'))
    args = parser.parse_args()
    if sys.platform != 'linux' or not args.java:
        parser.error('This real fixture requires Linux and an existing JDK 21+')
    java = Path(args.java).resolve()
    javac, jar_tool = java.parent / 'javac', java.parent / 'jar'
    if not javac.is_file() or not jar_tool.is_file():
        parser.error('The selected Java installation must include javac and jar')
    for group, name, version, jar_hash, pom_hash in PINS:
        for extension, digest in [('jar', jar_hash), ('pom', pom_hash)]:
            if len(digest) != 64 or any(c not in '0123456789abcdef' for c in digest):
                parser.error(f'Invalid fixed SHA256 pin: {group}:{name}:{version} {extension}')
    dbx = args.dbx_source.resolve()
    nonce = secrets.token_hex(8)
    base = ROOT / 'build/agentbridge' / nonce
    folder = ROOT / 'temp/agentbridge' / ('fixture-' + nonce)
    base.mkdir(parents=True)
    folder.mkdir(mode=0o700, parents=True)
    owner = {'owner': nonce, 'purpose': 'nyadb-h2-native-agent-fixture'}
    (folder / 'owner.json').write_text(json.dumps(owner))
    cache = ROOT / 'build/agentbridge/dependencies'
    cache.mkdir(parents=True, exist_ok=True)
    state = dict(owner, status='building', evidence=str(base))
    state_file = base / 'current-fixture.json'
    state_file.write_text(json.dumps(state, indent=2) + '\n')
    passed = False
    try:
        dependencies = []
        classpath_dependencies = []
        for group, name, version, jar_hash, pom_hash in PINS:
            coordinate = f'{group}:{name}:{version}'
            url = 'https://repo.maven.apache.org/maven2/' + group.replace('.', '/') + f'/{name}/{version}/'
            for extension, expected in [('jar', jar_hash), ('pom', pom_hash)]:
                target = cache / f'{name}-{version}.{extension}'
                if not target.exists():
                    temporary = cache / (target.name + '.' + nonce + '.tmp')
                    try:
                        with urllib.request.urlopen(url + target.name, timeout=30) as response:
                            temporary.write_bytes(response.read())
                        if sha(temporary) != expected:
                            raise RuntimeError(f'Maven dependency digest does not match its fixed pin: {coordinate} {extension}')
                        temporary.replace(target)
                    finally:
                        temporary.unlink(missing_ok=True)
                if sha(target) != expected:
                    raise RuntimeError(f'Retained Maven dependency differs from its fixed pin: {coordinate} {extension}')
                dependencies.append({'coordinate': coordinate, 'file': str(target), 'url': url + target.name, 'sha256': expected})
                if extension == 'jar' and name != 'h2':
                    classpath_dependencies.append(target)
        sources = sorted((dbx / 'agents/common/src/main/java').rglob('*.java')) + sorted((dbx / 'agents/drivers/h2/src/main/java').rglob('*.java'))
        if not sources:
            raise RuntimeError('Original DBX H2 agent sources are missing')
        classes = base / 'classes'
        classes.mkdir()
        argfile = base / 'javac-sources.txt'
        argfile.write_text('\n'.join('"' + str(path).replace('\\', '\\\\').replace('"', '\\"') + '"' for path in sources) + '\n')
        with (base / 'build.log').open('w') as log:
            subprocess.run([str(javac), '--release', '21', '-encoding', 'UTF-8', '-cp', os.pathsep.join(map(str, classpath_dependencies)), '-d', str(classes), '@' + str(argfile)], stdout=log, stderr=subprocess.STDOUT, check=True)
        shutil.copytree(dbx / 'agents/common/src/main/resources', classes, dirs_exist_ok=True)
        (classes / 'drivers').mkdir()
        for group, name, version, _, _ in PINS:
            if name == 'h2':
                shutil.copy2(cache / f'h2-{version}.jar', classes / 'drivers' / f'h2-{version}.jar')
        shutil.copy2(dbx / 'LICENSE', classes / 'DBX-LICENSE')
        shutil.copy2(dbx / 'NOTICE', classes / 'DBX-NOTICE')
        jar = base / 'dbx-agent-h2-fixture.jar'
        subprocess.run([str(jar_tool), '--create', '--date=2026-11-15T00:00:00Z', '--file', str(jar), '--main-class', 'com.dbx.agent.h2.H2Agent', '-C', str(classes), '.'], check=True)
        provenance = {'source': str(dbx), 'head': subprocess.check_output(['git', '-C', str(dbx), 'rev-parse', 'HEAD'], text=True).strip(), 'source_files': [{'path': str(path.relative_to(dbx)), 'sha256': sha(path)} for path in sources], 'dependencies': dependencies, 'jar': str(jar), 'jar_sha256': sha(jar), 'java': str(java), 'java_version': subprocess.run([str(java), '-version'], capture_output=True, text=True, check=True).stderr.strip(), 'scope': 'unmodified upstream H2/common sources; original license/notice and driver distributions retained; fixture only, no managed JRE/provider publication'}
        (base / 'source-provenance.json').write_text(json.dumps(provenance, indent=2) + '\n')
        password = folder / 'password'
        password.write_text(secrets.token_urlsafe(24) + '\n')
        password.chmod(0o600)
        classpath = [jar] + classpath_dependencies
        config = folder / 'probe.json'
        config.write_text(json.dumps({'java': str(java), 'classpath': list(map(str, classpath)), 'directory': str(folder), 'password_file': str(password), 'artifacts': [{'Path': str(path), 'SHA256': sha(path)} for path in classpath]}, indent=2) + '\n')
        config.chmod(0o600)
        state['status'] = 'testing'
        state_file.write_text(json.dumps(state, indent=2) + '\n')
        print(f'Original H2/common source fixture compiled; owner={nonce}', flush=True)
        env = dict(os.environ, NYADB_AGENT_H2_IT='1', NYADB_AGENT_H2_FIXTURE_FILE=str(config), CGO_ENABLED='1' if args.race else '0')
        command = ['go', 'test', '-timeout=3m', '-v', '-count=1']
        if args.race:
            command.append('-race')
        command.append('./internal/services/agentbridge')
        acceptance = base / 'acceptance.log'
        with acceptance.open('w') as log:
            subprocess.run(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, check=True, timeout=240)
        output = acceptance.read_text()
        for name in REQUIRED:
            if f'--- PASS: {name} ' not in output:
                raise RuntimeError('Required original bridge acceptance did not run and pass: ' + name)
        for version in ['1.4.200', '2.1.214', '2.4.240']:
            if f'--- PASS: TestH2AgentNativeVersionsValuesMetadataAndIndependentSessions/{version} ' not in output:
                raise RuntimeError('Real H2 kernel acceptance is missing: ' + version)
        if args.managed or args.workbench:
            jlink = java.parent / 'jlink'
            if not jlink.is_file():
                raise RuntimeError('Managed fixture requires the selected JDK jlink')
            package = base / 'managed-package'
            package.mkdir()
            with (base / 'jlink.log').open('w') as log:
                subprocess.run([str(jlink), '--add-modules', 'java.base,java.sql,java.naming,java.management,java.logging,java.xml,java.security.jgss,jdk.crypto.ec,jdk.unsupported', '--strip-debug', '--no-header-files', '--no-man-pages', '--output', str(package / 'jre')], stdout=log, stderr=subprocess.STDOUT, check=True, timeout=120)
            for artifact in classpath:
                shutil.copy2(artifact, package / artifact.name)
            shutil.copy2(dbx / 'LICENSE', package / 'DBX-LICENSE')
            shutil.copy2(dbx / 'NOTICE', package / 'DBX-NOTICE')
            shutil.copy2(base / 'source-provenance.json', package / 'sources.json')
            files = []
            for target in sorted(package.rglob('*')):
                if target.is_file():
                    files.append({'path': target.relative_to(package).as_posix(), 'sha256': sha(target), 'executable': bool(target.stat().st_mode & 0o111)})
            architecture = {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(platform.machine())
            if not architecture:
                raise RuntimeError('Unsupported managed fixture architecture')
            manifest = {'format': 1, 'key': 'h2', 'version': nonce, 'platform': 'linux-' + architecture, 'runtime': 'java', 'executable': 'jre/bin/java', 'main_class': 'com.dbx.agent.h2.H2Agent', 'classpath': [artifact.name for artifact in classpath], 'profiles': ['h2-v1', 'h2-v2', 'h2-v3', 'h2-auto'], 'files': files, 'licenses': ['DBX-LICENSE', 'DBX-NOTICE'] + [file['path'] for file in files if file['path'].startswith('jre/legal/')], 'provenance': 'sources.json'}
            (package / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
            archive = base / 'managed-h2.zip'
            with zipfile.ZipFile(archive, 'w', compression=zipfile.ZIP_DEFLATED) as output_zip:
                for target in sorted(package.rglob('*')):
                    if target.is_file():
                        # Materialize legal-file symlinks as complete regular
                        # files; the managed installer never imports symlinks.
                        output_zip.writestr(target.relative_to(package).as_posix(), target.read_bytes())
            managed_config = folder / 'managed.json'
            managed_config.write_text(json.dumps({'Archive': str(archive), 'SHA256': sha(archive), 'Directory': str(folder), 'PasswordFile': str(password)}, indent=2) + '\n')
            managed_config.chmod(0o600)
            managed_env = dict(env, NYADB_MANAGED_H2_IT='1', NYADB_MANAGED_H2_FIXTURE_FILE=str(managed_config))
            managed_command = command[:-1] + ['./internal/services/driverstore']
            with (base / 'managed-acceptance.log').open('w') as log:
                subprocess.run(managed_command, cwd=ROOT, env=managed_env, stdout=log, stderr=subprocess.STDOUT, check=True, timeout=240)
            managed_output = (base / 'managed-acceptance.log').read_text()
            for suffix in ['', '/1.4.200', '/2.1.214', '/2.4.240']:
                if '--- PASS: TestManagedH2OfflineRuntimeVersionsReceiptsAndOriginalLease' + suffix + ' ' not in managed_output:
                    raise RuntimeError('Managed runtime acceptance is missing: ' + suffix)
            if args.workbench:
                provider_env = dict(managed_env, NYADB_H2_PROVIDER_IT='1')
                provider_command = ['-timeout=30m' if arg == '-timeout=3m' else arg for arg in command[:-1]] + ['./internal/services/h2']
                with (base / 'provider-acceptance.log').open('w') as log:
                    subprocess.run(provider_command, cwd=ROOT, env=provider_env, stdout=log, stderr=subprocess.STDOUT, check=True, timeout=1830)
                provider_output = (base / 'provider-acceptance.log').read_text()
                verify_provider_output(provider_output)
                screenshots = base / 'editor-screenshots'
                editor_env = dict(managed_env, NYADB_H2_EDITOR_IT='1', NYADB_H2_EDITOR_SCREENSHOTS=str(screenshots))
                editor_command = ['-timeout=10m' if arg == '-timeout=3m' else arg for arg in command[:-1]]
                editor_command += ['-run=TestH2NativeEditorDriverImportPersistenceCredentialsAndIsolatedTesting', './internal/ui/connectioneditor']
                with (base / 'editor-acceptance.log').open('w') as log:
                    subprocess.run(editor_command, cwd=ROOT, env=editor_env, stdout=log, stderr=subprocess.STDOUT, check=True, timeout=630)
                verify_editor_output((base / 'editor-acceptance.log').read_text())
                for language in ['zh-Hans', 'en']:
                    for tab in ['general', 'advanced', 'notes']:
                        if not (screenshots / (language + '-' + tab + '.png')).is_file():
                            raise RuntimeError('H2 editor screenshot is missing: ' + language + '/' + tab)
                state['h2_connection_editor'] = 'passed'
                state['h2_editor_screenshots'] = str(screenshots)
                state['h2_editor_scope'] = 'explicit original driver import and pin; saved configuration and vault password restoration; isolated native tests of all four profiles; owned file creation and existing-file authentication; masked password; adapter/editor scope, with public shell checked separately'
                shell_screenshots = base / 'shell-screenshots'
                shell_env = dict(managed_env, NYADB_H2_SHELL_IT='1', NYADB_H2_SHELL_SCREENSHOTS=str(shell_screenshots))
                shell_command = ['-timeout=20m' if arg == '-timeout=3m' else arg for arg in command[:-1]]
                shell_command += ['-run=TestH2NativePublicChooserURISharedGridSQLStructureAndRecovery', './internal/ui/shell']
                with (base / 'shell-acceptance.log').open('w') as log:
                    subprocess.run(shell_command, cwd=ROOT, env=shell_env, stdout=log, stderr=subprocess.STDOUT, check=True, timeout=1230)
                verify_shell_output((base / 'shell-acceptance.log').read_text())
                for profile in ['h2-v1', 'h2-v2', 'h2-v3', 'h2-legacy']:
                    for view in ['grid', 'sql']:
                        if not (shell_screenshots / (profile + '-' + view + '.png')).is_file():
                            raise RuntimeError('H2 shell screenshot is missing: ' + profile + '/' + view)
                state['h2_public_shell'] = 'passed'
                state['h2_public_shell_scope'] = 'builtin provider and chooser; native JDBC URI retaining explicit pin; vault callback; native file session; isolated test of already-open shared file; original generation/SQL lease; qualified grid save; native structure; SQL; disconnect cleanup and explicit recovery without replay, all four profiles; TCP/TLS and further H2 semantics not verified'
                state['h2_workbench_provider'] = 'passed'
                state['h2_data_adapter'] = 'passed'
                state['h2_adapter_versions'] = ['1.4.200', '2.1.214', '2.4.240']
                state['h2_adapter_profiles'] = ['h2-v1', 'h2-v2', 'h2-v3', 'h2-legacy']
                state['h2_native_pool'] = 'passed; configured physical/workload/metadata capacity and canceled local admission'
                state['h2_adapter_modes'] = ['memory', 'file/create', 'file/existing', 'file/read-only']
                state['h2_adapter_scope'] = 'qualified metadata; typed paging/full rows/UTF-8 and binary chunks; native cursor export; conditional insert/update/delete; batch rollback; original native receipts in feature binding; canceled lock waiter acknowledgement; independent SQL leases; native pool limits and metadata reserve; file create/reopen/read-only; disconnect without replay; adapter scope, with public shell checked separately'
            state['managed_offline_h2'] = 'passed'
            state['managed_package_sha256'] = sha(archive)
            print('Managed offline JRE, original H2 versions, typed query receipts and original package leases passed', flush=True)
        passed = True
        print('Real H2 versions, exact values, metadata/DDL replay, transactions, original scopes, cursors, cancellation and file restart passed', flush=True)
    finally:
        journal = folder / 'processes.jsonl'
        records = [json.loads(line) for line in journal.read_text().splitlines()] if journal.exists() else []
        (base / 'original-processes.json').write_text(json.dumps(records, indent=2) + '\n')
        for record in records:
            cleanup_process(record)
        if json.loads((folder / 'owner.json').read_text()) != owner:
            raise RuntimeError('Private fixture ownership changed; refusing removal')
        shutil.rmtree(folder)
        state.update(status='closed', acceptance='passed' if passed else 'failed', race=args.race, original_processes_closed=len(records))
        state_file.write_text(json.dumps(state, indent=2) + '\n')
        if passed:
            (base / 'fixture-verification.json').write_text(json.dumps(state, indent=2) + '\n')
            (ROOT / 'build/agentbridge/latest-fixture.json').write_text(json.dumps(state, indent=2) + '\n')
        print(f'Captured original H2 agent processes closed; private fixture removed; owner={nonce}', flush=True)


if __name__ == '__main__':
    main()
