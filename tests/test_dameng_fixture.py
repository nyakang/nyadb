"""Verify DM8 fixture cleanup preserves foreign and replacement containers."""
import importlib.util
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("dameng_fixture", Path(__file__).resolve().parents[1] / "tools" / "verify_dameng_fixture.py")
FIXTURE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FIXTURE)


class DamengFixtureTests(unittest.TestCase):
    def test_cleanup_requires_matching_immutable_id_and_ownership(self):
        identifier = "a" * 64
        inspected = [{"Id": identifier, "Config": {"Labels": {"nyadb.fixture": "dameng", "nyadb.fixture.nonce": "nonce"}}}]
        with patch.object(FIXTURE.subprocess, "run", side_effect=[subprocess.CompletedProcess([], 0, json.dumps(inspected)), subprocess.CompletedProcess([], 0)]) as run:
            FIXTURE.remove_owned(identifier, "nonce")
            self.assertEqual(run.call_args_list[1].args[0], ["docker", "rm", "-f", identifier])

    def test_cleanup_preserves_replacements_foreign_and_malformed_results(self):
        identifier = "a" * 64
        rows = [
            {"Id": "b" * 64, "Config": {"Labels": {"nyadb.fixture": "dameng", "nyadb.fixture.nonce": "nonce"}}},
            {"Id": identifier, "Config": {"Labels": {"nyadb.fixture": "kingbase", "nyadb.fixture.nonce": "nonce"}}},
            {"Id": identifier, "Config": {"Labels": {"nyadb.fixture": "dameng", "nyadb.fixture.nonce": "other"}}},
            {"Id": identifier, "Config": None},
            {"Id": identifier},
            {"Id": "", "Config": {"Labels": {"nyadb.fixture": "dameng", "nyadb.fixture.nonce": "nonce"}}},
        ]
        for raw in [json.dumps([row]) for row in rows] + ["invalid-json", "[]", "null"]:
            with self.subTest(raw=raw), patch.object(FIXTURE.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, raw)) as run:
                FIXTURE.remove_owned(identifier, "nonce")
                self.assertEqual(run.call_count, 1)
        with patch.object(FIXTURE.subprocess, "run", return_value=subprocess.CompletedProcess([], 1, "")) as run:
            FIXTURE.remove_owned(identifier, "nonce")
            self.assertEqual(run.call_count, 1)


if __name__ == "__main__":
    unittest.main()
