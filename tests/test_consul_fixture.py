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

    def test_http_watch_foundation_appends_every_original_parent_and_retains_images(self):
        previous = self.copy_scope_before_startup(public_prefix_set=True, txn_max_request=8388608)
        report = self.copy_scope_before_startup(http_watch=True, txn_max_request=8388608)
        required = tuple(previous['required']) + fixture.HTTP_WATCH_REQUIRED
        self.assertEqual(len(previous['required']), 84)
        self.assertEqual(report['required'], list(required))
        self.assertEqual(len(set(required)), 92)
        self.assertEqual(report['scope'], 'private-consul-original-http-watch-with-public-batch-regression')
        self.assertTrue(report['public_prefix_set'])
        for package in ('./internal/services/consul', './internal/services/dbruntime', './internal/ui/shell', './internal/ui/kvworkspace', './internal/ui/dataworkspace'):
            self.assertIn(package, report['packages'])
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        self.assertEqual(fixture.accepted_cases(log, required), list(required))
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)
        for name in ('internal/db/contract/kv_http_watch.go', 'internal/services/consul/http_watch.go',
                     'internal/services/consul/http_watch_native_integration_test.go', 'internal/services/dbruntime/kv_http_watch.go'):
            self.assertIn(name, fixture.source_hashes())

    def test_http_watch_diagnostic_cannot_claim_regression_or_public_acceptance(self):
        report = self.copy_scope_before_startup(http_watch_probe=True)
        self.assertEqual(report['required'], list(fixture.HTTP_WATCH_REQUIRED))
        self.assertEqual(report['scope'], 'diagnostic-consul-original-http-watch-foundation')
        self.assertEqual(report['packages'], ['./internal/services/consul', './internal/services/dbruntime'])
        for flag in ('http_watch', 'public_prefix_set', 'prefix_delete_set', 'probe', 'session', 'public', 'tools', 'data_session',
                     'public_grid', 'data_mutations', 'public_grid_writes', 'key_copy', 'public_key_copy', 'prefix_delete',
                     'public_prefix_delete', 'public_prefix_set_probe', 'copy_limit_probe', 'grid_write_probe', 'copy_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(http_watch_probe=True, **{flag: True})
        for flag in ('probe', 'mutation_limit_probe', 'grid_write_probe', 'copy_probe', 'copy_limit_probe', 'public_copy_probe',
                     'prefix_delete_probe', 'public_prefix_probe', 'prefix_set_probe', 'public_prefix_set_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(http_watch=True, **{flag: True})

    def test_public_http_watch_requires_new_ui_parents_and_all_existing_regressions(self):
        foundation = self.copy_scope_before_startup(http_watch=True, txn_max_request=8388608)
        report = self.copy_scope_before_startup(public_http_watch=True, txn_max_request=8388608)
        required = tuple(foundation['required']) + fixture.PUBLIC_HTTP_WATCH_REQUIRED
        self.assertEqual(report['required'], list(required))
        self.assertEqual(report['scope'], 'public-consul-original-http-watch')
        self.assertEqual(len(set(required)), 96)
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        self.assertEqual(fixture.accepted_cases(log, required), list(required))
        for name in fixture.PUBLIC_HTTP_WATCH_REQUIRED:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)

    def test_public_http_watch_probe_never_claims_complete_acceptance(self):
        report = self.copy_scope_before_startup(public_http_watch_probe=True)
        self.assertEqual(report['scope'], 'diagnostic-consul-public-http-watch')
        self.assertEqual(report['required'], list(fixture.PUBLIC_HTTP_WATCH_REQUIRED))
        for flag in ('public_http_watch', 'http_watch', 'http_watch_probe', 'public_prefix_set', 'public_grid', 'public', 'probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(public_http_watch_probe=True, **{flag: True})

    def test_session_tools_foundation_retains_complete_public_renewal_acceptance(self):
        previous = self.copy_scope_before_startup(public_session_renewal=True, txn_max_request=8388608)
        report = self.copy_scope_before_startup(session_tools=True, txn_max_request=8388608)
        required = tuple(previous['required']) + fixture.SESSION_TOOLS_REQUIRED
        self.assertEqual(report['required'], list(required))
        self.assertEqual(len(set(required)), 112)
        self.assertTrue(report['public_session_renewal'])
        self.assertEqual(report['scope'], 'private-consul-session-tools-with-public-renewal-regression')
        self.assertEqual(report['packages'], previous['packages'])
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        self.assertEqual(fixture.accepted_cases(log, required), list(required))
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)
        with self.assertRaises(RuntimeError):
            fixture.accepted_cases(log, tuple(previous['required']))

    def test_session_tools_diagnostic_cannot_claim_regression_or_public_tools(self):
        report = self.copy_scope_before_startup(session_tools_probe=True)
        self.assertEqual(report['scope'], 'diagnostic-consul-session-tools')
        self.assertEqual(report['required'], list(fixture.SESSION_TOOLS_REQUIRED))
        self.assertEqual(report['packages'], ['./internal/services/consul'])
        for flag in ('session_tools', 'public_session_renewal', 'public_session_renewal_probe',
                     'session_renewal', 'session_renewal_probe', 'public', 'tools', 'public_grid',
                     'public_http_watch', 'http_watch', 'probe', 'copy_limit_probe', 'mutation_limit_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(session_tools_probe=True, **{flag: True})
        for flag in ('session_tools_probe', 'public_session_renewal_probe', 'session_renewal_probe',
                     'probe', 'copy_limit_probe', 'http_watch_probe', 'public_http_watch_probe',
                     'mutation_limit_probe', 'grid_write_probe', 'public_copy_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(session_tools=True, **{flag: True})

    def test_session_tools_source_manifest_includes_neutral_input_builder_and_native_tests(self):
        hashes = fixture.source_hashes()
        for name in ('internal/db/contract/kv_request_tools.go',
                     'internal/services/consul/kv_session_tools.go',
                     'internal/services/consul/kv_session_tools_test.go',
                     'internal/services/consul/kv_session_tools_native_integration_test.go'):
            self.assertIn(name, hashes)

    def test_public_session_renewal_keeps_foundation_regression_and_requires_real_workspaces(self):
        foundation = self.copy_scope_before_startup(session_renewal=True, txn_max_request=8388608)
        report = self.copy_scope_before_startup(public_session_renewal=True, txn_max_request=8388608)
        required = tuple(foundation['required']) + fixture.PUBLIC_SESSION_RENEWAL_REQUIRED
        self.assertEqual(report['required'], list(required))
        self.assertEqual(len(set(required)), 108)
        self.assertEqual(report['scope'], 'public-consul-original-session-renewal')
        self.assertTrue(report['session_renewal'])
        self.assertTrue(report['public_http_watch'])
        self.assertIn('./internal/ui/kvworkspace', report['packages'])
        self.assertIn('./internal/ui/shell', report['packages'])
        self.assertIn('./internal/ui/dataworkspace', report['packages'])
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        self.assertEqual(fixture.accepted_cases(log, required), list(required))
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)
        with self.assertRaises(RuntimeError):
            fixture.accepted_cases(log, tuple(foundation['required']))
        for name in ('internal/ui/kvworkspace/http_session_renewal_state.go',
                     'internal/ui/kvworkspace/http_session_renewal_view.go',
                     'internal/ui/kvworkspace/consul_http_session_renewal_native_integration_test.go',
                     'internal/ui/shell/consul_http_session_renewal_native_integration_test.go'):
            self.assertIn(name, fixture.source_hashes())

    def test_public_session_renewal_diagnostic_cannot_claim_full_acceptance(self):
        report = self.copy_scope_before_startup(public_session_renewal_probe=True)
        self.assertEqual(report['scope'], 'diagnostic-consul-public-session-renewal')
        self.assertEqual(report['required'], list(fixture.PUBLIC_SESSION_RENEWAL_REQUIRED))
        self.assertEqual(report['packages'], ['./internal/ui/kvworkspace', './internal/ui/shell'])
        for flag in ('public_session_renewal', 'session_renewal', 'session_renewal_probe', 'public_http_watch',
                     'http_watch', 'http_watch_probe', 'public_prefix_set', 'public_grid', 'public', 'probe',
                     'public_http_watch_probe', 'copy_limit_probe', 'mutation_limit_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(public_session_renewal_probe=True, **{flag: True})
        for flag in ('session_renewal_probe', 'probe', 'http_watch_probe', 'public_http_watch_probe', 'copy_limit_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(public_session_renewal=True, **{flag: True})

    def test_public_session_renewal_image_manifest_requires_bilingual_controls_and_original_faults(self):
        names = fixture.session_renewal_image_names()
        self.assertEqual(len(names), 128)
        for lang in ('zh-Hans', 'en'):
            for size in ('1280x800', '720x520'):
                for stage in ('form', 'confirmation', 'running', 'initial-read', 'stopped', 'cleared',
                              'overflow', 'missing', 'read-denied', 'write-denied', 'parked', 'recovered'):
                    self.assertIn(f'{lang}-{size}-workspace-session-renewal-{stage}.png', names)
                for mode in ('lost', 'partial', 'http500', 'http408', 'uuid', 'cancel', 'replace', 'parked'):
                    for prefix in ('unknown-', 'after-read-'):
                        self.assertIn(f'{lang}-{size}-workspace-session-renewal-{prefix}{mode}.png', names)
                for stage in ('confirmation', 'running', 'stopped', 'recovered'):
                    self.assertIn(f'{lang}-{size}-shell-session-renewal-{stage}.png', names)

    def copy_scope_before_startup(self, **options):
        # Stop before the first owned container can be created. The retained
        # report must still name the exact scope, parents and packages selected.
        import json
        import tempfile
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            with patch.object(fixture, 'ROOT', root), patch.object(fixture, 'source_hashes', return_value={}), patch.object(fixture, 'command', side_effect=RuntimeError('owned startup intentionally unavailable')):
                with self.assertRaisesRegex(RuntimeError, 'owned startup intentionally unavailable'):
                    fixture.verify(**options)
            paths = list((root/'build/consul').glob('owned-fixture-*.json'))
            self.assertEqual(len(paths), 1)
            report = json.loads(paths[0].read_text())
            self.assertFalse(report['passed'])
            self.assertTrue(report['private_files_removed'])
            self.assertNotIn('container_id', report)
            return report

    def test_copy_foundation_retains_all_public_write_regression_and_native_copy_parents(self):
        report = self.copy_scope_before_startup(key_copy=True, txn_max_request=8388608)
        required = (fixture.REQUIRED + fixture.DOCUMENT_REQUIRED + fixture.RUNTIME_REQUIRED +
                    fixture.PUBLIC_REQUIRED + fixture.TOOLS_REQUIRED + fixture.DATA_REQUIRED +
                    fixture.MUTATION_REQUIRED + fixture.GRID_REQUIRED + fixture.WRITE_GRID_REQUIRED +
                    fixture.COPY_REQUIRED)
        self.assertEqual(report['required'], list(required))
        self.assertEqual(len(required), 56)
        self.assertEqual(len(set(required)), 56)
        self.assertEqual(report['scope'], 'private-consul-original-conditional-copy-with-public-write-regression')
        self.assertTrue(report['public_grid_writes'])
        self.assertIn('./internal/ui/dataworkspace', report['packages'])
        self.assertIn('./internal/ui/shell', report['packages'])
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        self.assertEqual(fixture.accepted_cases(log, required), list(required))
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)
        for name in ('internal/db/contract/data_key_copy.go', 'internal/services/consul/data_key_copy.go',
                     'internal/services/consul/data_key_copy_native_integration_test.go',
                     'internal/services/dbruntime/data_key_copy_test.go'):
            self.assertIn(name, fixture.source_hashes())

    def test_copy_diagnostic_and_real_default_limit_use_only_the_original_adapter(self):
        diagnostic = self.copy_scope_before_startup(copy_probe=True, txn_max_request=8388608)
        limit = self.copy_scope_before_startup(copy_limit_probe=True)
        self.assertEqual(diagnostic['required'], list(fixture.COPY_REQUIRED))
        self.assertEqual(diagnostic['scope'], 'diagnostic-consul-conditional-copy-probe')
        self.assertEqual(limit['required'], [fixture.COPY_REQUIRED[-1]])
        self.assertEqual(limit['scope'], 'private-consul-original-copy-request-limit')
        self.assertEqual(limit['txn_max_request'], 524288)
        for report in (diagnostic, limit):
            self.assertEqual(report['packages'], ['./internal/services/consul'])
            self.assertFalse(report['public_grid_writes'])
            self.assertFalse(report['key_copy'])

    def test_copy_scopes_reject_downgrading_complete_regression_or_default_server_limit(self):
        for flag in ('copy_probe', 'copy_limit_probe'):
            for public_flag in ('probe', 'session', 'public', 'tools', 'data_session', 'public_grid',
                                'data_mutations', 'mutation_limit_probe', 'public_grid_writes', 'grid_write_probe'):
                with self.subTest(flag=flag, public_flag=public_flag), self.assertRaises(ValueError):
                    fixture.verify(**{flag: True, public_flag: True})
        for other in ('copy_probe', 'copy_limit_probe', 'probe', 'mutation_limit_probe', 'grid_write_probe'):
            with self.subTest(other=other), self.assertRaises(ValueError):
                fixture.verify(key_copy=True, **{other: True})
        with self.assertRaises(ValueError):
            fixture.verify(copy_limit_probe=True, txn_max_request=8388608)

    def test_public_copy_acceptance_requires_original_source_runtime_shell_and_every_regression(self):
        private = self.copy_scope_before_startup(key_copy=True, txn_max_request=8388608)
        public = self.copy_scope_before_startup(public_key_copy=True, txn_max_request=8388608)
        required = tuple(private['required']) + fixture.PUBLIC_COPY_REQUIRED
        self.assertEqual(public['scope'], 'public-consul-original-conditional-copy')
        self.assertEqual(public['required'], list(required))
        self.assertEqual(len(required), 62)
        self.assertEqual(len(set(required)), 62)
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        self.assertEqual(fixture.accepted_cases(log, required), list(required))
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)
        with self.assertRaises(RuntimeError):
            fixture.accepted_cases(log, tuple(private['required']))

    def test_public_copy_diagnostic_retains_exact_public_parent_set_and_original_packages(self):
        report = self.copy_scope_before_startup(public_copy_probe=True, txn_max_request=8388608)
        self.assertEqual(report['required'], list(fixture.PUBLIC_COPY_REQUIRED))
        self.assertEqual(report['scope'], 'diagnostic-consul-public-key-copy-probe')
        self.assertEqual(report['packages'], ['./internal/services/dbruntime', './internal/ui/shell', './internal/ui/dataworkspace'])
        self.assertFalse(report['public_key_copy'])
        self.assertFalse(report['key_copy'])

    def test_public_copy_cannot_replace_complete_acceptance_with_partial_diagnostics(self):
        for flag in ('probe', 'session', 'public', 'tools', 'data_session', 'public_grid', 'data_mutations',
                     'mutation_limit_probe', 'public_grid_writes', 'grid_write_probe', 'key_copy',
                     'copy_probe', 'copy_limit_probe', 'public_key_copy'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(public_copy_probe=True, **{flag: True})
        for flag in ('probe', 'mutation_limit_probe', 'grid_write_probe', 'copy_probe', 'copy_limit_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(public_key_copy=True, **{flag: True})

    def test_prefix_candidate_acceptance_retains_every_public_copy_regression(self):
        public = self.copy_scope_before_startup(public_key_copy=True, txn_max_request=8388608)
        prefix = self.copy_scope_before_startup(prefix_delete=True, txn_max_request=8388608)
        required = tuple(public['required']) + fixture.PREFIX_DELETE_REQUIRED
        self.assertEqual(prefix['scope'], 'private-consul-original-prefix-candidate-delete-with-public-copy-regression')
        self.assertEqual(prefix['required'], list(required))
        self.assertEqual(len(required), 67)
        self.assertEqual(len(set(required)), 67)
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        self.assertEqual(fixture.accepted_cases(log, required), list(required))
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)
        with self.assertRaises(RuntimeError):
            fixture.accepted_cases(log, tuple(public['required']))
        hashes = fixture.source_hashes()
        for name in ('internal/db/contract/data_key_prefix_delete.go',
                     'internal/services/consul/data_key_prefix_delete.go',
                     'internal/services/consul/data_key_prefix_delete_native_integration_test.go',
                     'internal/services/dbruntime/data_key_prefix_delete.go'):
            self.assertIn(name, hashes)

    def test_prefix_candidate_diagnostic_cannot_claim_full_regression(self):
        report = self.copy_scope_before_startup(prefix_delete_probe=True, txn_max_request=8388608)
        self.assertEqual(report['required'], list(fixture.PREFIX_DELETE_REQUIRED))
        self.assertEqual(report['scope'], 'diagnostic-consul-prefix-candidate-delete')
        self.assertEqual(report['packages'], ['./internal/services/consul'])
        self.assertFalse(report['prefix_delete'])
        self.assertFalse(report['public_key_copy'])
        for flag in ('probe', 'session', 'public', 'tools', 'data_session', 'public_grid',
                     'data_mutations', 'mutation_limit_probe', 'public_grid_writes',
                     'grid_write_probe', 'key_copy', 'copy_probe', 'copy_limit_probe',
                     'public_key_copy', 'public_copy_probe', 'prefix_delete'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(prefix_delete_probe=True, **{flag: True})
        for flag in ('probe', 'mutation_limit_probe', 'grid_write_probe', 'copy_probe',
                     'copy_limit_probe', 'public_copy_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(prefix_delete=True, **{flag: True})

    def test_prefix_candidate_cli_flags_reach_scope_validation_before_startup(self):
        import runpy
        from unittest.mock import patch
        for args in (['--prefix-delete-probe', '--probe'], ['--prefix-delete', '--copy-probe']):
            with self.subTest(args=args), patch('sys.argv', [str(ROOT/'tools/verify_consul_fixture.py'), *args]), patch('subprocess.run', side_effect=AssertionError('CLI started a fixture before validating prefix scope')), self.assertRaises(ValueError):
                runpy.run_path(str(ROOT/'tools/verify_consul_fixture.py'), run_name='__main__')

    def test_public_prefix_acceptance_retains_every_original_parent(self):
        private = self.copy_scope_before_startup(prefix_delete=True, txn_max_request=8388608)
        public = self.copy_scope_before_startup(public_prefix_delete=True, txn_max_request=8388608)
        required = tuple(private['required']) + fixture.PUBLIC_PREFIX_DELETE_REQUIRED
        self.assertEqual(public['scope'], 'public-consul-original-captured-prefix-candidate-delete')
        self.assertEqual(public['required'], list(required))
        self.assertEqual(len(required), 73)
        self.assertEqual(len(set(required)), 73)
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        self.assertEqual(fixture.accepted_cases(log, required), list(required))
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)
        with self.assertRaises(RuntimeError):
            fixture.accepted_cases(log, tuple(private['required']))

    def test_public_prefix_diagnostic_and_acceptance_scopes_are_distinct(self):
        report = self.copy_scope_before_startup(public_prefix_probe=True, txn_max_request=8388608)
        self.assertEqual(report['required'], list(fixture.PUBLIC_PREFIX_DELETE_REQUIRED))
        self.assertEqual(report['scope'], 'diagnostic-consul-public-prefix-candidate-delete')
        self.assertEqual(report['packages'], ['./internal/services/dbruntime', './internal/ui/shell', './internal/ui/dataworkspace'])
        self.assertFalse(report['public_prefix_delete'])
        for flag in ('probe', 'session', 'public', 'tools', 'data_session', 'public_grid', 'data_mutations',
                     'mutation_limit_probe', 'public_grid_writes', 'grid_write_probe', 'key_copy', 'copy_probe',
                     'copy_limit_probe', 'public_key_copy', 'public_copy_probe', 'prefix_delete',
                     'prefix_delete_probe', 'public_prefix_delete'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(public_prefix_probe=True, **{flag: True})
        for flag in ('probe', 'mutation_limit_probe', 'grid_write_probe', 'copy_probe',
                     'copy_limit_probe', 'public_copy_probe', 'prefix_delete_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(public_prefix_delete=True, **{flag: True})

    def test_public_prefix_cli_flags_reach_scope_validation_before_startup(self):
        import runpy
        from unittest.mock import patch
        for args in (['--public-prefix-probe', '--probe'], ['--public-prefix-delete', '--copy-probe']):
            with self.subTest(args=args), patch('sys.argv', [str(ROOT/'tools/verify_consul_fixture.py'), *args]), patch('subprocess.run', side_effect=AssertionError('CLI started fixture before validating public prefix scope')), self.assertRaises(ValueError):
                runpy.run_path(str(ROOT/'tools/verify_consul_fixture.py'), run_name='__main__')

    def test_prefix_set_acceptance_keeps_every_public_parent_and_original_batch_case(self):
        public = self.copy_scope_before_startup(public_prefix_delete=True, txn_max_request=8388608)
        batches = self.copy_scope_before_startup(prefix_delete_set=True, txn_max_request=8388608, test_timeout=1800, memory_mib=2048)
        required = tuple(public['required']) + fixture.PREFIX_SET_REQUIRED
        self.assertEqual(batches['required'], list(required))
        self.assertEqual(len(required), 78)
        self.assertEqual(len(set(required)), 78)
        self.assertEqual(batches['scope'], 'private-consul-original-prefix-explicit-batches-with-public-prefix-regression')
        self.assertEqual(batches['test_timeout'], 1800)
        self.assertEqual(batches['memory_mib'], 2048)
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)

    def test_prefix_set_diagnostic_cannot_replace_complete_regression(self):
        report = self.copy_scope_before_startup(prefix_set_probe=True, txn_max_request=8388608)
        self.assertEqual(report['required'], list(fixture.PREFIX_SET_REQUIRED))
        self.assertEqual(report['packages'], ['./internal/services/consul'])
        self.assertEqual(report['scope'], 'diagnostic-consul-original-prefix-explicit-batch-foundation')
        for flag in ('probe', 'session', 'public', 'tools', 'data_session', 'public_grid', 'data_mutations',
                     'mutation_limit_probe', 'public_grid_writes', 'grid_write_probe', 'key_copy', 'copy_probe',
                     'copy_limit_probe', 'public_key_copy', 'public_copy_probe', 'prefix_delete',
                     'prefix_delete_probe', 'public_prefix_delete', 'public_prefix_probe', 'prefix_delete_set'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(prefix_set_probe=True, **{flag: True})
        for flag in ('probe', 'mutation_limit_probe', 'grid_write_probe', 'copy_probe', 'copy_limit_probe',
                     'public_copy_probe', 'prefix_delete_probe', 'public_prefix_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(prefix_delete_set=True, **{flag: True})
        for timeout in (-1, 0, 59, 3601):
            with self.subTest(timeout=timeout), self.assertRaises(ValueError):
                fixture.verify(prefix_set_probe=True, test_timeout=timeout)

    def test_prefix_set_cli_validates_scope_before_startup(self):
        import runpy
        from unittest.mock import patch
        for args in (['--prefix-set-probe', '--probe'], ['--prefix-delete-set', '--copy-probe'], ['--prefix-delete-set', '--test-timeout', '0']):
            with self.subTest(args=args), patch('sys.argv', [str(ROOT/'tools/verify_consul_fixture.py'), *args]), patch('subprocess.run', side_effect=AssertionError('CLI started fixture before validating batch scope')), self.assertRaises(ValueError):
                runpy.run_path(str(ROOT/'tools/verify_consul_fixture.py'), run_name='__main__')

    def test_public_prefix_set_acceptance_retains_all_78_parents_and_adds_public_ui(self):
        private = self.copy_scope_before_startup(prefix_delete_set=True)
        public = self.copy_scope_before_startup(public_prefix_set=True)
        required = tuple(private['required']) + fixture.PUBLIC_PREFIX_SET_REQUIRED
        self.assertEqual(public['scope'], 'public-consul-original-prefix-explicit-batches')
        self.assertEqual(public['required'], list(required))
        self.assertEqual(len(required), 84)
        self.assertEqual(len(set(required)), 84)
        self.assertIn('./internal/ui/dataworkspace', public['packages'])
        self.assertIn('./internal/ui/shell', public['packages'])
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        self.assertEqual(fixture.accepted_cases(log, required), list(required))
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)

    def test_public_prefix_set_diagnostic_keeps_scope_and_cannot_claim_acceptance(self):
        report = self.copy_scope_before_startup(public_prefix_set_probe=True)
        self.assertEqual(report['scope'], 'diagnostic-consul-public-prefix-explicit-batches')
        self.assertEqual(report['required'], list(fixture.PUBLIC_PREFIX_SET_REQUIRED))
        self.assertEqual(report['packages'], ['./internal/services/dbruntime', './internal/ui/shell', './internal/ui/dataworkspace'])
        for flag in ('probe', 'session', 'public', 'tools', 'data_session', 'public_grid', 'data_mutations',
                     'mutation_limit_probe', 'public_grid_writes', 'grid_write_probe', 'key_copy', 'copy_probe',
                     'copy_limit_probe', 'public_key_copy', 'public_copy_probe', 'prefix_delete',
                     'prefix_delete_probe', 'public_prefix_delete', 'public_prefix_probe', 'prefix_delete_set',
                     'prefix_set_probe', 'public_prefix_set'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(public_prefix_set_probe=True, **{flag: True})

    def test_public_prefix_set_cannot_downgrade_retained_parents_or_timeout(self):
        for flag in ('probe', 'mutation_limit_probe', 'grid_write_probe', 'copy_probe', 'copy_limit_probe',
                     'public_copy_probe', 'prefix_delete_probe', 'public_prefix_probe', 'prefix_set_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(public_prefix_set=True, **{flag: True})
        report = self.copy_scope_before_startup(public_prefix_set=True, test_timeout=1800, memory_mib=2048)
        self.assertEqual(report['test_timeout'], 1800)
        self.assertEqual(report['memory_mib'], 2048)
        for timeout in (-1, 0, 59, 3601):
            with self.subTest(timeout=timeout), self.assertRaises(ValueError):
                fixture.verify(public_prefix_set=True, test_timeout=timeout)

    def test_public_prefix_set_source_manifest_includes_new_native_and_ui_ownership(self):
        hashes = fixture.source_hashes()
        for name in ('internal/ui/dataworkspace/data_key_prefix_delete_set.go',
                     'internal/ui/dataworkspace/data_key_prefix_delete_set_view.go',
                     'internal/ui/dataworkspace/data_key_prefix_delete_set_test.go',
                     'internal/ui/dataworkspace/data_key_prefix_delete_set_view_test.go',
                     'internal/ui/dataworkspace/consul_key_prefix_delete_set_native_integration_test.go',
                     'internal/services/dbruntime/consul_key_prefix_delete_set_native_integration_test.go',
                     'internal/ui/shell/consul_key_prefix_delete_set_native_integration_test.go'):
            self.assertIn(name, hashes)

    def test_public_prefix_set_cli_rejects_invalid_scope_before_fixture_startup(self):
        import runpy
        from unittest.mock import patch
        for args in (['--public-prefix-set-probe', '--probe'], ['--public-prefix-set', '--prefix-set-probe'],
                     ['--public-prefix-set', '--copy-limit-probe'], ['--public-prefix-set', '--test-timeout', '0']):
            with self.subTest(args=args), patch('sys.argv', [str(ROOT/'tools/verify_consul_fixture.py'), *args]), patch('subprocess.run', side_effect=AssertionError('CLI started fixture before validating public batch scope')), self.assertRaises(ValueError):
                runpy.run_path(str(ROOT/'tools/verify_consul_fixture.py'), run_name='__main__')


class ConsulSessionRenewalFixtureTests(unittest.TestCase):
    def test_native_renewal_parents_cannot_skip_recovery_acl_or_reply_faults(self):
        required = fixture.SESSION_RENEWAL_REQUIRED
        self.assertEqual(len(set(required)), 7)
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        self.assertEqual(fixture.accepted_cases(log, required), list(required))
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ',
                                                   '--- SKIP: TestConsulNative'+name+' '), required)
        for flags in ({'session_renewal':True}, {'public_http_watch':True}, {'http_watch_probe':True}):
            with self.assertRaises(ValueError):
                fixture.verify(session_renewal_probe=True, **flags)
        with self.assertRaises(ValueError):
            fixture.verify(session_renewal=True, probe=True)
        hashes = fixture.source_hashes()
        for name in ('internal/db/contract/kv_http_session_renewal.go',
                     'internal/services/consul/http_session_renewal_execute.go',
                     'internal/services/consul/http_session_renewal_native_integration_test.go',
                     'internal/services/dbruntime/consul_http_session_renewal_native_integration_test.go'):
            self.assertIn(name, hashes)
