import copy
import json
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'tools'))
import verify_agent_databend_database_create_fixture as database


class DatabendDatabaseCreateReceiptTests(unittest.TestCase):
    def receipt(self):
        target = {'Namespace': {'ProfileID': 7, 'Catalog': '', 'Schema': 'new"database'}, 'Generation': 73}
        sql = 'CREATE DATABASE "new""database"'
        before = {'Target': target, 'Engine': 'databend', 'NativeID': '', 'DDL': '', 'CreateSQL': sql,
                  'Exists': False, 'Revision': 'original-absence'}
        actual = copy.deepcopy(before)
        actual.update(Exists=True, NativeID='9007199254740993', DDL='CREATE DATABASE `new"database` ENGINE=DEFAULT', Revision='observed')
        result = {'columns': [], 'column_types': [], 'rows': [[]], 'affected_rows': 0, 'truncated': False}
        native = {'Receipt': {'Dispatched': True, 'ExecutorAcknowledged': True, 'RemoteError': None, 'Result': result},
                  'Original': {'jsonrpc': '2.0', 'id': 1, 'result': copy.deepcopy(result)}}
        return {'DocumentID': 'original', 'Sequence': 1, 'Original': before, 'SQL': sql,
                'NativeJSON': json.dumps(native), 'State': 'committed', 'Verified': True, 'Actual': actual}

    def test_creation_requires_original_absence_statement_acknowledgement_and_observation(self):
        database.verify_receipt(self.receipt(), 'committed', True)
        for change in [lambda r: r['Original'].update(Exists=True),
                       lambda r: r.update(SQL='CREATE DATABASE IF NOT EXISTS other'),
                       lambda r: r['Original'].update(CreateSQL='CREATE DATABASE other'),
                       lambda r: r['Actual']['Target'].update(Generation=74),
                       lambda r: r['Actual'].update(NativeID='0'),
                       lambda r: r['Actual'].update(DDL='different later database')]:
            receipt = self.receipt()
            change(receipt)
            with self.assertRaises(RuntimeError):
                database.verify_receipt(receipt, 'committed', True)
        receipt = self.receipt()
        native = json.loads(receipt['NativeJSON'])
        native['Receipt']['Result']['rows'] = []
        receipt['NativeJSON'] = json.dumps(native)
        with self.assertRaises(RuntimeError):
            database.verify_receipt(receipt, 'committed', True)

    def test_unknown_creation_cannot_be_repaired_by_a_later_database_read(self):
        receipt = self.receipt()
        receipt.update(State='uncertain', Verified=False, Actual=None)
        native = json.loads(receipt['NativeJSON'])
        native['Receipt'].update(Result=None, RemoteError={'OperationOutcome': 'unknown'})
        native['Original'].pop('result')
        native['Original']['error'] = {'data': {'operationOutcome': 'unknown'}}
        receipt['NativeJSON'] = json.dumps(native)
        database.verify_receipt(receipt, 'uncertain', False)
        receipt['Actual'] = self.receipt()['Actual']
        with self.assertRaises(RuntimeError):
            database.verify_receipt(receipt, 'uncertain', False)


if __name__ == '__main__':
    unittest.main()
