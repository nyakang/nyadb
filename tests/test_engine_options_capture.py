import importlib.util
from contextlib import closing
import json
from pathlib import Path
import sqlite3
import sys
import tempfile
import unittest

TOOLS = Path(__file__).resolve().parents[1] / "tools"
sys.path.insert(0, str(TOOLS))
spec = importlib.util.spec_from_file_location("engine_options_capture", TOOLS / "capture_engine_options_acceptance.py")
capture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(capture)


class EngineOptionsCaptureTests(unittest.TestCase):
    def test_configuration_identity_rejects_wrong_file_and_envelope(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config = root / "config.sqlite"
            with closing(sqlite3.connect(config)) as db, db:
                db.execute("CREATE TABLE connection_profiles(id INTEGER,kind TEXT,name TEXT,mysql_advanced_json TEXT)")
                envelope = dict(version=1,kind="sqlite",sqlite=dict(version=1,attachments=[dict(name="aux",file=str(root / "engine-attached.sqlite"))]))
                db.execute("INSERT INTO connection_profiles VALUES(7,'sqlite',?,?)", ("验收 SQLite 附加库",json.dumps(envelope)))
            self.assertEqual(capture.check_attachment(config,root),7)
            for mutation in ("file","kind","version"):
                bad = json.loads(json.dumps(envelope))
                if mutation == "file":
                    bad["sqlite"]["attachments"][0]["file"] = str(root / "main.sqlite")
                elif mutation == "kind":
                    bad["kind"] = "postgresql"
                else:
                    bad["version"] = 2
                with closing(sqlite3.connect(config)) as db, db:
                    db.execute("UPDATE connection_profiles SET mysql_advanced_json=?",(json.dumps(bad),))
                with self.assertRaises(RuntimeError):
                    capture.check_attachment(config,root)


if __name__ == "__main__":
    unittest.main()
