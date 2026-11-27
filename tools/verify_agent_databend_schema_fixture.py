#!/usr/bin/env python3
"""Verify Databend original table rename/drop, failure receipts and public workbench on an owned native server."""
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
REQUIRED = [('./internal/services/databend', name) for name in ['TestDatabendSchemaOriginalRenameIdentityAndGuards','TestDatabendSchemaOriginalLostReplyNeverReplays','TestDatabendSchemaOriginalCancellationAndCloseWaitForSettlement', 'TestDatabendSchemaOriginalDropIdentityViewsAndRecreation', 'TestDatabendSchemaOriginalDropLostReplyAndConcurrentMove', 'TestDatabendSchemaOriginalDropCancelledVerificationAndClose']] + [('./internal/ui/shell', 'TestDatabendNativePublicSchemaRenamePreviewReceiptAndParkedCleanup'), ('./internal/ui/shell', 'TestDatabendNativePublicSchemaDropConfirmationReceiptAndParkedCleanup')]


def verify_receipt(receipt, state, verified, operation='rename'):
    if (receipt.get('State') != state or receipt.get('Verified') is not verified or
            receipt.get('Operation') != operation or receipt.get('Sequence') != 1 or
            not receipt.get('DocumentID') or receipt.get('Rows') != 0):
        raise RuntimeError('Original rename outcome differs')
    original = receipt['Original']
    target = original['Target']
    identity = original['Schema']['Databend']
    destination = receipt['Destination']
    quote = lambda name: '"' + name.replace('"', '""') + '"'
    sql = ('ALTER TABLE ' + quote(target['Object']['Schema']) + '.' + quote(target['Object']['Name']) +
           ' RENAME TO ' + quote(destination))
    if operation == 'drop':
        sql = 'DROP TABLE ' + quote(target['Object']['Schema']) + '.' + quote(target['Object']['Name'])
        if destination != target['Object']['Name']:
            raise RuntimeError('Original DROP destination differs')
    if (target['Object']['ProfileID'] <= 0 or target['Generation'] <= 0 or
            target['Object']['Catalog'] or target['Object']['Kind'] != 'table' or
            identity['Database'] != target['Object']['Schema'] or int(identity['TableID']) <= 0 or
            receipt['Statements'] != [sql] or original['Schema']['Engine'] != 'databend'):
        raise RuntimeError('Original rename statement or identity differs')
    native = json.loads(receipt['NativeJSON'])
    response, transport_receipt = native['Original'], native['Receipt']
    if (transport_receipt.get('Dispatched') is not True or transport_receipt.get('ExecutorAcknowledged') is not True or
            response.get('jsonrpc') != '2.0' or not response.get('id')):
        raise RuntimeError('Original executor acknowledgement unavailable')
    if state == 'committed':
        result = transport_receipt.get('Result')
        if (transport_receipt.get('RemoteError') is not None or response.get('result') != result or
                result.get('columns') != [] or result.get('column_types') != [] or result.get('rows') != [[]] or
                result.get('affected_rows') != 0 or result.get('truncated') is not False):
            raise RuntimeError('Native DDL acknowledgement was replaced')
    else:
        error = transport_receipt.get('RemoteError') or {}
        if (transport_receipt.get('Result') is not None or error.get('OperationOutcome') != 'unknown' or
                response.get('error', {}).get('data', {}).get('operationOutcome') != 'unknown'):
            raise RuntimeError('Unknown native DDL was relabelled')
    if verified:
        actual = receipt['Actual']
        expected = json.loads(json.dumps(target))
        expected['Object']['Name'] = destination
        if (actual['Target'] != expected or actual['Schema']['Table']['Name'] != destination or
                actual['Schema']['Databend'] != identity or actual['Schema']['Exists'] is not (operation == 'rename') or
                (operation == 'drop' and actual['Schema']['DDL'] != '')):
            raise RuntimeError('Verified destination lost original native incarnation')
    elif receipt.get('Actual') is not None:
        raise RuntimeError('Unverified original DDL acquired a later result')


def verify_schema(base):
    def read(name):
        return json.loads((base / name).read_text())
    good = read('schema-rename.json')
    verify_receipt(good['original']['Receipt'], 'committed', True)
    if (good['before'] != good['after'] or good['before'] != [['18446744073709551615', '00ff80', 'original']] or
            any(good.get(k) is not True for k in ['sibling_rejected', 'recreated_rejected', 'altered_rejected', 'occupied_rejected'])):
        raise RuntimeError('Native rename identity/data/precheck evidence differs')
    lost = read('schema-lost-reply.json')
    verify_receipt(lost['original']['Receipt'], 'uncertain', False)
    if (lost.get('dispatches') != 1 or lost.get('read_did_not_settle') is not True or
            lost['independent_destination'] != [['lost_destination57', lost['original']['Receipt']['Original']['Schema']['Databend']['TableID']]]):
        raise RuntimeError('Lost native reply was replayed or not applied independently')
    for name, closing in [('cancel57', False), ('close57', True)]:
        value = read('schema-' + name + '.json')
        verify_receipt(value['original']['Receipt'], 'committed', False)
        if value.get('settlement_waited') is not True or value.get('close') is not closing:
            raise RuntimeError('Original cancellation/close settlement evidence missing')
    lines = (base / 'public-schema.jsonl').read_text().splitlines()
    if len(lines) != 1:
        raise RuntimeError('Public rename acceptance missing or duplicated')
    public = json.loads(lines[0])
    verify_receipt(public['receipt'], 'committed', True)
    if (public.get('processes') != 1 or public['before'] != public['after'] or
            public['before'] != [['18446744073709551615', '00ff80', 'retained']] or
            any(public.get(k) is not True for k in ['public_provider_registered', 'preview_cancelled', 'parked_retained',
                                                  'original_tabs_closed', 'unrelated_tab_preserved'])):
        raise RuntimeError('Public original receipt/lifecycle evidence differs')
    from PIL import Image
    images = {}
    for language in ['zh-Hans', 'en']:
        for width, height in [(1280, 800), (720, 520)]:
            for phase in ['rename-preview', 'rename-receipt', 'rename-complete']:
                path = base / 'workbench' / 'TestDatabendNativePublicSchemaRenamePreviewReceiptAndParkedCleanup' / language / f'{phase}-{width}x{height}.png'
                with Image.open(path) as img:
                    if img.size != (width, height):
                        raise RuntimeError('Public rename capture size differs')
                    img.verify()
                images[str(path.relative_to(base))] = transport.sha(path.read_bytes())
    drop = verify_drop(base)
    images.update(drop.pop('image_sha256'))
    return {'public': public, 'drop': drop, 'image_sha256': images, 'original_data_preserved': True, 'unknown_not_replayed': True}


