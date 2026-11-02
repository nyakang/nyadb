import importlib.util
from pathlib import Path
import sys
import tempfile
import unittest

TOOLS = Path(__file__).resolve().parents[1] / "tools"
sys.path.insert(0, str(TOOLS))
spec = importlib.util.spec_from_file_location("dictionary_capture", TOOLS / "capture_dictionary_acceptance.py")
capture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(capture)


class DictionaryArtifactTests(unittest.TestCase):
    def test_accepts_both_escaped_formats_and_rejects_wrong_scope_or_rows(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "dictionary"
            for format in ("html", "markdown"):
                text = "main / aux aux_only dictionary_label dictionary_view"
                if format == "markdown":
                    text = text.replace("_", "&#95;")
                path.write_text(text)
                self.assertEqual(len(capture.verify_artifact(path, format)), 64)
                for invalid in (text.replace("aux", "wrong", 1), text + " attached fixture"):
                    path.write_text(invalid)
                    with self.assertRaises(RuntimeError):
                        capture.verify_artifact(path, format)
