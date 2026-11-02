#!/usr/bin/env python3
"""Verify transactional SQLite deletion in an owned formal Linux process.

Invoke through verify_entry_acceptance.py. Writes affect only its attached
fixture. MySQL/PostgreSQL remain disconnected in the application.
"""
import argparse
from contextlib import closing
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import time
import uuid

from capture_alignment_gui_linux import NativeCapture
from capture_engine_options_acceptance import check_attachment
from verify_entry_acceptance import REPO, environment, manifest, spawn, terminate, validate


def fixture_state(path):
    with closing(sqlite3.connect(path.as_uri() + "?mode=ro", uri=True)) as db:
        schema = db.execute("SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY type,name").fetchall()
        shared = db.execute("SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='shared'").fetchone()[0]
        rows = db.execute("SELECT id,label FROM shared ORDER BY id").fetchall() if shared else None
        child = db.execute("SELECT count(*) FROM sqlite_schema WHERE name='drop_child'").fetchone()[0]
        child_rows = db.execute("SELECT id,parent FROM drop_child ORDER BY id").fetchall() if child else None
    return dict(schema=schema, rows=rows, child_rows=child_rows)


def require_removed(state):
    if state["rows"] is not None or any(row[1] in ("shared", "drop_index", "drop_trigger") for row in state["schema"]):
        raise RuntimeError("Formal deletion did not remove the original table and its own definitions")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--preview-only", action="store_true")
    options = parser.parse_args()
    root, state = validate(Path(os.environ["NYADB_ENTRY_ROOT"]))
    env = environment(root, state)
    if "app" in state["processes"]:
        raise RuntimeError("SQLite deletion requires its own formal process")
    config = root / "config/NyaDB/config.sqlite"
    if not config.exists():
        subprocess.run(["go", "run", "./tools/entryfixture", "seed-engine-options"], cwd=REPO, env=env, check=True)
    profile = check_attachment(config, root)
    attached = root / "engine-attached.sqlite"
    with closing(sqlite3.connect(attached)) as db:
        if db.execute("SELECT id,label FROM shared").fetchall() != [(1, "attached fixture")]:
            raise RuntimeError("Replay requires the unchanged owned source table")
        db.executescript("""
CREATE INDEX IF NOT EXISTS drop_index ON shared(label);
CREATE TRIGGER IF NOT EXISTS drop_trigger AFTER DELETE ON shared BEGIN SELECT 'own trigger'; END;
CREATE TABLE IF NOT EXISTS drop_child(id INTEGER PRIMARY KEY,parent REFERENCES shared(id) ON DELETE CASCADE);
INSERT OR IGNORE INTO drop_child VALUES(1,1);
""")
    before = fixture_state(attached)
    main_before = fixture_state(root / "engine-main.sqlite")
    evidence = root / "evidence/sqlite-drop-native" / ("capture-" + uuid.uuid4().hex[:12])
    binary = REPO / "build/linux-amd64/nyadb"
    expected_hash = hashlib.sha256(binary.read_bytes()).hexdigest()
    cap = NativeCapture(env["DISPLAY"], evidence, binary)
    app = spawn(root, state, "app", [str(binary)], env)
    manifest(root, state)
    window = None

    def click(x, y, button=1):
        cap.actions.append(dict(action="click", client_x=x, client_y=y, button=button))
        cap.click(window, x, y, button)
        time.sleep(.35)

    def key(*keys):
        cap.actions.append(dict(action="key", keys=list(keys)))
        cap.key(*keys)
        time.sleep(.35)

    def text(value):
        cap.actions.append(dict(action="text", text=value))
        cap.run("xdotool", "type", "--clearmodifiers", "--", value)
        time.sleep(.3)

    def capture(name, **observed):
        cap.capture(name, window, "sqlite-drop", dict(binary_sha256=expected_hash,
            profile_id=profile, namespace="aux", table="shared", original_comparison=False,
            real_sqlite=True, loading=False, **observed))

    try:
        window = cap.wait_window("^NyaDB$", pid=app.pid)
        actual_hash = hashlib.sha256(Path(f"/proc/{app.pid}/exe").read_bytes()).hexdigest()
        if actual_hash != expected_hash:
            raise RuntimeError("Running formal binary identity changed")
        cap.move_client(window, 30, 50)
        cap.resize(window, 1280, 800)
        time.sleep(1)
        click(120, 150)
        key("Return")
        time.sleep(1)
        click(105, 192)
        key("Return")
        time.sleep(1)
        capture("objects-1280")
        click(350, 213, 3)
        capture("menu-1280")
        key("Escape")
        # Selecting the grid row revealed this exact original navigator node.
        # Delete then exercises the same keyboard route as the Shell test.
        click(115, 254)
        key("Delete")
        capture("unread-preview-1280")
        click(718, 540)
        time.sleep(.8)
        capture("preview-1280", execution_confirmed=False)
        if fixture_state(attached) != before:
            raise RuntimeError("Deletion preview changed the source")
        if options.preview_only:
            return
        cap.resize(window, 840, 560)
        capture("preview-840", execution_confirmed=False)
        cap.resize(window, 1280, 800)
        click(500, 504)
        text("shared")
        click(870, 540)
        time.sleep(1)
        capture("foreign-key-refused-1280", execution_confirmed=True)
        if fixture_state(attached) != before:
            raise RuntimeError("Rejected foreign key deletion changed schema or cascaded rows")
        # Remove only the fixture dependency; the source is still unchanged.
        with closing(sqlite3.connect(attached)) as db:
            db.execute("DROP TABLE drop_child")
            db.commit()
        click(718, 540)
        time.sleep(.8)
        click(500, 504)
        key("ctrl+a")
        text("shared")
        capture("refreshed-preview-1280", execution_confirmed=False)
        click(870, 540)
        deadline = time.monotonic() + 8
        while fixture_state(attached)["rows"] is not None and time.monotonic() < deadline:
            time.sleep(.1)
        after = fixture_state(attached)
        require_removed(after)
        if fixture_state(root / "engine-main.sqlite") != main_before:
            raise RuntimeError("Deletion affected the same-named main table")
        capture("deleted-1280", execution_confirmed=True)
        cap.resize(window, 840, 560)
        capture("deleted-840", execution_confirmed=True)
        (evidence / "result.json").write_text(json.dumps(dict(run_id=state["id"],
            binary_sha256=actual_hash, profile_id=profile, namespace="aux",
            preview_unchanged=True, foreign_key_failure_unchanged=True,
            original_table_and_definitions_removed=True, main_unchanged=True,
            original_comparison=False), indent=2) + "\n")
    except BaseException:
        if window is not None:
            capture("FAILED-last-state")
        raise
    finally:
        cap.close()
        if "app" in state["processes"]:
            terminate(state["processes"].pop("app"))
        manifest(root, state)


if __name__ == "__main__":
    main()
