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
PROVIDER_REQUIRED = [
    'TestDatabendProviderOriginalNamespacesStructuresSQLAndPartialReceipts',
    'TestDatabendProviderOriginalPinnedProfileRestartCloneAndReconnect',
    'TestDatabendProviderOriginalSQLCancellationWaitsForExecutorAndKeepsIndependentLease',
]
RUNTIME_REQUIRED = ['TestDatabendRuntimeOriginalGenerationsIsolationAndLatePublication']
DATA_REQUIRED = [
    'TestDatabendDataOriginalTypedPagesChunksEditingAndStreamingExports',
    'TestDatabendDataOriginalImmutableWritesConflictsAndUnsentSuffix',
    'TestDatabendDataOriginalDroppedReplyKeepsUnknownWriteAndIndependentDocument',
    'TestDatabendDataOriginalCancellationWaitsForExecutorAndCloseReleasesRequests',
]
WORKBENCH_REQUIRED = [
    'TestDatabendWorkbenchOriginalTypedGridPagingConfirmationAndReceipts',
    'TestDatabendWorkbenchOriginalParkedWriteAndRevokedMetadata',
    'TestDatabendWorkbenchOriginalUnknownReceiptGuardAndExplicitSQLRecovery',
    'TestDatabendWorkbenchOriginalBoundedFieldsCompleteEditingAndExport',
    'TestDatabendWorkbenchOriginalIndependentReadCancellation',
    'TestDatabendWorkbenchOriginalLateReadCannotReplaceNewGeneration',
    'TestDatabendWorkbenchOriginalDisconnectWaitsForReadAndNeverReplaysSQL',
    'TestDatabendWorkbenchOriginalSameStatementZeroGuard',
    'TestDatabendWorkbenchOriginalRecreatedTableRejectsOldConfirmation',
    'TestDatabendWorkbenchOriginalWriteCancellationAndDisconnectGuard',
]


def verify_workbench_statement(executor, statement):
    native = json.loads(statement['NativeJSON'], parse_float=Decimal)
    original = json.loads(base64.b64decode(executor['native_bytes'], validate=True), parse_float=Decimal)
    if native.get('Receipt') != executor['receipt'] or native.get('Original') != original:
        raise RuntimeError('UI receipt changed or omitted its complete original executor response')


def verify_workbench_read_lifecycle(directory, database, records):
    reads = [json.loads(line) for line in (directory / 'workbench-read-lifecycle.jsonl').read_text().splitlines()]
    held = [json.loads(line) for line in (directory / 'workbench-held-reads.jsonl').read_text().splitlines()]
    generations = [json.loads(line) for line in (directory / 'workbench-generation-lifecycle.jsonl').read_text().splitlines()]
    expected = {WORKBENCH_REQUIRED[4] + '/' + scope: scope for scope in ['page', 'count', 'field']}
    if len(reads) != 3 or {r['test'] for r in reads} != set(expected):
        raise RuntimeError('Independent original page/count/field cancellation coverage differs')
    for row in reads:
        scope = expected[row['test']]
        if (row['scope'] != scope or row['object']['Schema'] != database or
                row['object']['Name'] != 'workbench_read_' + scope + '53' or
                row['count'] != (2 if scope == 'count' else 1) or row['matched_requests'] != (2 if scope == 'field' else 3) or
                row.get('public_provider_registered') is not False or
                any(row.get(k) is not True for k in ['cache_preserved', 'independent_read_survived',
                                                    'late_result_rejected', 'explicit_refresh'])):
            raise RuntimeError('Original read cancellation changed identity/cache or replayed a request')
        canceled = [r for r in records if r['test'] == row['test'] and r['receipt']['CancellationRequested']]
        if (len(canceled) != 1 or not canceled[0]['receipt']['Dispatched'] or
                not canceled[0]['receipt']['ExecutorAcknowledged'] or canceled[0]['receipt']['ProcessExitConfirmed']):
            raise RuntimeError('Original read cancellation lacks its live executor acknowledgement')
    expected[WORKBENCH_REQUIRED[5]] = 'page'
    expected[WORKBENCH_REQUIRED[6]] = 'page'
    if len(held) != 5 or {r['test'] for r in held} != set(expected):
        raise RuntimeError('Original delayed read response evidence is missing or duplicated')
    ids = set()
    for row in held:
        raw = base64.b64decode(row['native_bytes'], validate=True)
        value = json.loads(raw)
        if (transport.sha(raw) != row['native_sha256'] or row['status'] != 200 or
                value.get('error') is not None or row['scope'] != expected[row['test']] or
                not re.fullmatch('[0-9a-f]{32}', row['query_id']) or value.get('id') != row['query_id'] or
                row['query_id'] in ids or '"' + database + '"' not in row['sql']):
            raise RuntimeError('Original delayed read bytes/query identity differ')
        ids.add(row['query_id'])
    if len(generations) != 1:
        raise RuntimeError('Original workbench generation evidence missing or duplicated')
    row = generations[0]
    if (row['test'] != WORKBENCH_REQUIRED[5] or row['object']['Schema'] != database or
            row['object']['Name'] != 'workbench_generation53' or row['replacement_rows'] != 2 or
            not 0 < row['original_generation'] < row['replacement_generation'] or
            row.get('public_provider_registered') is not False or
            any(row.get(k) is not True for k in ['old_cache_preserved', 'old_document_revoked',
                                                'replacement_survived_old_close', 'disconnect_disposed_data'])):
        raise RuntimeError('Late original generation overwrote or disposed the replacement')
    disconnects = [json.loads(line) for line in (directory / 'workbench-disconnect-lifecycle.jsonl').read_text().splitlines()]
    if len(disconnects) != 1:
        raise RuntimeError('Pending-read disconnect evidence missing or duplicated')
    row = disconnects[0]
    cancellations = [r for r in records if r['test'] == WORKBENCH_REQUIRED[6] and r['receipt']['CancellationRequested']]
    # Disconnect can also cancel an inspector metadata read. Require exactly
    # the held data cursor's acknowledgement, and independently account for
    # every other dispatched reader; a background receipt cannot replace it.
    canceled = [r for r in cancellations if r['method'] == 'workbench-data-cursor']
    if (row['test'] != WORKBENCH_REQUIRED[6] or row['database'] != database or row['matched_requests'] != 2 or
            row['rows'] != [['1']] or row.get('recovery_replayed') is not False or
            row.get('public_provider_registered') is not False or
            not 0 < row['original_generation'] < row['replacement_generation'] or
            any(row.get(k) is not True for k in ['sql_text_retained', 'original_executor_acknowledged', 'data_disposed']) or
            any(r['receipt']['Dispatched'] and (not r['receipt']['ExecutorAcknowledged'] or
                r['receipt']['ProcessExitConfirmed']) for r in cancellations) or
            len(canceled) != 1 or not canceled[0]['receipt']['Dispatched'] or
            not canceled[0]['receipt']['ExecutorAcknowledged'] or canceled[0]['receipt']['ProcessExitConfirmed']):
        raise RuntimeError('Pending-read disconnect/recovery lost original acknowledgement or replayed SQL')
    return {'reads': reads, 'generations': generations, 'disconnects': disconnects, 'held_query_ids': sorted(ids)}


