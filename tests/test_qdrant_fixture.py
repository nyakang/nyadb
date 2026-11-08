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

    def test_profile_command_checks_native_config_storage_and_editor(self):
        command = fixture.native_test_command(True, profile=True)
        self.assertIn('-race', command)
        self.assertIn('-count=1', command)
        self.assertIn('-timeout=5m', command)
        self.assertEqual(command[command.index('-run')+1], '^TestQdrant')
        self.assertEqual(command[-4:], ['./internal/services/qdrant', './internal/services/connections',
                                        './internal/storage/localdb', './internal/ui/connectioneditor'])

    def test_session_command_includes_profiles_without_excluding_required_native_tests(self):
        command = fixture.native_test_command(True, session=True)
        self.assertIn('-race', command)
        self.assertEqual(command[command.index('-skip')+1], '^TestQdrantNativePublic')
        self.assertEqual(command[command.index('-run')+1], '^TestQdrant')
        self.assertEqual(command[-4:], ['./internal/services/qdrant', './internal/services/connections',
                                        './internal/storage/localdb', './internal/ui/connectioneditor'])
        for profile in [False, True]:
            old = fixture.native_test_command(True, profile=profile)
            self.assertEqual(old[old.index('-skip')+1], '^TestQdrantNative(Session|Public)')

    def test_public_command_checks_original_runtime_workspace_and_every_required_pass(self):
        command = fixture.native_test_command(True, public=True)
        self.assertNotIn('-skip', command)
        self.assertIn('-race', command)
        self.assertEqual(command[-3:], ['./internal/services/dbruntime', './internal/ui/sqlworkspace', './internal/ui/shell'])
        names = fixture.REQUIRED + fixture.PROFILE_REQUIRED + fixture.SESSION_REQUIRED + fixture.PUBLIC_REQUIRED
        log = '\n'.join('--- PASS: TestQdrantNative'+name+' (0.1s)' for name in names)
        fixture.check_acceptance(log, public=True)
        for name in fixture.PUBLIC_REQUIRED:
            with self.assertRaises(RuntimeError):
                fixture.check_acceptance(log.replace('--- PASS: TestQdrantNative'+name, '--- SKIP: TestQdrantNative'+name), public=True)

    def test_public_evidence_covers_provider_navigation_original_rest_and_translations(self):
        paths = fixture.source_hashes(profile=True)
        for name in ['internal/services/dbruntime/qdrant_integration_test.go',
                     'internal/ui/shell/qdrant_integration_test.go',
                     'internal/ui/sqlworkspace/qdrant_integration_test.go',
                     'internal/ui/navigation/qdrant_test.go',
                     'internal/ui/connectioncatalog/qdrant_test.go',
                     'internal/services/connections/descriptors.go', 'locales/en.json']:
            self.assertIn(name, paths)

    def test_session_acceptance_requires_original_metadata_and_executor_recovery(self):
        all_names = fixture.REQUIRED + fixture.PROFILE_REQUIRED + fixture.SESSION_REQUIRED
        log = '\n'.join('--- PASS: TestQdrantNative'+name+' (0.1s)' for name in all_names)
        fixture.check_acceptance(log, session=True)
        for name in fixture.SESSION_REQUIRED:
            for changed in [log.replace('--- PASS: TestQdrantNative'+name, '--- SKIP: TestQdrantNative'+name),
                            log.replace('--- PASS: TestQdrantNative'+name, '--- PASS: TestQdrantNative'+name+'Other')]:
                with self.assertRaises(RuntimeError):
                    fixture.check_acceptance(changed, session=True)

    def test_session_evidence_covers_original_leases_and_typed_response_completion(self):
        paths = fixture.source_hashes(profile=True)
        for name in ['internal/services/qdrant/session.go', 'internal/services/qdrant/query.go',
                     'internal/services/qdrant/metadata.go', 'internal/services/qdrant/session_test.go',
                     'internal/services/qdrant/resultsets.go', 'internal/services/qdrant/resultsets_test.go',
                     'internal/services/qdrant/resultsets_native_integration_test.go',
                     'internal/services/qdrant/session_native_integration_test.go',
                     'internal/db/contract/features.go', 'internal/services/qdrant/client.go']:
            self.assertIn(name, paths)

    def test_profile_acceptance_requires_original_checks_and_localized_forms(self):
        native = '\n'.join('--- PASS: TestQdrantNative'+name+' (0.1s)' for name in fixture.REQUIRED)
        profile = '\n'.join('--- PASS: TestQdrantNative'+name+' (0.1s)' for name in fixture.PROFILE_REQUIRED)
        fixture.check_acceptance(native+'\n'+profile, profile=True)
        for name in fixture.PROFILE_REQUIRED:
            for replacement in ['--- SKIP: TestQdrantNative'+name, '--- PASS: TestQdrantNative'+name+'Other']:
                changed = profile.replace('--- PASS: TestQdrantNative'+name, replacement)
                with self.assertRaises(RuntimeError):
                    fixture.check_acceptance(native+'\n'+changed, profile=True)
        with self.assertRaises(RuntimeError):
            fixture.check_acceptance(native, profile=True)

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

    def test_profile_evidence_covers_original_vault_storage_and_translations(self):
        paths = fixture.source_hashes(profile=True)
        for name in ['internal/services/connections/profiles.go',
                     'internal/services/connections/profile_validation.go',
                     'internal/services/connections/uri.go',
                     'internal/storage/localdb/engine_options.go',
                     'internal/storage/localdb/qdrant_options_json.go',
                     'internal/ui/connectioneditor/state.go',
                     'internal/ui/connectioneditor/save.go',
                     'internal/ui/connectioneditor/qdrant_native_integration_test.go',
                     'locales/zh-Hans.json', 'locales/en.json', 'locales/catalog_gen.go']:
            self.assertIn(name, paths)


if __name__ == '__main__':
    unittest.main()
