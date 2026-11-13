import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('verify_consul_fixture', ROOT/'tools/verify_consul_fixture.py')
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)


class ConsulFixtureTests(unittest.TestCase):
    def test_public_agent_check_retains_full_native_service_browser_regression(self):
        previous = self.copy_scope_before_startup(agent_check_probe=True)
        report = self.copy_scope_before_startup(public_agent_check_probe=True)
        required = fixture.PUBLIC_AGENT_CHECK_REQUIRED + tuple(previous['required'])
        self.assertEqual(tuple(report['required']), required)
        self.assertEqual(len(required), 55)
        self.assertEqual(len(set(required)), 55)
        self.assertEqual(report['scope'], 'public-consul-original-agent-check-tools-with-service-browser-regression')
        self.assertTrue(report['production_agent_check_tools_published'])
        self.assertTrue(report['production_agent_service_tools_published'])
        self.assertEqual(len(fixture.agent_check_image_names()), 292)
        self.assertEqual(len(fixture.agent_service_image_names()), 264)
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        self.assertEqual(fixture.accepted_cases(log, required), list(required))
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)

    def test_public_agent_check_schedules_every_original_parent_without_health_contamination(self):
        import re
        report = self.copy_scope_before_startup(public_agent_check_probe=True)
        required = tuple(report['required'])
        phases = fixture.agent_check_acceptance_test_phases(report['packages'], required, True, 1200, True)
        self.assertEqual(len(phases), 3)
        scheduled = [re.findall(r'TestConsulNative(\w+)', phase[phase.index('-run')+1]) for phase in phases]
        self.assertEqual(len(scheduled[0]), 46)
        self.assertCountEqual(scheduled[1], fixture.AGENT_CHECK_REQUIRED)
        self.assertCountEqual(scheduled[2], fixture.PUBLIC_AGENT_CHECK_REQUIRED)
        self.assertCountEqual(sum(scheduled, []), required)
        for phase in phases:
            for argument in ('-race', '-count=1', '-timeout=1200s'):
                self.assertIn(argument, phase)
        with self.assertRaises(ValueError):
            fixture.agent_check_acceptance_test_phases(report['packages'], required[:-1], public=True)

    def test_public_agent_check_rejects_every_mixed_scope_before_startup(self):
        import inspect
        from unittest.mock import patch
        flags = [name for name, parameter in inspect.signature(fixture.verify).parameters.items()
                 if parameter.default is False and name not in ('race', 'public_agent_check_probe')]
        for flag in flags:
            with self.subTest(flag=flag), patch('subprocess.run', side_effect=AssertionError('started mixed check fixture')), self.assertRaises(ValueError):
                fixture.verify(public_agent_check_probe=True, **{flag: True})

    def test_agent_check_foundation_retains_every_accepted_public_service_parent(self):
        previous = self.copy_scope_before_startup(public_agent_service_probe=True)
        report = self.copy_scope_before_startup(agent_check_probe=True)
        required = fixture.AGENT_CHECK_REQUIRED + tuple(previous['required'])
        self.assertEqual(tuple(report['required']), required)
        self.assertEqual(len(required), 51)
        self.assertEqual(len(required), len(set(required)))
        self.assertFalse(report['production_agent_check_tools_published'])
        self.assertTrue(report['production_agent_service_tools_published'])
        self.assertEqual(report['scope'], 'private-consul-native-agent-check-foundation-with-public-service-browser-regression')
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)

    def test_agent_check_foundation_phases_keep_health_before_critical_node_checks(self):
        report = self.copy_scope_before_startup(agent_check_probe=True)
        required = tuple(report['required'])
        phases = fixture.agent_check_acceptance_test_phases(report['packages'], required, True, 1200)
        self.assertEqual(len(phases), 2)
        first, second = phases
        self.assertIn('./internal/ui/kvworkspace', first)
        self.assertIn('./internal/ui/shell', first)
        self.assertNotIn('./internal/ui/shell', second)
        for name in fixture.AGENT_CHECK_REQUIRED:
            self.assertNotIn('TestConsulNative'+name+'|', first[first.index('-run')+1])
            self.assertIn('TestConsulNative'+name, second[second.index('-run')+1])
        import re
        scheduled = [name for phase in phases for name in re.findall(r'TestConsulNative(\w+)', phase[phase.index('-run')+1])]
        self.assertCountEqual(scheduled, required)
        self.assertEqual(len(scheduled), len(set(scheduled)))
        for phase in phases:
            self.assertIn('-race', phase)
            self.assertIn('-count=1', phase)
            self.assertIn('-timeout=1200s', phase)
        with self.assertRaises(ValueError):
            fixture.agent_check_acceptance_test_phases(report['packages'], required[:-1])

    def test_agent_check_foundation_rejects_every_mixed_scope_before_startup(self):
        import inspect
        from unittest.mock import patch
        flags = [name for name, parameter in inspect.signature(fixture.verify).parameters.items()
                 if parameter.default is False and name not in ('race', 'agent_check_probe')]
        for flag in flags:
            with self.subTest(flag=flag), patch('subprocess.run', side_effect=AssertionError('started mixed check foundation fixture')), self.assertRaises(ValueError):
                fixture.verify(agent_check_probe=True, **{flag: True})

    def test_public_agent_service_requires_real_ui_faults_shell_and_original_regression(self):
        previous = self.copy_scope_before_startup(agent_service_probe=True)
        report = self.copy_scope_before_startup(public_agent_service_probe=True)
        required = fixture.PUBLIC_AGENT_SERVICE_REQUIRED + tuple(previous['required'])
        self.assertEqual(tuple(report['required']), required)
        self.assertEqual(len(required), len(set(required)))
        self.assertEqual(len(required), 46)
        self.assertEqual(len(fixture.agent_service_image_names()), 264)
        self.assertTrue(report['public_agent_service_probe'])
        self.assertTrue(report['production_agent_writes_published'])
        self.assertEqual(report['scope'], 'public-consul-original-agent-service-tools-with-browser-regression')
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)

    def test_public_agent_service_rejects_every_mixed_scope_before_startup(self):
        import inspect
        from unittest.mock import patch
        flags = [name for name, parameter in inspect.signature(fixture.verify).parameters.items()
                 if parameter.default is False and name not in ('race', 'public_agent_service_probe')]
        for flag in flags:
            with self.subTest(flag=flag), patch('subprocess.run', side_effect=AssertionError('started mixed public Agent service fixture')), self.assertRaises(ValueError):
                fixture.verify(public_agent_service_probe=True, **{flag: True})

    def test_agent_service_native_requires_original_runtime_generations_and_no_replay(self):
        report = self.copy_scope_before_startup(agent_service_native_probe=True)
        required = fixture.AGENT_SERVICE_REQUIRED + fixture.AGENT_SERVICE_RUNTIME_REQUIRED + fixture.AGENT_SERVICE_EVIDENCE_REQUIRED
        self.assertEqual(tuple(report['required']), required)
        self.assertEqual(report['packages'], ['./internal/services/consul', './internal/services/dbruntime'])
        self.assertFalse(report['production_agent_writes_published'])
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)

    def test_agent_service_foundation_retains_all_original_browser_regressions(self):
        previous = self.copy_scope_before_startup(agent_browser_probe=True)
        report = self.copy_scope_before_startup(agent_service_probe=True)
        required = fixture.AGENT_SERVICE_REQUIRED + fixture.AGENT_SERVICE_RUNTIME_REQUIRED + fixture.AGENT_SERVICE_EVIDENCE_REQUIRED + tuple(previous['required'])
        self.assertEqual(tuple(report['required']), required)
        self.assertEqual(len(required), len(set(required)))
        self.assertFalse(report['production_agent_writes_published'])
        self.assertIn('./internal/services/dbruntime', report['packages'])

    def test_agent_service_scopes_reject_mixed_fixture_modes_before_startup(self):
        import inspect
        from unittest.mock import patch
        for scope in ('agent_service_probe', 'agent_service_native_probe'):
            flags = [name for name, parameter in inspect.signature(fixture.verify).parameters.items()
                     if parameter.default is False and name not in ('race', scope)]
            for flag in flags:
                with self.subTest(scope=scope, flag=flag), patch('subprocess.run', side_effect=AssertionError('started mixed Agent service fixture')), self.assertRaises(ValueError):
                    fixture.verify(**{scope: True, flag: True})

    def test_agent_service_evidence_requires_native_parents_without_public_claims(self):
        report = self.copy_scope_before_startup(agent_service_evidence_probe=True)
        self.assertEqual(report['scope'], 'diagnostic-consul-original-agent-service-evidence-only')
        self.assertTrue(report['agent_service_evidence_probe'])
        self.assertFalse(report['production_agent_writes_published'])
        self.assertEqual(tuple(report['required']), fixture.AGENT_SERVICE_EVIDENCE_REQUIRED)
        self.assertEqual(report['packages'], ['./internal/services/consul'])
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in fixture.AGENT_SERVICE_EVIDENCE_REQUIRED)
        fixture.accepted_cases(log, fixture.AGENT_SERVICE_EVIDENCE_REQUIRED)
        for name in fixture.AGENT_SERVICE_EVIDENCE_REQUIRED:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), fixture.AGENT_SERVICE_EVIDENCE_REQUIRED)

    def test_agent_service_evidence_rejects_every_other_scope_before_startup(self):
        import inspect
        from unittest.mock import patch
        flags = [name for name, parameter in inspect.signature(fixture.verify).parameters.items()
                 if parameter.default is False and name not in ('race', 'agent_service_evidence_probe')]
        for flag in flags:
            with self.subTest(flag=flag), patch('subprocess.run', side_effect=AssertionError('started mixed Agent evidence fixture')), self.assertRaises(ValueError):
                fixture.verify(agent_service_evidence_probe=True, **{flag: True})

    def test_agent_browser_scope_requires_public_ui_and_all_original_regression(self):
        report = self.copy_scope_before_startup(agent_browser_probe=True)
        expected = fixture.AGENT_BROWSER_REQUIRED + fixture.AGENT_READ_REQUIRED + fixture.HEALTH_WATCH_UI_REQUIRED + fixture.HEALTH_WATCH_REQUIRED + fixture.HEALTH_PROJECTION_REQUIRED + fixture.HEALTH_BROWSER_REQUIRED + fixture.HTTP_WATCH_REQUIRED + fixture.PUBLIC_HTTP_WATCH_REQUIRED
        self.assertEqual(report['scope'], 'public-consul-original-agent-browser-with-health-regression')
        self.assertTrue(report['agent_browser_probe'])
        self.assertTrue(report['agent_projection_probe'])
        self.assertTrue(report['agent_read_probe'])
        self.assertEqual(tuple(report['required']), expected)
        self.assertEqual(len(expected), 32)
        self.assertEqual(len(fixture.agent_browser_image_names()), 68)
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in expected)
        self.assertEqual(fixture.accepted_cases(log, expected), list(expected))
        for name in fixture.AGENT_BROWSER_REQUIRED:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), expected)

    def test_agent_browser_scope_rejects_mixed_modes_before_fixture_creation(self):
        for flag in ('agent_projection_probe', 'agent_read_probe', 'health_watch_ui_probe', 'health_watch_probe', 'health_browser_probe', 'health_projection', 'health_projection_probe', 'public_session_destroy', 'public', 'session', 'probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(agent_browser_probe=True, **{flag: True})

    def test_agent_projection_scope_retains_original_health_and_read_parents(self):
        report = self.copy_scope_before_startup(agent_projection_probe=True)
        expected = fixture.AGENT_READ_REQUIRED + fixture.HEALTH_WATCH_UI_REQUIRED + fixture.HEALTH_WATCH_REQUIRED + fixture.HEALTH_PROJECTION_REQUIRED + fixture.HEALTH_BROWSER_REQUIRED + fixture.HTTP_WATCH_REQUIRED + fixture.PUBLIC_HTTP_WATCH_REQUIRED
        self.assertEqual(report['scope'], 'focused-consul-original-agent-projection-with-health-regression')
        self.assertTrue(report['agent_projection_probe'])
        self.assertTrue(report['agent_read_probe'])
        self.assertEqual(report['required'], list(expected))
        self.assertEqual(len(expected), 29)
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in expected)
        self.assertEqual(fixture.accepted_cases(log, expected), list(expected))
        for name in expected:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), expected)

    def test_agent_projection_scope_rejects_mixed_modes_before_fixture_creation(self):
        for flag in ('agent_read_probe', 'health_watch_ui_probe', 'health_watch_probe', 'health_browser_probe', 'health_projection', 'health_projection_probe', 'public_session_destroy', 'public', 'session', 'probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(agent_projection_probe=True, **{flag: True})

    def test_agent_read_scope_retains_all_health_parents_and_rejects_skips(self):
        previous = self.copy_scope_before_startup(health_watch_ui_probe=True)
        report = self.copy_scope_before_startup(agent_read_probe=True)
        required = fixture.AGENT_READ_REQUIRED + tuple(previous['required'])
        self.assertEqual(report['scope'], 'focused-consul-original-agent-reads-with-health-regression')
        self.assertEqual(tuple(report['required']), required)
        self.assertEqual(len(set(required)), 29)
        self.assertTrue(report['agent_read_probe'])
        self.assertTrue(report['health_watch_ui_probe'])
        log = '\n'.join('--- PASS: TestConsulNative' + name + ' (0.1s)' for name in required)
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative' + name + ' ', '--- SKIP: TestConsulNative' + name + ' '), required)

    def test_agent_read_scope_rejects_mixed_modes_before_fixture_creation(self):
        import inspect
        from unittest.mock import patch
        flags = [name for name, parameter in inspect.signature(fixture.verify).parameters.items()
                 if parameter.default is False and name not in ('race', 'agent_read_probe')]
        for flag in flags:
            with self.subTest(flag=flag), patch('subprocess.run', side_effect=AssertionError('started mixed Agent fixture')), self.assertRaises(ValueError):
                fixture.verify(agent_read_probe=True, **{flag: True})

    def test_health_watch_ui_requires_every_original_parent_and_distinct_images(self):
        report = self.copy_scope_before_startup(health_watch_ui_probe=True)
        required = fixture.HEALTH_WATCH_UI_REQUIRED + fixture.HEALTH_WATCH_REQUIRED + fixture.HEALTH_PROJECTION_REQUIRED + fixture.HEALTH_BROWSER_REQUIRED + fixture.HTTP_WATCH_REQUIRED + fixture.PUBLIC_HTTP_WATCH_REQUIRED
        self.assertEqual(report['scope'], 'focused-consul-public-health-watch')
        self.assertEqual(tuple(report['required']), required)
        self.assertEqual(len(set(required)), 26)
        self.assertTrue(report['health_watch_ui_probe'])
        self.assertFalse(report['health_watch_probe'])
        self.assertEqual(len(fixture.health_watch_ui_image_names()), 84)
        self.assertFalse(fixture.health_watch_ui_image_names() & fixture.health_browser_image_names())
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)

    def test_health_watch_ui_rejects_mixed_scopes_before_fixture_creation(self):
        import inspect
        from unittest.mock import patch
        flags = [name for name, parameter in inspect.signature(fixture.verify).parameters.items()
                 if parameter.default is False and name not in ('race', 'health_watch_ui_probe')]
        for flag in flags:
            with self.subTest(flag=flag), patch('subprocess.run', side_effect=AssertionError('started mixed health Watch UI fixture')), self.assertRaises(ValueError):
                fixture.verify(health_watch_ui_probe=True, **{flag: True})

    def test_health_watch_scope_includes_original_health_and_http_watch_recovery(self):
        report = self.copy_scope_before_startup(health_watch_probe=True)
        required = fixture.HEALTH_WATCH_REQUIRED + fixture.HEALTH_PROJECTION_REQUIRED + fixture.HTTP_WATCH_REQUIRED + fixture.PUBLIC_HTTP_WATCH_REQUIRED
        self.assertEqual(report['scope'], 'focused-consul-native-health-watch')
        self.assertTrue(report['health_watch_probe'])
        self.assertEqual(tuple(report['required']), required)
        self.assertEqual(len(set(required)), 20)
        self.assertFalse(report['public_session_destroy'])
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)

    def test_health_watch_rejects_mixed_feature_scopes_before_startup(self):
        import inspect
        from unittest.mock import patch
        flags = [name for name, parameter in inspect.signature(fixture.verify).parameters.items()
                 if parameter.default is False and name not in ('race', 'health_watch_probe')]
        for flag in flags:
            with self.subTest(flag=flag), patch('subprocess.run', side_effect=AssertionError('started fixture before scope validation')), self.assertRaises(ValueError):
                fixture.verify(health_watch_probe=True, **{flag: True})

    def test_health_watch_cli_validates_scope_and_timeout_before_startup(self):
        import runpy
        from unittest.mock import patch
        for args in (['--health-watch-probe', '--health-browser-probe'], ['--health-watch-probe', '--health-projection'], ['--health-watch-probe', '--test-timeout', '0']):
            with self.subTest(args=args), patch('sys.argv', [str(ROOT/'tools/verify_consul_fixture.py'), *args]), patch('subprocess.run', side_effect=AssertionError('started invalid watch fixture')), self.assertRaises(ValueError):
                runpy.run_path(str(ROOT/'tools/verify_consul_fixture.py'), run_name='__main__')

    def test_health_projection_retains_all_138_public_parents_once_before_pressure(self):
        previous = self.copy_scope_before_startup(public_session_destroy=True)
        report = self.copy_scope_before_startup(health_projection=True)
        self.assertEqual(report['scope'], 'private-consul-health-projection-with-public-destroy-regression')
        self.assertEqual(report['required'], previous['required'] + list(fixture.HEALTH_PROJECTION_REQUIRED))
        self.assertTrue(report['public_session_destroy'])
        self.assertFalse(report['health_projection_probe'])
        phases = fixture.acceptance_test_phases(report['packages'], tuple(report['required']), True, 1800, True, True, True, True, True, True)
        names = [name for phase in phases for name in phase[phase.index('-run')+1][2:-2].split('|')]
        self.assertEqual(len(phases), 7)
        self.assertEqual(len(names), 141)
        self.assertEqual(len(set(names)), 141)
        self.assertEqual(sorted(names), sorted('TestConsulNative'+name for name in report['required']))
        self.assertEqual(names[:3], ['TestConsulNative'+name for name in fixture.HEALTH_PROJECTION_REQUIRED])
        self.assertEqual(names[3:6], ['TestConsulNative'+name for name in fixture.PUBLIC_SESSION_DESTROY_REQUIRED])
        log = '\n'.join('--- PASS: '+name+' (0.1s)' for name in names)
        fixture.accepted_cases(log, tuple(report['required']))
        for name in names:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: '+name+' ', '--- SKIP: '+name+' '), tuple(report['required']))
        with self.assertRaises(RuntimeError):
            fixture.accepted_cases(log + '\n--- PASS: '+names[0]+' (0.1s)', tuple(report['required']))
        for required in (tuple(previous['required']), tuple(report['required']) + fixture.HEALTH_PROJECTION_REQUIRED[:1]):
            with self.assertRaises(ValueError):
                fixture.acceptance_test_phases(report['packages'], required, health_projection=True)

    def test_health_diagnostic_rejects_mixed_scopes_and_full_acceptance_rejects_all_probes(self):
        import inspect
        report = self.copy_scope_before_startup(health_projection_probe=True)
        self.assertEqual(report['scope'], 'diagnostic-consul-health-projection')
        self.assertEqual(tuple(report['required']), fixture.HEALTH_PROJECTION_REQUIRED)
        self.assertEqual(report['packages'], ['./internal/services/consul'])
        self.assertFalse(report['public_session_destroy'])
        flags = [name for name, parameter in inspect.signature(fixture.verify).parameters.items()
                 if parameter.default is False and name not in ('race', 'health_projection_probe')]
        for flag in flags:
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(health_projection_probe=True, **{flag: True})
        for flag in flags + ['health_projection_probe']:
            if flag == 'probe' or flag.endswith('_probe'):
                with self.subTest(flag=flag), self.assertRaises(ValueError):
                    fixture.verify(health_projection=True, **{flag: True})
        for name in ('internal/db/contract/kv_http_health.go',
                     'internal/services/consul/kv_health_projection.go',
                     'internal/services/consul/kv_health_requests.go',
                     'internal/services/consul/kv_health_projection_test.go',
                     'internal/services/consul/kv_health_requests_test.go',
                     'internal/services/consul/kv_health_projection_native_integration_test.go'):
            self.assertIn(name, fixture.source_hashes())

    def test_public_destroy_requires_all_private_parents_and_bilingual_original_records(self):
        previous = self.copy_scope_before_startup(session_destroy_plan=True)
        report = self.copy_scope_before_startup(public_session_destroy=True)
        self.assertEqual(report['scope'], 'public-consul-session-destroy')
        self.assertEqual(report['required'], previous['required'] + list(fixture.PUBLIC_SESSION_DESTROY_REQUIRED))
        phases = fixture.acceptance_test_phases(report['packages'], tuple(report['required']), True, 1800, True, True, True, True, True)
        names = [name for phase in phases for name in phase[phase.index('-run')+1][2:-2].split('|')]
        self.assertEqual(len(phases), 6)
        self.assertEqual(len(names), 138)
        self.assertEqual(len(set(names)), 138)
        self.assertEqual(sorted(names), sorted('TestConsulNative'+name for name in report['required']))
        self.assertEqual(names[:3], ['TestConsulNative'+name for name in fixture.PUBLIC_SESSION_DESTROY_REQUIRED])
        self.assertEqual(len(fixture.session_destroy_image_names()), 44)
        log = '\n'.join('--- PASS: '+name+' (0.1s)' for name in names)
        for name in names:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: '+name+' ', '--- SKIP: '+name+' '), tuple(report['required']))
        for flag in ('public_session_destroy_probe', 'session_destroy_plan_probe', 'session_destroy_impact_probe', 'public_session_held_keys_probe', 'copy_limit_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(public_session_destroy=True, **{flag: True})

    def test_public_destroy_diagnostic_is_separate_and_rejects_all_mixed_scopes(self):
        report = self.copy_scope_before_startup(public_session_destroy_probe=True)
        self.assertEqual(report['scope'], 'diagnostic-consul-public-session-destroy')
        self.assertEqual(tuple(report['required']), fixture.PUBLIC_SESSION_DESTROY_REQUIRED)
        self.assertEqual(report['packages'], ['./internal/ui/kvworkspace', './internal/ui/shell'])
        for flag in ('public_session_destroy', 'session_destroy_plan', 'session_destroy_plan_probe', 'session_destroy_impact', 'session_destroy_impact_probe', 'public_session_held_keys', 'session_browser', 'copy_limit_probe', 'probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(public_session_destroy_probe=True, **{flag: True})

    def test_impact_foundation_keeps_all_public_held_key_parents_once_and_before_pressure(self):
        previous = self.copy_scope_before_startup(public_session_held_keys=True)
        report = self.copy_scope_before_startup(session_destroy_impact=True)
        self.assertEqual(report['scope'], 'private-consul-session-destroy-impact-with-public-held-keys-regression')
        self.assertEqual(report['required'], previous['required'] + list(fixture.SESSION_DESTROY_IMPACT_REQUIRED))
        phases = fixture.acceptance_test_phases(report['packages'], tuple(report['required']), True, 1800, True, True, True)
        self.assertEqual(len(phases), 4)
        names = [name for phase in phases for name in phase[phase.index('-run')+1][2:-2].split('|')]
        self.assertEqual(sorted(names), sorted('TestConsulNative'+name for name in report['required']))
        self.assertEqual(len(names), len(set(names)))
        self.assertEqual(len(names), 132)
        self.assertEqual(names[:3], ['TestConsulNative'+name for name in fixture.SESSION_DESTROY_IMPACT_REQUIRED])
        fixture.accepted_cases('\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in report['required']), report['required'])
        for invalid in (names[:-1], names + names[:1]):
            with self.assertRaises(RuntimeError):
                fixture.accepted_cases('\n'.join('--- PASS: '+name+' (0.1s)' for name in invalid), report['required'])

    def test_impact_diagnostic_and_full_scope_reject_mixed_probes_before_fixture_startup(self):
        report = self.copy_scope_before_startup(session_destroy_impact_probe=True)
        self.assertEqual(report['scope'], 'diagnostic-consul-session-destroy-impact')
        self.assertEqual(tuple(report['required']), fixture.SESSION_DESTROY_IMPACT_REQUIRED)
        for flags in ({'session_destroy_impact': True}, {'public_session_held_keys': True}, {'public_session_held_keys_probe': True}, {'copy_limit_probe': True}):
            with self.assertRaises(ValueError):
                fixture.verify(session_destroy_impact_probe=True, **flags)
        for flags in ({'session_held_keys_probe': True}, {'public_session_held_keys_probe': True}, {'copy_limit_probe': True}):
            with self.assertRaises(ValueError):
                fixture.verify(session_destroy_impact=True, **flags)

    def test_public_held_keys_requires_native_shell_and_every_retained_parent_once(self):
        previous = self.copy_scope_before_startup(session_held_keys=True)
        report = self.copy_scope_before_startup(public_session_held_keys=True)
        self.assertEqual(report['scope'], 'public-consul-session-held-keys')
        self.assertEqual(report['required'], previous['required'] + list(fixture.PUBLIC_SESSION_HELD_KEYS_REQUIRED))
        self.assertEqual(len(report['required']), 129)
        self.assertEqual(len(fixture.session_held_keys_image_names()), 52)
        phases = fixture.acceptance_test_phases(report['packages'], tuple(report['required']), True, 1800, True, True)
        self.assertEqual(len(phases), 3)
        names = []
        for phase in phases:
            self.assertIn('-count=1', phase)
            self.assertIn('-race', phase)
            names.extend(phase[phase.index('-run')+1][2:-2].split('|'))
        self.assertEqual(sorted(names), sorted('TestConsulNative'+name for name in report['required']))
        self.assertEqual(len(set(names)), 129)
        self.assertEqual(names[:6], ['TestConsulNative'+name for name in fixture.SESSION_HELD_KEYS_REQUIRED + fixture.PUBLIC_SESSION_HELD_KEYS_REQUIRED])
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in report['required'])
        for name in report['required']:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), tuple(report['required']))
        for flag in ('public_session_held_keys_probe', 'session_held_keys_probe', 'session_browser_probe', 'copy_limit_probe', 'probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(public_session_held_keys=True, **{flag: True})

    def test_public_held_keys_diagnostic_cannot_claim_foundation_or_browser_acceptance(self):
        report = self.copy_scope_before_startup(public_session_held_keys_probe=True)
        self.assertEqual(report['scope'], 'diagnostic-consul-session-held-keys-public')
        self.assertEqual(report['required'], list(fixture.PUBLIC_SESSION_HELD_KEYS_REQUIRED))
        self.assertEqual(report['packages'], ['./internal/ui/kvworkspace','./internal/ui/shell'])
        for flag in ('public_session_held_keys', 'session_held_keys', 'session_held_keys_probe', 'session_browser', 'session_browser_probe', 'session_projection', 'public', 'probe', 'copy_limit_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(public_session_held_keys_probe=True, **{flag: True})

    def test_session_held_keys_retains_every_original_public_browser_parent(self):
        previous = self.copy_scope_before_startup(session_browser=True)
        report = self.copy_scope_before_startup(session_held_keys=True)
        self.assertEqual(report['scope'], 'private-consul-session-held-keys-with-public-browser-regression')
        self.assertEqual(report['required'], previous['required'] + list(fixture.SESSION_HELD_KEYS_REQUIRED))
        self.assertEqual(len(report['required']), 126)
        self.assertEqual(len(set(report['required'])), 126)
        phases = fixture.acceptance_test_phases(report['packages'], tuple(report['required']), True, 1800, True)
        self.assertEqual(len(phases), 2)
        self.assertIn('./internal/services/consul', phases[0])
        self.assertNotIn('./internal/ui/kvworkspace', phases[0])
        parents = []
        for phase in phases:
            self.assertIn('-race', phase)
            self.assertIn('-count=1', phase)
            parents.extend(phase[phase.index('-run')+1][2:-2].split('|'))
        self.assertEqual(parents[:3], ['TestConsulNative'+name for name in fixture.SESSION_HELD_KEYS_REQUIRED])
        self.assertEqual(sorted(parents), sorted('TestConsulNative'+name for name in report['required']))
        self.assertEqual(len(set(parents)), 126)
        self.assertEqual(report['packages'], previous['packages'])
        self.assertTrue(report['session_browser'])
        self.assertTrue(report['session_projection'])
        self.assertTrue(report['public_session_tools'])
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in report['required'])
        self.assertEqual(fixture.accepted_cases(log, tuple(report['required'])), report['required'])
        for name in report['required']:
            for marker in ('SKIP', 'MISSING'):
                with self.subTest(name=name, marker=marker), self.assertRaises(RuntimeError):
                    fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- '+marker+': TestConsulNative'+name+' '), tuple(report['required']))
        for flag in ('session_held_keys_probe', 'session_browser_probe', 'session_projection_probe', 'public_session_tools_probe', 'session_tools_probe', 'session_renewal_probe', 'copy_limit_probe', 'http_watch_probe', 'probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(session_held_keys=True, **{flag: True})

    def test_session_held_keys_probe_and_cli_cannot_claim_browser_acceptance(self):
        report = self.copy_scope_before_startup(session_held_keys_probe=True)
        self.assertEqual(report['scope'], 'diagnostic-consul-session-held-keys')
        self.assertEqual(report['required'], list(fixture.SESSION_HELD_KEYS_REQUIRED))
        self.assertEqual(report['packages'], ['./internal/services/consul'])
        self.assertFalse(report['session_browser'])
        for flag in ('session_held_keys', 'session_browser', 'session_browser_probe', 'session_projection', 'session_projection_probe', 'public_session_tools', 'public_session_tools_probe', 'public', 'probe', 'session_renewal_probe', 'copy_limit_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(session_held_keys_probe=True, **{flag: True})
        import runpy
        from unittest.mock import patch
        for args in (['--session-held-keys', '--copy-limit-probe'], ['--session-held-keys-probe', '--session-browser']):
            with self.subTest(args=args), patch('sys.argv', [str(ROOT/'tools/verify_consul_fixture.py'), *args]), patch('subprocess.run', side_effect=AssertionError('CLI started fixture before validating held-key scope')), self.assertRaises(ValueError):
                runpy.run_path(str(ROOT/'tools/verify_consul_fixture.py'), run_name='__main__')

    def test_session_browser_requires_original_projection_shell_and_all_regressions(self):
        previous = self.copy_scope_before_startup(session_projection=True)
        report = self.copy_scope_before_startup(session_browser=True)
        self.assertEqual(report['scope'], 'public-consul-session-browser')
        self.assertEqual(report['required'], previous['required'] + list(fixture.SESSION_BROWSER_REQUIRED))
        self.assertEqual(len(report['required']), 123)
        self.assertEqual(report['packages'], previous['packages'])
        self.assertTrue(report['session_projection'])
        self.assertTrue(report['public_session_tools'])
        self.assertEqual(len(fixture.session_browser_image_names()), 60)
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in report['required'])
        self.assertEqual(fixture.accepted_cases(log, tuple(report['required'])), report['required'])
        for name in report['required']:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), tuple(report['required']))
        for flag in ('session_browser_probe', 'session_projection_probe', 'public_session_tools_probe', 'copy_limit_probe', 'probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(session_browser=True, **{flag: True})

    def test_session_browser_candidate_probe_cannot_claim_public_acceptance(self):
        report = self.copy_scope_before_startup(session_browser_probe=True)
        self.assertEqual(report['scope'], 'diagnostic-consul-session-browser')
        self.assertEqual(report['required'], list(fixture.SESSION_BROWSER_REQUIRED))
        self.assertEqual(report['packages'], ['./internal/ui/kvworkspace','./internal/ui/shell'])
        self.assertFalse(report['public_session_tools'])
        for flag in ('session_browser', 'session_projection', 'session_projection_probe', 'public_session_tools', 'public_session_tools_probe', 'public', 'probe', 'session_renewal_probe', 'copy_limit_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(session_browser_probe=True, **{flag: True})

    def test_session_projection_retains_every_public_tool_parent(self):
        previous = self.copy_scope_before_startup(public_session_tools=True)
        report = self.copy_scope_before_startup(session_projection=True)
        self.assertEqual(report['required'], previous['required'] + list(fixture.SESSION_PROJECTION_REQUIRED))
        self.assertEqual(report['packages'], previous['packages'])
        self.assertTrue(report['public_session_tools'])
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in report['required'])
        self.assertEqual(fixture.accepted_cases(log, tuple(report['required'])), report['required'])
        for name in report['required']:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), tuple(report['required']))

    def test_session_projection_diagnostic_is_distinct_and_cannot_skip_regression(self):
        report = self.copy_scope_before_startup(session_projection_probe=True)
        self.assertEqual(report['required'], list(fixture.SESSION_PROJECTION_REQUIRED))
        self.assertEqual(report['packages'], ['./internal/services/consul'])
        self.assertEqual(report['scope'], 'diagnostic-consul-session-projection')
        for flag in ('session_projection', 'public_session_tools', 'public_session_tools_probe', 'session_tools', 'session_renewal', 'public', 'probe', 'copy_limit_probe', 'http_watch_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(session_projection_probe=True, **{flag: True})
        for flag in ('public_session_tools_probe', 'session_tools_probe', 'session_renewal_probe', 'probe', 'copy_limit_probe', 'http_watch_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(session_projection=True, **{flag: True})

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

    def test_public_session_tools_retains_foundation_and_requires_original_ui(self):
        previous = self.copy_scope_before_startup(session_tools=True)
        report = self.copy_scope_before_startup(public_session_tools=True)
        required = tuple(previous['required']) + fixture.PUBLIC_SESSION_TOOLS_REQUIRED
        self.assertEqual(report['required'], list(required))
        self.assertEqual(len(set(required)), 117)
        self.assertEqual(report['scope'], 'public-consul-session-tools')
        self.assertEqual(report['packages'], previous['packages'])
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        self.assertEqual(fixture.accepted_cases(log, required), list(required))
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)
        with self.assertRaises(RuntimeError):
            fixture.accepted_cases(log, tuple(previous['required']))
        self.assertEqual(len(fixture.session_tools_image_names()), 220)
        for name in ('en-720x520-workspace-session-tool-http-session-create-form.png',
                     'zh-Hans-1280x800-workspace-session-tool-http-release-false.png',
                     'en-720x520-workspace-session-tool-http-acquire-unknown-replace.png',
                     'en-720x520-shell-session-tool-recovered.png'):
            self.assertIn(name, fixture.session_tools_image_names())

    def test_public_session_tools_diagnostic_cannot_claim_complete_acceptance(self):
        report = self.copy_scope_before_startup(public_session_tools_probe=True)
        self.assertEqual(report['scope'], 'diagnostic-consul-public-session-tools')
        self.assertEqual(report['required'], list(fixture.PUBLIC_SESSION_TOOLS_REQUIRED))
        self.assertEqual(report['packages'], ['./internal/ui/kvworkspace', './internal/ui/shell'])
        for flag in ('public_session_tools','session_tools','session_tools_probe',
                     'public_session_renewal','public_session_renewal_probe','session_renewal',
                     'session_renewal_probe','public','tools','public_grid','public_http_watch',
                     'http_watch','probe','copy_limit_probe','mutation_limit_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(public_session_tools_probe=True, **{flag: True})
        for flag in ('public_session_tools_probe','session_tools_probe','public_session_renewal_probe',
                     'session_renewal_probe','probe','copy_limit_probe','http_watch_probe',
                     'public_http_watch_probe','mutation_limit_probe','grid_write_probe','public_copy_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(public_session_tools=True, **{flag: True})

    def test_public_session_tools_source_and_cli_do_not_bypass_scope_checks(self):
        hashes = fixture.source_hashes()
        for name in ('internal/ui/kvworkspace/request_session_tools_view.go',
                     'internal/ui/kvworkspace/request_tools_files.go',
                     'internal/ui/kvworkspace/consul_session_tools_native_integration_test.go',
                     'internal/ui/kvworkspace/consul_session_tools_fault_native_integration_test.go',
                     'internal/ui/kvworkspace/consul_session_tools_selection_native_integration_test.go',
                     'internal/ui/shell/consul_session_tools_native_integration_test.go'):
            self.assertIn(name, hashes)
        import runpy
        from unittest.mock import patch
        for args in (['--public-session-tools-probe','--session-tools'],
                     ['--public-session-tools','--copy-limit-probe'],
                     ['--public-session-tools','--session-tools-probe']):
            with self.subTest(args=args), patch('sys.argv', [str(ROOT/'tools/verify_consul_fixture.py'), *args]), patch('subprocess.run', side_effect=AssertionError('CLI started fixture before validating Session tools scope')), self.assertRaises(ValueError):
                runpy.run_path(str(ROOT/'tools/verify_consul_fixture.py'), run_name='__main__')

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

    def test_destroy_plan_keeps_all_132_impact_parents_once_before_pressure(self):
        impact = self.copy_scope_before_startup(session_destroy_impact=True)
        plan = self.copy_scope_before_startup(session_destroy_plan=True)
        required = tuple(impact['required']) + fixture.SESSION_DESTROY_PLAN_REQUIRED
        self.assertEqual(plan['required'], list(required))
        self.assertEqual(len(required), 135)
        self.assertEqual(len(set(required)), 135)
        self.assertEqual(plan['scope'], 'private-consul-session-destroy-plan-with-public-held-keys-regression')
        phases = fixture.acceptance_test_phases(plan['packages'], required, True, 1800, True, True, True, True)
        names = []
        for phase in phases:
            self.assertIn('-race', phase)
            pattern = phase[phase.index('-run')+1]
            names.extend(name.removeprefix('TestConsulNative') for name in pattern[2:-2].split('|'))
        self.assertEqual(names[:3], list(fixture.SESSION_DESTROY_PLAN_REQUIRED))
        self.assertEqual(names[3:6], list(fixture.SESSION_DESTROY_IMPACT_REQUIRED))
        self.assertEqual(set(names), set(required))
        self.assertEqual(len(names), len(required))
        log = '\n'.join('--- PASS: TestConsulNative'+name+' (0.1s)' for name in required)
        self.assertEqual(fixture.accepted_cases(log, required), list(required))
        for name in required:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                fixture.accepted_cases(log.replace('--- PASS: TestConsulNative'+name+' ', '--- SKIP: TestConsulNative'+name+' '), required)

    def test_destroy_plan_diagnostic_cannot_downgrade_scope_or_claim_acceptance(self):
        plan = self.copy_scope_before_startup(session_destroy_plan_probe=True)
        self.assertEqual(plan['required'], list(fixture.SESSION_DESTROY_PLAN_REQUIRED))
        self.assertEqual(plan['scope'], 'diagnostic-consul-session-destroy-plan')
        self.assertEqual(plan['packages'], ['./internal/services/consul'])
        for flag in ('session_destroy_plan', 'session_destroy_impact', 'session_destroy_impact_probe',
                     'public_session_held_keys', 'public_session_held_keys_probe', 'session_held_keys',
                     'probe', 'public_grid', 'session_browser', 'copy_limit_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(session_destroy_plan_probe=True, **{flag: True})
        for flag in ('session_destroy_plan_probe', 'session_destroy_impact_probe', 'public_session_held_keys_probe',
                     'session_held_keys_probe', 'probe', 'public_prefix_set_probe', 'copy_limit_probe'):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                fixture.verify(session_destroy_plan=True, **{flag: True})
        hashes = fixture.source_hashes()
        for name in ('internal/services/consul/http_session_destroy_plan.go',
                     'internal/services/consul/http_session_destroy_plan_test.go',
                     'internal/services/consul/http_session_destroy_plan_native_integration_test.go'):
            self.assertIn(name, hashes)

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
