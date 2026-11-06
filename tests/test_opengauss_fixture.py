"""Ownership and secret-redaction checks; no real Docker containers are used."""
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tarfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("opengauss_fixture", Path(__file__).resolve().parents[1] / "tools" / "verify_opengauss_fixture.py")
FIXTURE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FIXTURE)


class OpenGaussFixtureTests(unittest.TestCase):
    def test_cleanup_only_removes_inspected_owned_id(self):
        inspected = [{"Id": "immutable-owned-id", "Config": {"Labels": {"nyadb.fixture": "opengauss", "nyadb.fixture.nonce": "nonce"}}}]
        with patch.object(FIXTURE.subprocess, "run", side_effect=[subprocess.CompletedProcess([], 0, json.dumps(inspected)), subprocess.CompletedProcess([], 0)]) as run:
            FIXTURE.remove_owned("reusable-name", "nonce")
            self.assertEqual(run.call_args_list[1].args[0], ["docker", "rm", "-f", "immutable-owned-id"])

    def test_cleanup_preserves_other_containers_and_malformed_inspection(self):
        for labels in [{}, {"nyadb.fixture": "mysql", "nyadb.fixture.nonce": "nonce"}, {"nyadb.fixture": "opengauss", "nyadb.fixture.nonce": "someone-else"}]:
            value = [{"Id": "untouched", "Config": {"Labels": labels}}]
            with patch.object(FIXTURE.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, json.dumps(value))) as run:
                FIXTURE.remove_owned("name", "nonce")
                self.assertEqual(run.call_count, 1)
        for raw in ["not-json", "[]", '[{"Id": "untouched", "Config": null}]']:
            with patch.object(FIXTURE.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, raw)) as run:
                FIXTURE.remove_owned("name", "nonce")
                self.assertEqual(run.call_count, 1)

    def test_failure_diagnostics_redact_both_secrets_in_server_logs(self):
        archive = io.BytesIO()
        with tarfile.open(fileobj=archive, mode="w") as output:
            content = b"server says role-secret and administrator-secret"
            member = tarfile.TarInfo("pg_log/server.log")
            member.size = len(content)
            output.addfile(member, io.BytesIO(content))
        results = [subprocess.CompletedProcess([], 0, "role-secret", "administrator-secret"), subprocess.CompletedProcess([], 0, archive.getvalue()), subprocess.CompletedProcess([], 1, b"", b"")]
        with patch.object(FIXTURE.subprocess, "run", side_effect=results):
            logs = FIXTURE.failure_logs("owned", "role-secret", "administrator-secret")
            self.assertNotIn("role-secret", logs)
            self.assertNotIn("administrator-secret", logs)
            self.assertIn("server says [redacted] and [redacted]", logs)

    def test_stalled_sql_probe_is_bounded(self):
        with patch.object(FIXTURE.subprocess, "run", side_effect=subprocess.TimeoutExpired([], 10)):
            self.assertEqual(FIXTURE.fixture_sql(["owned-probe"], "SELECT 1").returncode, 124)


if __name__ == "__main__":
    unittest.main()