def verify_workbench_write_faults(directory, database, records, independent):
    def journal(name):
        return [json.loads(line, parse_float=Decimal) for line in (directory / name).read_text().splitlines()]
    rows = journal('workbench-write-faults.jsonl')
    native = journal('workbench-native-writes.jsonl')
    incarnations = journal('workbench-table-incarnations.jsonl')
    expected = {WORKBENCH_REQUIRED[7]: ('zero_guard', 'workbench_zero54', 1, 2, 0)}
    for mode in ['insert', 'update', 'prefix']:
        expected[WORKBENCH_REQUIRED[8] + '/' + mode] = ('recreate_' + mode, 'workbench_recreate_' + mode + '54',
                                                       1 if mode == 'prefix' else 0, 1 if mode == 'insert' else 2, 1)
    for mode in ['cancel', 'disconnect']:
        expected[WORKBENCH_REQUIRED[9] + '/' + mode] = (mode, 'workbench_write_' + mode + '54', 1, 2, 1)
    if len(rows) != len(expected) or {r['test'] for r in rows} != set(expected):
        raise RuntimeError('Original write fault coverage missing or duplicated')
    if len(native) != 4 or len(incarnations) != 3 or {r['test'] for r in incarnations} != {
            WORKBENCH_REQUIRED[8] + '/' + mode for mode in ['insert', 'update', 'prefix']}:
        raise RuntimeError('Original native writes/table incarnations missing or duplicated')
    query_ids = set()
    for row in rows:
        mode, table, sent, total, affected = expected[row['test']]
        preview, receipt = row['preview'], row['receipt']['Receipt']
        obj = preview['Object']
        wanted_rows = ([['1', 'first'], ['1', 'first'], ['2', 'second']] if mode == 'zero_guard' else
                       [['1', 'changed'], ['2', 'second']] if mode in ['cancel', 'disconnect'] else
                       [['1', 'first'], ['2', 'second']])
        sql = 'SELECT id,txt FROM "' + database + '".' + table + ' ORDER BY id' + (',txt' if mode == 'zero_guard' else '')
        if (row['mode'] != mode or obj['Schema'] != database or obj['Name'] != table or obj['Kind'] != 'table' or
                obj['ProfileID'] <= 0 or obj.get('Catalog', '') != '' or receipt['Object'] != obj or
                row['writes'] != sent or row['rows'] != wanted_rows or independent.get((row['test'], sql)) != wanted_rows or
                row['receipt']['Request'] != preview or not re.fullmatch('[0-9a-f]{64}', preview['Metadata']) or
                not re.fullmatch('[1-9][0-9]*', preview['Incarnation']) or
                len(receipt['Statements']) != total or len(preview['Statements']) != total or
                any(row.get(k) is not True for k in ['draft_retained', 'cache_preserved', 'write_blocked'])):
            raise RuntimeError('Original write fault changed confirmation, destination, draft or persisted rows')
        executed = [r for r in records if r['test'] == row['test'] and r['method'] == 'workbench-data-write']
        http = [r for r in native if r['test'] == row['test']]
        if len(executed) != sent or len(http) != sent:
            raise RuntimeError('Original write fault replayed or omitted native statement')
        for i, statement in enumerate(receipt['Statements']):
            kind = 'insert' if mode == 'recreate_insert' else 'update'
            if (statement['EditIndex'] != i or statement['Destination'] != obj or statement['Kind'] != kind or
                    statement['State'] != ('acknowledged' if i < sent else 'not_sent') or
                    statement['AffectedRows'] != (affected if i < sent else 0)):
                raise RuntimeError('Original prefix acknowledgement/zero count/unsent suffix differs')
            if i >= sent:
                if statement.get('NativeJSON'):
                    raise RuntimeError('Unsent suffix has fabricated executor evidence')
                continue
            verify_workbench_statement(executed[i], statement)
            original = executed[i]['receipt']
            if (not original['Dispatched'] or not original['ExecutorAcknowledged'] or original['ProcessExitConfirmed'] or
                    original['Result'].get('rows') != [[affected]] or
                    original['Result'].get('columns') != ['number of rows updated'] or
                    original['Result'].get('column_types') != ['uint64'] or
                    original['Result'].get('affected_rows') != 0 or original['Result'].get('truncated') is not False):
                raise RuntimeError('Write fault lacks original live native count acknowledgement')
            wire = http[i]
            raw = base64.b64decode(wire['native_bytes'], validate=True)
            value = json.loads(raw)
            if (transport.sha(raw) != wire['native_sha256'] or wire['status'] != 200 or value.get('error') is not None or
                    wire['sql'] != preview['Statements'][i]['SQL'] or not re.fullmatch('[0-9a-f]{32}', wire['query_id']) or
                    value.get('id') != wire['query_id'] or wire['query_id'] in query_ids):
                raise RuntimeError('Write fault native bytes/query identity or confirmed SQL differs')
            query_ids.add(wire['query_id'])
        if mode in ['cancel', 'disconnect'] and (not executed[0]['receipt']['CancellationRequested'] or
                not 0 < row['original_generation'] == row['current_generation'] or
                row.get('waited_for_original') is not True or row.get('disconnect_guarded') is not True):
            raise RuntimeError('Canceled original write lost acknowledgement or disconnect guard')
        if mode.startswith('recreate_'):
            evidence = next(r for r in incarnations if r['test'] == row['test'])
            before, after = evidence['original'], evidence['replacement']
            if (len(before) != 1 or len(after) != 1 or len(before[0]) != 1 or len(after[0]) != 1 or
                    before == after or before[0][0] != preview['Incarnation'] or
                    not re.fullmatch('[1-9][0-9]*', after[0][0]) or
                    evidence['original_ddl'] != evidence['replacement_ddl'] or len(evidence['original_ddl']) != 1):
                raise RuntimeError('Recreated table lacks distinct native incarnation with identical definition')
            for observed in [evidence['original_rows'], evidence['replacement_rows']]:
                if len(observed) != 2 or any(len(r) != 2 or r[0] != str(i + 1) or
                        not re.fullmatch('[0-9]+', r[1]) for i, r in enumerate(observed)):
                    raise RuntimeError('Recreated table lacks its original hidden row identities')
            identity_sql = "SELECT table_id FROM system.tables WHERE database='" + database + "' AND name='" + table + "'"
            row_sql = 'SELECT id,_row_id FROM "' + database + '".' + table + ' ORDER BY id'
            ddl_sql = 'SHOW CREATE TABLE "' + database + '".' + table
            if (independent.get((row['test'], identity_sql)) != before + after or
                    independent.get((row['test'], ddl_sql)) != evidence['original_ddl'] + evidence['replacement_ddl'] or
                    independent.get((row['test'], row_sql)) != evidence['original_rows'] + evidence['replacement_rows']):
                raise RuntimeError('Recreated table identity/row IDs lack independent original HTTP evidence')
    return {'faults': rows, 'incarnations': incarnations, 'native_query_ids': sorted(query_ids)}


