import sqlite3
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "tools"))
from capture_sqlite_designer_acceptance import check_added, fixture_state


class SQLiteDesignerEvidenceTests(unittest.TestCase):
    def fixture(self, root):
        path = Path(root) / "owned.sqlite"
        with sqlite3.connect(path) as db:
            db.execute("CREATE TABLE shared(id INTEGER PRIMARY KEY,label TEXT)")
            db.execute("INSERT INTO shared VALUES(1,'attached fixture')")
        return path

    def test_read_only_verification_does_not_create_missing_files(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "missing.sqlite"
            with self.assertRaises(sqlite3.OperationalError):
                fixture_state(path)
            self.assertFalse(path.exists())

    def test_preview_remains_unapplied_and_committed_null_column_is_verified(self):
        with tempfile.TemporaryDirectory() as root:
            path = self.fixture(root)
            self.assertEqual(fixture_state(path), (["id", "label"], (1, "attached fixture")))
            self.assertFalse(check_added(path))
            with sqlite3.connect(path) as db:
                db.execute("ALTER TABLE shared ADD COLUMN native_added TEXT")
            self.assertTrue(check_added(path))
            self.assertEqual(fixture_state(path)[1], (1, "attached fixture"))

    def test_changed_retained_values_or_unexpected_default_fail_evidence(self):
        with tempfile.TemporaryDirectory() as root:
            path = self.fixture(root)
            with sqlite3.connect(path) as db:
                db.execute("ALTER TABLE shared ADD COLUMN native_added TEXT DEFAULT 'changed'")
            with self.assertRaisesRegex(RuntimeError, "retained data"):
                check_added(path)
            with sqlite3.connect(path) as db:
                db.execute("UPDATE shared SET label='wrong namespace',native_added=NULL")
            with self.assertRaisesRegex(RuntimeError, "retained data"):
                check_added(path)


if __name__ == "__main__":
    unittest.main()