def verify_drop(base):
    def read(name):
        return json.loads((base / name).read_text())
    good = read('schema-drop.json')
    verify_receipt(good['original']['Receipt'], 'committed', True, 'drop')
    if (any(good.get(k) is not True for k in ['dependent_view_retained', 'dependent_view_query_rejected', 'recreated_preserved']) or
            good['replacement_table_id'] == good['original']['Receipt']['Original']['Schema']['Databend']['TableID']):
        raise RuntimeError('Native DROP dependency/recreation evidence differs')
    for name, state, dispatches, moved in [('drop_lost58', 'uncertain', 1, False), ('drop_moved58', 'committed', 2, True)]:
        value = read('schema-' + name + '.json')
        verify_receipt(value['original']['Receipt'], state, False, 'drop')
        if (value.get('dispatches') != dispatches or value.get('replay_rejected') is not True or
                value.get('original_name_absent') is not True or value.get('concurrent_original_preserved') is not moved):
            raise RuntimeError('Unknown or concurrently moved DROP original was replayed or misidentified')
    for name, closing in [('drop_cancel58', False), ('drop_close58', True)]:
        value = read('schema-' + name + '.json')
        verify_receipt(value['original']['Receipt'], 'committed', False, 'drop')
        if value.get('settlement_waited') is not True or value.get('close') is not closing:
            raise RuntimeError('Original DROP cancellation/close settlement missing')
    lines = (base / 'public-drop.jsonl').read_text().splitlines()
    if len(lines) != 1:
        raise RuntimeError('Public DROP acceptance missing or duplicated')
    public = json.loads(lines[0])
    verify_receipt(public['receipt'], 'committed', True, 'drop')
    if (public.get('processes') != 1 or public.get('before') != [['18446744073709551615', '00ff80']] or
            any(public.get(k) is not True for k in ['public_provider_registered', 'wrong_confirmation_rejected', 'preview_cancelled',
                                                  'parked_retained', 'original_tabs_closed', 'unrelated_tab_preserved'])):
        raise RuntimeError('Public DROP confirmation or original cleanup evidence differs')
    from PIL import Image
    images = {}
    for language in ['zh-Hans', 'en']:
        for width, height in [(1280, 800), (720, 520)]:
            for phase in ['drop-preview', 'drop-receipt', 'drop-complete']:
                path = base / 'workbench' / 'TestDatabendNativePublicSchemaDropConfirmationReceiptAndParkedCleanup' / language / f'{phase}-{width}x{height}.png'
                with Image.open(path) as img:
                    if img.size != (width, height):
                        raise RuntimeError('Public DROP capture size differs')
                    img.verify()
                images[str(path.relative_to(base))] = transport.sha(path.read_bytes())
    return {'public': public, 'image_sha256': images, 'original_incarnation_verified': True, 'unknown_not_replayed': True}


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
    base = ROOT / 'build/databend' / ('schema-' + nonce)
    base.mkdir(parents=True, mode=0o700)
    print('Evidence: ' + str(base), flush=True)
    result = {'scope': 'native_databend_table_rename_drop_and_public_workbench', 'status': 'running', 'pins': pins, 'image': transport.IMAGE, 'race': args.race}
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
        env.update(GOWORK='off', NYADB_DATABEND_MANAGED_IT='1', NYADB_DATABEND_PUBLIC_IT='1', NYADB_DATABEND_MANAGED_FIXTURE=str(fixture_file))
        for package, name in REQUIRED:
            log = base / (name + '.log')
            command = ['go', 'test', '-count=1', '-v', '-timeout=12m']
            if args.race:
                command.append('-race')
            command += ['-run', '^' + name + '$', package]
            if transport.run_diagnostics(command, env, log, timeout=780):
                raise RuntimeError('Native schema diagnostics failed; inspect ' + str(log))
            managed.verify_named_acceptance(log.read_text(), [name])
        result['evidence'] = verify_schema(base)
        result['original_processes_exited'] = transport.verify_processes(base)
        if result['original_processes_exited'] != 8:
            raise RuntimeError('Original rename fixture process count differs')
        if managed.verify_build(original, dbx, cache) != pins or managed.verify_build(reproduced, dbx, cache) != pins:
            raise RuntimeError('Original package changed during schema acceptance')
        result.update(status='passed', public_acceptance=True, unverified=['native TLS/cloud/warehouse/tenant/routes', 'other native versions and operating systems', 'database create; structure editing; copy/clear UI; non-FUSE tables; tenant-global program identity; concurrent DDL isolation'])
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
    import signal
    def stop(signum, frame):
        raise KeyboardInterrupt("interrupted " + str(signum))
    signal.signal(signal.SIGTERM, stop)
    main()
