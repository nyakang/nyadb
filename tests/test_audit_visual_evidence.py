"""Synthetic self-tests: no app launch, no database connections or V claims."""
import hashlib
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path

from PIL import Image

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("audit_visual_evidence",
                                              ROOT / "tools" / "audit_visual_evidence.py")
audit = importlib.util.module_from_spec(spec)
spec.loader.exec_module(audit)


class CaptureEvidenceAuditTests(unittest.TestCase):
    def setUp(self):
        self.work = tempfile.TemporaryDirectory()
        self.addCleanup(self.work.cleanup)
        self.base = Path(self.work.name)
        self.img = self.base / "capture.png"
        Image.new("RGB", (320, 220), (25, 34, 38)).save(self.img)
        self.record = self.base / "capture.json"
        self.item = {
            "application": "nyadb", "scene": "data",
            "source_version": "sha256:" + "a" * 64,
            "screenshot": "capture.png",
            "screenshot_sha256": hashlib.sha256(self.img.read_bytes()).hexdigest(),
            "client_crop": [20, 10, 180, 130], "viewport_width": 160,
            "viewport_height": 120, "observation_evidence": ["capture.json"],
            "platform": "linux-x11", "locale": "zh-Hans", "theme": "dark",
            "render_ready": True, "render_stability": {"consecutive_stable_comparisons": 3},
            "dpi_x": 96, "dpi_y": 96, "actual_scale": 1.0,
            "font_family": "Noto Sans CJK SC", "font_size_dip": 12,
            "ui_state": {"database_backed": True, "loading": False, "selection": ["field:id"],
                         "expanded": [], "menu_state": "column-context"},
            "database_host_port": "127.0.0.1:34306", "server_identity": "server-uuid",
            "fixture_fingerprint": "fixture-sha256", "rows_fingerprint": "rows-sha256",
            "schema": "nyadb_struct_a", "table": "child", "read_status": "success",
            "selected_column": "id", "visible_columns": ["id", "name"], "sort_rules": [],
        }

    def check(self, expected):
        self.record.write_text(json.dumps(self.item))
        result = audit.audit(self.record, "nyadb")
        self.assertEqual(result["status"], expected, result)
        return result

    def test_valid_one_sided_record_is_only_side_ready(self):
        result = self.check("SIDE_READY")
        self.assertIn("does not authenticate", result["note"])

    def test_actual_fixture_capture_unknown_scale_and_font_are_blocked(self):
        self.item["actual_scale"] = "unknown"
        self.item["font_family"] = "unknown"
        self.item["font_size_dip"] = "unknown"
        result = self.check("BLOCKED")
        self.assertTrue(any("actual_scale" in e for e in result["blockers"]))
        self.assertTrue(any("font_family" in e for e in result["blockers"]))

    def test_image_integrity_and_geometry_cannot_be_faked_by_metadata(self):
        self.item["screenshot_sha256"] = "0" * 64
        self.item["viewport_width"] = 161
        result = self.check("BLOCKED")
        self.assertTrue(any("screenshot_sha256" in e for e in result["blockers"]))
        self.assertTrue(any("client_crop" in e for e in result["blockers"]))

    def test_picture_paths_must_stay_inside_capture_directory(self):
        self.item["screenshot"] = "../capture.png"
        result = self.check("BLOCKED")
        self.assertTrue(any("screenshot" in e for e in result["blockers"]))

    def test_real_database_read_identity_is_not_inferred_from_screenshot(self):
        del self.item["server_identity"]
        del self.item["rows_fingerprint"]
        result = self.check("BLOCKED")
        self.assertTrue(any("server_identity" in e for e in result["blockers"]))
        self.assertTrue(any("rows_fingerprint" in e for e in result["blockers"]))

    def test_unknown_or_loading_state_is_blocked(self):
        self.item["ui_state"]["loading"] = True
        self.item["render_stability"] = {"consecutive_stable_comparisons": 1}
        result = self.check("BLOCKED")
        self.assertTrue(any("ui_state.loading" in e for e in result["blockers"]))
        self.assertTrue(any("render_stability" in e for e in result["blockers"]))

    def test_nyadb_observation_cannot_be_labeled_as_original(self):
        self.record.write_text(json.dumps(self.item))
        result = audit.audit(self.record, "navicat")
        self.assertEqual(result["status"], "BLOCKED")
        self.assertTrue(any("application" in e for e in result["blockers"]))

    def test_non_database_scene_does_not_require_row_fingerprint(self):
        self.item["scene"] = "workbench"
        self.item["ui_state"]["database_backed"] = False
        for key in ("database_host_port", "server_identity", "fixture_fingerprint",
                    "rows_fingerprint", "schema", "table", "read_status",
                    "selected_column", "visible_columns", "sort_rules"):
            del self.item[key]
        self.check("SIDE_READY")


if __name__ == "__main__":
    unittest.main()
