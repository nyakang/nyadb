#!/usr/bin/env python3
"""Verify owned native H2 TCP/TLS servers and original NyaDB sessions on Linux.

Uses a retained managed bundle and fixed official H2 jars. All credentials,
keystores, databases and processes belong to this fixture. No existing database
or Java process is changed. Records exact required profiles and original cleanup.
"""
import argparse
import json
import os
from pathlib import Path
import secrets
import select
import shutil
import subprocess
import sys
import urllib.request

import verify_agent_h2_fixture as common

ROOT = Path(__file__).resolve().parent.parent
PROFILES = {'h2-v1': '1.4.200', 'h2-v2': '2.1.214', 'h2-v3': '2.4.240', 'h2-legacy': '2.1.214'}


def verify_network_output(output):
    name = 'TestH2NativeTCPAndTLSAuthenticationDataCancellationAndRecovery'
    for suffix in [''] + ['/' + profile + '/' + mode for profile in PROFILES for mode in ['tcp', 'ssl']]:
        if '--- PASS: ' + name + suffix + ' ' not in output:
            raise RuntimeError('Missing original H2 network acceptance: ' + suffix)
    if '\nFAIL' in output:
        raise RuntimeError('Native H2 network acceptance failed')


def verify_shell_output(output):
    name = 'TestH2NativeNetworkPublicGridSQLAndExplicitRecovery'
    for suffix in [''] + ['/' + profile + '/' + mode for profile in PROFILES for mode in ['tcp', 'ssl']]:
        if '--- PASS: ' + name + suffix + ' ' not in output:
            raise RuntimeError('Missing original H2 network shell acceptance: ' + suffix)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--archive', required=True, type=Path)
    parser.add_argument('--race', action='store_true')
    parser.add_argument('--shell', action='store_true')
    parser.add_argument('--java', default=shutil.which('java'))
    args = parser.parse_args()
    if sys.platform != 'linux' or not args.java:
        parser.error('Requires Linux and an existing JDK 21+')
    java = Path(args.java).resolve()
    javac, keytool = java.parent / 'javac', java.parent / 'keytool'
    if not javac.is_file() or not keytool.is_file():
        parser.error('The selected Java must include javac and keytool')
    archive = args.archive.resolve()
    nonce = secrets.token_hex(8)
    folder = ROOT / 'temp/agentbridge' / ('network-' + nonce)
    evidence = ROOT / 'build/connection-alignment' / ('network-' + nonce)
    folder.mkdir(parents=True, mode=0o700)
    evidence.mkdir(parents=True)
    owner = dict(owner=nonce, purpose='nyadb-native-h2-network-fixture')
    (folder / 'owner.json').write_text(json.dumps(owner))
    password = folder / 'password'
    password.write_text(secrets.token_urlsafe(24) + '\n')
    password.chmod(0o600)
    servers, processes, records, jars = {}, [], [], {}
    passed = False
    print('Owned H2 network evidence=' + str(evidence), flush=True)
    try:
        cache = ROOT / 'build/agentbridge/dependencies'
        cache.mkdir(parents=True, exist_ok=True)
        for version in set(PROFILES.values()):
            expected = next(pin[3] for pin in common.PINS if pin[1] == 'h2' and pin[2] == version)
            jar = cache / ('h2-' + version + '.jar')
            if not jar.exists():
                temporary = folder / jar.name
                url = 'https://repo.maven.apache.org/maven2/com/h2database/h2/' + version + '/' + jar.name
                with urllib.request.urlopen(url, timeout=30) as response:
                    temporary.write_bytes(response.read())
                if common.sha(temporary) != expected:
                    raise RuntimeError('Official native H2 jar differs from fixed pin')
                temporary.replace(jar)
            if common.sha(jar) != expected:
                raise RuntimeError('Retained native H2 jar differs from fixed pin')
            jars[version] = jar
        certs = {}
        with (evidence / 'certificate-build.log').open('w') as log:
            for label, ip in [('trusted', '127.0.0.1'), ('wrong-host', '127.0.0.2')]:
                store, ca = folder / (label + '.p12'), folder / (label + '.pem')
                command = [str(keytool), '-genkeypair', '-alias', label, '-keyalg', 'EC', '-groupname', 'secp256r1', '-validity', '2', '-dname', 'CN=owned-h2-fixture', '-ext', 'SAN=ip:' + ip, '-keystore', str(store), '-storetype', 'PKCS12', '-storepass:file', str(password), '-keypass:file', str(password)]
                subprocess.run(command, stdout=log, stderr=subprocess.STDOUT, check=True, timeout=30)
                subprocess.run([str(keytool), '-exportcert', '-rfc', '-alias', label, '-keystore', str(store), '-storepass:file', str(password), '-file', str(ca)], stdout=log, stderr=subprocess.STDOUT, check=True, timeout=30)
                certs[label] = (store, ca)
        classes = folder / 'classes'
        classes.mkdir()
        source = ROOT / 'tools/fixtures/H2NetworkServer.java'
        subprocess.run([str(javac), '--release', '21', '-cp', str(jars['2.4.240']), '-d', str(classes), str(source)], check=True, timeout=30)
        for profile, version in list(PROFILES.items()) + [('wrong-host', '2.4.240')]:
            db = folder / profile
            db.mkdir(mode=0o700)
            store, ca = certs['wrong-host' if profile == 'wrong-host' else 'trusted']
            log = (evidence / (profile + '-server.log')).open('w')
            process = subprocess.Popen([str(java), '-cp', os.pathsep.join([str(classes), str(jars[version])]), 'H2NetworkServer', str(db), str(password), str(store)], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=log, start_new_session=True, text=True)
            processes.append(process)
            original = common.process_state(process.pid)
            records.append(dict(pid=process.pid, start_ticks=original['start_ticks'], agent=str(classes), java=str(java)))
            if not select.select([process.stdout], [], [], 30)[0]:
                raise RuntimeError('Owned native H2 server did not become ready')
            ports = json.loads(process.stdout.readline())
            servers[profile] = dict(ports, CAFile=str(ca))
        config = dict(Archive=str(archive), SHA256=common.sha(archive), Directory=str(folder), PasswordFile=str(password), Servers={p: servers[p] for p in PROFILES}, WrongHostPort=servers['wrong-host']['SSLPort'])
        config_path = folder / 'network.json'
        config_path.write_text(json.dumps(config))
        config_path.chmod(0o600)
        (evidence / 'provenance.json').write_text(json.dumps(dict(archive=str(archive), archive_sha256=config['SHA256'], java=str(java), native_h2_jars={v: common.sha(p) for v, p in jars.items()}, fixture_source_sha256=common.sha(source)), indent=2))
        env = dict(os.environ, CGO_ENABLED='1' if args.race else '0', NYADB_H2_PROVIDER_IT='1', NYADB_H2_NETWORK_IT='1', NYADB_MANAGED_H2_FIXTURE_FILE=str(config_path))
        command = ['go', 'test', '-timeout=25m', '-v', '-count=1', '-run=TestH2NativeTCPAndTLSAuthenticationDataCancellationAndRecovery']
        if args.race:
            command.append('-race')
        command.append('./internal/services/h2')
        with (evidence / 'acceptance.log').open('w') as log:
            subprocess.run(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, check=True, timeout=1530)
        verify_network_output((evidence / 'acceptance.log').read_text())
        if args.shell:
            shell_env = dict(env, NYADB_H2_NETWORK_SHELL_IT='1', NYADB_H2_NETWORK_SCREENSHOTS=str(evidence / 'screenshots'))
            shell_command = ['go', 'test', '-timeout=25m', '-v', '-count=1', '-run=TestH2NativeNetworkPublicGridSQLAndExplicitRecovery']
            if args.race:
                shell_command.append('-race')
            shell_command.append('./internal/ui/shell')
            with (evidence / 'shell-acceptance.log').open('w') as log:
                subprocess.run(shell_command, cwd=ROOT, env=shell_env, stdout=log, stderr=subprocess.STDOUT, check=True, timeout=1530)
            verify_shell_output((evidence / 'shell-acceptance.log').read_text())
            for profile in PROFILES:
                for mode in ['tcp', 'ssl']:
                    if not (evidence / 'screenshots' / (profile + '-' + mode + '-sql.png')).is_file():
                        raise RuntimeError('Missing original native H2 network screenshot')
        passed = True
    finally:
        journal = folder / 'processes.jsonl'
        if journal.exists():
            records.extend(json.loads(line) for line in journal.read_text().splitlines())
        (evidence / 'original-processes.json').write_text(json.dumps(records, indent=2))
        for record in records:
            common.cleanup_process(record)
        for process in processes:
            process.wait(timeout=10)
        if json.loads((folder / 'owner.json').read_text()) != owner:
            raise RuntimeError('Private fixture ownership changed')
        shutil.rmtree(folder)
        state = dict(owner, acceptance='passed' if passed else 'failed', status='closed', original_processes_closed=len(records), race=args.race, profiles=PROFILES, public_shell=args.shell and passed, scope='native TCP and verified TLS; trusted/unknown chain and original-host rejection; typed metadata/data/save receipts; observed blocked write cancellation/original executor acknowledgement; authentication/session isolation/explicit recovery; optional shell proves original-generation/grid-save/structure/SQL/disconnect/physical recovery without replay')
        (evidence / 'verification.json').write_text(json.dumps(state, indent=2))
        print(json.dumps(state), flush=True)
    return 0 if passed else 1


if __name__ == '__main__':
    sys.exit(main())
