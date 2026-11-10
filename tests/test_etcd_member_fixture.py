"""Member fixtures must not remove a foreign or occupied Docker network."""
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import patch

TOOLS = Path(__file__).resolve().parents[1]/'tools'
SPEC = importlib.util.spec_from_file_location('etcd_member_fixture', TOOLS/'verify_etcd_member_fixture.py')
FIXTURE = importlib.util.module_from_spec(SPEC)
with patch.object(sys, 'path', [str(TOOLS), *sys.path]):
    SPEC.loader.exec_module(FIXTURE)


class EtcdMemberFixtureTests(unittest.TestCase):
    def record(self):
        return [{'Id': 'a'*64, 'Labels': {'nyadb.fixture': 'etcd-member', 'nyadb.fixture.nonce': 'original'}, 'Containers': {}}]

    def test_removes_only_unoccupied_original_network_id(self):
        with patch.object(FIXTURE.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, json.dumps(self.record()), '')), patch.object(FIXTURE, 'run') as run:
            FIXTURE.remove_network('a'*64, 'original')
            run.assert_called_once_with(['docker', 'network', 'rm', 'a'*64])

    def test_preserves_replaced_foreign_and_occupied_networks(self):
        for changed in ('Id', 'label', 'nonce', 'occupied'):
            record = self.record()
            if changed == 'Id':
                record[0]['Id'] = 'b'*64
            elif changed == 'occupied':
                record[0]['Containers'] = {'foreign-container': {}}
            else:
                key = 'nyadb.fixture' if changed == 'label' else 'nyadb.fixture.nonce'
                record[0]['Labels'][key] = 'foreign'
            with self.subTest(changed=changed), patch.object(FIXTURE.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, json.dumps(record), '')), patch.object(FIXTURE, 'run') as run:
                with self.assertRaises(RuntimeError):
                    FIXTURE.remove_network('a'*64, 'original')
                run.assert_not_called()

    def test_rejects_mutable_names_without_inspection(self):
        for identifier in (None, '', 'reusable-name', 'a'*12, 'A'*64):
            with self.subTest(identifier=identifier), patch.object(FIXTURE.subprocess, 'run') as run:
                with self.assertRaises(RuntimeError):
                    FIXTURE.remove_network(identifier, 'original')
                run.assert_not_called()

    def test_malformed_inspection_never_removes(self):
        for raw in ('[]', '{}', '[null]', 'not-json'):
            with self.subTest(raw=raw), patch.object(FIXTURE.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, raw, '')), patch.object(FIXTURE, 'run') as run:
                with self.assertRaises(RuntimeError):
                    FIXTURE.remove_network('a'*64, 'original')
                run.assert_not_called()

    def test_daemon_failure_is_not_absence(self):
        with patch.object(FIXTURE.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, '', 'daemon unavailable')):
            with self.assertRaises(RuntimeError):
                FIXTURE.remove_network('a'*64, 'original')

    def test_already_absent_original_needs_no_removal(self):
        with patch.object(FIXTURE.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, '', 'No such network')), patch.object(FIXTURE, 'run') as run:
            FIXTURE.remove_network('a'*64, 'original')
            run.assert_not_called()


if __name__ == '__main__':
    unittest.main()
