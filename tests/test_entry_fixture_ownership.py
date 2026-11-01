import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "tools"))
import verify_entry_acceptance as fixture


class FixtureOwnershipTests(unittest.TestCase):
    def test_pid_reuse_is_never_terminated(self):
        with patch.object(fixture, "birth", return_value="new-start"), patch.object(fixture.os, "killpg") as kill:
            fixture.terminate({"pid": 999, "birth": "original-start"})
        kill.assert_not_called()

    def test_unknown_process_birth_is_never_terminated(self):
        with patch.object(fixture, "birth", return_value=None), patch.object(fixture.os, "killpg") as kill:
            fixture.terminate({"pid": 999, "birth": None})
        kill.assert_not_called()

    def test_foreign_container_is_never_removed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            secret = root / "secrets.json"
            secret.write_text("isolated secret")
            state = {"id": "owned-id", "container": "fixture-name", "processes": {}}
            inspected = Mock(returncode=0, stdout="different-label\n")
            with patch.object(fixture.subprocess, "run", return_value=inspected), patch.object(fixture, "command") as command:
                fixture.stop(root, state)
            command.assert_not_called()
            self.assertFalse(secret.exists())
            self.assertTrue(json.loads((root / "run.json").read_text())["stopped"])

    def test_mismatched_marker_cannot_authorize_cleanup(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            root = base / "r-fixture"
            root.mkdir()
            (root / ".owned").write_text("unrelated-id")
            (root / "run.json").write_text(json.dumps({"id": "owned-id"}))
            with patch.object(fixture, "BASE", base):
                with self.assertRaisesRegex(RuntimeError, "identity mismatch"):
                    fixture.validate(root)

    def test_run_outside_ignored_fixture_directory_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(RuntimeError, "owned acceptance"):
                fixture.validate(Path(directory))
