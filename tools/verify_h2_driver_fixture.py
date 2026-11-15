#!/usr/bin/env python3
"""Verify H2 automatic/custom drivers using owned files and pinned native jars.

Retains the original agent/JRE payloads, declares its existing automatic mode
in a new immutable fixture package, and runs actual NyaDB provider/UI paths.
"""
import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import sys
import zipfile

import verify_agent_h2_fixture as common

ROOT = Path(__file__).resolve().parent.parent
PROFILES = {'h2-v1': '1.4.200', 'h2-v2': '2.1.214', 'h2-v3': '2.4.240'}


def verify_driver_output(output, shell=False):
    name = 'TestH2NativeAutomaticAndCustomPublicDriverWorkflows' if shell else 'TestH2NativeAutomaticAndCustomDriverOwnership'
    leaves = [''] + ['/' + mode + '/' + profile for mode in ['auto', 'custom'] for profile in PROFILES]
    if not shell:
        leaves += ['/page-store', '/new-file', '/auto-rejections']
    for suffix in leaves:
        if '--- PASS: ' + name + suffix + ' ' not in output:
            raise RuntimeError('Missing original H2 driver acceptance: ' + suffix)
    if any(line.strip().startswith(('FAIL', '--- FAIL:')) for line in output.splitlines()):
        raise RuntimeError('Original H2 driver acceptance failed')


