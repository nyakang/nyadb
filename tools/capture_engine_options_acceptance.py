#!/usr/bin/env python3
"""Capture PG/SQLite configuration in an owned Linux acceptance run.

Run through verify_entry_acceptance.py with an empty --start-only run.
PostgreSQL remains offline; SQLite uses only files seeded under the run root.
"""
import hashlib
from contextlib import closing
import json
import os
from pathlib import Path
import re
import sqlite3
import subprocess
import time

from capture_alignment_gui_linux import NativeCapture
from verify_entry_acceptance import REPO, environment, manifest, spawn, terminate, validate


def read_options(config, name):
    with closing(sqlite3.connect(config.as_uri() + "?mode=ro", uri=True)) as db:
        row = db.execute("SELECT id,kind,mysql_advanced_json FROM connection_profiles WHERE name=?", (name,)).fetchone()
    if row is None:
        raise RuntimeError("Owned configuration profile missing")
    return row[0], row[1], json.loads(row[2])


def check_attachment(config, root, name="aux"):
    profile_id, kind, envelope = read_options(config, "验收 SQLite 附加库")
    entries = envelope.get("sqlite", {}).get("attachments", [])
    if (kind != "sqlite" or envelope.get("kind") != kind or envelope.get("version") != 1
            or len(entries) != 1 or entries[0].get("name") != name
            or Path(entries[0].get("file", "")) != root / "engine-attached.sqlite"):
        raise RuntimeError("SQLite attachment configuration lost its original identity")
    return profile_id


def main():
    root, state = validate(Path(os.environ["NYADB_ENTRY_ROOT"]))
    env = environment(root, state)
    if "app" in state["processes"]:
        raise RuntimeError("Replay requires its own formal process")
    config = root / "config/NyaDB/config.sqlite"
    if not config.exists():
        subprocess.run(["go", "run", "./tools/entryfixture", "seed-engine-options"], cwd=REPO, env=env, check=True)
    sqlite_id = check_attachment(config, root)
    binary = REPO / "build/linux-amd64/nyadb"
    expected_hash = hashlib.sha256(binary.read_bytes()).hexdigest()
    cap = NativeCapture(env["DISPLAY"], root / "evidence/engine-options-native", binary)
    window, pid = None, None

    def start():
        nonlocal window, pid
        spawn(root, state, "app", [str(binary)], env)
        manifest(root, state)
        pid = state["processes"]["app"]["pid"]
        window = cap.wait_window("^NyaDB$", pid=pid)
        if hashlib.sha256(Path(f"/proc/{pid}/exe").read_bytes()).hexdigest() != expected_hash:
            raise RuntimeError("Running application hash changed")
        cap.move_client(window, 30, 50)
        cap.resize(window, 1280, 800)
        time.sleep(1)

    def stop():
        terminate(state["processes"].pop("app"))
        manifest(root, state)

    def click(x, y, target=None, button=1):
        cap.actions.append(dict(action="click", client_x=x, client_y=y, button=button))
        cap.click(target or window, x, y, button=button)
        time.sleep(.3)

    def key(*keys):
        cap.actions.append(dict(action="key", keys=list(keys)))
        cap.key(*keys)
        time.sleep(.3)

    def capture(name, target=None, **observed):
        cap.capture(name, target or window, "engine-options", dict(loading=False, binary_sha256=expected_hash, **observed))

    def edit(kind, row_y):
        click(130, row_y, button=3)
        key("Down", "Down", "Return")
        child = cap.wait_window("^" + re.escape("编辑连接 (" + kind + ")") + "$", pid=pid)
        cap.move_client(child, 300, 120)
        return child

    try:
        start()
        capture("profiles-1280", postgres_connected=False)
        child = edit("SQLite", 150)
        click(76, 14, child)  # Advanced tab
        capture("sqlite-advanced-900", child)
        click(140, 14, child)  # Attached databases tab
        click(220, 164, child)  # select the saved attachment row
        capture("sqlite-attached-900", child, sqlite_profile_id=sqlite_id)
        cap.resize(child, 720, 520)
        capture("sqlite-attached-720", child, sqlite_profile_id=sqlite_id)
        key("Escape")
        child = edit("PostgreSQL", 128)
        click(76, 14, child)
        capture("postgres-advanced-900", child, postgres_connected=False)
        click(130, 14, child)
        capture("postgres-tls-900", child, postgres_connected=False)
        cap.resize(child, 720, 520)
        capture("postgres-tls-720", child, postgres_connected=False)
        key("Escape")
        cap.resize(window, 840, 560)
        capture("profiles-840", postgres_connected=False)
        stop()
        start()
        if check_attachment(config, root) != sqlite_id:
            raise RuntimeError("Restart changed SQLite profile identity")
        child = edit("SQLite", 150)
        click(140, 14, child)
        capture("sqlite-attached-restart", child, profile_identity_verified=True)
        key("Escape")
        # Establish only the owned SQLite connection. PostgreSQL remains offline.
        click(120, 150)
        key("Return")
        time.sleep(1)
        capture("sqlite-attached-namespaces", sqlite_profile_id=sqlite_id, real_sqlite=True)
        click(105, 192)  # explicit open of the aux namespace
        key("Return")
        time.sleep(.8)
        capture("sqlite-aux-objects", sqlite_profile_id=sqlite_id, namespace="aux")
        click(350, 190)  # the actual shared object row, rather than the tree category
        key("Return")
        time.sleep(.8)
        capture("sqlite-aux-data-1280", sqlite_profile_id=sqlite_id, namespace="aux", table="shared")
        cap.resize(window, 840, 560)
        capture("sqlite-aux-data-840", sqlite_profile_id=sqlite_id, namespace="aux", table="shared")
        for file, expected in (("engine-main.sqlite", "main fixture"), ("engine-attached.sqlite", "attached fixture")):
            with closing(sqlite3.connect((root/file).as_uri()+"?mode=ro", uri=True)) as db:
                if db.execute("SELECT label FROM shared WHERE id=1").fetchone()[0] != expected:
                    raise RuntimeError("Configuration browsing changed fixture data")
        (root / "evidence/engine-options-result.json").write_text(json.dumps(dict(
            run_id=state["id"], binary_sha256=expected_hash, sqlite_profile_id=sqlite_id,
            configuration_restart_verified=True, postgres_connected=False,
            fixture_data_retained=True, sqlite_namespace="aux", sqlite_table="shared"), indent=2) + "\n")
    finally:
        cap.close()
        if "app" in state["processes"]:
            stop()


if __name__ == "__main__":
    main()
