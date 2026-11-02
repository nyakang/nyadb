#!/usr/bin/env python3
"""Capture SQLite view CRUD in a fresh owned formal Linux process.

Invoke through verify_entry_acceptance.py. All writes target its disposable
SQLite fixture. Menu exploration does not count as full acceptance.
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


def identifier(name):
    return '"' + name.replace('"', '""') + '"'


def view_state(path, name):
    with closing(sqlite3.connect(path.as_uri() + "?mode=ro", uri=True)) as db:
        rows = db.execute("SELECT type,name,tbl_name,sql FROM sqlite_schema WHERE tbl_name=? ORDER BY type,name", (name,)).fetchall()
        if not any(row[0] == "view" for row in rows):
            return None
        columns = [row[1] for row in db.execute(f"PRAGMA table_info({identifier(name)})").fetchall()]
        values = db.execute(f"SELECT id,label FROM {identifier(name)} ORDER BY id").fetchall()
    return dict(schema=rows, columns=columns, rows=values)


def require_view(state, name, label, trigger=False):
    if state is None or state["columns"] != ["id", "label"] or state["rows"] != [(1, label)]:
        raise RuntimeError("Formal view query, columns or result differ")
    definitions = state["schema"]
    if sorted(row[0] for row in definitions) != (["trigger", "view"] if trigger else ["view"]):
        raise RuntimeError("Formal view definitions are incomplete")
    if trigger and not any(row[1] == "view_update" and row[2] == name and "UPDATE shared SET label=new.label" in row[3] for row in definitions):
        raise RuntimeError("Formal view trigger target or original body differ")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--explore-menu", action="store_true")
    parser.add_argument("--explore-submenu", action="store_true")
    parser.add_argument("--explore-view-menu", action="store_true")
    parser.add_argument("--preview-only", action="store_true")
    opts = parser.parse_args()
    root, state = validate(Path(os.environ["NYADB_ENTRY_ROOT"]))
    env = environment(root, state)
    if "app" in state["processes"]:
        raise RuntimeError("View acceptance requires its own formal process")
    config = root / "config/NyaDB/config.sqlite"
    if not config.exists():
        subprocess.run(["go", "run", "./tools/entryfixture", "seed-engine-options"], cwd=REPO, env=env, check=True)
    profile = check_attachment(config, root)
    attached = root / "engine-attached.sqlite"
    before, main_before = fixture_state(attached), fixture_state(root / "engine-main.sqlite")
    if before["rows"] != [(1, "attached fixture")]:
        raise RuntimeError("View acceptance requires a fresh owned source")
    evidence = root / "evidence/sqlite-view-native" / ("capture-" + uuid.uuid4().hex[:12])
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
        cap.capture(name, window, "sqlite-view", dict(binary_sha256=expected_hash,
            profile_id=profile, namespace="aux", real_sqlite=True, loading=False,
            original_comparison=False, **observed))

    def source_unchanged():
        now = fixture_state(attached)
        # New view entries are expected. All original definitions and table
        # rows must remain; none of the view programs is executed as DML.
        original_names = {row[1] for row in before["schema"]}
        kept = [row for row in now["schema"] if row[1] in original_names]
        if kept != before["schema"] or now["rows"] != before["rows"] or fixture_state(root / "engine-main.sqlite") != main_before:
            raise RuntimeError("View operation changed the source or main namespace")

    def menu(action):
        click(350, 190, 3)
        capture(action + "-menu-1280")
        click(465, dict(alter=272, rename=294, drop=316)[action])
        time.sleep(.6)

    def preview(name):
        click(855, 681)
        capture(name + "-preview-1280", execution_confirmed=False)
        cap.resize(window, 840, 560)
        capture(name + "-preview-840", execution_confirmed=False)
        cap.resize(window, 1280, 800)

    def confirm(name):
        click(963, 681)
        time.sleep(1)
        capture(name + "-result-1280", execution_requested=True)

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
        click(1025, 138)
        capture("new-menu-1280")
        if opts.explore_menu:
            return
        click(1100, 233)
        capture("new-submenu-1280")
        if opts.explore_submenu:
            return
        click(1235, 233)
        time.sleep(.7)
        capture("create-unread-1280")
        click(500, 170)
        key("ctrl+a")
        text("acceptance_view")
        click(490, 300)
        key("ctrl+a")
        text('CREATE VIEW "aux"."acceptance_view"(id,label) AS SELECT id,label FROM shared')
        click(710, 681)
        time.sleep(.7)
        preview("create")
        if view_state(attached, "acceptance_view") is not None:
            raise RuntimeError("View preview executed its SQL")
        source_unchanged()
        if opts.preview_only:
            return
        confirm("create")
        require_view(view_state(attached, "acceptance_view"), "acceptance_view", "attached fixture")
        with closing(sqlite3.connect(attached)) as db:
            db.execute("CREATE TRIGGER view_update INSTEAD OF UPDATE OF label ON acceptance_view BEGIN UPDATE shared SET label=new.label WHERE id=new.id; END")
            db.commit()
        if opts.explore_view_menu:
            click(350, 190, 3)
            capture("view-menu-1280")
            return
        menu("alter")
        click(490, 300)
        key("ctrl+a")
        text('CREATE VIEW "aux"."acceptance_view"(id,label) AS SELECT id,upper(label) FROM shared')
        preview("alter")
        require_view(view_state(attached, "acceptance_view"), "acceptance_view", "attached fixture", True)
        confirm("alter")
        require_view(view_state(attached, "acceptance_view"), "acceptance_view", "ATTACHED FIXTURE", True)
        with closing(sqlite3.connect(attached)) as db:
            db.execute("CREATE VIEW dependent AS SELECT id,label FROM acceptance_view")
            db.commit()
        original = view_state(attached, "acceptance_view")
        menu("rename")
        click(500, 170)
        key("ctrl+a")
        text("renamed_view")
        preview("rename")
        confirm("rename-refused")
        if view_state(attached, "acceptance_view") != original or view_state(attached, "renamed_view") is not None:
            raise RuntimeError("Rejected rename changed the original view")
        with closing(sqlite3.connect(attached)) as db:
            if db.execute("SELECT id,label FROM dependent").fetchall() != [(1, "ATTACHED FIXTURE")]:
                raise RuntimeError("Rejected rename broke its dependency")
            db.execute("DROP VIEW dependent")
            db.commit()
        click(710, 681)
        time.sleep(.7)
        preview("rename-refreshed")
        confirm("rename")
        require_view(view_state(attached, "renamed_view"), "renamed_view", "ATTACHED FIXTURE", True)
        if view_state(attached, "acceptance_view") is not None:
            raise RuntimeError("Original renamed view remains")
        menu("drop")
        click(500, 646)
        text("renamed_view")
        preview("drop")
        confirm("drop")
        if view_state(attached, "renamed_view") is not None:
            raise RuntimeError("Confirmed view deletion did not remove it")
        with closing(sqlite3.connect(attached.as_uri() + "?mode=ro", uri=True)) as db:
            if db.execute("SELECT count(*) FROM sqlite_schema WHERE name='view_update'").fetchone()[0]:
                raise RuntimeError("Dropped view trigger remains")
        source_unchanged()
        if fixture_state(attached) != before:
            raise RuntimeError("View CRUD left unexpected fixture definitions")
        cap.resize(window, 840, 560)
        capture("drop-result-840", execution_confirmed=True)
        (evidence / "result.json").write_text(json.dumps(dict(run_id=state["id"],
            binary_sha256=actual_hash, profile_id=profile, namespace="aux",
            create=True, alter=True, rename_dependency_refused=True, rename=True,
            drop=True, trigger_preserved=True, previews_unchanged=True,
            source_and_main_unchanged=True, original_comparison=False), indent=2) + "\n")
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
