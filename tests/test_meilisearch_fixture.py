import importlib.util
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('meilisearch_fixture', Path(__file__).resolve().parents[1]/'tools/verify_meilisearch_fixture.py')
fixture = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(fixture)


class MeilisearchFixtureTests(unittest.TestCase):
    def test_keys_require_native_anonymous_refusal_or_authenticated_crud(self):
        for mode in fixture.MODES:
            names = fixture.required_passes(mode, keys=True)
            expected = fixture.KEYS_COMMON + (fixture.KEYS_ANONYMOUS if mode == 'anonymous' else fixture.KEYS_AUTHENTICATED)
            self.assertTrue(set(expected).issubset(names))
            self.assertEqual(bool(set(fixture.KEYS_ANONYMOUS).intersection(names)), mode == 'anonymous')
            self.assertEqual(bool(set(fixture.KEYS_AUTHENTICATED).intersection(names)), mode != 'anonymous')
            command = fixture.test_command(mode, True, keys=True)
            self.assertIn('./internal/services/dbruntime', command)
            self.assertNotIn('./internal/ui/shell', command)
            complete = '\n'.join('--- PASS: TestMeilisearchNative'+name+' (0.01s)' for name in names)
            if mode == 'mtls':
                complete += '\nnative mTLS accepted owned client certificate and rejected its absence'
            fixture.check_acceptance(complete, mode, keys=True)
            for name in expected:
                with self.assertRaises(RuntimeError):
                    fixture.check_acceptance(complete.replace('--- PASS: TestMeilisearchNative'+name, '--- SKIP: TestMeilisearchNative'+name), mode, keys=True)
        hashes = fixture.source_hashes(keys=True)
        for path in ['internal/db/contract/keys.go', 'internal/services/dbruntime/keys.go', 'internal/services/meilisearch/keys_document.go']:
            self.assertIn(path, hashes)
        # Private key evidence adds to earlier accepted scopes when requested.
        for flag in ['task_management', 'settings', 'search']:
            names = fixture.required_passes('tls', keys=True, **{flag: True})
            self.assertTrue(set(fixture.required_passes('tls', **{flag: True})).issubset(names))

    def test_task_management_requires_original_counts_and_no_replay(self):
        for mode in fixture.MODES:
            names = fixture.required_passes(mode, task_management=True)
            self.assertTrue(set(fixture.required_passes(mode, task_panel=True)).issubset(names))
            self.assertTrue(set(fixture.TASK_MANAGEMENT_REQUIRED).issubset(names))
            self.assertEqual(fixture.TASK_MANAGEMENT_SCOPED in names, mode != 'anonymous')
            command = fixture.test_command(mode, True, task_management=True)
            self.assertIn('./internal/ui/shell', command)
            complete = '\n'.join('--- PASS: TestMeilisearchNative'+name+' (0.01s)' for name in names)
            if mode == 'mtls':
                complete += '\nnative mTLS accepted owned client certificate and rejected its absence'
            fixture.check_acceptance(complete, mode, task_management=True)
            for name in fixture.TASK_MANAGEMENT_REQUIRED:
                with self.assertRaises(RuntimeError):
                    fixture.check_acceptance(complete.replace('--- PASS: TestMeilisearchNative'+name, '--- SKIP: TestMeilisearchNative'+name), mode, task_management=True)
        for path in ['internal/db/contract/task_management.go', 'internal/services/dbruntime/task_management.go', 'internal/ui/objectworkspace/task_management.go']:
            self.assertIn(path, fixture.source_hashes(task_management=True))

    def test_settings_require_original_admission_conflicts_and_public_editor(self):
        for mode in fixture.MODES:
            names = fixture.required_passes(mode, settings=True)
            self.assertTrue(set(fixture.required_passes(mode, public=True, tasks=True)).issubset(names))
            self.assertTrue(set(fixture.SETTINGS_REQUIRED).issubset(names))
            self.assertEqual(set(fixture.SETTINGS_SCOPED).issubset(names), mode != 'anonymous')
            command = fixture.test_command(mode, True, settings=True)
            self.assertIn('./internal/ui/shell', command)
            self.assertIn('-race', command)
            complete = '\n'.join('--- PASS: TestMeilisearchNative'+name+' (0.01s)' for name in names)
            if mode == 'mtls':
                complete += '\nnative mTLS accepted owned client certificate and rejected its absence'
            fixture.check_acceptance(complete, mode, settings=True)
            for name in fixture.SETTINGS_REQUIRED:
                with self.assertRaises(RuntimeError):
                    fixture.check_acceptance(complete.replace('--- PASS: TestMeilisearchNative'+name, '--- SKIP: TestMeilisearchNative'+name), mode, settings=True)
        hashes = fixture.source_hashes(settings=True)
        for path in ['internal/db/contract/settings.go', 'internal/services/dbruntime/settings.go', 'internal/ui/objectworkspace/settings.go', 'internal/ui/shell/meilisearch_settings_integration_test.go']:
            self.assertIn(path, hashes)

    def test_task_panel_requires_public_layers_native_task_leases_and_ui(self):
        for mode in fixture.MODES:
            names = fixture.required_passes(mode, task_panel=True)
            self.assertTrue(set(fixture.required_passes(mode, public=True, tasks=True)).issubset(names))
            self.assertTrue(set(fixture.TASK_PANEL_REQUIRED).issubset(names))
            self.assertEqual(fixture.TASK_PANEL_SCOPED in names, mode != 'anonymous')
            command = fixture.test_command(mode, True, task_panel=True)
            self.assertIn('./internal/ui/shell', command)
            self.assertIn('-race', command)
            complete = '\n'.join('--- PASS: TestMeilisearchNative'+name+' (0.01s)' for name in names)
            if mode == 'mtls':
                complete += '\nnative mTLS accepted owned client certificate and rejected its absence'
            fixture.check_acceptance(complete, mode, task_panel=True)
            for name in fixture.TASK_PANEL_REQUIRED:
                with self.assertRaises(RuntimeError):
                    fixture.check_acceptance(complete.replace('--- PASS: TestMeilisearchNative'+name, '--- SKIP: TestMeilisearchNative'+name), mode, task_panel=True)
        hashes = fixture.source_hashes(task_panel=True)
        self.assertIn('internal/db/contract/tasks.go', hashes)
        self.assertIn('internal/services/dbruntime/tasks.go', hashes)
        self.assertIn('internal/ui/objectworkspace/tasks.go', hashes)
        self.assertIn('internal/ui/shell/meilisearch_tasks_integration_test.go', hashes)

    def test_tasks_acceptance_requires_native_mutation_counts_and_original_observers(self):
        for mode in fixture.MODES:
            names = fixture.required_passes(mode, tasks=True)
            self.assertTrue(set(fixture.TASK_REQUIRED).issubset(names))
            self.assertEqual(fixture.TASK_SCOPED in names, mode != 'anonymous')
            command = fixture.test_command(mode, True, tasks=True)
            self.assertIn('-race', command)
            self.assertNotIn('./internal/ui/shell', command)
            complete = '\n'.join('--- PASS: TestMeilisearchNative'+name+' (0.01s)' for name in names)
            if mode == 'mtls':
                complete += '\nnative mTLS accepted owned client certificate and rejected its absence'
            fixture.check_acceptance(complete, mode, tasks=True)
            for name in fixture.TASK_REQUIRED + ([] if mode == 'anonymous' else [fixture.TASK_SCOPED]):
                with self.assertRaises(RuntimeError):
                    fixture.check_acceptance(complete.replace('--- PASS: TestMeilisearchNative'+name, '--- SKIP: TestMeilisearchNative'+name), mode, tasks=True)
        hashes = fixture.source_hashes(tasks=True)
        self.assertIn('internal/services/meilisearch/task_catalog.go', hashes)
        self.assertIn('internal/services/meilisearch/query.go', hashes)

    def test_search_acceptance_requires_native_sources_scopes_and_public_ui(self):
        for mode in fixture.MODES:
            names = fixture.required_passes(mode, search=True)
            self.assertTrue(set(fixture.required_passes(mode, public=True)).issubset(names))
            self.assertTrue(set(fixture.SEARCH_REQUIRED).issubset(names))
            self.assertEqual(fixture.SEARCH_SCOPED in names, mode != 'anonymous')
            command = fixture.test_command(mode, True, search=True)
            self.assertIn('-race', command)
            self.assertIn('./internal/ui/shell', command)
            complete = '\n'.join('--- PASS: TestMeilisearchNative'+name+' (0.01s)' for name in names)
            if mode == 'mtls':
                complete += '\nnative mTLS accepted owned client certificate and rejected its absence'
            fixture.check_acceptance(complete, mode, search=True)
            for name in fixture.SEARCH_REQUIRED:
                with self.assertRaises(RuntimeError):
                    fixture.check_acceptance(complete.replace('--- PASS: TestMeilisearchNative'+name, '--- SKIP: TestMeilisearchNative'+name), mode, search=True)
            public = '\n'.join('--- PASS: TestMeilisearchNative'+name+' (0.01s)' for name in fixture.required_passes(mode, public=True))
            with self.assertRaises(RuntimeError):
                fixture.check_acceptance(public, mode, search=True)
        hashes = fixture.source_hashes(search=True)
        for path in ['internal/ui/dataworkspace/native_selector.go', 'internal/ui/shell/meilisearch_search_integration_test.go', 'locales/en.json', 'locales/zh-Hans.json']:
            self.assertIn(path, hashes)

    def test_data_acceptance_requires_original_leases_and_complete_export(self):
        for mode in fixture.MODES:
            names = fixture.required_passes(mode, data=True)
            for name in fixture.DATA_REQUIRED + fixture.DOCUMENT_REQUIRED + fixture.SESSION_REQUIRED:
                self.assertIn(name, names)
            self.assertEqual(fixture.DATA_SCOPED in names, mode != 'anonymous')
            command = fixture.test_command(mode, True, data=True)
            self.assertIn('-race', command)
            self.assertIn('DataQualifiedOffsetPagingFingerprintsAndCompleteExport', command[-2])
            ordinary = '\n'.join('--- PASS: TestMeilisearchNative'+name+' (0.01s)' for name in fixture.required_passes(mode, session=True, documents=True))
            with self.assertRaises(RuntimeError):
                fixture.check_acceptance(ordinary, mode, data=True)
            complete = '\n'.join('--- PASS: TestMeilisearchNative'+name+' (0.01s)' for name in names)
            if mode == 'mtls':
                complete += '\nnative mTLS accepted owned client certificate and rejected its absence'
            fixture.check_acceptance(complete, mode, data=True)

    def test_documents_acceptance_requires_complete_enumeration_and_native_read_lifecycle(self):
        for mode in fixture.MODES:
            names = fixture.required_passes(mode, documents=True)
            self.assertTrue(set(fixture.DOCUMENT_REQUIRED).issubset(names))
            self.assertEqual(fixture.DOCUMENT_SCOPED in names, mode != 'anonymous')
            command = fixture.test_command(mode, True, documents=True)
            self.assertIn('-race', command)
            self.assertIn('DocumentsEnumerationBeyondSearchLimitAndExactIDs', command[-2])
            log = '\n'.join('--- PASS: TestMeilisearchNative'+name+' (0.01s)' for name in fixture.required_passes(mode))
            with self.assertRaises(RuntimeError):
                fixture.check_acceptance(log, mode, documents=True)

    def test_public_acceptance_requires_all_native_layers_and_scoped_ui(self):
        for mode in fixture.MODES:
            names = fixture.required_passes(mode, public=True)
            for name in fixture.PROFILE_REQUIRED + fixture.SESSION_REQUIRED + fixture.PUBLIC_REQUIRED:
                self.assertIn(name, names)
            self.assertEqual(fixture.PUBLIC_SCOPED in names, mode != 'anonymous')
            command = fixture.test_command(mode, True, public=True)
            for package in ['./internal/ui/connectioneditor', './internal/services/dbruntime', './internal/ui/sqlworkspace', './internal/ui/shell']:
                self.assertIn(package, command)
            log = '\n'.join('--- PASS: TestMeilisearchNative'+name+' (0.01s)' for name in fixture.required_passes(mode, True, True))
            with self.assertRaises(RuntimeError):
                fixture.check_acceptance(log, mode, public=True)

    def test_session_acceptance_requires_original_lifecycle_and_native_permissions(self):
        for mode in fixture.MODES:
            names = fixture.required_passes(mode, session=True)
            for name in fixture.SESSION_REQUIRED:
                self.assertIn(name, names)
            self.assertEqual(fixture.SESSION_SCOPED in names, mode != 'anonymous')
            command = fixture.test_command(mode, True, session=True)
            self.assertIn('-race', command)
            self.assertIn('SessionLostCancelReleaseCloseAndRecovery', command[-2])
            log = '\n'.join('--- PASS: TestMeilisearchNative'+name+' (0.01s)' for name in fixture.required_passes(mode))
            if mode == 'mtls':
                log += '\nnative mTLS accepted owned client certificate and rejected its absence'
            with self.assertRaises(RuntimeError):
                fixture.check_acceptance(log, mode, session=True)
            complete = '\n'.join('--- PASS: TestMeilisearchNative'+name+' (0.01s)' for name in names)
            if mode == 'mtls':
                complete += '\nnative mTLS accepted owned client certificate and rejected its absence'
            fixture.check_acceptance(complete, mode, session=True)

    def test_profile_acceptance_requires_native_reopen_cancel_and_localized_forms(self):
        command = fixture.test_command('tls', True, True)
        self.assertIn('./internal/ui/connectioneditor', command)
        for name in fixture.PROFILE_REQUIRED:
            self.assertIn(name, command[-3])
        ordinary = '\n'.join('--- PASS: TestMeilisearchNative'+name+' (0.01s)' for name in fixture.required_passes('tls'))
        with self.assertRaises(RuntimeError):
            fixture.check_acceptance(ordinary, 'tls', True)
        complete = '\n'.join('--- PASS: TestMeilisearchNative'+name+' (0.01s)' for name in fixture.required_passes('tls', True))
        fixture.check_acceptance(complete, 'tls', True)
    def test_required_passes_reject_missing_skip_failure_and_false_mtls(self):
        for mode in fixture.MODES:
            log = '\n'.join('--- PASS: TestMeilisearchNative'+name+' (0.01s)' for name in fixture.required_passes(mode))
            if mode == 'mtls':
                with self.assertRaises(RuntimeError):
                    fixture.check_acceptance(log, mode)
                log += '\nnative mTLS accepted owned client certificate and rejected its absence'
            fixture.check_acceptance(log, mode)
            for invalid in [log.replace('--- PASS:', '--- SKIP:', 1), log+'\n--- SKIP: unexpected (0.0s)', log+'\n--- FAIL: unexpected (0.0s)', log+'\nWARNING: DATA RACE']:
                with self.assertRaises(RuntimeError):
                    fixture.check_acceptance(invalid, mode)
        self.assertNotIn(fixture.SCOPED, fixture.test_command('anonymous', False)[-2])
        self.assertIn('-race', fixture.test_command('tls', True))

    def test_cleanup_requires_exact_immutable_id_and_both_labels(self):
        identifier = 'a'*64
        nonce = 'owned-nonce'
        record = dict(Id=identifier, Config=dict(Labels={'nyadb.fixture':'meilisearch', 'nyadb.fixture.nonce':nonce}))
        for changed in [dict(record, Id='b'*64), dict(record, Config=dict(Labels={'nyadb.fixture':'other', 'nyadb.fixture.nonce':nonce})), dict(record, Config=dict(Labels={'nyadb.fixture':'meilisearch', 'nyadb.fixture.nonce':'other'}))]:
            result = subprocess.CompletedProcess([], 0, json.dumps([changed]), '')
            with patch.object(fixture.subprocess, 'run', return_value=result) as run:
                with self.assertRaises(RuntimeError):
                    fixture.remove_owned(identifier, nonce)
                self.assertEqual(run.call_count, 1)
        with patch.object(fixture.subprocess, 'run') as run:
            for invalid in ['container-name', '', 'a'*63]:
                with self.assertRaises(RuntimeError):
                    fixture.remove_owned(invalid, nonce)
            run.assert_not_called()

    def test_redaction_removes_native_generated_api_keys(self):
        raw = 'Server listening at localhost\nMaster key: owned-private\nGenerated API Key: '+('a'*64)+'\nAuthorization: Bearer token\nhealth available'
        redacted = fixture.redacted_server_log(raw, 'owned-private')
        self.assertNotIn('owned-private', redacted)
        self.assertNotIn('a'*64, redacted)
        self.assertNotIn('Bearer token', redacted)
        self.assertIn('health available', redacted)


if __name__ == '__main__':
    unittest.main()
