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
