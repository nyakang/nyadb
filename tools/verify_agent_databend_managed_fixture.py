#!/usr/bin/env python3
"""Verify a reproducible original managed Databend package on an owned product.

Accepts Linux package/install/process/pool foundations only. Public profiles,
providers and workspaces require separate product acceptance.
"""
import argparse
import base64
from decimal import Decimal
import json
import os
from pathlib import Path, PurePosixPath
import re
import secrets
import time
import urllib.error
import urllib.request
import zipfile

import build_databend_package as builder
import verify_agent_databend_transport_fixture as transport

ROOT = builder.ROOT
REQUIRED = [
    'TestDatabendManagedOriginalInstallPoolMetadataCursorAndReceipts',
    'TestDatabendManagedOriginalLateOpenCloseAndExplicitProcessRecovery',
]


def verify_named_acceptance(output):
    for name in REQUIRED:
        if not re.search(r'^\s*--- PASS: ' + re.escape(name) + r' \(', output, re.MULTILINE):
            raise RuntimeError('Required managed original diagnostic missing: ' + name)


def verify_archive(archive, package, manifest):
    expected = {f['path']: f for f in manifest['files']}
    expected['manifest.json'] = {'executable': False}
    with zipfile.ZipFile(archive) as z:
        if z.namelist() != sorted(expected, key=PurePosixPath):
            raise RuntimeError('Managed archive payload/order/unique identities differ')
        for name, item in expected.items():
            entry = z.getinfo(name)
            mode = 0o100755 if item['executable'] else 0o100644
            if (z.read(name) != (package / name).read_bytes() or
                    entry.external_attr >> 16 != mode or entry.date_time != builder.EPOCH):
                raise RuntimeError('Managed archive bytes/modes/timestamps differ')


def verify_build(base, dbx, cache):
    package = base / 'package'
    manifest = builder.verify_package(package)
    pins = json.loads(builder.PINS.read_text())
    builder.verify_sources(dbx, pins)
    if json.loads((package / 'provenance/pins.json').read_text()) != pins:
        raise RuntimeError('Original fixed package inputs differ')
    source = json.loads((package / manifest['provenance']).read_text())
    compiled = sorted(i['path'] for i in pins['dbx_source_files']
                      if '/src/main/java/' in i['path'] and i['path'].endswith('.java'))
    if (source['dbx_revision'] != pins['dbx_revision'] or
            source['source_files'] != pins['dbx_source_files'] or
            source['compiled_source_files'] != compiled or
            source['dependencies'] != pins['files'] or source['components'] != pins['components'] or
            source['builder_sha256'] != builder.sha(Path(builder.__file__)) or
            source['pins_sha256'] != builder.sha(builder.PINS) or
            source['public_provider_registered'] is not False or
            source['original_driver_modified'] is not False):
        raise RuntimeError('Original source/compiler/dependency provenance differs')
    for item in pins['dbx_source_files']:
        if builder.sha(package / 'provenance/dbx' / item['path']) != item['sha256']:
            raise RuntimeError('Retained original source/legal file changed')
    acquired = {item['path']: builder.fetch(item, cache, True) for item in pins['files']}
    jars = {i['coordinate']: acquired[i['path']] for i in pins['files'] if i['extension'] == 'jar'}
    jdbc = jars['com.databend:databend-jdbc:0.4.8']
    source_jar = next(acquired[i['path']] for i in pins['files'] if i['extension'] == 'sources')
    expected_classpath = ['dbx-agent-databend.jar'] + [jars[c].name for c in pins['launch_coordinates']]
    if (manifest['classpath'] != expected_classpath or manifest['executable'] != 'jre/bin/java' or
            manifest['platform'] not in ['linux-amd64', 'linux-arm64'] or
            source['agent_sha256'] != builder.sha(package / expected_classpath[0])):
        raise RuntimeError('Managed original launch identity differs')
    for name in expected_classpath[1:]:
        original = next(p for p in jars.values() if p.name == name)
        if builder.sha(package / name) != builder.sha(original):
            raise RuntimeError('Original unmodified launch dependency changed')
    mapping = builder.shaded_provenance(jdbc, {c: p for c, p in jars.items()
                                               if c not in pins['launch_coordinates']},
                                       source_jar, pins['relocations'])
    if json.loads((package / 'provenance/shaded-members.json').read_text()) != mapping:
        raise RuntimeError('Complete shaded class/native ownership differs')
    legal = []
    for item in pins['files']:
        path = acquired[item['path']]
        if item['extension'] == 'pom':
            if builder.sha(package / 'provenance/maven' / item['path']) != item['sha256']:
                raise RuntimeError('Complete original parent/runtime POM changed')
        elif item['extension'] == 'sources':
            if builder.sha(package / 'provenance' / path.name) != item['sha256']:
                raise RuntimeError('Original JDBC source archive changed')
        elif item['extension'] == 'legal':
            legal.append({'file': 'legal/' + path.name, 'coordinate': item['coordinate'],
                          'source_url': item['url'], 'sha256': item['sha256']})
        elif item['extension'] == 'jar':
            component = next(c for c in pins['components'] if c['coordinate'] == item['coordinate'])
            with zipfile.ZipFile(path) as z:
                for index, entry in enumerate(component['embedded_legal']):
                    if builder.raw_sha(z.read(entry['member'])) != entry['sha256']:
                        raise RuntimeError('Original component embedded legal bytes differ')
                    legal.append({'file': 'legal/' + path.stem + '-' + str(index) + '.txt',
                                  'coordinate': item['coordinate'], 'jar_sha256': item['sha256'],
                                  'member': entry['member'], 'sha256': entry['sha256']})
    if json.loads((package / 'provenance/legal-members.json').read_text()) != legal:
        raise RuntimeError('Complete original component legal ownership differs')
    for item in legal:
        if builder.sha(package / item['file']) != item['sha256']:
            raise RuntimeError('Original legal payload changed')
    licenses = ['DBX-LICENSE', 'DBX-NOTICE'] + [f['path'] for f in manifest['files']
                                             if f['path'].startswith(('legal/', 'jre/legal/'))]
    if manifest['licenses'] != licenses:
        raise RuntimeError('Complete original license list differs')
    observed = {'archive': 'databend.zip', 'archive_sha256': builder.sha(base / 'databend.zip'),
                'manifest_sha256': builder.sha(package / 'manifest.json'), 'files': len(manifest['files']),
                'licenses': len(licenses), 'shaded_classes': len(mapping['classes']),
                'exact_native_binaries': len(mapping['native_binaries'])}
    if json.loads((base / 'build.json').read_text()) != observed:
        raise RuntimeError('Build report differs from actual immutable payload')
    if (observed['files'], observed['licenses'], observed['shaded_classes'],
            observed['exact_native_binaries']) != (393, 149, 4734, 18):
        raise RuntimeError('Fixed original package coverage is incomplete')
    verify_archive(base / 'databend.zip', package, manifest)
    return observed | {'version': manifest['version'], 'source_files': len(compiled),
                       'fixed_downloads': len(pins['files']), 'components': len(pins['components'])}


