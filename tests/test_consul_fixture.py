import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('verify_consul_fixture', ROOT/'tools/verify_consul_fixture.py')
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)


class ConsulFixtureTests(unittest.TestCase):
    def test_session_acceptance_requires_every_original_document_and_runtime_parent(self):
        required = fixture.REQUIRED + fixture.DOCUMENT_REQUIRED + fixture.RUNTIME_REQUIRED
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        self.assertEqual(fixture.accepted_cases(log, required), list(required))
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ',
                                                   '--- SKIP: TestConsulNative'+name+' '), required)
        with self.assertRaises(RuntimeError):
            fixture.accepted_cases(log+'\n'+log, required)
        with self.assertRaises(RuntimeError):
            fixture.accepted_cases(log+'\nWARNING: DATA RACE', required)
        with self.assertRaises(RuntimeError):
            fixture.accepted_cases(log+'\nFAIL\tfixture', required)

    def test_source_manifest_covers_shared_contract_runtime_and_original_scope_dispatch(self):
        hashes = fixture.source_hashes()
        for name in ('internal/db/contract/kv.go', 'internal/db/contract/kv_http.go',
                     'internal/services/consul/session.go', 'internal/services/consul/document_lease.go',
                     'internal/services/dbruntime/kv.go',
                     'internal/services/dbruntime/consul_native_integration_test.go',
                     'internal/ui/kvworkspace/events_state.go', 'go.mod', 'go.sum',
                     'tools/verify_consul_fixture.py'):
            self.assertIn(name, hashes)
        self.assertFalse(any(name.startswith(('docs/', 'temp/', 'build/')) for name in hashes))

    def test_probe_cannot_claim_original_session_acceptance(self):
        with self.assertRaises(ValueError):
            fixture.verify(probe=True, session=True)

    def test_structured_scope_requires_original_parents_and_all_tool_parents(self):
        required = fixture.REQUIRED + fixture.DOCUMENT_REQUIRED + fixture.RUNTIME_REQUIRED + fixture.PUBLIC_REQUIRED + fixture.TOOLS_REQUIRED
        self.assertEqual(len(required), 25)
        self.assertEqual(len(set(required)), len(required))
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        self.assertEqual(fixture.accepted_cases(log, required), list(required))
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)
        with self.assertRaises(RuntimeError):
            fixture.accepted_cases(log, required[:-3])
        with self.assertRaises(ValueError):
            fixture.verify(probe=True, tools=True)
        hashes = fixture.source_hashes()
        for name in ('internal/db/contract/kv_request_tools.go', 'internal/services/consul/kv_request_tools.go',
                     'internal/ui/kvworkspace/request_tools_state.go', 'internal/ui/kvworkspace/request_tools_view.go',
                     'internal/ui/kvworkspace/consul_tools_native_integration_test.go',
                     'internal/services/dbruntime/consul_tools_native_integration_test.go'):
            self.assertIn(name, hashes)

    def test_private_data_scope_retains_public_tools_and_requires_all_data_parents(self):
        required = fixture.REQUIRED + fixture.DOCUMENT_REQUIRED + fixture.RUNTIME_REQUIRED + fixture.PUBLIC_REQUIRED + fixture.TOOLS_REQUIRED + fixture.DATA_REQUIRED
        self.assertEqual(len(required), 29)
        self.assertEqual(len(set(required)), len(required))
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        self.assertEqual(fixture.accepted_cases(log, required), list(required))
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)
        with self.assertRaises(ValueError):
            fixture.verify(probe=True, data_session=True)
        hashes = fixture.source_hashes()
        for name in ('internal/services/consul/data_document.go', 'internal/services/consul/data_read.go',
                     'internal/services/consul/data_field.go', 'internal/services/consul/data_export.go',
                     'internal/services/consul/data_native_integration_test.go'):
            self.assertIn(name, hashes)

    def test_public_scope_keeps_all_private_parents_and_requires_real_ui_parents(self):
        required = fixture.REQUIRED + fixture.DOCUMENT_REQUIRED + fixture.RUNTIME_REQUIRED + fixture.PUBLIC_REQUIRED
        self.assertEqual(len(required), 22)
        self.assertEqual(len(set(required)), len(required))
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        self.assertEqual(fixture.accepted_cases(log, required), list(required))
        for name in fixture.PUBLIC_REQUIRED:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)
        with self.assertRaises(RuntimeError):
            fixture.accepted_cases(log, fixture.REQUIRED + fixture.DOCUMENT_REQUIRED + fixture.RUNTIME_REQUIRED)
        with self.assertRaises(ValueError):
            fixture.verify(probe=True, public=True)
        hashes = fixture.source_hashes()
        for name in ('internal/services/connections/consul_options.go', 'internal/storage/localdb/consul_options_json.go',
                     'internal/ui/connectioneditor/consul_native_integration_test.go',
                     'internal/ui/kvworkspace/http_receipt.go', 'internal/ui/kvworkspace/consul_native_integration_test.go',
                     'internal/ui/shell/consul_native_integration_test.go', 'locales/en.json'):
            self.assertIn(name, hashes)

    def test_public_grid_retains_all_parents_and_requires_original_runtime_and_shared_ui(self):
        required = (fixture.REQUIRED + fixture.DOCUMENT_REQUIRED + fixture.RUNTIME_REQUIRED +
                    fixture.PUBLIC_REQUIRED + fixture.TOOLS_REQUIRED + fixture.DATA_REQUIRED + fixture.GRID_REQUIRED)
        self.assertEqual(len(required), 35)
        self.assertEqual(len(set(required)), len(required))
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        self.assertEqual(fixture.accepted_cases(log, required), list(required))
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)
        with self.assertRaises(ValueError):
            fixture.verify(probe=True, public_grid=True)
        hashes = fixture.source_hashes()
        for name in ('internal/services/dbruntime/consul_data_native_integration_test.go',
                     'internal/ui/dataworkspace/consul_native_integration_test.go',
                     'internal/ui/shell/consul_grid_native_integration_test.go',
                     'internal/ui/objectworkspace/search_view.go'):
            self.assertIn(name, hashes)

    def test_public_grid_writes_cannot_pass_with_missing_runtime_shell_or_regression(self):
        regression = (fixture.REQUIRED + fixture.DOCUMENT_REQUIRED + fixture.RUNTIME_REQUIRED +
                      fixture.PUBLIC_REQUIRED + fixture.TOOLS_REQUIRED + fixture.DATA_REQUIRED +
                      fixture.MUTATION_REQUIRED + fixture.GRID_REQUIRED)
        required = regression + fixture.WRITE_GRID_REQUIRED
        self.assertEqual(len(required), 50)
        self.assertEqual(len(set(required)), 50)
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        self.assertEqual(fixture.accepted_cases(log, required), list(required))
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)
        partial = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in fixture.WRITE_GRID_REQUIRED)
        with self.assertRaises(RuntimeError):
            fixture.accepted_cases(partial, required)
        hashes = fixture.source_hashes()
        for name in ('internal/services/dbruntime/consul_mutation_native_integration_test.go',
                     'internal/ui/shell/consul_mutation_native_integration_test.go',
                     'internal/ui/dataworkspace/consul_mutation_native_integration_test.go'):
            self.assertIn(name, hashes)
