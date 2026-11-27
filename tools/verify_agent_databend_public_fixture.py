#!/usr/bin/env python3
"""Verify public Databend entry points against an owned server and original package."""
import argparse
import base64
import json
import os
from pathlib import Path
import secrets
import time
import urllib.error
import urllib.request

import verify_agent_databend_managed_fixture as managed
import verify_agent_databend_transport_fixture as transport

ROOT = managed.ROOT
REQUIRED = [
    ('./internal/bootstrap', 'TestDatabendNativePublicBootstrapEditorImportVaultTestAndLiveSession'),
    ('./internal/ui/shell', 'TestDatabendNativePublicShellChooserGridWriteAndExplicitSQLRecovery'),
]


def verify_public(base):
    def one(name):
        rows = [json.loads(line) for line in (base / name).read_text().splitlines()]
        if len(rows) != 1:
            raise RuntimeError('Missing or duplicated public acceptance')
        return rows[0]
    editor, shell = one('public-bootstrap.jsonl'), one('public-shell.jsonl')
    if (editor.get('public_provider_registered') is not True or
            shell.get('public_provider_registered') is not True or
            editor.get('test_preserved_session') is not True or
            editor.get('driver_version') != 'jdbc-0.4.8-agent-0.1.82' or
            editor.get('driver_sha256') != '7069b794a21d8995b67d6918c6c106967ce94a18ae9a470159401bce18032bfd' or
            shell.get('typed_grid') is not True or shell.get('preview_cancel_kept_draft') is not True or
            shell.get('sql_text_retained') is not True or shell.get('recovery_replayed') is not False or
            shell.get('replacement_generation', 0) <= shell.get('original_generation', 0) or
            shell.get('processes') != 2 or shell.get('vault_recovery') is not True or shell.get('menu_connect') is not True):
        raise RuntimeError('Public entry point or original lifecycle evidence differs')
    envelope = shell['receipt']
    statements = envelope['Receipt']['Statements']
    if envelope.get('CommitConfirmed', False) is not False or len(statements) != 1:
        raise RuntimeError('Public receipt lost statement scope')
    statement = statements[0]
    if statement.get('State') != 'acknowledged' or statement.get('AffectedRows') != 1:
        raise RuntimeError('Public mutation lacks original acknowledgement')
    # Preserve original native acknowledgement independently of the UI summary.
    native = json.loads(statement['NativeJSON'])
    receipt = native['Receipt']
    original = native['Original']
    if (receipt.get('ExecutorAcknowledged') is not True or receipt.get('Dispatched') is not True or
            receipt.get('Result') != original.get('result') or original.get('jsonrpc') != '2.0' or not original.get('id')):
        raise RuntimeError('Original public executor response differs')
    controls = json.loads((base / 'public-editor-controls.json').read_text())
    if len(controls) != 12:
        raise RuntimeError('Public bilingual editor matrix incomplete')
    from PIL import Image
    images = list(base.glob('*.png')) + list((base / 'workbench').rglob('*.png'))
    required_images = set()
    shell_name = REQUIRED[1][1]
    for language in ['zh-Hans', 'en']:
        for width, height in [(920, 780), (720, 520)]:
            required_images.add(f'{language}-{width}x{height}-public-chooser.png')
            for tab in ['general', 'advanced', 'ssl']:
                required_images.add(f'{language}-{width}x{height}-public-editor-{tab}.png')
        for width, height in [(1280, 800), (720, 520)]:
            for phase in ['grid', 'preview', 'receipt', 'disconnected', 'recovered']:
                required_images.add(f'workbench/{shell_name}/{language}/public-{phase}-{width}x{height}.png')
    if not required_images.issubset({str(p.relative_to(base)) for p in images}):
        raise RuntimeError('Public chooser/editor/workbench captures missing')
    for name, labels in controls.items():
        expected = 5 if name.endswith('general') else 7 if name.endswith('advanced') else 6
        if len(labels) != expected:
            raise RuntimeError('Public control evidence incomplete')
        for box in labels.values():
            with Image.open(base / box['Screenshot']) as img:
                w, h = img.size
            if (box['W'] <= 0 or box['H'] < box['ExpectedHeight'] or box['ExpectedHeight'] < 12 or
                    box['X'] < 0 or box['Y'] < 29 or box['X']+box['W'] > w+1 or box['Y']+box['H'] > h-45):
                raise RuntimeError('Public editor control clipped')
    hashes = {}
    for path in images:
        with Image.open(path) as img:
            img.verify()
        hashes[str(path.relative_to(base))] = transport.sha(path.read_bytes())
    return {'editor': editor, 'shell': shell, 'image_sha256': hashes}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--build', type=Path, required=True)
    parser.add_argument('--reproduced-build', type=Path, required=True)
    parser.add_argument('--dbx', type=Path, required=True)
    parser.add_argument('--cache', type=Path, required=True)
    parser.add_argument('--race', action='store_true')
    args = parser.parse_args()
    original, reproduced, dbx, cache = [p.resolve() for p in [args.build,args.reproduced_build,args.dbx,args.cache]]
    if original == reproduced:
        raise RuntimeError('Independent original package builds required')
    pins = managed.verify_build(original,dbx,cache)
    if managed.verify_build(reproduced,dbx,cache) != pins:
        raise RuntimeError('Original packages do not reproduce')
    nonce = secrets.token_hex(8)
    base = ROOT / 'build/databend' / ('public-' + nonce)
    base.mkdir(parents=True, mode=0o700)
    print('Evidence: ' + str(base), flush=True)
    result = {'scope': 'public_databend_builtin_editor_and_workbench', 'status': 'running', 'pins': pins, 'image': transport.IMAGE, 'race': args.race, 'public_provider_registered': True}
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
        native_sql('CREATE DATABASE "nyadbXtransport_' + nonce + '"')
        native_sql('CREATE TABLE "nyadbXtransport_' + nonce + '".foreign_table (id UInt64)')
        native_sql('CREATE VIEW "' + database + '".native_view AS SELECT id,exact,payload,absent FROM "' + database + '".managed_values')
        (base / 'independent-http-seed.json').write_text(json.dumps(journal, indent=2))
        fixture = {'Endpoint': endpoint, 'Username': username, 'PasswordFile': str(credential),
                   'Package': str(original / 'package'), 'Directory': str(base), 'Database': database,
                   'Archive': str(original / 'databend.zip'), 'SHA256': pins['archive_sha256'], 'Version': pins['version']}
        fixture_file = base / 'fixture.json'
        fixture_file.write_text(json.dumps(fixture, indent=2))
        env = os.environ.copy()
        env.update(GOWORK='off', NYADB_DATABEND_PUBLIC_IT='1', NYADB_DATABEND_MANAGED_FIXTURE=str(fixture_file))
        for package, name in REQUIRED:
            log = base / (name + '.log')
            command = ['go', 'test', '-count=1', '-v', '-timeout=12m']
            if args.race:
                command.append('-race')
            command += ['-run', '^' + name + '$', package]
            if transport.run_diagnostics(command, env, log, timeout=780):
                raise RuntimeError('Public native diagnostics failed; inspect ' + str(log))
            managed.verify_named_acceptance(log.read_text(), [name])
        result['public'] = verify_public(base)
        result['original_processes_exited'] = transport.verify_processes(base)
        if result['original_processes_exited'] != 4:
            raise RuntimeError('Public original process coverage differs')
        if native_sql('SELECT count(*) FROM "' + database + '".managed_writes WHERE id=999') != [['0']]:
            raise RuntimeError('Recovery replayed original SQL')
        if native_sql('SELECT absent FROM "' + database + '".managed_values') != [['public edited 猫']]:
            raise RuntimeError('Public write missing from original server')
        (base / 'independent-http-final.json').write_text(json.dumps(journal, indent=2))
        if managed.verify_build(original, dbx, cache) != pins or managed.verify_build(reproduced, dbx, cache) != pins:
            raise RuntimeError('Original package changed during public acceptance')
        result.update(status='passed', unverified=['native TLS/cloud/warehouse/tenant/routes', 'other native versions and operating systems', 'table actions and tenant-global functions'])
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
    print(json.dumps({k:v for k,v in result.items() if k != 'public'}, indent=2), flush=True)


if __name__ == '__main__':
    main()
