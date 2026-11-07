import importlib.util
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('solr_fixture', Path(__file__).resolve().parents[1]/'tools/verify_solr_fixture.py')
FIXTURE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FIXTURE)


class SolrFixtureOwnershipTests(unittest.TestCase):
    def test_cleanup_rejects_mutable_names(self):
        with patch.object(FIXTURE.subprocess, 'run') as run:
            with self.assertRaises(RuntimeError):
                FIXTURE.remove_owned('solr-server', 'owned')
            run.assert_not_called()

    def test_cleanup_rejects_changed_identity_and_labels(self):
        original = 'a'*64
        for record in [
            {'Id': 'b'*64, 'Config': {'Labels': {'nyadb.fixture': 'solr', 'nyadb.fixture.nonce': 'owned'}}},
            {'Id': original, 'Config': {'Labels': {'nyadb.fixture': 'solr', 'nyadb.fixture.nonce': 'other'}}},
            {'Id': original, 'Config': {'Labels': {'nyadb.fixture': 'mysql', 'nyadb.fixture.nonce': 'owned'}}},
        ]:
            with self.subTest(record=record), patch.object(FIXTURE.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, json.dumps([record]), '')) as run:
                with self.assertRaises(RuntimeError):
                    FIXTURE.remove_owned(original, 'owned')
                self.assertEqual(run.call_count, 1)
                self.assertEqual(run.call_args.args[0], ['docker', 'inspect', original])

    def test_cleanup_preserves_missing_container_and_removes_only_owned_id(self):
        original = 'a'*64
        with patch.object(FIXTURE.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, '', 'missing')) as run:
            FIXTURE.remove_owned(original, 'owned')
            self.assertEqual(run.call_count, 1)
        record = {'Id': original, 'Config': {'Labels': {'nyadb.fixture': 'solr', 'nyadb.fixture.nonce': 'owned'}}}
        with patch.object(FIXTURE.subprocess, 'run', side_effect=[subprocess.CompletedProcess([], 0, json.dumps([record]), ''), subprocess.CompletedProcess([], 0, '', '')]) as run:
            FIXTURE.remove_owned(original, 'owned')
            self.assertEqual(run.call_args.args[0], ['docker', 'rm', '-f', '-v', original])
            self.assertTrue(run.call_args.kwargs['check'])
