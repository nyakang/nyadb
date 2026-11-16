import base64
import copy
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
    def test_every_managed_case_must_pass_without_skip_or_substitution(self):
        output = '\n'.join('--- PASS: ' + name + ' (0.01s)' for name in FIXTURE.REQUIRED)
        FIXTURE.verify_named_acceptance(output)
        for line in output.splitlines():
            with self.subTest(line=line), self.assertRaises(RuntimeError):
                FIXTURE.verify_named_acceptance(output.replace(line, line.replace('PASS', 'SKIP')))
        with self.assertRaises(RuntimeError):
            FIXTURE.verify_named_acceptance('--- PASS: TestDatabendPublicProvider (0.01s)')

    def test_private_provider_and_runtime_require_their_actual_named_cases(self):
        names = FIXTURE.REQUIRED + FIXTURE.PROVIDER_REQUIRED + FIXTURE.RUNTIME_REQUIRED
        output = '\n'.join('--- PASS: ' + name + ' (0.01s)' for name in names)
        FIXTURE.verify_named_acceptance(output, names)
        for name in names:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                FIXTURE.verify_named_acceptance(output.replace('PASS: ' + name, 'SKIP: ' + name), names)

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
