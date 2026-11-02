import importlib.util
from pathlib import Path
import sqlite3
import sys
import tempfile
import unittest

TOOLS = Path(__file__).resolve().parents[1] / "tools"
sys.path.insert(0, str(TOOLS))
spec = importlib.util.spec_from_file_location("sqlite_drop_capture", TOOLS / "capture_sqlite_drop_acceptance.py")
capture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(capture)


class SQLiteDropArtifactTests(unittest.TestCase):
    def test_requires_table_and_own_definitions_absent(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "owned.sqlite"
            db = sqlite3.connect(path)
            self.addCleanup(db.close)
            db.executescript("CREATE TABLE shared(id,label); INSERT INTO shared VALUES(1,'attached fixture'); CREATE INDEX drop_index ON shared(label);")
            before = capture.fixture_state(path)
            with self.assertRaises(RuntimeError):
                capture.require_removed(before)
            db.execute("DROP TABLE shared")
            db.commit()
            capture.require_removed(capture.fixture_state(path))
            db.executescript("CREATE TABLE other(value); CREATE INDEX drop_index ON other(value);")
            with self.assertRaises(RuntimeError):
                capture.require_removed(capture.fixture_state(path))