def verify_workbench_independent_write(directory, database, records, independent, lifecycle):
    rows = [json.loads(line, parse_float=Decimal) for line in (directory / 'workbench-independent-write.jsonl').read_text().splitlines()]
    if len(rows) != 1:
        raise RuntimeError('Independent document write evidence missing or duplicated')
    row = rows[0]
    original, other = row['original_object'], row['independent_object']
    unknown = next(r for r in lifecycle if r['mode'] == 'unknown')
    writes = [r for r in records if r['test'] == row['test'] and r['method'] == 'workbench-data-write']
    receipt = row['independent_receipt']['Receipt']
    statements = receipt['Statements']
    sql = 'SELECT id FROM "' + database + '".workbench_independent54 ORDER BY id'
    if (row['test'] != WORKBENCH_REQUIRED[2] or row['original_writes'] != 2 or len(writes) != 3 or
            row['original_receipt'] != unknown['receipt'] or original != unknown['receipt']['Receipt']['Object'] or
            other != {**original, 'Name': 'workbench_independent54'} or original['Name'] != 'workbench_unknown52' or
            other['Schema'] != database or receipt['Object'] != other or len(statements) != 1 or
            statements[0]['State'] != 'acknowledged' or statements[0]['AffectedRows'] != 1 or
            statements[0]['Destination'] != other or statements[0]['Kind'] != 'insert' or
            row['independent_rows'] != [['54']] or independent.get((row['test'], sql)) != [['54']] or
            row.get('unknown_receipt_unchanged') is not True or row.get('original_draft_retained') is not True):
        raise RuntimeError('Independent document changed/unlocked the original unknown write')
    verify_workbench_statement(writes[-1], statements[0])
    result = writes[-1]['receipt']['Result']
    if result.get('columns') != ['number of rows inserted'] or result.get('rows') != [[1]]:
        raise RuntimeError('Independent document lacks native one-row insertion acknowledgement')
    return row


