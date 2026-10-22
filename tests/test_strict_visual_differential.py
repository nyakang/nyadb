"""Non-production self-tests for evidence-gated screenshot comparison."""
import importlib.util
import tempfile
import unittest
from pathlib import Path

from PIL import Image


module_path = Path(__file__).resolve().parents[1] / "tools" / "strict_visual_differential.py"
spec = importlib.util.spec_from_file_location("strict_visual_differential", module_path)
visual = importlib.util.module_from_spec(spec)
spec.loader.exec_module(visual)


class StrictVisualDifferentialTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        for name in ("navicat.png", "nyadb.png"):
            Image.new("RGB", (1600, 960), (12, 32, 45)).save(self.base / name)
        (self.base / "original-confirmed.txt").write_text("explicit live evidence")
        (self.base / "candidate-confirmed.txt").write_text("explicit live evidence")
        row = {
            "database_host_port": "127.0.0.1:34306",
            "server_identity": "ephemeral-server-uuid-01",
            "fixture_fingerprint": "schema-checksum-abc",
            "rows_fingerprint": "rows-3-sha256-abc",
            "schema": "nyadb_struct_a", "table": "child",
            "read_status": "success", "dpi_x": 100.099, "dpi_y": 99.934,
            "viewport_width": 1280, "viewport_height": 800,
            "selected_column": "id", "visible_columns": ["id", "tenant_id"],
            "sort_rules": ["id asc"], "menu_state": "closed",
        }
        self.manifest = {
            "crop": [160, 80, 1440, 880],
            "regions": {"data": [251, 140, 1075, 740]},
            "mae_thresholds": {"data": 8.0},
            "navicat": dict(row, screenshot="navicat.png", observation_evidence=["original-confirmed.txt"]),
            "nyadb": dict(row, screenshot="nyadb.png", observation_evidence=["candidate-confirmed.txt"]),
        }

    def test_identical_images_with_grounded_state_pass(self):
        result = visual.evaluate(self.manifest, self.base)
        self.assertEqual(result["status"], "PASS")
        self.assertAlmostEqual(result["regions"]["data"]["rgb_mae"], 0)

    def test_missing_live_navicat_data_blocks_even_if_images_match(self):
        self.manifest["navicat"]["read_status"] = "1045"
        result = visual.evaluate(self.manifest, self.base)
        self.assertEqual(result["status"], "BLOCKED")
        self.assertIn("navicat: live database data read not confirmed", result["gate_errors"])

    def test_mismatched_rows_and_dpi_block(self):
        self.manifest["navicat"]["rows_fingerprint"] = "old-rows"
        self.manifest["navicat"]["dpi_x"] = 96
        result = visual.evaluate(self.manifest, self.base)
        self.assertEqual(result["status"], "BLOCKED")
        self.assertTrue(any("rows_fingerprint" in s for s in result["gate_errors"]))
        self.assertTrue(any("dpi_x" in s for s in result["gate_errors"]))

    def test_matched_evidence_with_pixel_difference_fails(self):
        Image.new("RGB", (1600, 960), (250, 250, 250)).save(self.base / "nyadb.png")
        result = visual.evaluate(self.manifest, self.base)
        self.assertEqual(result["status"], "FAIL")
        self.assertTrue(any("data: MAE" in s for s in result["gate_errors"]))

    def test_missing_roi_threshold_fails(self):
        del self.manifest["mae_thresholds"]["data"]
        result = visual.evaluate(self.manifest, self.base)
        self.assertEqual(result["status"], "FAIL")

    def test_incorrect_window_size_blocks_pixel_evaluation(self):
        Image.new("RGB", (1280, 800), (12, 32, 45)).save(self.base / "nyadb.png")
        result = visual.evaluate(self.manifest, self.base)
        self.assertEqual(result["status"], "BLOCKED")
        self.assertTrue(any("pixel comparison" in s for s in result["gate_errors"]))


if __name__ == "__main__":
    unittest.main()
