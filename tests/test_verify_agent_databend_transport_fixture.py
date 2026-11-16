import base64
import copy
from decimal import Decimal
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location(
    'databend_transport_fixture',
    Path(__file__).resolve().parents[1] / 'tools/verify_agent_databend_transport_fixture.py')
FIXTURE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FIXTURE)


class DatabendTransportDiagnosticTests(unittest.TestCase):
    def output(self):
        names = FIXTURE.REQUIRED + [
            'TestDatabendTransportLocalOriginalTLSGatewayAndAuthentication/' + name
            for name in ['untrusted-certificate', 'wrong-hostname', 'wrong-password']]
        return '\n'.join('--- PASS: ' + name + ' (0.01s)' for name in names)

    def test_diagnostics_require_every_named_case_and_reject_skips(self):
        output = self.output()
        FIXTURE.verify_named_acceptance(output)
        for line in output.splitlines():
            with self.subTest(line=line), self.assertRaises(RuntimeError):
                FIXTURE.verify_named_acceptance(output.replace(line, line.replace('PASS', 'SKIP')))
        with self.assertRaises(RuntimeError):
            FIXTURE.verify_named_acceptance('--- PASS: TestDatabendPublicProvider (0.01s)')

    def receipt(self, error=False):
        raw = (b'{"jsonrpc":"2.0","id":7,"error":{"code":-1,"message":"original executor error"}}'
               if error else
               b'{"jsonrpc":"2.0","id":7,"result":{"rows":[[9007199254740993,9007199254740993.12345678901234567890,null]]}}')
        envelope = json.loads(raw, parse_float=Decimal)
        return {'test': FIXTURE.REQUIRED[0], 'native_bytes': base64.b64encode(raw).decode(),
                'native_sha256': FIXTURE.sha(raw), 'receipt': {
                    'Dispatched': True, 'ExecutorAcknowledged': True,
                    'Result': None if error else envelope['result'],
                    'RemoteError': {'Code': -1} if error else None}}

    def test_original_decimal_binary_and_null_envelopes_cannot_be_projected_or_changed(self):
        FIXTURE.verify_receipt(self.receipt())
        FIXTURE.verify_receipt(self.receipt(error=True))
        for change in ['bytes', 'ack', 'dispatched', 'decimal', 'null', 'id', 'missing-result', 'contradictory', 'error']:
            with self.subTest(change=change):
                record = self.receipt(error=change == 'error')
                if change == 'bytes':
                    record['native_bytes'] = base64.b64encode(b'{}').decode()
                elif change == 'ack':
                    record['receipt']['ExecutorAcknowledged'] = False
                elif change == 'dispatched':
                    record['receipt']['Dispatched'] = False
                elif change == 'decimal':
                    record['receipt']['Result']['rows'][0][1] = 9007199254740994.0
                elif change == 'null':
                    record['receipt']['Result']['rows'][0][2] = 'NULL'
                elif change == 'error':
                    record['receipt']['RemoteError']['Code'] = 99
                else:
                    native = json.loads(base64.b64decode(record['native_bytes']))
                    if change == 'id':
                        native['id'] = 0
                    elif change == 'missing-result':
                        del native['result']
                    else:
                        native['error'] = {'code': -1}
                    raw = json.dumps(native).encode()
                    record['native_bytes'], record['native_sha256'] = base64.b64encode(raw).decode(), FIXTURE.sha(raw)
                with self.assertRaises(RuntimeError):
                    FIXTURE.verify_receipt(record)

    def test_complete_original_receipts_require_all_diagnostic_owners(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'original-receipts.jsonl'
            records = []
            # Construct directly from the raw envelope; Decimal must not pass
            # through ordinary JSON's floating point projection.
            for name in FIXTURE.REQUIRED:
                record = self.receipt()
                text = '{"test":' + json.dumps(name) + ',"native_bytes":' + json.dumps(record['native_bytes'])
                text += ',"native_sha256":' + json.dumps(record['native_sha256'])
                text += ',"receipt":{"Dispatched":true,"ExecutorAcknowledged":true,"RemoteError":null,"Result":'
                text += '{"rows":[[9007199254740993,9007199254740993.12345678901234567890,null]]}}}'
                records.append(text)
            path.write_text('\n'.join(records))
            self.assertEqual(FIXTURE.verify_receipts(Path(directory)), len(FIXTURE.REQUIRED))
            path.write_text('\n'.join(records[:-1]))
            with self.assertRaises(RuntimeError):
                FIXTURE.verify_receipts(Path(directory))

    def test_cleanup_checks_original_ownership_and_redacts_bootstrap_secrets(self):
        observed = {'Id': 'original', 'Config': {'Labels': {'nyadb.databend.transport': 'owned'}},
                    'Mounts': [{'Type': 'volume', 'Name': 'original-volume'}]}
        for change in ['none', 'id', 'label', 'volume']:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                invalid = copy.deepcopy(observed)
                if change == 'id':
                    invalid['Id'] = 'replacement'
                elif change == 'label':
                    invalid['Config']['Labels']['nyadb.databend.transport'] = 'foreign'
                replies = [json.dumps([invalid]).encode(), b'password=original-secret', b'original',
                           b'original-volume\n' if change == 'volume' else b'foreign-volume\n']
                with mock.patch.object(FIXTURE, 'run', side_effect=replies) as run:
                    if change == 'none':
                        self.assertEqual(FIXTURE.cleanup_owned_container(Path(directory), 'original', 'owned', 'original-secret'), 1)
                        self.assertNotIn(b'original-secret', (Path(directory) / 'server.log').read_bytes())
                        self.assertIn(mock.call(['docker', 'rm', '-f', '-v', 'original']), run.call_args_list)
                    else:
                        with self.assertRaises(RuntimeError):
                            FIXTURE.cleanup_owned_container(Path(directory), 'original', 'owned', 'original-secret')
                        if change != 'volume':
                            run.assert_called_once_with(['docker', 'inspect', 'original'])


if __name__ == '__main__':
    unittest.main()