def verify_workbench_receipts(directory, database):
    from PIL import Image
    records = [json.loads(line, parse_float=Decimal) for line in
               (directory / 'original-receipts.jsonl').read_text().splitlines()]
    records = [r for r in records if any(r['test'] == n or r['test'].startswith(n + '/')
                                       for n in WORKBENCH_REQUIRED)]
    for record in records:
        transport.verify_receipt(record)
    for name in WORKBENCH_REQUIRED:
        methods = {r['method'] for r in records if r['test'] == name or r['test'].startswith(name + '/')}
        required = {'workbench-handshake', 'workbench-metadata-open', 'workbench-metadata-query', 'workbench-data-cursor'}
        if name in WORKBENCH_REQUIRED[:4] + WORKBENCH_REQUIRED[7:]:
            required.add('workbench-data-write')
        if required - methods:
            raise RuntimeError('Shared workbench lacks original connection/data/write receipts: ' + name)
    acceptance = [json.loads(line) for line in
                  (directory / 'workbench-acceptance.jsonl').read_text().splitlines()]
    if len(acceptance) != 1:
        raise RuntimeError('Original shared-grid acceptance missing or duplicated')
    core = acceptance[0]
    if (core['test'] != WORKBENCH_REQUIRED[0] or core['object']['Schema'] != database or
            core['pages'] != [200, 5] or core.get('public_provider_registered') is not False or
            any(core.get(k) is not True for k in ['typed_values', 'field_read', 'complete_editor',
                'preview_cancel_kept_draft', 'native_update_insert_delete', 'empty_map_ui_writes', 'omitted_defaults'])):
        raise RuntimeError('Original shared workbench identity/types/workflows incomplete')
    mutations = [r['receipt']['Result'] for r in records if r['test'] == WORKBENCH_REQUIRED[0]
                 and r['method'] == 'workbench-data-write']
    if ([r.get('columns') for r in mutations] != [['number of rows updated'], ['number of rows inserted'],
                                                 ['number of rows deleted']] or
            any(r.get('rows') != [[1]] or r.get('column_types') != ['uint64'] or
                r.get('affected_rows') != 0 or r.get('truncated') is not False for r in mutations)):
        raise RuntimeError('Original UI mutations lack complete native counts or were replayed')
    core_executors = [r for r in records if r['test'] == WORKBENCH_REQUIRED[0] and
                      r['method'] == 'workbench-data-write']
    copied = core['receipt']['Receipt']['Statements']
    if len(copied) != 1 or copied[0]['State'] != 'acknowledged' or copied[0]['AffectedRows'] != 1:
        raise RuntimeError('Copied original UI update receipt is incomplete')
    verify_workbench_statement(core_executors[0], copied[0])
    lifecycle = [json.loads(line) for line in
                 (directory / 'workbench-lifecycle.jsonl').read_text().splitlines()]
    if len(lifecycle) != 3 or {r['mode'] for r in lifecycle} != {'park', 'metadata', 'unknown'}:
        raise RuntimeError('Original parked/metadata/unknown workbench matrix incomplete')
    for row in lifecycle:
        states = [s['State'] for s in row['receipt']['Receipt']['Statements']]
        expected = {'park': (2, ['acknowledged', 'acknowledged'], [['1', 'changed'], ['2', 'changed']]),
                    'metadata': (1, ['acknowledged', 'not_sent'], [['1', 'changed'], ['2', 'second']]),
                    'unknown': (2, ['acknowledged', 'unknown', 'not_sent'], [['1'], ['2']])}[row['mode']]
        if (row['writes'], states, row['rows']) != expected:
            raise RuntimeError('Original ordered UI receipt/persisted prefix/suffix differs')
        dispatched = [r for r in records if r['test'] == row['test'] and
                      r['method'] == 'workbench-data-write' and r['receipt']['Dispatched']]
        sent = [s for s in row['receipt']['Receipt']['Statements'] if s['State'] != 'not_sent']
        if len(dispatched) != len(sent) + (1 if row['mode'] == 'unknown' else 0):
            raise RuntimeError('UI receipt does not correspond to original dispatched executors')
        for executor, statement in zip(dispatched, sent):
            verify_workbench_statement(executor, statement)
        if row['mode'] == 'unknown' and (row.get('draft_guard') is not True or
                row.get('sql_text_retained') is not True or row.get('recovery_replayed') is not False or
                not 0 < row['original_generation'] < row['replacement_generation']):
            raise RuntimeError('Original unknown receipt/SQL recovery guard incomplete')
    http_records = [json.loads(line) for line in
                    (directory / 'workbench-http-receipts.jsonl').read_text().splitlines()]
    independent = {}
    for row in http_records:
        raw = base64.b64decode(row['native_bytes'], validate=True)
        value = json.loads(raw)
        if transport.sha(raw) != row['native_sha256'] or value.get('error') is not None:
            raise RuntimeError('Independent workbench native response bytes/error differ')
        independent.setdefault((row['test'], row['sql']), []).extend(value.get('data') or [])
    for row in lifecycle:
        table = 'workbench_' + row['mode'] + '52'
        sql = 'SELECT ' + ('id' if row['mode'] == 'unknown' else 'id,txt') + ' FROM "' + database + '".' + table + ' ORDER BY id'
        if independent.get((row['test'], sql)) != row['rows']:
            raise RuntimeError('Workbenches lack independent original persisted-row evidence')
    disconnect_sql = 'SELECT id FROM "' + database + '".workbench_disconnect53 ORDER BY id'
    if independent.get((WORKBENCH_REQUIRED[6], disconnect_sql)) != [['1']]:
        raise RuntimeError('Disconnect recovery lacks independent native proof that SQL was not replayed')
    write_faults = verify_workbench_write_faults(directory, database, records, independent)
    independent_write = verify_workbench_independent_write(directory, database, records, independent, lifecycle)
    fields = [json.loads(line) for line in (directory / 'workbench-fields.jsonl').read_text().splitlines()]
    if (len(fields) != 1 or fields[0]['test'] != WORKBENCH_REQUIRED[3] or
            fields[0]['preview_units'] != 1024 or fields[0]['chunk_boundaries'] != [1024, 2048, 2052] or
            fields[0]['complete_edit_units'] != 2054 or fields[0].get('foreign_grant_rejected') is not True or
            fields[0].get('foreign_plan_rejected') is not True or
            fields[0]['full_jsonl']['object']['Schema'] != database or
            fields[0]['full_jsonl']['object']['Name'] != 'workbench_fields52' or
            [c['Name'] for c in fields[0]['full_jsonl']['columns']] != ['payload'] or
            len(fields[0]['full_jsonl']['values']) != 1 or
            fields[0]['full_jsonl']['values'][0].get('kind') != 'text' or
            fields[0]['full_jsonl']['values'][0].get('truncated', False) is not False or
            fields[0]['full_jsonl']['values'][0].get('text') != '猫' * 2049 + 'end 改'):
        raise RuntimeError('Original bounded fields/complete editor/export or signed document isolation incomplete')
    phases = {WORKBENCH_REQUIRED[0]: ['grid', 'inspector', 'editor', 'preview', 'receipt'],
              WORKBENCH_REQUIRED[1] + '/park': ['park'],
              WORKBENCH_REQUIRED[1] + '/metadata': ['metadata'],
              WORKBENCH_REQUIRED[2]: ['unknown-receipt', 'independent-write', 'disconnected', 'recovered'],
              WORKBENCH_REQUIRED[3]: ['bounded-inspector', 'complete-editor'],
              WORKBENCH_REQUIRED[5]: ['generation-recovered'],
              WORKBENCH_REQUIRED[6]: ['disconnect-recovered'],
              WORKBENCH_REQUIRED[7]: ['zero-guard']}
    for mode in ['insert', 'update', 'prefix']:
        phases[WORKBENCH_REQUIRED[8] + '/' + mode] = ['recreated-table']
    for mode in ['cancel', 'disconnect']:
        phases[WORKBENCH_REQUIRED[9] + '/' + mode] = ['write-acknowledged']
    for scope in ['page', 'count', 'field']:
        phases[WORKBENCH_REQUIRED[4] + '/' + scope] = ['read-recovered']
    read_lifecycle = verify_workbench_read_lifecycle(directory, database, records)
    captures = []
    for test, names in phases.items():
        for language in ['zh-Hans', 'en']:
            for width, height in [(1280, 800), (720, 520)]:
                for phase in names:
                    path = directory / 'workbench' / test / language / f'{phase}-{width}x{height}.png'
                    with Image.open(path) as rendered:
                        rendered.load()
                        if rendered.size != (width, height) or not rendered.getbbox():
                            raise RuntimeError('Original localized workbench capture is empty or has wrong dimensions')
                    captures.append({'path': str(path.relative_to(directory)), 'sha256': transport.sha(path.read_bytes())})
    return {'required_diagnostics': WORKBENCH_REQUIRED, 'original_receipts': len(records),
            'independent_http_receipts': len(http_records), 'captures': captures,
            'read_lifecycle': read_lifecycle,
            'write_faults': write_faults, 'independent_write': independent_write,
            'public_provider_registered': False}


