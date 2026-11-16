import base64
import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location('cassandra_agent_fixture', Path(__file__).resolve().parents[1] / 'tools/verify_agent_cassandra_fixture.py')
FIXTURE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FIXTURE)


class CassandraAgentAcceptanceTests(unittest.TestCase):
    def pass_output(self, names):
        return '\n'.join('--- PASS: ' + name + ' (0.01s)' for name in names)

    def test_managed_gate_rejects_local_diagnostics_original_tests_and_skips(self):
        for names in [FIXTURE.LOCAL_REQUIRED, FIXTURE.ORIGINAL_REQUIRED]:
            with self.subTest(names=names), self.assertRaises(RuntimeError):
                FIXTURE.verify_acceptance(self.pass_output(names))
        output = self.pass_output(FIXTURE.REQUIRED)
        FIXTURE.verify_acceptance(output)
        for name in FIXTURE.REQUIRED:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                FIXTURE.verify_acceptance(output.replace('--- PASS: ' + name, '--- SKIP: ' + name))

    def test_original_cursor_gate_requires_both_actual_source_tests(self):
        output = self.pass_output(FIXTURE.ORIGINAL_REQUIRED)
        FIXTURE.verify_acceptance(output, FIXTURE.ORIGINAL_REQUIRED)
        for name in FIXTURE.ORIGINAL_REQUIRED:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                FIXTURE.verify_acceptance(output.replace('--- PASS: ' + name, '--- SKIP: ' + name), FIXTURE.ORIGINAL_REQUIRED)

    def test_packaging_does_not_accept_missing_compiled_module_licenses(self):
        FIXTURE.require_module_licenses([])
        missing = [{'path': 'github.com/datastax/astra-client-go/v2', 'version': 'v2.2.54'}]
        with self.assertRaisesRegex(RuntimeError, 'managed package acceptance blocked'):
            FIXTURE.require_module_licenses(missing)
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(FIXTURE, 'build_original', return_value=({}, 'version', missing)):
            with self.assertRaises(RuntimeError):
                FIXTURE.build_bundle(Path(directory), {})
            self.assertEqual(list(Path(directory).iterdir()), [])

    def receipt(self, error=False):
        original = {'jsonrpc': '2.0', 'id': 7}
        original['error' if error else 'result'] = {'code': -1, 'message': 'original executor canceled'} if error else {'rows': [['9007199254740993', None]], 'has_more': False}
        raw = json.dumps(original).encode()
        receipt = {'Dispatched': True, 'ExecutorAcknowledged': True, 'CancellationRequested': error,
                   'Result': None if error else original['result'], 'RemoteError': {'Code': -1} if error else None}
        return {'receipt': receipt, 'native_bytes': base64.b64encode(raw).decode(), 'native_sha256': FIXTURE.digest(raw)}

    def test_original_receipt_rejects_changed_bytes_acknowledgement_and_projections(self):
        record = self.receipt()
        FIXTURE.verify_receipt(record)
        FIXTURE.verify_receipt(self.receipt(error=True))
        for change in ['bytes', 'ack', 'dispatched', 'result', 'error', 'contradictory', 'missing-result', 'id']:
            with self.subTest(change=change):
                invalid = self.receipt(error=change == 'error')
                native = json.loads(base64.b64decode(invalid['native_bytes']))
                if change == 'bytes':
                    invalid['native_bytes'] = base64.b64encode(b'{}').decode()
                elif change in ['ack', 'dispatched']:
                    invalid['receipt']['ExecutorAcknowledged' if change == 'ack' else 'Dispatched'] = False
                elif change == 'result':
                    invalid['receipt']['Result'] = {'rows': [['rounded', None]]}
                elif change == 'error':
                    invalid['receipt']['RemoteError']['Code'] = 99
                else:
                    if change == 'contradictory':
                        native['error'] = {'code': -1}
                    elif change == 'missing-result':
                        del native['result']
                    else:
                        native['id'] = 0
                    raw = json.dumps(native).encode()
                    invalid['native_bytes'], invalid['native_sha256'] = base64.b64encode(raw).decode(), FIXTURE.digest(raw)
                with self.assertRaises(RuntimeError):
                    FIXTURE.verify_receipt(invalid)

    def evidence(self, base):
        syntax = self.receipt(error=True)
        raw = json.loads(base64.b64decode(syntax['native_bytes']))
        raw['error']['data'] = {'sqlState': '0x2000'}
        raw = json.dumps(raw).encode()
        syntax['native_bytes'], syntax['native_sha256'] = base64.b64encode(raw).decode(), FIXTURE.digest(raw)
        syntax['receipt']['RemoteError'].update(NativeCode='0x2000', SQLState='')
        values = {'opening': [self.receipt() for _ in range(3)], 'query_receipt': self.receipt(),
                  'syntax_receipt': syntax,
                  'metadata_receipt': self.receipt(), 'page_receipts': [self.receipt() for _ in range(3)],
                  'bounded_receipt': self.receipt(), 'pages': [{'SQL': {'Sets': [{'Rows': [[] for _ in range(n)]}]}} for n in [100, 100, 5]],
                  'bounded_terminal': {'SQL': {'Truncated': True}},
                  'empty_page': {'HasMore': False, 'SQL': {'Sets': [{'Rows': []}]}},
                  'empty_receipt': self.receipt(), 'early_close_acknowledged': True}
        cancellation = {'original_receipt': self.receipt(error=True), 'original_pid': 41, 'independent_pid': 42}
        (base / 'managed-values-cursors.json').write_text(json.dumps(values))
        (base / 'managed-cancellation.json').write_text(json.dumps(cancellation))
        return values, cancellation

    def test_cancellation_and_paging_require_original_processes_and_complete_receipts(self):
        for change in ['none', 'same-pid', 'no-cancel', 'not-error', 'page', 'truncation', 'opening', 'page-receipt', 'empty', 'early-close', 'native-code']:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                base = Path(directory)
                values, cancellation = self.evidence(base)
                if change == 'same-pid':
                    cancellation['independent_pid'] = cancellation['original_pid']
                elif change == 'no-cancel':
                    cancellation['original_receipt']['receipt']['CancellationRequested'] = False
                elif change == 'not-error':
                    cancellation['original_receipt'] = self.receipt()
                    cancellation['original_receipt']['receipt']['CancellationRequested'] = True
                elif change == 'page':
                    values['pages'][-1]['SQL']['Sets'][0]['Rows'].pop()
                elif change == 'truncation':
                    values['bounded_terminal']['SQL']['Truncated'] = False
                elif change == 'opening':
                    values['opening'].pop()
                elif change == 'page-receipt':
                    values['page_receipts'].pop()
                elif change == 'empty':
                    values['empty_page']['HasMore'] = True
                elif change == 'early-close':
                    values['early_close_acknowledged'] = False
                elif change == 'native-code':
                    values['syntax_receipt']['receipt']['RemoteError']['SQLState'] = '0x2000'
                (base / 'managed-values-cursors.json').write_text(json.dumps(values))
                (base / 'managed-cancellation.json').write_text(json.dumps(cancellation))
                if change == 'none':
                    self.assertEqual(FIXTURE.verify_receipts(base), 12)
                else:
                    with self.assertRaises(RuntimeError):
                        FIXTURE.verify_receipts(base)

    def test_cleanup_checks_original_ownership_and_verifies_anonymous_volume_removal(self):
        inspected = {'Id': 'original', 'Config': {'Labels': {'nyadb.cassandra.agent.fixture': 'owned'}},
                     'Mounts': [{'Type': 'volume', 'Name': 'original-volume'}]}
        for change in ['none', 'identity', 'label', 'volume']:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                invalid = copy.deepcopy(inspected)
                if change == 'identity':
                    invalid['Id'] = 'replacement'
                elif change == 'label':
                    invalid['Config']['Labels']['nyadb.cassandra.agent.fixture'] = 'foreign'
                responses = [json.dumps([invalid]).encode(), b'original logs', b'original', b'original-volume\n' if change == 'volume' else b'foreign-volume\n']
                with mock.patch.object(FIXTURE, 'run', side_effect=responses) as run:
                    if change == 'none':
                        self.assertEqual(FIXTURE.cleanup_owned_container(Path(directory), 'original', 'owned'), 1)
                        self.assertIn(mock.call(['docker', 'rm', '-f', '-v', 'original']), run.call_args_list)
                    else:
                        with self.assertRaises(RuntimeError):
                            FIXTURE.cleanup_owned_container(Path(directory), 'original', 'owned')
                        if change != 'volume':
                            run.assert_called_once_with(['docker', 'inspect', 'original'])


if __name__ == '__main__':
    unittest.main()
