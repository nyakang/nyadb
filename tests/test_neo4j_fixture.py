import importlib.util
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('neo4j_fixture', Path(__file__).resolve().parents[1]/'tools/verify_neo4j_fixture.py')
FIXTURE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FIXTURE)


class Neo4jFixtureOwnershipTests(unittest.TestCase):
    def test_cleanup_rejects_mutable_names_and_foreign_ownership(self):
        with patch.object(FIXTURE.subprocess, 'run') as run:
            with self.assertRaises(RuntimeError):
                FIXTURE.remove_owned('neo4j', 'owned')
            run.assert_not_called()
        original = 'a'*64
        for identifier, engine, nonce in [('b'*64, 'neo4j', 'owned'), (original, 'mysql', 'owned'), (original, 'neo4j', 'foreign')]:
            record = {'Id': identifier, 'Config': {'Labels': {'nyadb.fixture': engine, 'nyadb.fixture.nonce': nonce}}}
            with self.subTest(record=record), patch.object(FIXTURE.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, json.dumps([record]), '')) as run:
                with self.assertRaises(RuntimeError):
                    FIXTURE.remove_owned(original, 'owned')
                self.assertEqual(run.call_count, 1)

    def test_cleanup_rejects_missing_ambiguous_and_uninspectable_nodes(self):
        for result in [subprocess.CompletedProcess([], 1, '', ''), subprocess.CompletedProcess([], 0, '[]', '')]:
            with patch.object(FIXTURE.subprocess, 'run', return_value=result) as run:
                with self.assertRaises(RuntimeError):
                    FIXTURE.remove_owned('a'*64, 'owned')
                self.assertEqual(run.call_count, 1)

    def test_cleanup_removes_only_verified_original_container(self):
        original = 'a'*64
        record = {'Id': original, 'Config': {'Labels': {'nyadb.fixture': 'neo4j', 'nyadb.fixture.nonce': 'owned'}}}
        with patch.object(FIXTURE.subprocess, 'run', side_effect=[subprocess.CompletedProcess([], 0, json.dumps([record]), ''), subprocess.CompletedProcess([], 0, '', '')]) as run:
            FIXTURE.remove_owned(original, 'owned')
            self.assertEqual(run.call_args.args[0], ['docker', 'rm', '-f', '-v', original])
            self.assertTrue(run.call_args.kwargs['check'])

    def test_acceptance_requires_every_native_case_without_skips_or_races(self):
        passed = '\n'.join('--- PASS: TestNeo4jNative'+name+' (0.01s)' for name in FIXTURE.REQUIRED)
        FIXTURE.check_acceptance(passed)
        for log in [passed.replace('--- PASS:', '--- SKIP:', 1), passed+'\n    --- SKIP: child (0.0s)', passed+'\nWARNING: DATA RACE', passed+'\n--- FAIL: child (0.0s)']:
            with self.subTest(log=log), self.assertRaises(RuntimeError):
                FIXTURE.check_acceptance(log)

    def test_profile_acceptance_requires_real_reopen_canceled_handshake_and_form_evidence(self):
        native = '\n'.join('--- PASS: TestNeo4jNative'+name+' (0.01s)' for name in FIXTURE.REQUIRED)
        with self.assertRaises(RuntimeError):
            FIXTURE.check_acceptance(native, profile=True)
        profile = '\n'.join('--- PASS: TestNeo4jNative'+name+' (0.01s)' for name in FIXTURE.PROFILE_REQUIRED)
        FIXTURE.check_acceptance(native+'\n'+profile, profile=True)
        with self.assertRaises(RuntimeError):
            FIXTURE.check_acceptance(native+'\n'+profile.replace('--- PASS:', '--- SKIP:', 1), profile=True)

    def test_session_acceptance_requires_metadata_typed_leases_and_original_interruption(self):
        names = FIXTURE.REQUIRED + FIXTURE.PROFILE_REQUIRED + FIXTURE.SESSION_REQUIRED
        passed = '\n'.join('--- PASS: TestNeo4jNative'+name+' (0.01s)' for name in names)
        FIXTURE.check_acceptance(passed, profile=True, session=True)
        for name in FIXTURE.SESSION_REQUIRED:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                FIXTURE.check_acceptance(passed.replace('--- PASS: TestNeo4jNative'+name, '--- SKIP: TestNeo4jNative'+name), profile=True, session=True)