def verify_named_acceptance(output, names=None):
    for name in REQUIRED if names is None else names:
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


def verify_provider_receipts(directory, database):
    records = [json.loads(line, parse_float=Decimal) for line in
               (directory / 'original-receipts.jsonl').read_text().splitlines()]
    for record in records:
        transport.verify_receipt(record)
    groups = {name: [r for r in records if r['test'] == name] for name in PROVIDER_REQUIRED}
    for name, group in groups.items():
        methods = {r['method'] for r in group}
        if {'provider-handshake', 'provider-metadata-open', 'provider-metadata-query',
                'provider-query-open', 'provider-query-executor'} - methods:
            raise RuntimeError('Private provider lacks original startup/metadata/SQL evidence: ' + name)
    first = groups[PROVIDER_REQUIRED[0]]
    namespaces = [r['receipt']['Result'] for r in first if r['method'] == 'provider-metadata-query'
                  and (r['receipt']['Result'] or {}).get('columns') == ['databases_in_default']]
    if len(namespaces) != 1 or [database] not in namespaces[0]['rows'] or [database.replace('nyadb_transport_', 'nyadbXtransport_', 1)] not in namespaces[0]['rows']:
        raise RuntimeError('Original literal namespace metadata lacks the independent database')
    objects = [r['receipt']['Result'] for r in first if r['method'] == 'provider-metadata-list_objects']
    if (len(objects) != 1 or 'foreign_table' in {o['name'] for o in objects[0]} or
            {'managed_values', 'native_view'} - {o['name'] for o in objects[0]}):
        raise RuntimeError('Native original metadata omitted owned objects or leaked wildcard identities')
    structures = [r['receipt']['Result'] for r in first if r['method'] == 'provider-metadata-query']
    if sum(s.get('columns') == ['Field', 'Type', 'Null', 'Default', 'Extra'] for s in structures) != 2:
        raise RuntimeError('Native table/view DESCRIBE metadata is incomplete')
    definitions = [s.get('rows', []) for s in structures if s.get('columns') in
                   [['Table', 'Create Table'], ['View', 'Create View']]]
    if (len(definitions) != 2 or not any('ENGINE=FUSE' in row[1] for rows in definitions for row in rows)):
        raise RuntimeError('Complete native table/view definitions or engine clauses missing')
    executors = [r['receipt'] for r in first if r['method'] == 'provider-query-executor']
    errors = [r for r in executors if r.get('RemoteError')]
    if len(errors) != 1 or not errors[0]['ExecutorAcknowledged']:
        raise RuntimeError('Original failed SQL executor envelope missing')
    counts = [r['Result'] for r in executors if (r['Result'] or {}).get('columns') in
              [['number of rows inserted'], ['number of rows updated'], ['number of rows deleted']]]
    if (len(counts) != 3 or any(s.get('rows') != [[1]] or s.get('column_types') != ['uint64'] or
                              s.get('affected_rows') != 0 for s in counts)):
        raise RuntimeError('Private provider native mutation counts differ from unchanged original receipts')
    journal = [json.loads(line) for line in (directory / 'independent-http-receipts.jsonl').read_text().splitlines()]
    partial = [r for r in journal if r['test'] == PROVIDER_REQUIRED[0]]
    rows = []
    for r in partial:
        raw = base64.b64decode(r['native_bytes'], validate=True)
        value = json.loads(raw)
        if (transport.sha(raw) != r['native_sha256'] or value.get('error') is not None or
                r['sql'] != 'SELECT id FROM "' + database + '".managed_writes WHERE id IN (41,42) ORDER BY id'):
            raise RuntimeError('Independent private provider partial-write evidence differs')
        rows += value.get('data') or []
    if rows != [['41']]:
        raise RuntimeError('First provider write was not durable or unsent suffix was executed')
    cancellation = json.loads((directory / 'provider-cancellation.json').read_text())
    canceled = [r['receipt'] for r in groups[PROVIDER_REQUIRED[2]]
                if r['method'] == 'provider-query-executor' and r['receipt']['CancellationRequested']]
    if (len(canceled) != 1 or not canceled[0]['ExecutorAcknowledged'] or canceled[0]['ProcessExitConfirmed'] or
            not re.fullmatch('[0-9a-f]{32}', cancellation.get('original_query_id', '')) or
            cancellation.get('submitted') != 1 or cancellation.get('final_http_status') != 200 or
            cancellation.get('executor_acknowledged') is not True or cancellation.get('native_kill_or_rollback_accepted') is not False):
        raise RuntimeError('Private provider cancellation lacks its original executor/final acknowledgement')
    cancellation_rows = []
    for r in journal:
        if r['test'] != PROVIDER_REQUIRED[2]:
            continue
        raw = base64.b64decode(r['native_bytes'], validate=True)
        value = json.loads(raw)
        if (transport.sha(raw) != r['native_sha256'] or value.get('error') is not None or
                r['sql'] != 'SELECT count() FROM "' + database + '".managed_writes WHERE id=59'):
            raise RuntimeError('Independent canceled SQL suffix identity/bytes differ')
        cancellation_rows += value.get('data') or []
    if cancellation_rows != [['0']]:
        raise RuntimeError('Canceled SQL suffix was dispatched or replayed')
    return {'required_diagnostics': PROVIDER_REQUIRED,
            'complete_original_receipts': sum(map(len, groups.values())),
            'durable_first_write': 41, 'unsent_write': 42,
            'public_provider_registered': False}


