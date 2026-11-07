"""StarRocks fixture cleanup only addresses its immutable owned container ID."""
import importlib.util
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("starrocks_fixture", Path(__file__).resolve().parents[1] / "tools" / "verify_starrocks_fixture.py")
FIXTURE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FIXTURE)


class StarRocksFixtureTests(unittest.TestCase):
    def test_cleanup_removes_only_original_owned_id(self):
        identifier = "a" * 64
        inspected = [{"Id": identifier, "Config": {"Labels": {"nyadb.fixture": "starrocks", "nyadb.fixture.nonce": "nonce"}}}]
        with patch.object(FIXTURE.subprocess, "run", side_effect=[subprocess.CompletedProcess([], 0, json.dumps(inspected)), subprocess.CompletedProcess([], 0)]) as run:
            FIXTURE.remove_owned(identifier, "nonce")
            self.assertEqual(run.call_args_list[1].args[0], ["docker", "rm", "-f", identifier])

    def test_cleanup_refuses_foreign_or_replaced_ownership(self):
        for identifier, labels in [("b" * 64, {"nyadb.fixture": "starrocks", "nyadb.fixture.nonce": "nonce"}), ("a" * 64, {}), ("a" * 64, {"nyadb.fixture": "starrocks", "nyadb.fixture.nonce": "other"}), ("a" * 64, {"nyadb.fixture": "mysql", "nyadb.fixture.nonce": "nonce"})]:
            inspected = [{"Id": identifier, "Config": {"Labels": labels}}]
            with patch.object(FIXTURE.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, json.dumps(inspected))) as run:
                with self.assertRaises(RuntimeError):
                    FIXTURE.remove_owned("a" * 64, "nonce")
                self.assertEqual(run.call_count, 1)

    def test_cleanup_rejects_mutable_names_and_preserves_missing_id(self):
        for value in ["reusable-name", "", None, "A" * 64]:
            with patch.object(FIXTURE.subprocess, "run") as run:
                with self.assertRaises(RuntimeError):
                    FIXTURE.remove_owned(value, "nonce")
                run.assert_not_called()
        with patch.object(FIXTURE.subprocess, "run", return_value=subprocess.CompletedProcess([], 1, "")) as run:
            FIXTURE.remove_owned("a" * 64, "nonce")
            self.assertEqual(run.call_count, 1)


if __name__ == "__main__":
    unittest.main()
