import ctypes
import importlib.util
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import Mock, patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "tools"))
import capture_alignment_gui_linux as capture


class NativeCaptureTests(unittest.TestCase):
    def test_absent_scrot_uses_available_backend(self):
        fixture = capture.NativeCapture.__new__(capture.NativeCapture)
        fixture.run = Mock()
        with patch.object(capture.shutil, "which", side_effect=lambda x: "/usr/bin/import" if x == "import" else None):
            fixture.screenshot(Path("frame.png"))
        self.assertEqual(fixture.screenshot_backend, "imagemagick-import")
        self.assertEqual(fixture.run.call_args.args[0], "import")

    def test_failed_scrot_falls_back_without_accepting_failed_frame(self):
        fixture = capture.NativeCapture.__new__(capture.NativeCapture)
        fixture.run = Mock(side_effect=[subprocess.CalledProcessError(1, "scrot"), ""])
        path = Mock()
        with patch.object(capture.shutil, "which", return_value="available"):
            fixture.screenshot(path)
        path.unlink.assert_called_once_with(missing_ok=True)
        self.assertEqual(fixture.screenshot_backend, "imagemagick-import")

    def test_f2_resolves_keymap_and_releases_only_held_modifiers(self):
        fixture = capture.NativeCapture.__new__(capture.NativeCapture)
        fixture.connection = 1
        fixture.x11 = Mock()
        fixture.xtest = Mock()
        fixture.x11.XStringToKeysym.return_value = 0xffbf
        fixture.x11.XKeysymToKeycode.return_value = 71  # deliberately not fixed F2 code
        modifier_codes = (ctypes.c_ubyte * 8)(50, 0, 37, 64, 0, 0, 0, 0)
        mapping = capture.XModifierKeymap(1, modifier_codes)
        fixture.x11.XGetModifierMapping.return_value = ctypes.pointer(mapping)
        def pressed(_, buf):
            buf[64 // 8] |= 1 << (64 % 8)
        fixture.x11.XQueryKeymap.side_effect = pressed
        fixture.function_key("F2")
        events = [(call.args[1], bool(call.args[2])) for call in fixture.xtest.XTestFakeKeyEvent.call_args_list]
        self.assertEqual(events, [(64, False), (71, True), (71, False), (64, True)])
        fixture.x11.XFreeModifiermap.assert_called_once()

    def test_explicit_chords_keep_requested_modifiers(self):
        fixture = capture.NativeCapture.__new__(capture.NativeCapture)
        fixture.run, fixture.function_key = Mock(), Mock()
        fixture.key("F2", "ctrl+s", "alt+F2")
        fixture.function_key.assert_called_once_with("F2")
        self.assertEqual([c.args[-1] for c in fixture.run.call_args_list], ["ctrl+s", "alt+F2"])
