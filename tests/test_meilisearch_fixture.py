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