def verify_data_receipts(directory):
    records = [json.loads(line, parse_float=Decimal) for line in
               (directory / 'original-receipts.jsonl').read_text().splitlines()]
    for record in records:
        transport.verify_receipt(record)
    for name in DATA_REQUIRED:
        methods = {r['method'] for r in records if r['test'] == name}
        if {'provider-handshake', 'provider-data-open', 'provider-data-close',
                'provider-data-cursor'} - methods:
            raise RuntimeError('Native data diagnostic lacks original request/close receipts: ' + name)
    reads = json.loads((directory / 'data-read.json').read_text())
    if reads != {'page_rows': 205, 'unicode_units': 3600, 'binary_bytes': 2100,
                 'json_null_distinct': True, 'export_bytes': 1200000, 'selected_rows': 2,
                 'export_max_row_bytes': 8 << 20, 'editing_max_row_bytes': 1 << 20}:
        raise RuntimeError('Native data paging/chunks/export evidence differs')
    writes = json.loads((directory / 'data-writes.json').read_text())
    if any(writes.get(k) is not True for k in ['native_insert_update_delete', 'exact_typed_values',
                                              'duplicates_rejected', 'immutable_inputs', 'no_transaction_claim']):
        raise RuntimeError('Native data write diagnostic is incomplete')
    stale = writes['stale_receipt']
    if (stale['Applied'] != 0 or stale['Incomplete'] is not True or stale['CommitUncertain'] or
            [s['State'] for s in stale['Sequential']['Statements']] != ['not_sent', 'not_sent']):
        raise RuntimeError('Stale original row dispatched a mutation')
    unknown = json.loads((directory / 'data-unknown.json').read_text())
    receipt = unknown['original_receipt']
    if (unknown['dispatches'] != 3 or unknown['unknown_preserved'] is not True or
            unknown['independent_document_survived'] is not True or receipt['Applied'] != 1 or
            receipt['Incomplete'] is not True or receipt['CommitUncertain'] is not True or
            [s['State'] for s in receipt['Sequential']['Statements']] != ['acknowledged', 'unknown', 'not_sent']):
        raise RuntimeError('Dropped original write reply was replayed/settled or lost its suffix')
    cancel = json.loads((directory / 'data-cancellation.json').read_text())
    cancelled = [r['receipt'] for r in records if r['test'] == DATA_REQUIRED[3] and
                 r['method'] == 'provider-data-cursor' and r['receipt']['CancellationRequested']]
    if (len(cancelled) != 1 or not cancelled[0]['ExecutorAcknowledged'] or
            cancelled[0]['ProcessExitConfirmed'] or cancel['submitted'] != 1 or
            cancel['final_http_status'] != 200 or cancel['close_waited'] is not True or
            cancel['independent_sql_survived'] is not True or cancel['native_kill_or_rollback_accepted']):
        raise RuntimeError('Native data cancellation did not await original executor/final')
    native_writes = [r['receipt'] for r in records if r['test'] == DATA_REQUIRED[1] and r['method'] == 'provider-data-write']
    if (len(native_writes) != 3 or
            [r['Result'].get('columns') for r in native_writes] !=
            [['number of rows inserted'], ['number of rows updated'], ['number of rows deleted']] or
            any(r['Result'].get('rows') != [[1]] or not r['ExecutorAcknowledged'] for r in native_writes)):
        raise RuntimeError('Independent native insert/update/delete receipts differ')
    return {'reads': reads, 'writes': True, 'unknown_preserved': True,
            'cancel_waited': True, 'public_provider_registered': False}


