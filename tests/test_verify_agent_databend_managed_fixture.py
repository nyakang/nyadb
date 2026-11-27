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
                       'wrong_bytes', 'query_id', 'old_generation', 'disposed_replacement', 'replayed_sql', 'disconnect_no_ack']:
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
                root = Path(directory)
                for filename, rows in [('workbench-read-lifecycle.jsonl', r), ('workbench-held-reads.jsonl', h),
                                       ('workbench-generation-lifecycle.jsonl', g),
                                       ('workbench-disconnect-lifecycle.jsonl', disconnect)]:
                    (root / filename).write_text(''.join(json.dumps(row) + '\n' for row in rows))
                if change == 'none':
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
