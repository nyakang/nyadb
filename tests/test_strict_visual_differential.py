"""Non-production self-tests for evidence-gated screenshot comparison."""
import hashlib
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path
from copy import deepcopy

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


class SceneVisualDifferentialTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        common = {
            "scene": "workbench", "source_version": "captured-build-01",
            "platform": "linux-x11", "locale": "zh-Hans", "theme": "dark",
            "actual_scale": 1, "font_family": "Noto Sans CJK SC", "font_size_dip": 12,
            "dpi_x": 96, "dpi_y": 96, "viewport_width": 160, "viewport_height": 120,
            "render_ready": True,
            "ui_state": {"database_backed": False, "loading": False,
                         "selection": [], "expanded": [], "menu_state": "closed"},
        }
        self.manifest = {
            "manifest_version": 2, "scene": "workbench",
            "regions": {"workspace": [0, 0, 160, 120]},
            "mae_thresholds": {"workspace": 8},
            "geometry": {"toolbar": {"navicat": [10, 10, 80, 20], "nyadb": [10, 10, 80, 20],
                                      "navicat_evidence": "navicat.txt", "nyadb_evidence": "nyadb.txt"}},
            "color_samples": {"background": {"navicat": [0, 0, 8, 8], "nyadb": [0, 0, 8, 8]}},
        }
        for side, offset in (("navicat", (20, 20)), ("nyadb", (40, 30))):
            (self.base / f"{side}.txt").write_text("Observation and measured toolbar rectangle.")
            self.manifest[side] = dict(deepcopy(common), screenshot=f"{side}.png",
                                       observation_evidence=[f"{side}.txt"],
                                       client_crop=[offset[0], offset[1], offset[0] + 160, offset[1] + 120])
            self.write_client(side, (35, 38, 41))

    def write_client(self, side, color):
        width = 400 if side == "navicat" else 500
        image = Image.new("RGB", (width, 300), "black")
        image.paste(Image.new("RGB", (160, 120), color), tuple(self.manifest[side]["client_crop"][:2]))
        image.save(self.base / f"{side}.png")

    def check(self, expected):
        result = visual.evaluate(self.manifest, self.base)
        self.assertEqual(result["status"], expected, result)
        if expected == "BLOCKED":
            self.assertEqual(result["regions"], {}, "blocked evidence must not produce pixel metrics")
        return result

    def database_scene(self, scene):
        self.manifest["scene"] = scene
        for side in ("navicat", "nyadb"):
            item = self.manifest[side]
            item["scene"] = scene
            item["ui_state"]["database_backed"] = True
            item.update(database_host_port="127.0.0.1:34306", server_identity="test-server-uuid",
                        fixture_fingerprint="fixture-sha256", rows_fingerprint="result-sha256",
                        schema="fixture", read_status="success")
            if scene in {"structure", "data"}:
                item["table"] = "child"
            if scene == "data":
                item.update(selected_column="id", visible_columns=["id", "name"], sort_rules=["id asc"])

    def test_workbench_at_different_desktop_positions_without_database_passes(self):
        result = self.check("PASS")
        self.assertEqual(result["geometry"]["toolbar"]["delta_dip"], [0, 0, 0, 0])
        self.assertEqual(result["color_samples"]["background"]["navicat_rgb"], [35, 38, 41])
        self.assertIn("navicat.txt", result["observation_evidence_sha256"]["navicat"])

    def test_connection_scene_accepts_its_own_client_size(self):
        self.manifest["scene"] = "connection"
        for side in ("navicat", "nyadb"):
            self.manifest[side]["scene"] = "connection"
            self.manifest[side]["ui_state"]["tab"] = "SSL"
        self.check("PASS")

    def test_each_database_scene_requires_grounded_read(self):
        for scene in ("objects", "structure", "data"):
            with self.subTest(scene=scene):
                baseline = deepcopy(self.manifest)
                self.database_scene(scene)
                self.check("PASS")
                self.manifest["navicat"]["read_status"] = "not_connected"
                self.check("BLOCKED")
                self.manifest = baseline

    def test_database_scene_cannot_be_downgraded_to_workbench(self):
        self.database_scene("data")
        self.manifest["scene"] = "workbench"
        self.check("BLOCKED")
        for side in ("navicat", "nyadb"):
            self.manifest[side]["scene"] = "workbench"
            self.manifest[side]["ui_state"]["database_backed"] = False
        self.check("BLOCKED")  # DB fingerprints still identify a database capture.

    def test_equal_unknown_database_identities_are_not_evidence(self):
        self.database_scene("data")
        for side in ("navicat", "nyadb"):
            self.manifest[side]["server_identity"] = "unknown"
        self.check("BLOCKED")

    def test_matching_db_names_do_not_replace_row_or_fixture_identity(self):
        self.database_scene("data")
        self.manifest["nyadb"]["rows_fingerprint"] = "different-snapshot"
        self.check("BLOCKED")

    def test_rendering_and_loading_must_be_ready(self):
        for field in ("render_ready", "loading"):
            with self.subTest(field=field):
                item = self.manifest["navicat"]
                if field == "loading":
                    item["ui_state"]["loading"] = True
                else:
                    item[field] = False
                self.check("BLOCKED")
                item["render_ready"] = True
                item["ui_state"]["loading"] = False

    def test_claimed_ready_black_frame_is_blocked(self):
        self.write_client("navicat", "black")
        result = self.check("BLOCKED")
        self.assertTrue(any("black frame" in message for message in result["gate_errors"]))

    def test_same_size_but_different_font_scale_or_selection_is_blocked(self):
        for key, value in (("font_family", "Different Font"), ("actual_scale", 1.25),
                           ("dpi_x", 100), ("locale", "en"), ("font_size_dip", 13)):
            with self.subTest(key=key):
                original = self.manifest["nyadb"][key]
                self.manifest["nyadb"][key] = value
                self.check("BLOCKED")
                self.manifest["nyadb"][key] = original
        self.manifest["nyadb"]["ui_state"]["selection"] = ["profile:2"]
        self.check("BLOCKED")

    def test_crop_must_match_viewport_and_stay_inside_screenshot(self):
        original = self.manifest["nyadb"]["client_crop"]
        self.manifest["nyadb"]["client_crop"] = [40, 30, 199, 150]
        self.check("BLOCKED")
        self.manifest["nyadb"]["client_crop"] = [490, 30, 650, 150]
        self.check("BLOCKED")
        self.manifest["nyadb"]["client_crop"] = original

    def test_geometry_tolerance_is_two_dip_and_requires_measurement_source(self):
        sample = self.manifest["geometry"]["toolbar"]
        sample["nyadb"][0] += 2
        self.check("PASS")
        sample["nyadb"][0] += 1
        self.check("FAIL")
        sample["nyadb_evidence"] = "unrecorded.txt"
        self.check("BLOCKED")

    def test_geometry_is_normalized_by_actual_scale(self):
        for side in ("navicat", "nyadb"):
            self.manifest[side]["actual_scale"] = 1.5
        self.manifest["geometry"]["toolbar"]["nyadb"][0] += 3
        self.check("PASS")  # 3 device pixels = 2 DIP.

    def test_color_tolerance_cannot_be_hidden_by_loose_roi_threshold(self):
        self.manifest["mae_thresholds"]["workspace"] = 255
        self.write_client("nyadb", (38, 41, 44))
        self.check("PASS")
        self.write_client("nyadb", (39, 38, 41))
        self.check("FAIL")

    def test_nonuniform_color_sample_is_insufficient_evidence(self):
        path = self.base / "navicat.png"
        with Image.open(path) as image:
            image.putpixel((20, 20), (255, 0, 0))
            image.save(path)
        self.check("BLOCKED")

    def test_roi_pixels_can_fail_even_when_landmarks_and_colors_match(self):
        path = self.base / "nyadb.png"
        with Image.open(path) as image:
            image.paste("white", (60, 70, 170, 140))
            image.save(path)
        result = self.check("FAIL")
        self.assertTrue(any("MAE" in message for message in result["gate_errors"]))

    def test_missing_or_nonfinite_threshold_fails(self):
        for value in (None, float("nan"), -1):
            self.manifest["mae_thresholds"]["workspace"] = value
            self.check("FAIL")
        self.manifest["mae_thresholds"] = {}
        self.check("FAIL")

    def test_missing_scene_source_evidence_or_landmarks_blocks(self):
        for group in ("scene", "geometry", "color_samples", "regions"):
            original = self.manifest.pop(group)
            self.check("BLOCKED")
            self.manifest[group] = original
        self.manifest["navicat"]["source_version"] = "unknown"
        self.check("BLOCKED")
        self.manifest["navicat"]["source_version"] = "known-build"
        self.manifest["navicat"]["observation_evidence"] = []
        self.check("BLOCKED")

    def test_native_observation_json_binds_image_identity_and_state(self):
        for side in ("navicat", "nyadb"):
            item = self.manifest[side]
            capture = {
                "application": side,
                "scene": item["scene"],
                "source_version": item["source_version"],
                "platform": item["platform"],
                "locale": item["locale"],
                "theme": item["theme"],
                "viewport_width": item["viewport_width"],
                "viewport_height": item["viewport_height"],
                "client_crop": item["client_crop"],
                "actual_scale": item["actual_scale"],
                "font_family": item["font_family"],
                "font_size_dip": item["font_size_dip"],
                "dpi_x": item["dpi_x"],
                "dpi_y": item["dpi_y"],
                "ui_state": item["ui_state"],
                "screenshot_sha256": hashlib.sha256(
                    (self.base / item["screenshot"]).read_bytes()).hexdigest(),
            }
            (self.base / f"{side}-capture.json").write_text(json.dumps(capture))
            item["observation_evidence"].append(f"{side}-capture.json")
        self.check("PASS")
        with Image.open(self.base / "nyadb.png") as pic:
            pic.putpixel((50, 50), (40, 38, 41))
            pic.save(self.base / "nyadb.png")
        result = self.check("BLOCKED")
        self.assertTrue(any("digest" in e for e in result["gate_errors"]))
        self.write_client("nyadb", (35, 38, 41))
        self.manifest["nyadb"]["ui_state"]["menu_state"] = "column-context"
        result = self.check("BLOCKED")
        self.assertTrue(any("ui_state differs from observation" in e for e in result["gate_errors"]))

    def test_misattributed_native_capture_does_not_count_as_original(self):
        item = self.manifest["navicat"]
        name = "wrong-original-capture.json"
        (self.base / name).write_text(json.dumps({
            "application": "nyadb", "screenshot_sha256":
                hashlib.sha256((self.base / "navicat.png").read_bytes()).hexdigest()}))
        item["observation_evidence"].append(name)
        result = self.check("BLOCKED")
        self.assertTrue(any("application identity" in e for e in result["gate_errors"]))


if __name__ == "__main__":
    unittest.main()
