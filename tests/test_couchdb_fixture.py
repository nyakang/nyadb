import importlib.util
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('couchdb_fixture', Path(__file__).resolve().parents[1]/'tools/verify_couchdb_fixture.py')
FIXTURE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FIXTURE)


class CouchDBFixtureTests(unittest.TestCase):
    def test_cleanup_requires_captured_id_and_both_labels(self):
        identifier = 'a'*64
        nonce = 'owned-nonce'
        record = {'Id': identifier, 'Config': {'Labels': {'nyadb.fixture': 'couchdb', 'nyadb.fixture.nonce': nonce}}}
        for change in [lambda r: r.update(Id='b'*64), lambda r: r['Config']['Labels'].update({'nyadb.fixture': 'solr'}), lambda r: r['Config']['Labels'].update({'nyadb.fixture.nonce': 'other'})]:
            bad = json.loads(json.dumps(record))
            change(bad)
            with patch.object(FIXTURE.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, json.dumps([bad]), '')) as run:
                with self.assertRaises(RuntimeError):
                    FIXTURE.remove_owned(identifier, nonce)
                self.assertEqual(run.call_count, 1)
        with patch.object(FIXTURE.subprocess, 'run', side_effect=[subprocess.CompletedProcess([], 0, json.dumps([record]), ''), subprocess.CompletedProcess([], 0)]) as run:
            FIXTURE.remove_owned(identifier, nonce)
            self.assertEqual(run.call_args.args[0], ['docker', 'rm', '-f', '-v', identifier])

    def test_unknown_or_uninspectable_ids_never_remove(self):
        with patch.object(FIXTURE.subprocess, 'run') as run:
            for identifier in ['owned-name', 'a'*12, None]:
                with self.assertRaises(RuntimeError):
                    FIXTURE.remove_owned(identifier, 'nonce')
            run.assert_not_called()
        with patch.object(FIXTURE.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, '', '')) as run:
            with self.assertRaises(RuntimeError):
                FIXTURE.remove_owned('a'*64, 'nonce')
            self.assertEqual(run.call_count, 1)

    def test_acceptance_requires_all_native_passes_and_no_skips(self):
        log = '\n'.join('--- PASS: TestCouchDBNative'+name+' (0.03s)' for name in FIXTURE.REQUIRED)
        FIXTURE.check_acceptance(log)
        for bad in [log.replace('--- PASS:', '--- SKIP:', 1), log.split('\n', 1)[1], log+'\n    --- SKIP: TestUnexpected (0.0s)', log.replace('TestCouchDBNative', 'TestOther')]:
            with self.assertRaises(RuntimeError):
                FIXTURE.check_acceptance(bad)

    def test_profile_acceptance_requires_native_ui_passes_and_correct_package_filter(self):
        base = '\n'.join('--- PASS: TestCouchDBNative'+name+' (0.03s)' for name in FIXTURE.REQUIRED)
        with self.assertRaises(RuntimeError):
            FIXTURE.check_acceptance(base, profile=True)
        complete = base+'\n'+'\n'.join('--- PASS: TestCouchDBNative'+name+' (0.03s)' for name in FIXTURE.PROFILE_REQUIRED)
        FIXTURE.check_acceptance(complete, profile=True)
        command = FIXTURE.native_test_command(profile=True, race=True)
        self.assertIn('-race', command)
        self.assertLess(command.index('-run'), command.index('./internal/storage/localdb'))
        self.assertIn('./internal/ui/connectioneditor', command)

    def test_session_acceptance_requires_every_native_lifecycle_pass(self):
        base = '\n'.join('--- PASS: TestCouchDBNative'+name+' (0.03s)' for name in FIXTURE.REQUIRED)
        with self.assertRaises(RuntimeError):
            FIXTURE.check_acceptance(base, session=True)
        complete = base+'\n'+'\n'.join('--- PASS: TestCouchDBNative'+name+' (0.03s)' for name in FIXTURE.SESSION_REQUIRED)
        FIXTURE.check_acceptance(complete, session=True)
        for name in FIXTURE.SESSION_REQUIRED:
            with self.assertRaises(RuntimeError):
                FIXTURE.check_acceptance(complete.replace('--- PASS: TestCouchDBNative'+name, '--- FAIL: TestCouchDBNative'+name), session=True)

    def test_workbench_requires_native_provider_ui_recovery_and_packages(self):
        names = FIXTURE.REQUIRED+FIXTURE.PROFILE_REQUIRED+FIXTURE.SESSION_REQUIRED+FIXTURE.WORKBENCH_REQUIRED+FIXTURE.DATA_REQUIRED
        complete = '\n'.join('--- PASS: TestCouchDBNative'+name+' (0.03s)' for name in names)
        FIXTURE.check_acceptance(complete, profile=True, session=True, workbench=True)
        for name in FIXTURE.WORKBENCH_REQUIRED+FIXTURE.DATA_REQUIRED:
            with self.assertRaises(RuntimeError):
                FIXTURE.check_acceptance(complete.replace('--- PASS: TestCouchDBNative'+name, '--- FAIL: TestCouchDBNative'+name), profile=True, session=True, workbench=True)
        command = FIXTURE.native_test_command(profile=True, race=True, workbench=True)
        for package in ['dbruntime', 'shell', 'sqlworkspace', 'objectworkspace', 'navigation', 'connectioncatalog', 'dataworkspace']:
            self.assertTrue(any(value.endswith('/'+package) for value in command))

    def test_document_reads_are_mandatory_native_acceptance(self):
        required = ['DocumentAllDocsMangoExactJSONAndDeletedBoundary', 'DocumentMangoLostReadCancellationNeverReplays']
        complete = '\n'.join('--- PASS: TestCouchDBNative'+name+' (0.03s)' for name in FIXTURE.REQUIRED)
        for name in required:
            self.assertIn(name, FIXTURE.REQUIRED)
            with self.assertRaises(RuntimeError):
                FIXTURE.check_acceptance(complete.replace('--- PASS: TestCouchDBNative'+name, '--- FAIL: TestCouchDBNative'+name))

    def test_data_shared_grid_requires_native_ui_pass_and_package(self):
        base = '\n'.join('--- PASS: TestCouchDBNative'+name+' (0.03s)' for name in FIXTURE.REQUIRED)
        with self.assertRaises(RuntimeError):
            FIXTURE.check_acceptance(base, data=True)
        complete = base+'\n'+'\n'.join('--- PASS: TestCouchDBNative'+name+' (0.03s)' for name in FIXTURE.DATA_REQUIRED)
        FIXTURE.check_acceptance(complete, data=True)
        for name in FIXTURE.DATA_REQUIRED:
            with self.assertRaises(RuntimeError):
                FIXTURE.check_acceptance(complete.replace('--- PASS: TestCouchDBNative'+name, '--- SKIP: TestCouchDBNative'+name), data=True)
        command = FIXTURE.native_test_command(data=True, race=True)
        self.assertIn('./internal/ui/dataworkspace', command)
        self.assertIn('-race', command)

    def test_data_leases_require_native_paging_complete_export_and_cancellation(self):
        complete = '\n'.join('--- PASS: TestCouchDBNative'+name+' (0.03s)' for name in FIXTURE.REQUIRED)
        for name in ['DataPagingOriginalRowsFieldsAndCompleteExport', 'DataCanceledOriginalPageKeepsIndependentCountAndLease']:
            self.assertIn(name, FIXTURE.REQUIRED)
            with self.assertRaises(RuntimeError):
                FIXTURE.check_acceptance(complete.replace('--- PASS: TestCouchDBNative'+name, '--- SKIP: TestCouchDBNative'+name))


if __name__ == '__main__':
    unittest.main()
