import importlib.util
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("cockroach_fixture", Path(__file__).resolve().parents[1] / "tools" / "verify_cockroach_fixture.py")
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)


class CockroachFixtureOwnershipTests(unittest.TestCase):
    def test_foreign_name_or_product_cannot_authorize_cleanup(self):
        for labels in [
            {"nyadb.fixture": "cockroach", "nyadb.fixture.nonce": "foreign"},
            {"nyadb.fixture": "postgres", "nyadb.fixture.nonce": "ours"},
        ]:
            value = [{"Id": "foreign-id", "Config": {"Labels": labels}}]
            with patch.object(fixture.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, json.dumps(value))) as run:
                fixture.remove_owned("reused-name", "ours")
                self.assertEqual(run.call_count, 1)

    def test_cleanup_uses_verified_immutable_id(self):
        value = [{"Id": "our-id", "Config": {"Labels": {"nyadb.fixture": "cockroach", "nyadb.fixture.nonce": "ours"}}}]
        with patch.object(fixture.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, json.dumps(value))) as run:
            fixture.remove_owned("reused-name", "ours")
            self.assertEqual(run.call_args.args[0], ["docker", "rm", "-f", "our-id"])

    def test_missing_labels_malformed_or_absent_inspect_never_removes(self):
        for raw in ["invalid", "[]", "null", json.dumps([{"Config": None}]), json.dumps([{"Id": "foreign-id", "Config": {"Labels": None}}])]:
            with patch.object(fixture.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, raw)) as run:
                fixture.remove_owned("container", "ours")
                self.assertEqual(run.call_count, 1)
        with patch.object(fixture.subprocess, "run", return_value=subprocess.CompletedProcess([], 1, "")) as run:
            fixture.remove_owned("container", "ours")
            self.assertEqual(run.call_count, 1)


if __name__ == "__main__":
    unittest.main()
