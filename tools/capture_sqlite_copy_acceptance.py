#!/usr/bin/env python3
"""Capture SQLite structure/data copies in an owned formal Linux process.

Run through verify_entry_acceptance.py. Only the disposable attached SQLite
fixture is modified. Exploration is recorded separately from full acceptance.
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
from capture_sqlite_drop_acceptance import fixture_state
from verify_entry_acceptance import REPO, birth, environment, manifest, spawn, terminate, validate


def identifier(value):
    return '"' + value.replace('"', '""') + '"'


def table_state(path, table):
    with closing(sqlite3.connect(path.as_uri() + "?mode=ro", uri=True)) as db:
        schema = db.execute("SELECT type,name,tbl_name,sql FROM sqlite_schema WHERE tbl_name=? ORDER BY type,name", (table,)).fetchall()
        if not any(row[0] == "table" for row in schema):
            return None
        name = identifier(table)
        columns = db.execute(f"PRAGMA table_xinfo({name})").fetchall()
        indexes = []
        for row in db.execute(f"PRAGMA index_list({name})").fetchall():
            indexes.append((row[2:], db.execute(f"PRAGMA index_xinfo({identifier(row[1])})").fetchall()))
        rows = db.execute(f"SELECT id,parent,label,typeof(raw),hex(raw),typeof(n),n,typeof(amount),amount,generated FROM {name} ORDER BY id").fetchall()
        foreign_keys = db.execute(f"PRAGMA foreign_key_list({name})").fetchall()
        sequence = db.execute("SELECT typeof(seq),seq FROM sqlite_sequence WHERE name=?", (table,)).fetchall()
    return dict(schema=schema, columns=columns, indexes=indexes, rows=rows, foreign_keys=foreign_keys, sequence=sequence)


def require_copy(source, copied, destination, with_data):
    if copied is None or copied["columns"] != source["columns"] or copied["indexes"] != source["indexes"]:
        raise RuntimeError("Copied fields, generated fields or index semantics differ")
    expected_fk = [list(row) for row in source["foreign_keys"]]
    for row in expected_fk:
        if row[2].lower() == "shared":
            row[2] = destination
    if [list(row) for row in copied["foreign_keys"]] != expected_fk:
        raise RuntimeError("Copied self-reference differs")
    expected_rows, expected_sequence = (source["rows"], source["sequence"]) if with_data else ([], [])
    if copied["rows"] != expected_rows or copied["sequence"] != expected_sequence:
        raise RuntimeError("Copied typed rows, NULL/binary/numeric values or sequence differ")
    defs = copied["schema"]
    if sorted(row[0] for row in defs) != ["index", "table", "trigger"]:
        raise RuntimeError("Copied schema definitions are incomplete")
    if not any(row[1] == destination + "_copy_index" and row[2] == destination for row in defs):
        raise RuntimeError("Copied index identity differs")
    if not any(row[1] == destination + "_copy_trigger" and "UPDATE shared SET label=new.label" in row[3] for row in defs):
        raise RuntimeError("Copied trigger identity or original body differs")
    if not any(row[0] == "table" and "STRICT" in row[3] and "CHECK" in row[3] for row in defs):
        raise RuntimeError("Copied table options or CHECK constraint differ")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--explore-menu", action="store_true")
    parser.add_argument("--preview-only", action="store_true")
    options = parser.parse_args()
    root, state = validate(Path(os.environ["NYADB_ENTRY_ROOT"]))
    env = environment(root, state)
    if "app" in state["processes"]:
        raise RuntimeError("SQLite copy requires its own formal process")
    config = root / "config/NyaDB/config.sqlite"
    if not config.exists():
        subprocess.run(["go", "run", "./tools/entryfixture", "seed-engine-options"], cwd=REPO, env=env, check=True)
    profile = check_attachment(config, root)
    attached = root / "engine-attached.sqlite"
    with closing(sqlite3.connect(attached)) as db:
        if db.execute("SELECT id,label FROM shared").fetchall() != [(1, "attached fixture")]:
            raise RuntimeError("Copy acceptance requires a fresh owned source")
        db.executescript("""
