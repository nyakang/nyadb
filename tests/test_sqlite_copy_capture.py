import importlib.util
from pathlib import Path
import sqlite3
import sys
import tempfile
import unittest

TOOLS = Path(__file__).resolve().parents[1] / "tools"
sys.path.insert(0, str(TOOLS))
spec = importlib.util.spec_from_file_location("sqlite_copy_capture", TOOLS / "capture_sqlite_copy_acceptance.py")
capture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(capture)


class SQLiteCopyArtifactTests(unittest.TestCase):
    def fixture(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        path = Path(directory.name) / "owned.sqlite"
        db = sqlite3.connect(path)
        self.addCleanup(db.close)
        for name in ("shared", "copy_data", "copy_empty"):
            index = "copy_index" if name == "shared" else name + "_copy_index"
            trigger = "copy_trigger" if name == "shared" else name + "_copy_trigger"
            db.executescript(f"""
CREATE TABLE {name}(id INTEGER PRIMARY KEY AUTOINCREMENT,parent INTEGER REFERENCES {name}(id),label TEXT,raw BLOB,n INTEGER,amount TEXT,generated TEXT GENERATED ALWAYS AS (label || ':' || n) STORED,CHECK(n>=0)) STRICT;
CREATE UNIQUE INDEX {index} ON {name}(label COLLATE NOCASE DESC) WHERE label IS NOT NULL;
CREATE TRIGGER {trigger} AFTER UPDATE OF label ON {name} BEGIN UPDATE shared SET label=new.label WHERE id=new.id; END;
""")
            if name != "copy_empty":
                db.executescript(f"""
INSERT INTO {name}(id,parent,label,raw,n,amount) VALUES(1,1,'saved',x'00ff',9223372036854775806,'12345678901234567890.123456789'),(2,1,NULL,NULL,0,NULL),(100,1,'deleted',x'01',1,'0');
DELETE FROM {name} WHERE id=100;
""")
        db.commit()
        return path, db

    def test_full_and_structure_only_artifacts(self):
        path, _ = self.fixture()
        original = capture.table_state(path, "shared")
        capture.require_copy(original, capture.table_state(path, "copy_data"), "copy_data", True)
        capture.require_copy(original, capture.table_state(path, "copy_empty"), "copy_empty", False)
        self.assertIsNone(capture.table_state(path, "absent"))

    def test_rejects_changed_typed_rows_sequence_and_definition(self):
        for sql in ("UPDATE copy_data SET raw=x'00fe' WHERE id=1", "UPDATE copy_data SET amount='12345678901234567890.123456788' WHERE id=1", "UPDATE copy_data SET raw=x'' WHERE id=2", "UPDATE sqlite_sequence SET seq=2 WHERE name='copy_data'", "DROP TRIGGER copy_data_copy_trigger"):
            with self.subTest(sql=sql):
                path, db = self.fixture()
                original = capture.table_state(path, "shared")
                db.execute(sql)
                db.commit()
                with self.assertRaises(RuntimeError):
                    capture.require_copy(original, capture.table_state(path, "copy_data"), "copy_data", True)

    def test_identifier_keeps_quotes_as_identifier(self):
        self.assertEqual(capture.identifier('with"quote'), '"with""quote"')


if __name__ == "__main__":
    unittest.main()
