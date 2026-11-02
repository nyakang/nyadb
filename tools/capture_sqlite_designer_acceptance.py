#!/usr/bin/env python3
"""Exercise SQLite design in the formal program using only an owned fixture.

Run with verify_entry_acceptance.py --run <owned-run> -- <this command>.
PostgreSQL remains offline. All writes are confined to the run's attached file.
"""
import argparse
import hashlib
from contextlib import closing
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
        # table_info: cid, name, type, notnull, dflt_value, pk
        names = [row[1] for row in db.execute("PRAGMA table_info(shared)")]
        row = db.execute("SELECT id,label FROM shared WHERE id=1").fetchone()
    return names, row


def check_added(path):
    with closing(sqlite3.connect(path.as_uri() + "?mode=ro", uri=True)) as db:
        names = [row[1] for row in db.execute("PRAGMA table_info(shared)")]
        if "native_added" not in names:
            return False
        row = db.execute("SELECT id,label,native_added FROM shared WHERE id=1").fetchone()
    if row != (1, "attached fixture", None):
        raise RuntimeError("Native schema apply changed retained data")
    return True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--draft-only", action="store_true", help="capture the field form without confirming any SQL")
    options = parser.parse_args()
    root, state = validate(Path(os.environ["NYADB_ENTRY_ROOT"]))
    env = environment(root, state)
    if "app" in state["processes"]:
        raise RuntimeError("SQLite designer requires its own formal process")
    config = root / "config/NyaDB/config.sqlite"
    if not config.exists():
        subprocess.run(["go", "run", "./tools/entryfixture", "seed-engine-options"], cwd=REPO, env=env, check=True)
    profile = check_attachment(config, root)
    for filename, expected in (("engine-main.sqlite", "main fixture"), ("engine-attached.sqlite", "attached fixture")):
        if fixture_state(root / filename) != (["id", "label"], (1, expected)):
            raise RuntimeError("Replay requires an unchanged owned SQLite fixture")
    binary = REPO / "build/linux-amd64/nyadb"
    expected_hash = hashlib.sha256(binary.read_bytes()).hexdigest()
    evidence = root / "evidence/sqlite-designer-native" / ("capture-" + uuid.uuid4().hex[:12])
    cap = NativeCapture(env["DISPLAY"], evidence, binary)
    app = spawn(root, state, "app", [str(binary)], env)
    manifest(root, state)
    window = None

    def click(x, y):
        cap.actions.append(dict(action="click", client_x=x, client_y=y))
        cap.click(window, x, y)
        time.sleep(.3)

    def key(*keys):
        cap.actions.append(dict(action="key", keys=list(keys)))
        cap.key(*keys)
        time.sleep(.3)

    def capture(name, **observed):
        cap.capture(name, window, "sqlite-designer", dict(binary_sha256=expected_hash,
            profile_id=profile, namespace="aux", table="shared", original_comparison=False,
            loading=False,
            **observed))

    try:
        window = cap.wait_window("^NyaDB$", pid=app.pid)
        actual_hash = hashlib.sha256(Path(f"/proc/{app.pid}/exe").read_bytes()).hexdigest()
        if actual_hash != expected_hash:
            raise RuntimeError("Running formal binary identity changed")
        cap.source_version = "sha256:" + actual_hash
        cap.move_client(window, 30, 50)
        cap.resize(window, 1280, 800)
        time.sleep(1)
        click(120, 150)
        key("Return")
        time.sleep(.8)
        click(105, 192)
        key("Return")
        time.sleep(.8)
        click(350, 190)
        click(355, 137)
        time.sleep(.8)
        capture("structure-readonly-1280", execution_confirmed=False)
        click(630, 137)
        time.sleep(.8)
        capture("designer-fields-1280", execution_confirmed=False)
        click(292, 211)
        capture("designer-added-draft-1280", execution_confirmed=False)
        # Name input below the bounded definition list.
        click(420, 441)
        key("ctrl+a")
        cap.actions.append(dict(action="text", text="native_added"))
        cap.run("xdotool", "type", "--clearmodifiers", "--", "native_added")
        time.sleep(.3)
        capture("designer-name-draft-1280", execution_confirmed=False)
        if options.draft_only:
            return
        # Ctrl+S uses the same workbench save/preview scope as the File menu.
        key("ctrl+s")
        capture("designer-preview-1280", execution_confirmed=False)
        if check_added(root / "engine-attached.sqlite"):
            raise RuntimeError("Preview executed before confirmation")
        # Confirmation button at the lower right of the preview modal.
        click(954, 563)
        deadline = time.monotonic() + 8
        while not check_added(root / "engine-attached.sqlite"):
            if time.monotonic() > deadline:
                capture("FAILED-confirmation", execution_confirmed=False)
                raise RuntimeError("Formal native apply did not create the intended column")
            time.sleep(.1)
        time.sleep(.7)
        capture("designer-applied-1280", execution_confirmed=True, fixture_data_verified=True)
        click(350, 320)  # Select the verified new field without changing its form.
        cap.resize(window, 840, 560)
        capture("designer-applied-840", execution_confirmed=True, fixture_data_verified=True)
        cap.resize(window, 1280, 800)
        for page, name in enumerate(("constraints", "indexes", "triggers", "options", "ddl"), 1):
            click(600, 178)
            key("Home", *("Down" for _ in range(page)), "Return")
            capture("designer-" + name + "-1280", execution_confirmed=True, fixture_data_verified=True)
        # A new index stays a draft. Closing the tab must present the leave
        # guard; no SQL confirmation is issued for this definition.
        click(600, 178)
        key("Home", "Down", "Down", "Return")
        click(292, 211)
        cap.resize(window, 840, 560)
        capture("designer-index-draft-840", execution_confirmed=True, pending_definition_confirmed=False)
        key("ctrl+w")
        capture("designer-dirty-guard-840", execution_confirmed=True, pending_definition_confirmed=False)
        with closing(sqlite3.connect((root / "engine-attached.sqlite").as_uri() + "?mode=ro", uri=True)) as db:
            if db.execute("SELECT name FROM sqlite_schema WHERE type IN ('index','trigger') AND tbl_name='shared'").fetchall():
                raise RuntimeError("Native definition draft was executed without confirmation")
        if not check_added(root / "engine-attached.sqlite"):
            raise RuntimeError("Native draft changed the verified field")
        names, row = fixture_state(root / "engine-main.sqlite")
        if names != ["id", "label"] or row != (1, "main fixture"):
            raise RuntimeError("Attached native design changed main namespace")
        (root / "evidence/sqlite-designer-result.json").write_text(json.dumps(dict(
            run_id=state["id"], binary_sha256=actual_hash, profile_id=profile,
            namespace="aux", fixture_apply_verified=True, retained_data_verified=True,
            main_namespace_unchanged=True, postgres_connected=False,
            pending_definition_confirmed=False,
            capture_directory=str(evidence.relative_to(root))), indent=2) + "\n")
    finally:
        cap.close()
        if "app" in state["processes"]:
            terminate(state["processes"].pop("app"))
        manifest(root, state)


if __name__ == "__main__":
    main()
