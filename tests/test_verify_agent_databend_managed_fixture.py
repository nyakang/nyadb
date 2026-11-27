import base64
import copy
from decimal import Decimal
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock
import warnings
import zipfile

TOOLS = Path(__file__).resolve().parents[1] / 'tools'
SPEC = importlib.util.spec_from_file_location('databend_managed_fixture',
                                             TOOLS / 'verify_agent_databend_managed_fixture.py')
FIXTURE = importlib.util.module_from_spec(SPEC)
with mock.patch.object(sys, 'path', [str(TOOLS), *sys.path]):
    SPEC.loader.exec_module(FIXTURE)


class DatabendManagedDiagnosticTests(unittest.TestCase):
    def test_independent_write_cannot_replace_unknown_receipt(self):
        name, database = FIXTURE.WORKBENCH_REQUIRED[2], 'native_database'
        original = {'ProfileID': 7, 'Catalog': '', 'Schema': database, 'Kind': 'table', 'Name': 'workbench_unknown52'}
        other = {**original, 'Name': 'workbench_independent54'}
        unknown = {'Receipt': {'Object': original, 'Statements': [{'State': 'unknown'}]}}
        writes = [self.receipt(name, 'workbench-data-write', {
            'rows': [[1]], 'columns': ['number of rows inserted'], 'query_timings_ms': {'jdbc_execute': i}})
            for i in [1, 2, 54]]
        statement = {'Kind': 'insert', 'Destination': other, 'State': 'acknowledged', 'AffectedRows': 1,
                     'NativeJSON': json.dumps({'Receipt': writes[-1]['receipt'],
                                              'Original': json.loads(base64.b64decode(writes[-1]['native_bytes']))})}
        evidence = {'test': name, 'original_object': original, 'independent_object': other, 'original_writes': 2,
                    'original_receipt': unknown, 'independent_receipt': {'Receipt': {'Object': other, 'Statements': [statement]}},
                    'independent_rows': [['54']], 'unknown_receipt_unchanged': True, 'original_draft_retained': True}
        lifecycle = [{'mode': 'unknown', 'receipt': copy.deepcopy(unknown)}]
        sql = 'SELECT id FROM "' + database + '".workbench_independent54 ORDER BY id'
        for change in ['none', 'unlocked', 'lost_draft', 'changed_original', 'foreign_document', 'replayed', 'readback', 'wrong_executor']:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                row, records = copy.deepcopy((evidence, writes))
                http = {(name, sql): [['54']]}
                if change == 'unlocked': row['unknown_receipt_unchanged'] = False
                elif change == 'lost_draft': row['original_draft_retained'] = False
                elif change == 'changed_original': row['original_receipt']['Receipt']['Statements'][0]['State'] = 'acknowledged'
                elif change == 'foreign_document': row['independent_object']['ProfileID'] = 8
                elif change == 'replayed': records.append(copy.deepcopy(records[0]))
                elif change == 'readback': http[(name, sql)] = [['55']]
                elif change == 'wrong_executor': records[-1] = records[0]
                root = Path(directory)
                (root / 'workbench-independent-write.jsonl').write_text(json.dumps(row) + '\n')
                if change == 'none':
                    FIXTURE.verify_workbench_independent_write(root, database, records, http, lifecycle)
                else:
                    with self.assertRaises(RuntimeError):
                        FIXTURE.verify_workbench_independent_write(root, database, records, http, lifecycle)

    def test_write_faults_require_original_counts_incarnations_and_unsent_suffix(self):
        database = 'native_database'
        faults, native, incarnations, records, independent = [], [], [], [], {}
        cases = [(FIXTURE.WORKBENCH_REQUIRED[7], 'zero_guard', 'workbench_zero54', 1, 2, 0)]
        for mode in ['insert', 'update', 'prefix']:
            cases.append((FIXTURE.WORKBENCH_REQUIRED[8] + '/' + mode, 'recreate_' + mode,
                          'workbench_recreate_' + mode + '54', int(mode == 'prefix'), 1 if mode == 'insert' else 2, 1))
        for mode in ['cancel', 'disconnect']:
            cases.append((FIXTURE.WORKBENCH_REQUIRED[9] + '/' + mode, mode, 'workbench_write_' + mode + '54', 1, 2, 1))
        for name, mode, table, sent, total, affected in cases:
            obj = {'ProfileID': 1, 'Catalog': '', 'Schema': database, 'Kind': 'table', 'Name': table}
            plan = {'Object': obj, 'Metadata': 'a' * 64, 'Incarnation': '100',
                    'Statements': [{'SQL': f'UPDATE "{database}"."{table}" SET id={i}'} for i in range(total)]}
            statements = []
            for i in range(total):
                item = {'EditIndex': i, 'Kind': 'insert' if mode == 'recreate_insert' else 'update',
                        'Destination': obj, 'State': 'acknowledged' if i < sent else 'not_sent',
                        'AffectedRows': affected if i < sent else 0, 'NativeJSON': ''}
                if i < sent:
                    record = self.receipt(name, 'workbench-data-write',
                                          {'columns': ['number of rows updated'], 'rows': [[affected]],
                                           'column_types': ['uint64'], 'affected_rows': 0, 'truncated': False},
                                          canceled=mode in ['cancel', 'disconnect'])
                    records.append(record)
                    item['NativeJSON'] = json.dumps({'Receipt': record['receipt'],
                                                     'Original': json.loads(base64.b64decode(record['native_bytes']))})
                    query_id = f'{len(native)+1:032x}'
                    raw = json.dumps({'id': query_id, 'data': [[str(affected)]], 'error': None}).encode()
                    native.append({'test': name, 'sql': plan['Statements'][i]['SQL'], 'query_id': query_id,
                                   'status': 200, 'native_bytes': base64.b64encode(raw).decode(),
                                   'native_sha256': FIXTURE.transport.sha(raw)})
                statements.append(item)
            rows = ([['1', 'first'], ['1', 'first'], ['2', 'second']] if mode == 'zero_guard' else
                    [['1', 'changed'], ['2', 'second']] if mode in ['cancel', 'disconnect'] else
                    [['1', 'first'], ['2', 'second']])
            faults.append({'test': name, 'mode': mode, 'preview': plan,
                           'receipt': {'Request': copy.deepcopy(plan), 'Receipt': {'Object': obj, 'Statements': statements}},
                           'writes': sent, 'rows': rows, 'draft_retained': True, 'cache_preserved': True, 'write_blocked': True,
                           'original_generation': 2, 'current_generation': 2, 'waited_for_original': True, 'disconnect_guarded': True})
            sql = f'SELECT id,txt FROM "{database}".{table} ORDER BY id' + (',txt' if mode == 'zero_guard' else '')
            independent[(name, sql)] = rows
            if mode.startswith('recreate_'):
                ddl, row_ids = [[table, 'CREATE TABLE same_definition']], [['1', '0'], ['2', '1']]
                incarnations.append({'test': name, 'original': [['100']], 'replacement': [['200']],
                                     'original_ddl': ddl, 'replacement_ddl': copy.deepcopy(ddl),
                                     'original_rows': row_ids, 'replacement_rows': copy.deepcopy(row_ids)})
                independent[(name, f"SELECT table_id FROM system.tables WHERE database='{database}' AND name='{table}'")] = [['100'], ['200']]
                independent[(name, f'SHOW CREATE TABLE "{database}".{table}')] = ddl + ddl
                independent[(name, f'SELECT id,_row_id FROM "{database}".{table} ORDER BY id')] = row_ids + row_ids
        for change in ['none', 'zero_becomes_one', 'sent_suffix', 'missing_incarnation', 'same_incarnation',
                       'different_ddl', 'identity_http', 'request', 'no_ack', 'no_cancel', 'process_exit',
                       'changed_native_bytes', 'changed_sql', 'duplicate_query', 'draft_lost', 'scope_missing', 'replayed']:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                f, n, inc, r, http = copy.deepcopy((faults, native, incarnations, records, independent))
                if change == 'zero_becomes_one': f[0]['receipt']['Receipt']['Statements'][0]['AffectedRows'] = 1
                elif change == 'sent_suffix': f[0]['receipt']['Receipt']['Statements'][1]['State'] = 'acknowledged'
                elif change == 'missing_incarnation': f[0]['preview']['Incarnation'] = ''
                elif change == 'same_incarnation': inc[0]['replacement'] = inc[0]['original']
                elif change == 'different_ddl': inc[0]['replacement_ddl'][0][1] += ' changed'
                elif change == 'identity_http':
                    key = next(k for k in http if k[1].startswith('SELECT table_id'))
                    http[key] = [['100'], ['201']]
                elif change == 'request': f[0]['receipt']['Request']['Metadata'] = 'b' * 64
                elif change == 'no_ack': r[0]['receipt']['ExecutorAcknowledged'] = False
                elif change == 'no_cancel': r[-1]['receipt']['CancellationRequested'] = False
                elif change == 'process_exit': r[0]['receipt']['ProcessExitConfirmed'] = True
                elif change == 'changed_native_bytes': n[0]['native_sha256'] = '0' * 64
                elif change == 'changed_sql': n[0]['sql'] += ' changed'
                elif change == 'duplicate_query': n[-1]['query_id'] = n[0]['query_id']
                elif change == 'draft_lost': f[0]['draft_retained'] = False
                elif change == 'scope_missing': f.pop()
                elif change == 'replayed': r.append(copy.deepcopy(r[0]))
                root = Path(directory)
                for name, rows in [('workbench-write-faults.jsonl', f), ('workbench-native-writes.jsonl', n),
                                   ('workbench-table-incarnations.jsonl', inc)]:
                    (root / name).write_text(''.join(json.dumps(row) + '\n' for row in rows))
                if change == 'none':
                    FIXTURE.verify_workbench_write_faults(root, database, r, http)
                else:
                    with self.assertRaises(RuntimeError):
                        FIXTURE.verify_workbench_write_faults(root, database, r, http)

    def test_read_lifecycle_rejects_wrong_owner_replay_and_unacknowledged_cancellation(self):
        database = 'original_database'
        reads, held, records = [], [], []
        for i, scope in enumerate(['page', 'count', 'field']):
            name = FIXTURE.WORKBENCH_REQUIRED[4] + '/' + scope
            reads.append({'test': name, 'scope': scope, 'object': {
                'Schema': database, 'Name': 'workbench_read_' + scope + '53'},
                'count': 2 if scope == 'count' else 1, 'matched_requests': 2 if scope == 'field' else 3,
                'cache_preserved': True, 'independent_read_survived': True,
                'late_result_rejected': True, 'explicit_refresh': True, 'public_provider_registered': False})
            record = self.receipt(name, 'workbench-data-cursor', {'rows': []})
            record['receipt']['CancellationRequested'] = True
            records.append(record)
        record = self.receipt(FIXTURE.WORKBENCH_REQUIRED[6], 'workbench-data-cursor', {'rows': []})
        record['receipt']['CancellationRequested'] = True
        records.append(record)
        disconnects = [{'test': FIXTURE.WORKBENCH_REQUIRED[6], 'database': database,
                        'matched_requests': 2, 'rows': [['1']], 'recovery_replayed': False,
                        'public_provider_registered': False, 'original_generation': 1,
                        'replacement_generation': 2, 'sql_text_retained': True,
                        'original_executor_acknowledged': True, 'data_disposed': True}]
        for i, (name, scope) in enumerate([(r['test'], r['scope']) for r in reads] +
                                          [(FIXTURE.WORKBENCH_REQUIRED[n], 'page') for n in [5, 6]]):
            query_id = f'{i+1:032x}'
            raw = json.dumps({'id': query_id, 'data': [['1']], 'error': None}).encode()
            held.append({'test': name, 'scope': scope, 'sql': 'SELECT * FROM "' + database + '".t',
                         'query_id': query_id, 'status': 200, 'native_bytes': base64.b64encode(raw).decode(),
                         'native_sha256': FIXTURE.transport.sha(raw)})
        generations = [{'test': FIXTURE.WORKBENCH_REQUIRED[5], 'object': {
            'Schema': database, 'Name': 'workbench_generation53'}, 'replacement_rows': 2,
            'original_generation': 1, 'replacement_generation': 2, 'old_cache_preserved': True,
            'old_document_revoked': True, 'replacement_survived_old_close': True,
            'disconnect_disposed_data': True, 'public_provider_registered': False}]
        for change in ['none', 'owner', 'replay', 'missing_scope', 'duplicate', 'no_ack', 'exit',
                       'wrong_bytes', 'query_id', 'old_generation', 'disposed_replacement', 'replayed_sql', 'disconnect_no_ack',
                       'background_ack', 'background_no_ack', 'background_replaces_cursor']:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                r, h, g, evidence = copy.deepcopy((reads, held, generations, records))
                disconnect = copy.deepcopy(disconnects)
                if change == 'owner': r[0]['object']['Schema'] = 'other_database'
                elif change == 'replay': r[0]['matched_requests'] += 1
                elif change == 'missing_scope': r.pop()
                elif change == 'duplicate': h[-1] = copy.deepcopy(h[0])
                elif change == 'no_ack': evidence[0]['receipt']['ExecutorAcknowledged'] = False
                elif change == 'exit': evidence[0]['receipt']['ProcessExitConfirmed'] = True
                elif change == 'wrong_bytes': h[0]['native_sha256'] = '0' * 64
                elif change == 'query_id': h[0]['query_id'] = 'f' * 32
                elif change == 'old_generation': g[0]['replacement_generation'] = 1
                elif change == 'disposed_replacement': g[0]['replacement_survived_old_close'] = False
                elif change == 'replayed_sql': disconnect[0]['rows'].append(['999'])
                elif change == 'disconnect_no_ack': evidence[-1]['receipt']['ExecutorAcknowledged'] = False
                elif change in ['background_ack', 'background_no_ack', 'background_replaces_cursor']:
                    background = self.receipt(FIXTURE.WORKBENCH_REQUIRED[6], 'workbench-metadata-query',
                                              {'columns': ['Field'], 'rows': [['id']]}, canceled=True)
                    if change == 'background_no_ack': background['receipt']['ExecutorAcknowledged'] = False
                    if change == 'background_replaces_cursor': evidence.pop()
                    evidence.append(background)
                root = Path(directory)
                for filename, rows in [('workbench-read-lifecycle.jsonl', r), ('workbench-held-reads.jsonl', h),
                                       ('workbench-generation-lifecycle.jsonl', g),
                                       ('workbench-disconnect-lifecycle.jsonl', disconnect)]:
                    (root / filename).write_text(''.join(json.dumps(row) + '\n' for row in rows))
                if change in ['none', 'background_ack']:
                    FIXTURE.verify_workbench_read_lifecycle(root, database, evidence)
                else:
                    with self.assertRaises(RuntimeError):
                        FIXTURE.verify_workbench_read_lifecycle(root, database, evidence)

    def test_copied_workbench_receipt_preserves_complete_original_response_and_exact_timings(self):
        wire = self.receipt(FIXTURE.WORKBENCH_REQUIRED[0], 'workbench-data-write',
                            {'rows': [[1]], 'query_timings_ms': {'jdbc_execute': 0.558238}})
        executor = json.loads(json.dumps(wire), parse_float=Decimal)
        native = {'Receipt': wire['receipt'],
                  'Original': json.loads(base64.b64decode(wire['native_bytes']))}
        FIXTURE.verify_workbench_statement(executor, {'NativeJSON': json.dumps(native)})
        for change in ['missing', 'result', 'original', 'acknowledgement']:
            with self.subTest(change=change), self.assertRaises(RuntimeError):
                altered = copy.deepcopy(native)
                if change == 'missing':
                    altered.pop('Original')
                elif change == 'result':
                    altered['Receipt']['Result']['rows'] = [[2]]
                elif change == 'original':
                    altered['Original']['result']['query_timings_ms']['jdbc_execute'] = 0.558239
                else:
                    altered['Receipt']['ExecutorAcknowledged'] = False
                FIXTURE.verify_workbench_statement(executor, {'NativeJSON': json.dumps(altered)})

    def test_every_managed_case_must_pass_without_skip_or_substitution(self):
        output = '\n'.join('--- PASS: ' + name + ' (0.01s)' for name in FIXTURE.REQUIRED)
        FIXTURE.verify_named_acceptance(output)
        for line in output.splitlines():
            with self.subTest(line=line), self.assertRaises(RuntimeError):
                FIXTURE.verify_named_acceptance(output.replace(line, line.replace('PASS', 'SKIP')))
        with self.assertRaises(RuntimeError):
            FIXTURE.verify_named_acceptance('--- PASS: TestDatabendPublicProvider (0.01s)')

    def test_private_provider_and_runtime_require_their_actual_named_cases(self):
        names = FIXTURE.REQUIRED + FIXTURE.PROVIDER_REQUIRED + FIXTURE.RUNTIME_REQUIRED + FIXTURE.DATA_REQUIRED + FIXTURE.WORKBENCH_REQUIRED
        output = '\n'.join('--- PASS: ' + name + ' (0.01s)' for name in names)
        FIXTURE.verify_named_acceptance(output, names)
        for name in names:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                FIXTURE.verify_named_acceptance(output.replace('PASS: ' + name, 'SKIP: ' + name), names)

    def test_diagnostic_budget_preserves_original_group_cleanup_acknowledgement(self):
        for timeout in [None, 1020]:
            with self.subTest(timeout=timeout), tempfile.TemporaryDirectory() as directory:
                process = mock.Mock(pid=731)
                process.wait.side_effect = [FIXTURE.transport.subprocess.TimeoutExpired(['go', 'test'], 1),
                                            FIXTURE.transport.subprocess.TimeoutExpired(['go', 'test'], 10), 0]
                process.poll.return_value = None
                with mock.patch.object(FIXTURE.transport.subprocess, 'Popen', return_value=process) as opened, \
                        mock.patch.object(FIXTURE.transport.os, 'killpg') as killed, \
                        self.assertRaises(FIXTURE.transport.subprocess.TimeoutExpired):
                    options = {} if timeout is None else {'timeout': timeout}
                    FIXTURE.transport.run_diagnostics(['go', 'test'], {}, Path(directory) / 'log', **options)
                self.assertTrue(opened.call_args.kwargs['start_new_session'])
                self.assertEqual(process.wait.call_args_list,
                                 [mock.call(timeout=540 if timeout is None else timeout), mock.call(timeout=10), mock.call()])
                self.assertEqual(killed.call_args_list,
                                 [mock.call(731, FIXTURE.transport.signal.SIGTERM), mock.call(731, FIXTURE.transport.signal.SIGKILL)])

    def test_archive_requires_exact_complete_bytes_order_modes_and_timestamps(self):
        for change in ['none', 'bytes', 'missing', 'duplicate', 'mode', 'time', 'order']:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                for name in ['manifest.json', 'java', 'LICENSE']:
                    (root / name).write_bytes(name.encode())
                manifest = {'files': [{'path': 'java', 'executable': True},
                                      {'path': 'LICENSE', 'executable': False}]}
                names = ['LICENSE', 'java', 'manifest.json']
                if change == 'missing':
                    names.pop()
                if change == 'duplicate':
                    names.append('java')
                if change == 'order':
                    names.reverse()
                archive = root / 'package.zip'
                with zipfile.ZipFile(archive, 'w') as z:
                    for name in names:
                        entry = zipfile.ZipInfo(name, FIXTURE.builder.EPOCH)
                        entry.create_system = 3
                        entry.external_attr = (0o100755 if name == 'java' else 0o100644) << 16
                        if name == 'java' and change == 'mode':
                            entry.external_attr = 0o100644 << 16
                        if change == 'time':
                            entry.date_time = (2025, 1, 1, 0, 0, 0)
                        with warnings.catch_warnings():
                            warnings.simplefilter('ignore', UserWarning)
                            z.writestr(entry, b'foreign' if name == 'java' and change == 'bytes' else name.encode())
                if change == 'none':
                    FIXTURE.verify_archive(archive, root, manifest)
                else:
                    with self.assertRaises(RuntimeError):
                        FIXTURE.verify_archive(archive, root, manifest)

    def receipt(self, name, method, result, canceled=False):
        raw = json.dumps({'jsonrpc': '2.0', 'id': 7, 'result': result}).encode()
        return {'test': name, 'method': method, 'native_bytes': base64.b64encode(raw).decode(),
                'native_sha256': FIXTURE.transport.sha(raw), 'receipt': {
                    'Dispatched': True, 'ExecutorAcknowledged': True, 'CancellationRequested': canceled,
                    'ProcessExitConfirmed': False, 'Result': result, 'RemoteError': None}}

    def test_packaged_source_and_legal_claims_must_match_fixed_builder_inputs(self):
        pins = json.loads(FIXTURE.builder.PINS.read_text())
        compiled = sorted(i['path'] for i in pins['dbx_source_files']
                          if '/src/main/java/' in i['path'] and i['path'].endswith('.java'))
        source = {'dbx_revision': pins['dbx_revision'], 'source_files': pins['dbx_source_files'],
                  'compiled_source_files': compiled, 'dependencies': pins['files'],
                  'components': pins['components'], 'builder_sha256': FIXTURE.builder.sha(Path(FIXTURE.builder.__file__)),
                  'pins_sha256': FIXTURE.builder.sha(FIXTURE.builder.PINS),
                  'public_provider_registered': False, 'original_driver_modified': False}
        for change in ['revision', 'source', 'compiled', 'dependency', 'legal', 'builder', 'public', 'modified']:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                package = root / 'package'
                (package / 'provenance').mkdir(parents=True)
                (package / 'provenance/pins.json').write_text(json.dumps(pins))
                altered = copy.deepcopy(source)
                if change == 'revision':
                    altered['dbx_revision'] = 'foreign'
                elif change == 'source':
                    altered['source_files'].pop()
                elif change == 'compiled':
                    altered['compiled_source_files'].append('agents/common/src/test/java/Foreign.java')
                elif change == 'dependency':
                    altered['dependencies'][0]['sha256'] = '0' * 64
                elif change == 'legal':
                    altered['components'][0]['embedded_legal'] = []
                elif change == 'builder':
                    altered['builder_sha256'] = '0' * 64
                elif change == 'public':
                    altered['public_provider_registered'] = True
                else:
                    altered['original_driver_modified'] = True
                (package / 'sources.json').write_text(json.dumps(altered))
                with mock.patch.object(FIXTURE.builder, 'verify_package', return_value={'provenance': 'sources.json'}), \
                        mock.patch.object(FIXTURE.builder, 'verify_sources'), \
                        self.assertRaisesRegex(RuntimeError, 'source/compiler/dependency provenance'):
                    FIXTURE.verify_build(root, root / 'dbx', root / 'cache')

    def test_managed_receipts_require_cursor_pages_and_original_late_executor(self):
        originals = []
        for name in FIXTURE.REQUIRED:
            for method in ['managed-handshake', 'managed-open-initialize', 'managed-query']:
                originals.append(self.receipt(name, method, {'ok': True}))
            originals.append(self.receipt(name, 'managed-query', {
                'columns': ['number of rows inserted'], 'column_types': ['uint64'],
                'rows': [[1]], 'affected_rows': 0, 'truncated': False}))
        for size in [100, 100, 5]:
            originals.append(self.receipt(FIXTURE.REQUIRED[0], 'managed-cursor', {'rows': [[1]] * size}))
        for _ in range(2):
            originals.append(self.receipt(FIXTURE.REQUIRED[0], 'managed-query',
                                          {'columns': ['value'], 'rows': [['Asia/Shanghai']]}))
        originals.append(self.receipt(FIXTURE.REQUIRED[1], 'managed-late-open',
                                      {'ok': True, 'exactMetadataIdentity': False}, True))
        for change in ['none', 'missing-page', 'ack', 'control', 'cancel', 'bytes', 'owner', 'renamed-control', 'count', 'timezone']:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                records = copy.deepcopy(originals)
                if change == 'missing-page':
                    records.pop(-4)
                elif change == 'ack':
                    records[-1]['receipt']['ExecutorAcknowledged'] = False
                elif change == 'control':
                    records[-1]['method'] = 'cancel_session'
                elif change == 'cancel':
                    records[-1]['receipt']['CancellationRequested'] = False
                elif change == 'bytes':
                    records[-1]['native_sha256'] = '0' * 64
                elif change == 'owner':
                    records[-1]['test'] = 'replacement'
                elif change == 'renamed-control':
                    records[-1] = self.receipt(FIXTURE.REQUIRED[1], 'managed-late-open', {'ok': True}, True)
                elif change == 'count':
                    records[3] = self.receipt(FIXTURE.REQUIRED[0], 'managed-query', {
                        'columns': ['number of rows inserted'], 'column_types': ['uint64'],
                        'rows': [[0]], 'affected_rows': 0, 'truncated': False})
                elif change == 'timezone':
                    records[-2] = self.receipt(FIXTURE.REQUIRED[0], 'managed-query',
                                               {'columns': ['value'], 'rows': [['UTC']]})
                base = Path(directory)
                (base / 'original-receipts.jsonl').write_text('\n'.join(map(json.dumps, records)))
                if change == 'none':
                    self.assertEqual(FIXTURE.verify_receipts(base), len(originals))
                else:
                    with self.assertRaises(RuntimeError):
                        FIXTURE.verify_receipts(base)

    def test_independent_native_reads_reject_replay_missing_owners_and_forged_bytes(self):
        records = []
        for name, identifier in zip(FIXTURE.REQUIRED, [17, 29]):
            raw = json.dumps({'data': [['1']], 'error': None}).encode()
            records.append({'test': name, 'sql': 'SELECT count() FROM "nyadb_transport_' + 'a' * 16 +
                            '".managed_writes WHERE id=' + str(identifier),
                            'native_bytes': base64.b64encode(raw).decode(), 'native_sha256': FIXTURE.transport.sha(raw)})
            final = json.dumps({'data': [], 'error': None}).encode()
            records.append(dict(records[-1], native_bytes=base64.b64encode(final).decode(),
                                native_sha256=FIXTURE.transport.sha(final)))
        for change in ['none', 'missing', 'replay', 'bytes', 'identity']:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                native = copy.deepcopy(records)
                if change == 'missing':
                    native = native[:2]
                elif change == 'replay':
                    raw = json.dumps({'data': [['2']], 'error': None}).encode()
                    native[-1].update(native_bytes=base64.b64encode(raw).decode(), native_sha256=FIXTURE.transport.sha(raw))
                elif change == 'bytes':
                    native[-1]['native_sha256'] = '0' * 64
                elif change == 'identity':
                    native[-1]['sql'] = native[-1]['sql'].replace('id=29', 'id=17')
                base = Path(directory)
                (base / 'independent-http-receipts.jsonl').write_text('\n'.join(map(json.dumps, native)))
                if change == 'none':
                    self.assertFalse(FIXTURE.verify_independent_writes(base)['recovery_replayed'])
                else:
                    with self.assertRaises(RuntimeError):
                        FIXTURE.verify_independent_writes(base)


if __name__ == '__main__':
    unittest.main()