def verify_receipts(directory):
    records = [json.loads(line, parse_float=Decimal) for line in
               (directory / 'original-receipts.jsonl').read_text().splitlines()]
    for record in records:
        transport.verify_receipt(record)
    for name in REQUIRED:
        methods = {r['method'] for r in records if r['test'] == name}
        expected = {'managed-handshake', 'managed-open-initialize', 'managed-query'}
        expected.add('managed-cursor' if name == REQUIRED[0] else 'managed-late-open')
        if expected - methods:
            raise RuntimeError('A required managed diagnostic lacks original executor receipts')
        mutations = [r['receipt']['Result'] for r in records if r['test'] == name and
                     r['method'] == 'managed-query' and
                     (r['receipt']['Result'] or {}).get('columns') == ['number of rows inserted']]
        if (len(mutations) != 1 or mutations[0].get('column_types') != ['uint64'] or
                mutations[0].get('rows') != [[1]] or mutations[0].get('affected_rows') != 0 or
                mutations[0].get('truncated') is not False):
            raise RuntimeError('Managed native insertion count/complete acknowledgement differs')
    pages = [r['receipt']['Result'] for r in records if r['method'] == 'managed-cursor']
    defaults = [r['receipt']['Result'] for r in records if r['test'] == REQUIRED[0] and
                r['method'] == 'managed-query' and
                (r['receipt']['Result'] or {}).get('rows') == [['Asia/Shanghai']]]
    if len(defaults) != 2 or any(p.get('columns') != ['value'] for p in defaults):
        raise RuntimeError('Original non-UTC defaults were changed or lack executor evidence')
    # Page structure is checked again independently of the Go assertions.
    sizes = [len(p.get('rows', [])) for p in pages]
    if sizes != [100, 100, 5]:
        raise RuntimeError('Original managed cursor page receipts are incomplete')
    late = [r for r in records if r['method'] == 'managed-late-open']
    if (len(late) != 1 or not late[0]['receipt']['CancellationRequested'] or
            late[0]['receipt'].get('ProcessExitConfirmed') is not False or
            late[0]['receipt']['Result'] != {'ok': True, 'exactMetadataIdentity': False}):
        raise RuntimeError('Captured late opening lacks its original cancellation acknowledgement')
    return len(records)


