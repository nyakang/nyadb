import copy
import sys
import unittest
from pathlib import Path
from PIL import Image, ImageDraw

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "tools"))
import capture_common_entries_acceptance as capture


class CommonEntryCaptureTests(unittest.TestCase):
    def directory(self):
        return {"version": 1, "entries": [{"id": "favorite", "profile_id": 3,
            "kind": "object", "name": "原生收藏表", "group_id": "group",
            "object": {"ProfileID": 3, "Catalog": "main", "Schema": "main",
                       "Kind": "table", "Name": "fixture"}}],
            "groups": [{"id": "group", "name": "原生收藏分组"}]}

    def test_restart_keeps_all_three_identities(self):
        data = self.directory()
        identity = capture.check_directory(data, "grouped")
        self.assertEqual(identity, ("favorite", "group", 3))
        capture.check_directory(data, "grouped", identity)
        for field in ("id", "profile_id", "group_id"):
            broken = copy.deepcopy(data)
            broken["entries"][0][field] = "changed"
            with self.assertRaises(RuntimeError):
                capture.check_directory(broken, "grouped", identity)

    def test_empty_and_confirmed_removal_reject_retained_records(self):
        empty = {"version": 1, "entries": [], "groups": []}
        capture.check_directory(empty, "empty")
        capture.check_directory(empty, "removed")
        for phase in ("empty", "removed"):
            with self.assertRaises(RuntimeError):
                capture.check_directory(self.directory(), phase)

    def test_qualified_identity_is_checked_beyond_display_name(self):
        for field in ("ProfileID", "Catalog", "Schema", "Kind", "Name"):
            data = self.directory()
            data["entries"][0]["object"][field] = "wrong"
            with self.assertRaises(RuntimeError):
                capture.check_directory(data, "grouped")

    def test_confirmation_detection_ignores_focus_borders_and_toolbar(self):
        frame = Image.new("RGB", (1280, 800), (40, 45, 50))
        draw = ImageDraw.Draw(frame)
        draw.rectangle((700,420,794,447),fill=(0,160,245))
        draw.rectangle((425,380,815,405),outline=(0,160,245))
        draw.rectangle((20,40,90,69),fill=(0,160,245))
        self.assertEqual(capture.primary_button_center(frame), (747,434))

    def test_ambiguous_or_absent_confirmation_never_clicks(self):
        frame = Image.new("RGB", (1280,800), (40,45,50))
        with self.assertRaises(RuntimeError):
            capture.primary_button_center(frame)
        draw = ImageDraw.Draw(frame)
        draw.rectangle((450,420,549,447),fill=(0,160,245))
        draw.rectangle((700,420,799,447),fill=(0,160,245))
        with self.assertRaises(RuntimeError):
            capture.primary_button_center(frame)


if __name__ == "__main__":
    unittest.main()