def verify_runtime_receipts(directory):
    records = [json.loads(line, parse_float=Decimal) for line in
               (directory / 'original-receipts.jsonl').read_text().splitlines()]
    records = [r for r in records if r['test'] in RUNTIME_REQUIRED]
    for record in records:
        transport.verify_receipt(record)
    methods = {r['method'] for r in records}
    if {'runtime-handshake', 'runtime-metadata-open', 'runtime-metadata-query',
            'runtime-query-open', 'runtime-query-executor'} - methods:
        raise RuntimeError('Private runtime lacks original process/session/SQL receipts')
    queries = [r['receipt']['Result'] for r in records if r['method'] == 'runtime-query-executor']
    writes = [q for q in queries if (q or {}).get('columns') == ['number of rows inserted']]
    if len(writes) != 1 or writes[0].get('rows') != [[1]] or writes[0].get('affected_rows') != 0:
        raise RuntimeError('Original runtime durable write was lost or replayed')
    recovered = [q for q in queries if (q or {}).get('columns') == ['id']]
    if len(recovered) != 1 or recovered[0].get('rows') != [[55]]:
        raise RuntimeError('Native recovered query lacks exact original durable/unsent evidence')
    generations = [json.loads(line) for line in (directory / 'runtime-generations.jsonl').read_text().splitlines()]
    if (len(generations) != 1 or generations[0]['test'] != RUNTIME_REQUIRED[0] or
            not 0 < generations[0]['original'] < generations[0]['independent'] < generations[0]['replacement'] < generations[0]['late_replacement'] or
            generations[0]['first_write'] != 55 or generations[0]['unsent_write'] != 56 or generations[0]['recovery_replayed'] is not False):
        raise RuntimeError('Private runtime original generation/late replacement evidence differs')
    return {'required_diagnostics': RUNTIME_REQUIRED, 'original_receipts': len(records),
            'generations': generations[0], 'public_provider_registered': False}


