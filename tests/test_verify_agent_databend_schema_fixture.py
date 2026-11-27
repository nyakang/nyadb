import copy
import json
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'tools'))
import verify_agent_databend_schema_fixture as schema


class DatabendSchemaReceiptVerifierTests(unittest.TestCase):
    def receipt(self):
        target = {'Object': {'ProfileID': 8, 'Catalog': '', 'Schema': 'original', 'Kind': 'table', 'Name': 't'}, 'Generation': 5}
        original = {'Target': target, 'Schema': {'Engine': 'databend', 'Databend': {'Database': 'original', 'TableID': '71'}}}
        actual = copy.deepcopy(original)
        actual['Target']['Object']['Name'] = 'renamed'
        actual['Schema'].update(Table={'Name': 'renamed'}, Exists=True)
        result = {'columns': [], 'column_types': [], 'rows': [[]], 'affected_rows': 0, 'truncated': False}
        native = {'Receipt': {'Dispatched': True, 'ExecutorAcknowledged': True, 'RemoteError': None, 'Result': result},
                  'Original': {'jsonrpc': '2.0', 'id': 9, 'result': copy.deepcopy(result)}}
        return {'State': 'committed', 'Verified': True, 'Operation': 'rename', 'Sequence': 1, 'DocumentID': 'owning document',
                'Rows': 0, 'Original': original, 'Destination': 'renamed', 'Actual': actual,
                'Statements': ['ALTER TABLE "original"."t" RENAME TO "renamed"'], 'NativeJSON': json.dumps(native)}

    def test_complete_original_single_ddl(self):
        schema.verify_receipt(self.receipt(), 'committed', True)

    def test_rejects_later_table_and_qualified_destination(self):
        for mutate in [lambda r: r['Actual']['Schema']['Databend'].update(TableID='72'),
                       lambda r: r['Actual']['Target'].update(Generation=6),
                       lambda r: r.update(Statements=['ALTER TABLE "original"."t" RENAME TO "other"."renamed"'])]:
            r = self.receipt()
            mutate(r)
            with self.assertRaises(RuntimeError):
                schema.verify_receipt(r, 'committed', True)

    def test_rejects_rewritten_or_incomplete_original_acknowledgement(self):
        for mutate in [lambda n: n['Original']['result'].update(affected_rows=1),
                       lambda n: n['Receipt'].update(ExecutorAcknowledged=False),
                       lambda n: n['Receipt']['Result'].update(rows=[]),
                       lambda n: n['Receipt'].update(Dispatched=False)]:
            r = self.receipt()
            n = json.loads(r['NativeJSON'])
            mutate(n)
            r['NativeJSON'] = json.dumps(n)
            with self.assertRaises(RuntimeError):
                schema.verify_receipt(r, 'committed', True)

    def test_unknown_cannot_acquire_verified_snapshot(self):
        r = self.receipt()
        r.update(State='uncertain', Verified=False, Actual=None)
        n = json.loads(r['NativeJSON'])
        n['Receipt'].update(Result=None, RemoteError={'OperationOutcome': 'unknown'})
        n['Original'].pop('result')
        n['Original']['error'] = {'data': {'operationOutcome': 'unknown'}}
        r['NativeJSON'] = json.dumps(n)
        schema.verify_receipt(r, 'uncertain', False)
        r['Actual'] = self.receipt()['Actual']
        with self.assertRaises(RuntimeError):
            schema.verify_receipt(r, 'uncertain', False)

    def test_drop_requires_original_absent_incarnation_and_unconditional_statement(self):
        r = self.receipt()
        r.update(Operation='drop', Destination='t', Statements=['DROP TABLE "original"."t"'])
        r['Actual'] = copy.deepcopy(r['Original'])
        r['Actual']['Schema'].update(Table={'Name': 't'}, Exists=False, DDL='')
        schema.verify_receipt(r, 'committed', True, 'drop')
        for mutate in [lambda v: v['Actual']['Schema'].update(Exists=True),
                       lambda v: v['Actual']['Schema']['Databend'].update(TableID='72'),
                       lambda v: v.update(Statements=['DROP TABLE IF EXISTS "original"."t"']),
                       lambda v: v.update(Destination='different')]:
            changed = copy.deepcopy(r)
            mutate(changed)
            with self.assertRaises(RuntimeError):
                schema.verify_receipt(changed, 'committed', True, 'drop')


if __name__ == '__main__':
    unittest.main()
