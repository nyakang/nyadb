import importlib.util
import json
from pathlib import Path
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('verify_qdrant_fixture', ROOT/'tools/verify_qdrant_fixture.py')
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)


class QdrantFixtureTests(unittest.TestCase):
    def test_exact_required_passes_and_race(self):
        log = '\n'.join('--- PASS: TestQdrantNative'+name+' (0.1s)' for name in fixture.REQUIRED)
        fixture.check_acceptance(log)
        for changed in [log.replace('--- PASS:', '--- SKIP:', 1), log+'\nWARNING: DATA RACE', log+'\n--- FAIL: TestOther (0s)']:
            with self.assertRaises(RuntimeError):
                fixture.check_acceptance(changed)

    def test_owned_fixture_cleanup_refuses_other_resources(self):
        for identifier in ['', 'container-name', 'a'*63, 'a'*64+';docker rm']:
            with patch.object(fixture.subprocess, 'run') as run:
                with self.assertRaises(RuntimeError):
                    fixture.remove_owned(identifier, 'owned')
                run.assert_not_called()

    def test_native_command_is_explicit_and_bounded(self):
        command = fixture.native_test_command(True)
        self.assertIn('-race', command)
        self.assertIn('-timeout=5m', command)
        self.assertIn('^TestQdrantNative', command)
        self.assertEqual(command[-1], './internal/services/qdrant')

    def test_cleanup_requires_nonce_product_and_immutable_id(self):
        identifier = 'a'*64
        own = {'Id': identifier, 'Config': {'Labels': {'nyadb.fixture': 'qdrant', 'nyadb.fixture.nonce': 'owned'}}}
        for labels in [{'nyadb.fixture': 'qdrant', 'nyadb.fixture.nonce': 'other'},
                       {'nyadb.fixture': 'couchdb', 'nyadb.fixture.nonce': 'owned'}, {}]:
            record = dict(own, Config={'Labels': labels})
            with patch.object(fixture.subprocess, 'run') as run:
                run.return_value.returncode = 0
                run.return_value.stdout = json.dumps([record])
                with self.assertRaises(RuntimeError):
                    fixture.remove_owned(identifier, 'owned')
                self.assertEqual(run.call_count, 1)
        with patch.object(fixture.subprocess, 'run') as run:
            run.return_value.returncode = 0
            run.return_value.stdout = json.dumps([own])
            fixture.remove_owned(identifier, 'owned')
            self.assertEqual(run.call_args_list[-1].args[0], ['docker', 'rm', '-f', '-v', identifier])

    def test_unknown_mode_does_not_create_resource(self):
        with patch.object(fixture.subprocess, 'run') as run:
            with self.assertRaises(ValueError):
                fixture.verify('other')
            run.assert_not_called()

    def test_evidence_covers_native_and_public_config_sources(self):
        paths = fixture.source_hashes()
        for name in ['internal/services/qdrant/client.go', 'internal/services/qdrant/native.go',
                     'internal/services/qdrant/native_integration_test.go',
                     'internal/services/connections/qdrant_config.go', 'tests/test_qdrant_fixture.py']:
            self.assertIn(name, paths)


if __name__ == '__main__':
    unittest.main()