def verify_independent_writes(directory):
    observed = {name: [] for name in REQUIRED}
    for line in (directory / 'independent-http-receipts.jsonl').read_text().splitlines():
        r = json.loads(line)
        raw = base64.b64decode(r['native_bytes'], validate=True)
        if transport.sha(raw) != r['native_sha256']:
            raise RuntimeError('Independent original native HTTP bytes changed')
        value = json.loads(raw)
        identifier = 17 if r['test'] == REQUIRED[0] else 29 if r['test'] == REQUIRED[1] else None
        if (identifier is None or not re.fullmatch(r'SELECT count\(\) FROM "nyadb_transport_[0-9a-f]{16}"\.managed_writes WHERE id=' + str(identifier), r['sql']) or
                value.get('error') is not None):
            raise RuntimeError('Independent durable managed writes/recovery evidence differs')
        observed[r['test']] += value.get('data') or []
    if any(rows != [['1']] for rows in observed.values()):
        raise RuntimeError('Managed write/recovery lacks exactly one independent native count')
    return {'independent_exactly_once_writes': [17, 29], 'recovery_replayed': False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--build', type=Path, required=True)
    parser.add_argument('--reproduced-build', type=Path, required=True)
    parser.add_argument('--dbx', type=Path, default=ROOT / 'temp/dbx')
    parser.add_argument('--cache', type=Path, default=ROOT / 'build/databend/package-cache')
    parser.add_argument('--race', action='store_true')
    args = parser.parse_args()
    original, reproduced, dbx, cache = [p.resolve() for p in
                                       [args.build, args.reproduced_build, args.dbx, args.cache]]
    if original == reproduced:
        raise RuntimeError('Two independently constructed package directories are required')
    pins = verify_build(original, dbx, cache)
    if verify_build(reproduced, dbx, cache) != pins:
        raise RuntimeError('Independent original builds did not reproduce identical archives')
    nonce = secrets.token_hex(8)
    base = ROOT / 'build/databend' / ('managed-' + nonce)
    base.mkdir(parents=True, mode=0o700)
    print('Evidence: ' + str(base), flush=True)
    result = {'scope': 'original_managed_linux_package_install_process_pool', 'status': 'running',
              'pins': pins, 'image': transport.IMAGE, 'race': args.race,
              'public_provider_registered': False, 'independent_builds_identical': True}
    secret, username = secrets.token_hex(24), 'nyadb_managed'
    credential, envfile = base / 'private-password', base / 'private-container-env'
    for path, value in [(credential, secret), (envfile, 'QUERY_DEFAULT_USER=' + username + '\nQUERY_DEFAULT_PASSWORD=' + secret + '\n')]:
        with path.open('x') as output:
            os.chmod(path, 0o600)
            output.write(value)
    container = None
    try:
        container = transport.run(['docker', 'run', '-d', '--name', 'nyadb-databend-managed-' + nonce,
                                   '--label', 'nyadb.databend.transport=' + nonce, '--memory=4g', '--cpus=4',
                                   '--env-file', str(envfile), '-p', '127.0.0.1::8000', transport.IMAGE]).decode().strip()
        envfile.unlink()
        record = json.loads(transport.run(['docker', 'inspect', container]))[0]
        binding = record['NetworkSettings']['Ports']['8000/tcp']
        if len(binding) != 1 or binding[0]['HostIp'] != '127.0.0.1':
            raise RuntimeError('Owned managed product escaped local fixture')
        (base / 'original-container.json').write_text(json.dumps({
            'id': record['Id'], 'image_id': record['Image'], 'image': transport.IMAGE,
            'labels': record['Config']['Labels'], 'ports': binding,
            'volumes': [m['Name'] for m in record['Mounts'] if m['Type'] == 'volume'],
        }, indent=2))
        endpoint = 'http://127.0.0.1:' + binding[0]['HostPort']
        journal = []

        def native_sql(sql):
            headers = {'Content-Type': 'application/json', 'Authorization': 'Basic ' + base64.b64encode((username + ':' + secret).encode()).decode()}
            request = urllib.request.Request(endpoint + '/v1/query', data=json.dumps({
                'sql': sql, 'pagination': {'wait_time_secs': 1, 'max_rows_per_page': 1000}}).encode(), headers=headers)
            rows = []
            for _ in range(200):
                with urllib.request.urlopen(request, timeout=20) as reply:
                    raw = reply.read()
                    value = json.loads(raw)
                    journal.append({'sql': sql, 'native_bytes': base64.b64encode(raw).decode(), 'native_sha256': transport.sha(raw)})
                if value.get('error'):
                    raise RuntimeError('Owned managed native SQL failed')
                rows += value.get('data') or []
                next_uri = value.get('next_uri')
                if not next_uri:
                    return rows
                if not next_uri.startswith('/v1/query/'):
                    raise RuntimeError('Native managed query escaped original identity')
                request = urllib.request.Request(endpoint + next_uri, headers={'Authorization': headers['Authorization']})
            raise RuntimeError('Native managed fixture query did not finish')

        deadline = time.monotonic() + 150
        while True:
            try:
                version = native_sql('SELECT version()')[0][0]
                break
            except (urllib.error.URLError, TimeoutError, ConnectionError, IndexError):
                if time.monotonic() >= deadline:
                    raise RuntimeError('Owned Databend did not become ready')
                time.sleep(1)
        result['reported_server_version'] = version
        print('Owned server: ' + version, flush=True)
        native_sql("SET GLOBAL timezone='Asia/Shanghai'")
        if native_sql("SELECT value FROM system.settings WHERE name='timezone'") != [['Asia/Shanghai']]:
            raise RuntimeError('Owned server did not retain original non-UTC defaults')
        database = 'nyadb_transport_' + nonce
        native_sql('CREATE DATABASE "' + database + '"')
        native_sql('CREATE TABLE "' + database + '".managed_values (id UInt64 NOT NULL, exact Decimal(38,20) NOT NULL, payload Binary NOT NULL, absent Nullable(String))')
        native_sql('INSERT INTO "' + database + '".managed_values VALUES (18446744073709551615, 9007199254740993.12345678901234567890, unhex(\'00ff80\'), NULL)')
        native_sql('CREATE TABLE "' + database + '".managed_pages (id UInt64, payload Binary)')
        native_sql('INSERT INTO "' + database + '".managed_pages SELECT number, unhex(\'00ff\') FROM numbers(205)')
        native_sql('CREATE TABLE "' + database + '".managed_writes (id UInt64)')
        (base / 'independent-http-seed.json').write_text(json.dumps(journal, indent=2))
        fixture = {'Endpoint': endpoint, 'Username': username, 'PasswordFile': str(credential),
                   'Package': str(original / 'package'), 'Directory': str(base), 'Database': database,
                   'Archive': str(original / 'databend.zip'), 'SHA256': pins['archive_sha256'], 'Version': pins['version']}
        fixture_file = base / 'fixture.json'
        fixture_file.write_text(json.dumps(fixture, indent=2))
        env = os.environ.copy()
        env.update(NYADB_DATABEND_MANAGED_IT='1', NYADB_DATABEND_MANAGED_FIXTURE=str(fixture_file))
        command = ['go', 'test', '-count=1', '-v', '-timeout=8m']
        if args.race:
            command.append('-race')
        command += ['-run', '^TestDatabendManagedOriginal', './internal/services/databend']
        if transport.run_diagnostics(command, env, base / 'go-test.log'):
            raise RuntimeError('Original managed diagnostics failed; inspect ' + str(base / 'go-test.log'))
        verify_named_acceptance((base / 'go-test.log').read_text())
        result['original_executor_receipts'] = verify_receipts(base)
        result['writes'] = verify_independent_writes(base)
        result['original_processes_exited'] = transport.verify_processes(base)
        if result['original_processes_exited'] != 5:
            raise RuntimeError('Original independent process lifecycle coverage differs')
        if verify_build(original, dbx, cache) != pins or verify_build(reproduced, dbx, cache) != pins:
            raise RuntimeError('Original managed artifacts/sources changed during product verification')
        result.update(status='passed', required_diagnostics=REQUIRED, original_package_unchanged=True,
                      original_source_unchanged=True, unverified=[
                          'native Windows/macOS packages and product execution',
                          'public config persistence/URI/chooser/editor/provider/workspaces',
                          'native TLS/cloud/warehouse/routes/other versions/modes',
                          'native kill/rollback and concurrent keyless mutation guarantees',
                      ])
    except BaseException as error:
        result.update(status='failed', error=str(error))
        raise
    finally:
        credential.unlink(missing_ok=True)
        envfile.unlink(missing_ok=True)
        if container:
            count = transport.cleanup_owned_container(base, container, nonce, secret)
            result.update(original_container_removed=True, anonymous_volumes_removed=count)
        (base / 'verification.json').write_text(json.dumps(result, indent=2))
    print(json.dumps(result, indent=2), flush=True)


if __name__ == '__main__':
    main()
