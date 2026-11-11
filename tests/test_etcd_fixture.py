"""Only the immutable, inspected original etcd fixture may be removed."""
import importlib.util
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('etcd_fixture', Path(__file__).resolve().parents[1]/'tools/verify_etcd_fixture.py')
FIXTURE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FIXTURE)


class EtcdFixtureTests(unittest.TestCase):
    def record(self, identifier='a'*64, labels=None):
        if labels is None:
            labels = {'nyadb.fixture': 'etcd', 'nyadb.fixture.nonce': 'original'}
        return [{'Id': identifier, 'Config': {'Labels': labels}}]

    def test_cleanup_removes_only_verified_original_id(self):
        inspected = subprocess.CompletedProcess([], 0, json.dumps(self.record()), '')
        removed = subprocess.CompletedProcess([], 0, '', '')
        with patch.object(FIXTURE.subprocess, 'run', side_effect=[inspected, removed]) as run:
            FIXTURE.remove_owned('a'*64, 'original')
            self.assertEqual(run.call_args_list[1].args[0], ['docker', 'rm', '-f', 'a'*64])

    def test_cleanup_rejects_foreign_and_replaced_ownership(self):
        for record in [self.record('b'*64), self.record(labels={}), self.record(labels={'nyadb.fixture':'etcd','nyadb.fixture.nonce':'other'}), self.record(labels={'nyadb.fixture':'redis','nyadb.fixture.nonce':'original'})]:
            with self.subTest(record=record), patch.object(FIXTURE.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, json.dumps(record), '')) as run:
                with self.assertRaises(RuntimeError):
                    FIXTURE.remove_owned('a'*64, 'original')
                self.assertEqual(run.call_count, 1)

    def test_cleanup_rejects_mutable_names(self):
        for identifier in ('reusable-name', '', None, 'A'*64, 'a'*12):
            with self.subTest(identifier=identifier), patch.object(FIXTURE.subprocess, 'run') as run:
                with self.assertRaises(RuntimeError):
                    FIXTURE.remove_owned(identifier, 'original')
                run.assert_not_called()

    def test_cleanup_rejects_malformed_inspection(self):
        for text in ('not json', '[]', '{}', '[null]', '[{"Id":"wrong"}]'):
            with self.subTest(text=text), patch.object(FIXTURE.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, text, '')) as run:
                with self.assertRaises(RuntimeError):
                    FIXTURE.remove_owned('a'*64, 'original')
                self.assertEqual(run.call_count, 1)

    def test_cleanup_does_not_treat_docker_failure_as_absence(self):
        with patch.object(FIXTURE.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, '', 'daemon unavailable')) as run:
            with self.assertRaises(RuntimeError):
                FIXTURE.remove_owned('a'*64, 'original')
            self.assertEqual(run.call_count, 1)

    def test_cleanup_preserves_already_absent_original(self):
        with patch.object(FIXTURE.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, '', 'No such container')) as run:
            FIXTURE.remove_owned('a'*64, 'original')
            self.assertEqual(run.call_count, 1)


    def test_snapshot_session_rejects_other_acceptance_scopes_before_fixture_creation(self):
        for flag in ('profile', 'session', 'workspace', 'data', 'public', 'auth_native',
                     'compaction_native', 'member_native', 'alarm_native', 'snapshot_native'):
            with self.subTest(flag=flag), patch.object(FIXTURE, 'run') as run:
                with self.assertRaises(ValueError):
                    FIXTURE.verify('basic', True, '3.7.3', snapshot_session=True, **{flag: True})
                run.assert_not_called()


if __name__ == '__main__':
    unittest.main()