def declare_automatic_package(original, target):
    parent_sha = common.sha(original)
    with zipfile.ZipFile(original) as source, zipfile.ZipFile(target, 'w', compression=zipfile.ZIP_DEFLATED) as output:
        manifest = json.loads(source.read('manifest.json'))
        if manifest['key'] != 'h2' or manifest['main_class'] != 'com.dbx.agent.h2.H2Agent' or not set(PROFILES) <= set(manifest['profiles']):
            raise RuntimeError('Original fixed native H2 package required')
        manifest['version'] = 'auto-' + parent_sha[:32]
        manifest['profiles'] = list(dict.fromkeys(manifest['profiles'] + ['h2-auto']))
        origin = 'automatic-profile-provenance.json'
        provenance = json.dumps(dict(kind='owned-fixture-existing-auto-profile-declaration', parent_archive_sha256=parent_sha, original_provenance=manifest['provenance'], original_payloads_unchanged=True), indent=2).encode() + b'\n'
        manifest['provenance'] = origin
        manifest['files'].append(dict(path=origin, sha256=hashlib.sha256(provenance).hexdigest(), executable=False))
        for entry in source.infolist():
            if entry.filename == 'manifest.json':
                continue
            with source.open(entry) as raw, output.open(copy.copy(entry), 'w') as saved:
                shutil.copyfileobj(raw, saved, length=128 << 10)
        for name, raw in [('manifest.json', json.dumps(manifest, indent=2).encode() + b'\n'), (origin, provenance)]:
            header = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
            header.compress_type = zipfile.ZIP_DEFLATED
            header.external_attr = 0o100600 << 16
            output.writestr(header, raw)
    return parent_sha


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--archive', type=Path, required=True)
    parser.add_argument('--race', action='store_true')
    parser.add_argument('--shell', action='store_true')
    parser.add_argument('--shell-only', action='store_true', help='Run only public UI acceptance in a separate owned fixture; does not prove provider acceptance')
    args = parser.parse_args()
    if sys.platform != 'linux' or not shutil.which('java'):
        parser.error('Requires Linux and an existing JDK 21+')
    java = Path(shutil.which('java')).resolve()
    nonce = secrets.token_hex(8)
    private = ROOT / 'temp/agentbridge' / ('drivers-' + nonce)
    evidence = ROOT / 'build/connection-alignment' / ('drivers-' + nonce)
    private.mkdir(parents=True, mode=0o700)
    evidence.mkdir(parents=True)
    owner = dict(owner=nonce, purpose='nyadb-native-h2-driver-fixture')
    (private / 'owner.json').write_text(json.dumps(owner))
    password = private / 'password'
    password.write_text(secrets.token_urlsafe(24) + '\n')
    password.chmod(0o600)
    records = []
    passed = False
    print('Owned native H2 driver evidence=' + str(evidence), flush=True)
    try:
        archive = evidence / 'auto-managed-h2.zip'
        parent_sha = declare_automatic_package(args.archive.resolve(), archive)
        jars = {}
        for profile, version in PROFILES.items():
            jar = ROOT / 'build/agentbridge/dependencies' / ('h2-' + version + '.jar')
            expected = next(pin[3] for pin in common.PINS if pin[1] == 'h2' and pin[2] == version)
            if common.sha(jar) != expected:
                raise RuntimeError('Original fixed native JDBC jar differs')
            jars[profile] = jar
        classes = private / 'classes'
        classes.mkdir()
        fixture = ROOT / 'tools/fixtures/H2DriverFiles.java'
        with (evidence / 'file-build.log').open('w') as log:
            subprocess.run([str(java.parent / 'javac'), '--release', '21', '-d', str(classes), str(fixture)], check=True, stdout=log, stderr=subprocess.STDOUT, timeout=30)
            for profile, jar in jars.items():
                process = subprocess.Popen([str(java), '-cp', str(classes) + os.pathsep + str(jar), 'H2DriverFiles', str(private), str(password), profile], stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
                state = common.process_state(process.pid)
                records.append(dict(pid=process.pid, start_ticks=state['start_ticks'], agent=str(classes), java=str(java)))
                process.wait(timeout=30)
                if process.returncode:
                    raise RuntimeError('Owned original native driver files failed')
        config = dict(Archive=str(archive), SHA256=common.sha(archive), Directory=str(private), PasswordFile=str(password), DriverJARs={p: str(j) for p, j in jars.items()})
        config_file = private / 'drivers.json'
        config_file.write_text(json.dumps(config))
        config_file.chmod(0o600)
        (evidence / 'provenance.json').write_text(json.dumps(dict(original_archive_sha256=parent_sha, archive_sha256=config['SHA256'], native_jars={p: common.sha(j) for p, j in jars.items()}, fixture_source_sha256=common.sha(fixture)), indent=2))
        env = dict(os.environ, CGO_ENABLED='1' if args.race else '0', NYADB_H2_PROVIDER_IT='1', NYADB_H2_DRIVER_IT='1', NYADB_MANAGED_H2_FIXTURE_FILE=str(config_file))
        for shell in ([True] if args.shell_only else [False, True] if args.shell else [False]):
            current = dict(env)
            name = 'TestH2NativeAutomaticAndCustomDriverOwnership'
            package = './internal/services/h2'
            logfile = evidence / 'acceptance.log'
            if shell:
                current.update(NYADB_H2_DRIVER_SHELL_IT='1', NYADB_H2_DRIVER_SCREENSHOTS=str(evidence / 'screenshots'))
                name = 'TestH2NativeAutomaticAndCustomPublicDriverWorkflows'
                package = './internal/ui/shell'
                logfile = evidence / 'shell-acceptance.log'
            command = ['go', 'test', '-timeout=35m', '-v', '-count=1', '-run=' + name]
            if args.race:
                command.append('-race')
            command.append(package)
            with logfile.open('w') as log:
                subprocess.run(command, cwd=ROOT, env=current, stdout=log, stderr=subprocess.STDOUT, check=True, timeout=2130)
            verify_driver_output(logfile.read_text(), shell)
        if args.shell or args.shell_only:
            for mode in ['auto', 'custom']:
                for profile in PROFILES:
                    if not (evidence / 'screenshots' / (mode + '-' + profile + '-sql.png')).is_file():
                        raise RuntimeError('Missing original H2 driver public screenshot')
            for profile in PROFILES:
                if not (evidence / 'screenshots' / ('custom-' + profile + '-advanced.png')).is_file():
                    raise RuntimeError('Missing original H2 custom import public screenshot')
        passed = True
    finally:
        journal = private / 'processes.jsonl'
        if journal.exists():
            records.extend(json.loads(line) for line in journal.read_text().splitlines())
        (evidence / 'original-processes.json').write_text(json.dumps(records, indent=2))
        for record in records:
            common.cleanup_process(record)
        if json.loads((private / 'owner.json').read_text()) != owner:
            raise RuntimeError('Original fixture ownership changed')
        shutil.rmtree(private)
        state = dict(owner, status='closed', acceptance='passed' if passed else 'failed', original_processes_closed=len(records), race=args.race, provider_acceptance=passed and not args.shell_only, public_shell=(args.shell or args.shell_only) and passed, native_profiles=PROFILES, scope='public chooser/editor/import/test/save/grid/SQL/recovery only' if args.shell_only else 'real automatic existing-file formats/PageStore/new-file/rejections and fixed original JDBC selection; complete custom local JAR graph snapshots; original typed data/write receipts/cancellation/leases/recovery; optional actual chooser/editor/import/test/save/grid/SQL/recovery paths')
        (evidence / 'verification.json').write_text(json.dumps(state, indent=2))
        print(json.dumps(state), flush=True)
    return 0 if passed else 1


if __name__ == '__main__':
    sys.exit(main())