DROP TABLE shared;
CREATE TABLE shared(id INTEGER PRIMARY KEY AUTOINCREMENT,parent INTEGER REFERENCES shared(id),label TEXT,raw BLOB,n INTEGER,amount TEXT,generated TEXT GENERATED ALWAYS AS (label || ':' || n) STORED,CHECK(n>=0)) STRICT;
CREATE UNIQUE INDEX copy_index ON shared(label COLLATE NOCASE DESC) WHERE label IS NOT NULL;
CREATE TRIGGER copy_trigger AFTER UPDATE OF label ON shared BEGIN UPDATE shared SET label=new.label WHERE id=new.id; END;
INSERT INTO shared(id,parent,label,raw,n,amount) VALUES(1,1,'attached fixture',x'00ff80',9223372036854775806,'12345678901234567890.123456789'),(2,1,NULL,NULL,0,NULL),(100,1,'deleted high water',x'01',1,'0');
DELETE FROM shared WHERE id=100;
""")
    before = table_state(attached, "shared")
    main_before = fixture_state(root / "engine-main.sqlite")
    evidence = root / "evidence/sqlite-copy-native" / ("capture-" + uuid.uuid4().hex[:12])
    binary = REPO / "build/linux-amd64/nyadb"
    expected_hash = hashlib.sha256(binary.read_bytes()).hexdigest()
    cap = NativeCapture(env["DISPLAY"], evidence, binary)
    app = spawn(root, state, "app", [str(binary)], env)
    manifest(root, state)
    app_identity = dict(state["processes"]["app"], run_id=state["id"], binary_sha256=expected_hash)
    (evidence / "app-identity.json").write_text(json.dumps(app_identity, indent=2) + "\n")
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
        cap.capture(name, window, "sqlite-copy", dict(binary_sha256=expected_hash,
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
        accepted = []
        for destination, with_data in [("copy_empty", False), ("copy_data", True)]:
            # Before the first copy the source is the only grid row. After
            # refresh the copied table sorts before it, so use the second row.
            click(350, 213 if accepted else 190, 3)
            capture(destination + "-menu-1280")
            click(450, 423 if accepted else 400)
            capture(destination + "-submenu-1280")
            if options.explore_menu:
                return
            click(650, (423 if accepted else 400) + (22 if not with_data else 0))
            capture(destination + "-unread-preview-1280", execution_confirmed=False)
            click(765, 628)
            time.sleep(.8)
            capture(destination + "-preview-1280", execution_confirmed=False)
            if table_state(attached, "shared") != before or table_state(attached, destination) is not None:
                raise RuntimeError("Copy preview changed schema or source data")
            if options.preview_only:
                return
            click(490, 170)
            key("ctrl+a")
            text(destination)
            cap.resize(window, 840, 560)
            capture(destination + "-preview-840", execution_confirmed=False)
            cap.resize(window, 1280, 800)
            click(916, 628)
            deadline = time.monotonic() + 10
            while table_state(attached, destination) is None and time.monotonic() < deadline:
                time.sleep(.1)
            copied = table_state(attached, destination)
            require_copy(before, copied, destination, with_data)
            if table_state(attached, "shared") != before or fixture_state(root / "engine-main.sqlite") != main_before:
                raise RuntimeError("Copy changed the source or same-named main table")
            capture(destination + "-result-1280", execution_confirmed=True)
            cap.resize(window, 840, 560)
            capture(destination + "-result-840", execution_confirmed=True)
            cap.resize(window, 1280, 800)
            accepted.append(dict(destination=destination, with_data=with_data, rows=len(copied["rows"])))
        (evidence / "result.json").write_text(json.dumps(dict(run_id=state["id"],
            binary_sha256=actual_hash, profile_id=profile, namespace="aux", copies=accepted,
            preview_unchanged=True, source_unchanged=True, main_unchanged=True,
            original_comparison=False), indent=2) + "\n")
    except BaseException:
        if window is not None:
            capture("FAILED-last-state")
        raise
    finally:
        cap.close()
        if "app" in state["processes"]:
            terminate(state["processes"].pop("app"))
        app_identity["exit"] = app.wait(timeout=10)
        app_identity["stopped"] = birth(app.pid) != app_identity["birth"]
        (evidence / "app-identity.json").write_text(json.dumps(app_identity, indent=2) + "\n")
        manifest(root, state)


if __name__ == "__main__":
    main()
