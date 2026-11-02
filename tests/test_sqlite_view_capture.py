import importlib.util
from pathlib import Path
import sqlite3
import sys
import tempfile
import unittest

TOOLS = Path(__file__).resolve().parents[1] / "tools"
sys.path.insert(0, str(TOOLS))
spec = importlib.util.spec_from_file_location("sqlite_view_capture", TOOLS / "capture_sqlite_view_acceptance.py")
capture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(capture)


class SQLiteViewArtifactTests(unittest.TestCase):
    def fixture(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        path = Path(directory.name) / "owned.sqlite"
        db = sqlite3.connect(path)
        self.addCleanup(db.close)
        db.executescript("CREATE TABLE shared(id INTEGER PRIMARY KEY,label TEXT);INSERT INTO shared VALUES(1,'attached fixture');CREATE VIEW acceptance_view(id,label) AS SELECT id,label FROM shared;")
        db.commit()
        return path, db

    def test_original_altered_and_deleted_view_artifacts(self):
        path, db = self.fixture()
        capture.require_view(capture.view_state(path, "acceptance_view"), "acceptance_view", "attached fixture")
        db.execute("DROP VIEW acceptance_view")
        db.execute("CREATE VIEW acceptance_view(id,label) AS SELECT id,upper(label) FROM shared")
        db.execute("CREATE TRIGGER view_update INSTEAD OF UPDATE OF label ON acceptance_view BEGIN UPDATE shared SET label=new.label WHERE id=new.id; END")
        db.commit()
        capture.require_view(capture.view_state(path, "acceptance_view"), "acceptance_view", "ATTACHED FIXTURE", True)
        db.execute("DROP VIEW acceptance_view")
        db.commit()
        self.assertIsNone(capture.view_state(path, "acceptance_view"))
        self.assertEqual(db.execute("SELECT count(*) FROM sqlite_schema WHERE name='view_update'").fetchone()[0], 0)
        self.assertEqual(db.execute("SELECT id,label FROM shared").fetchall(), [(1, "attached fixture")])

    def test_rejects_changed_results_and_missing_trigger(self):
        path, db = self.fixture()
        with self.assertRaises(RuntimeError):
            capture.require_view(capture.view_state(path, "acceptance_view"), "acceptance_view", "wrong")
        with self.assertRaises(RuntimeError):
            capture.require_view(capture.view_state(path, "acceptance_view"), "acceptance_view", "attached fixture", True)
        db.execute("UPDATE shared SET label=NULL")
        db.commit()
        with self.assertRaises(RuntimeError):
            capture.require_view(capture.view_state(path, "acceptance_view"), "acceptance_view", "attached fixture")


if __name__ == "__main__":
    unittest.main()
