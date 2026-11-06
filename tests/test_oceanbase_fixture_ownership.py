import importlib.util
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("oceanbase_fixture", Path(__file__).resolve().parents[1] / "tools" / "verify_oceanbase_fixture.py")
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)


class OceanBaseFixtureOwnershipTests(unittest.TestCase):
    def test_foreign_container_is_never_removed(self):
        value = [{"Id": "foreign-id", "Config": {"Labels": {"nyadb.fixture": "oceanbase", "nyadb.fixture.nonce": "foreign"}}}]
        with patch.object(fixture.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, json.dumps(value))) as run:
            fixture.remove_owned("container", "reused-name", "ours")
            self.assertEqual(run.call_count, 1)

    def test_cleanup_uses_verified_id(self):
        value = [{"Id": "our-id", "Config": {"Labels": {"nyadb.fixture": "oceanbase", "nyadb.fixture.nonce": "ours"}}}]
        with patch.object(fixture.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, json.dumps(value))) as run:
            fixture.remove_owned("container", "reused-name", "ours")
            self.assertEqual(run.call_args.args[0], ["docker", "rm", "-f", "our-id"])

    def test_missing_labels_and_malformed_inspect_cannot_authorize_cleanup(self):
        for raw in ["invalid", "[]", json.dumps([{"Id": "foreign-id", "Config": {"Labels": None}}])]:
            with patch.object(fixture.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, raw)) as run:
                fixture.remove_owned("container", "name", "ours")
                self.assertEqual(run.call_count, 1)


if __name__ == "__main__":
    unittest.main()
