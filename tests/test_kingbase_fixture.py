"""Check fixture cleanup cannot remove an unrelated or replaced container."""
import importlib.util
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("kingbase_fixture", Path(__file__).resolve().parents[1] / "tools" / "verify_kingbase_fixture.py")
FIXTURE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FIXTURE)


class KingbaseFixtureTests(unittest.TestCase):
    def test_cleanup_uses_only_the_verified_owned_container_id(self):
        inspected = [{"Id": "owned-id", "Config": {"Labels": {"nyadb.fixture": "kingbase", "nyadb.fixture.nonce": "nonce"}}}]
        with patch.object(FIXTURE.subprocess, "run", side_effect=[subprocess.CompletedProcess([], 0, json.dumps(inspected)), subprocess.CompletedProcess([], 0)]) as run:
            FIXTURE.remove_owned("reusable-name", "nonce")
            self.assertEqual(run.call_args_list[1].args[0], ["docker", "rm", "-f", "owned-id"])

    def test_cleanup_preserves_foreign_missing_and_malformed_containers(self):
        for labels in [{}, {"nyadb.fixture": "gaussdb", "nyadb.fixture.nonce": "nonce"}, {"nyadb.fixture": "kingbase", "nyadb.fixture.nonce": "different"}]:
            inspected = [{"Id": "untouched", "Config": {"Labels": labels}}]
            with patch.object(FIXTURE.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, json.dumps(inspected))) as run:
                FIXTURE.remove_owned("name", "nonce")
                self.assertEqual(run.call_count, 1)
        for raw in ["invalid-json", "[]", '[{"Id":"untouched","Config":null}]', '[{"Id":"","Config":{"Labels":{"nyadb.fixture":"kingbase","nyadb.fixture.nonce":"nonce"}}}]']:
            with patch.object(FIXTURE.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, raw)) as run:
                FIXTURE.remove_owned("name", "nonce")
                self.assertEqual(run.call_count, 1)
        with patch.object(FIXTURE.subprocess, "run", return_value=subprocess.CompletedProcess([], 1, "")) as run:
            FIXTURE.remove_owned("missing", "nonce")
            self.assertEqual(run.call_count, 1)


if __name__ == "__main__":
    unittest.main()
