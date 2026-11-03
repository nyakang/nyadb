from pathlib import Path
import json
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch

TOOLS = Path(__file__).resolve().parents[1] / "tools"
sys.path.insert(0, str(TOOLS))
import capture_postgres_program_acceptance as capture


class PostgresProgramCleanupTest(unittest.TestCase):
    def test_capture_and_manifest_failures_still_release_owned_resources(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state = dict(id="owned", processes=dict(app=dict(pid=123)))
            cap = Mock()
            cap.close.side_effect = RuntimeError("capture failed")
            with patch.object(capture, "terminate") as terminate, \
                 patch.object(capture, "manifest", side_effect=OSError("manifest failed")), \
                 patch.object(capture, "command", side_effect=["owned", "removed"]) as command, \
                 patch.object(capture.subprocess, "run", return_value=subprocess.CompletedProcess([], 1)):
                with self.assertRaises(BaseExceptionGroup):
                    capture.cleanup_resources(root, state, cap, "fixture", root)
                terminate.assert_called_once_with(dict(pid=123))
                self.assertNotIn("app", state["processes"])
                self.assertEqual(command.call_args_list[-1].args[0], ["docker", "rm", "-f", "fixture"])
            record = json.loads((root / "cleanup.json").read_text())
            self.assertTrue(record["app_stopped"])
            self.assertTrue(record["container_removed"])
            self.assertEqual(len(record["errors"]), 2)

    def test_failed_termination_keeps_identity_and_foreign_container_is_retained(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state = dict(id="owned", processes=dict(app=dict(pid=123)))
            with patch.object(capture, "terminate", side_effect=OSError("terminate failed")), \
                 patch.object(capture, "manifest") as manifest, \
                 patch.object(capture, "command", return_value="foreign") as command:
                with self.assertRaises(BaseExceptionGroup):
                    capture.cleanup_resources(root, state, None, "fixture", root)
                self.assertIn("app", state["processes"])
                manifest.assert_called_once_with(root, state)
                self.assertEqual(command.call_count, 1)
            record = json.loads((root / "cleanup.json").read_text())
            self.assertFalse(record["container_removed"])
            self.assertFalse(record["app_stopped"])


if __name__ == "__main__":
    unittest.main()