def verify_independent_writes(directory):
    observed = {name: [] for name in REQUIRED}
    for line in (directory / 'independent-http-receipts.jsonl').read_text().splitlines():
        r = json.loads(line)
        if r['test'] not in REQUIRED:
            continue
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
    parser.add_argument('--provider', action='store_true', help='Also verify private provider/config/SQL/metadata acceptance')
    parser.add_argument('--data', action='store_true', help='Also verify private native data documents and independent writes')
    parser.add_argument('--workbench', action='store_true', help='Also verify private original shared workbench UI and lifecycle')
    args = parser.parse_args()
    args.data = args.data or args.workbench
    args.provider = args.provider or args.data
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
    result = {'scope': 'original_managed_linux_package_install_process_pool' + ('_private_provider' if args.provider else '') + ('_private_data' if args.data else '') + ('_private_workbench' if args.workbench else ''), 'status': 'running',
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
        if args.provider:
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
        env.update(NYADB_DATABEND_MANAGED_IT='1', NYADB_DATABEND_MANAGED_FIXTURE=str(fixture_file))
        command = ['go', 'test', '-count=1', '-v', '-timeout=15m' if args.data else '-timeout=8m']
        if args.race:
            command.append('-race')
        pattern = '^TestDatabend(ManagedOriginal|ProviderOriginal|DataOriginal)' if args.data else '^TestDatabend(ManagedOriginal|ProviderOriginal)' if args.provider else '^TestDatabendManagedOriginal'
        command += ['-run', pattern, './internal/services/databend']
        if transport.run_diagnostics(command, env, base / 'go-test.log', timeout=1020 if args.data else 540):
            raise RuntimeError('Original managed diagnostics failed; inspect ' + str(base / 'go-test.log'))
        if args.provider:
            runtime_command = ['go', 'test', '-count=1', '-v', '-timeout=8m']
            if args.race:
                runtime_command.append('-race')
            runtime_command += ['-run', '^TestDatabendRuntimeOriginal', './internal/services/dbruntime']
            if transport.run_diagnostics(runtime_command, env, base / 'runtime-go-test.log'):
                raise RuntimeError('Original runtime diagnostics failed; inspect ' + str(base / 'runtime-go-test.log'))
            verify_named_acceptance((base / 'runtime-go-test.log').read_text(), RUNTIME_REQUIRED)
        if args.workbench:
            env['NYADB_DATABEND_WORKBENCH_IT'] = '1'
            shell_command = ['go', 'test', '-count=1', '-v', '-timeout=25m']
            if args.race:
                shell_command.append('-race')
            shell_command += ['-run', '^TestDatabendWorkbenchOriginal', './internal/ui/shell']
            if transport.run_diagnostics(shell_command, env, base / 'shell-go-test.log', timeout=1620):
                raise RuntimeError('Original shared workbench diagnostics failed; inspect ' + str(base / 'shell-go-test.log'))
            verify_named_acceptance((base / 'shell-go-test.log').read_text(), WORKBENCH_REQUIRED)
        required = REQUIRED + (PROVIDER_REQUIRED if args.provider else []) + (DATA_REQUIRED if args.data else [])
        verify_named_acceptance((base / 'go-test.log').read_text(), required)
        result['original_executor_receipts'] = verify_receipts(base)
        result['writes'] = verify_independent_writes(base)
        if args.provider:
            result['private_provider'] = verify_provider_receipts(base, database)
            result['private_runtime'] = verify_runtime_receipts(base)
        if args.data:
            result['private_data'] = verify_data_receipts(base)
        if args.workbench:
            result['private_workbench'] = verify_workbench_receipts(base, database)
        result['original_processes_exited'] = transport.verify_processes(base)
        if result['original_processes_exited'] != (38 if args.workbench else 19 if args.data else 15 if args.provider else 5):
            raise RuntimeError('Original independent process lifecycle coverage differs')
        if verify_build(original, dbx, cache) != pins or verify_build(reproduced, dbx, cache) != pins:
            raise RuntimeError('Original managed artifacts/sources changed during product verification')
        result.update(status='passed', required_diagnostics=required, original_package_unchanged=True,
                      original_source_unchanged=True, unverified=[
                          'native Windows/macOS packages and product execution',
                          'public URI/chooser/editor/provider/workspaces' if args.provider else 'public config persistence/URI/chooser/editor/provider/workspaces',
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
