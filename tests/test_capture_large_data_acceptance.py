from pathlib import Path
import sqlite3
import sys
import tempfile
import unittest

TOOLS = Path(__file__).resolve().parents[1] / "tools"
sys.path.insert(0, str(TOOLS))
import capture_large_data_acceptance as capture


class LargeDataFixtureTest(unittest.TestCase):
    def test_fixture_is_exact_repeatable_read_only_and_refuses_reuse(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "fixture.sqlite"
            capture.seed(path)
            before = capture.fingerprint(path)
            self.assertEqual(before["rows"], 250)
            self.assertEqual(before,capture.expected_fingerprint())
            self.assertEqual(before, capture.fingerprint(path))
            with self.assertRaises(RuntimeError):
                capture.seed(path)
            self.assertEqual(before, capture.fingerprint(path))
            with sqlite3.connect(path) as db:
                db.execute("UPDATE advanced SET n=0 WHERE id=1")
            self.assertNotEqual(before, capture.fingerprint(path))


if __name__ == "__main__":
    unittest.main()
