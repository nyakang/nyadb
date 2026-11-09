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

    def test_public_acceptance_requires_runtime_workbench_and_native_no_replay(self):
        names = FIXTURE.REQUIRED + FIXTURE.PROFILE_REQUIRED + FIXTURE.SESSION_REQUIRED + FIXTURE.PUBLIC_REQUIRED
        passed = '\n'.join('--- PASS: TestNeo4jNative'+name+' (0.01s)' for name in names)
        FIXTURE.check_acceptance(passed, profile=True, session=True, public=True)
        for name in FIXTURE.PUBLIC_REQUIRED:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                FIXTURE.check_acceptance(passed.replace('--- PASS: TestNeo4jNative'+name, '--- SKIP: TestNeo4jNative'+name), profile=True, session=True, public=True)

    def test_data_acceptance_requires_original_graph_reads_and_interruption(self):
        names = FIXTURE.REQUIRED + FIXTURE.PROFILE_REQUIRED + FIXTURE.SESSION_REQUIRED + FIXTURE.DATA_REQUIRED
        passed = '\n'.join('--- PASS: TestNeo4jNative'+name+' (0.01s)' for name in names)
        FIXTURE.check_acceptance(passed, data=True)
        for name in FIXTURE.DATA_REQUIRED:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                FIXTURE.check_acceptance(passed.replace('--- PASS: TestNeo4jNative'+name, '--- SKIP: TestNeo4jNative'+name), data=True)

    def test_grid_acceptance_requires_shared_public_data_and_explicit_original_reopen(self):
        names = FIXTURE.REQUIRED + FIXTURE.PROFILE_REQUIRED + FIXTURE.SESSION_REQUIRED + FIXTURE.PUBLIC_REQUIRED + FIXTURE.DATA_REQUIRED + FIXTURE.GRID_REQUIRED
        passed = '\n'.join('--- PASS: TestNeo4jNative'+name+' (0.01s)' for name in names)
        FIXTURE.check_acceptance(passed, grid=True)
        for name in FIXTURE.GRID_REQUIRED:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                FIXTURE.check_acceptance(passed.replace('--- PASS: TestNeo4jNative'+name, '--- SKIP: TestNeo4jNative'+name), grid=True)

    def test_graph_grid_editing_acceptance_keeps_native_transaction_and_fault_cases(self):
        self.assertTrue({
            'DataWriteExplicitTransactionsLockComparisonRollbackAndCommit',
            'DataWritePropertiesTypedAtomicReceiptsAndOldRows',
            'DataWriteBlockedOriginalComparisonCancellationAndErrors',
        }.issubset(FIXTURE.DATA_REQUIRED))
        self.assertTrue({
            'PublicDataPropertyEditingTypedReceiptsParkConflictsAndContinuedEdits',
            'PublicDataCommitReplyLostCanceledOriginalCloseNeverReplay',
        }.issubset(FIXTURE.GRID_REQUIRED))

    def test_property_write_followup_requires_all_native_grid_cases(self):
        names = FIXTURE.WRITE_REQUIRED + FIXTURE.GRID_REQUIRED
        self.assertEqual(len(names), 8)
        passed = '\n'.join('--- PASS: TestNeo4jNative'+name+' (0.01s)' for name in names)
        FIXTURE.check_acceptance(passed, grid_writes=True)
        for name in names:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                FIXTURE.check_acceptance(passed.replace('--- PASS: TestNeo4jNative'+name, '--- SKIP: TestNeo4jNative'+name), grid_writes=True)

    def test_entity_foundation_requires_actual_native_mutations_and_faults(self):
        self.assertEqual(len(FIXTURE.ENTITY_REQUIRED), 4)
        self.assertTrue(set(FIXTURE.ENTITY_REQUIRED).issubset(FIXTURE.DATA_REQUIRED))
        passed = '\n'.join('--- PASS: TestNeo4jNative'+name+' (0.01s)' for name in FIXTURE.ENTITY_REQUIRED)
        FIXTURE.check_acceptance(passed, entity_native=True)
        for name in FIXTURE.ENTITY_REQUIRED:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                FIXTURE.check_acceptance(passed.replace('--- PASS: TestNeo4jNative'+name, '--- SKIP: TestNeo4jNative'+name), entity_native=True)

    def test_entity_grid_requires_public_decisions_commit_faults_and_lifecycle(self):
        names = FIXTURE.WRITE_REQUIRED + FIXTURE.ENTITY_REQUIRED + FIXTURE.ENTITY_PREVIEW_REQUIRED + FIXTURE.GRID_REQUIRED + FIXTURE.ENTITY_GRID_REQUIRED
        required = {'PublicDataEntityCreationTypedOmittedAndPropertylessConfirmedIdentity', 'PublicDataEntityDeleteChoiceBlockedAndAdjacentBatchConfirmed', 'PublicDataEntityPreviewChangedObservationRollsBackOriginalBatch', 'PublicDataEntityCommitReplyLostCanceledOriginalCloseNeverReplay', 'PublicDataEntityPreviewParkGenerationCancelAndOriginalClose'}
        self.assertEqual(set(FIXTURE.ENTITY_GRID_REQUIRED), required)
        passed = ''.join('--- PASS: TestNeo4jNative'+name+' (0.01s)\n' for name in names)
        FIXTURE.check_acceptance(passed, entity_grid=True)
        for name in names:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                FIXTURE.check_acceptance(passed.replace('--- PASS: TestNeo4jNative'+name, '--- SKIP: TestNeo4jNative'+name), entity_grid=True)
        self.assertEqual(len(FIXTURE.ENTITY_GRID_IMAGE_STAGES), 12)
        self.assertTrue({'entity-delete-choice', 'entity-delete-plain-blocked', 'entity-delete-detach-preview', 'entity-commit-unknown-close', 'entity-preview-parked'}.issubset(FIXTURE.ENTITY_GRID_IMAGE_STAGES))

    def test_entity_preview_requires_native_edges_one_attempt_and_original_faults(self):
        self.assertEqual(len(FIXTURE.ENTITY_PREVIEW_REQUIRED), 4)
        self.assertTrue(set(FIXTURE.ENTITY_PREVIEW_REQUIRED).issubset(FIXTURE.DATA_REQUIRED))
        names = FIXTURE.WRITE_REQUIRED + FIXTURE.ENTITY_REQUIRED + FIXTURE.ENTITY_PREVIEW_REQUIRED
        self.assertEqual(len(names), 11)
        passed = '\n'.join('--- PASS: TestNeo4jNative'+name+' (0.01s)' for name in names)
        FIXTURE.check_acceptance(passed, entity_preview=True)
        for name in names:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                FIXTURE.check_acceptance(passed.replace('--- PASS: TestNeo4jNative'+name, '--- SKIP: TestNeo4jNative'+name), entity_preview=True)
