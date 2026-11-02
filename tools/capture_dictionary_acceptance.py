#!/usr/bin/env python3
"""Verify the formal dictionary window using an owned SQLite acceptance run.

Invoke through verify_entry_acceptance.py. PostgreSQL and MySQL remain offline
in the application. Only this run's SQLite fixture and evidence files are used.
"""
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


def fixture_digest(path):
    with closing(sqlite3.connect(path.as_uri() + "?mode=ro", uri=True)) as db:
        schema = db.execute("SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY type,name").fetchall()
        rows = db.execute("SELECT id,label FROM shared ORDER BY id").fetchall()
    return hashlib.sha256(json.dumps([schema, rows], ensure_ascii=False).encode()).hexdigest()


def verify_artifact(path, format):
    text = path.read_text()
    expected = ("main / aux", "aux_only", "dictionary_label", "dictionary_view")
    if format == "markdown":
        expected = tuple(value.replace("_", "&#95;") for value in expected)
    if any(value not in text for value in expected):
        raise RuntimeError("Native dictionary artifact lost original namespace or metadata")
    if "attached fixture" in text or "main fixture" in text:
        raise RuntimeError("Dictionary exported fixture rows")
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    root, state = validate(Path(os.environ["NYADB_ENTRY_ROOT"]))
    env = environment(root, state)
    if "app" in state["processes"]:
        raise RuntimeError("Dictionary acceptance requires its own formal process")
    config = root / "config/NyaDB/config.sqlite"
    if not config.exists():
        subprocess.run(["go", "run", "./tools/entryfixture", "seed-engine-options"], cwd=REPO, env=env, check=True)
    profile = check_attachment(config, root)
    attached = root / "engine-attached.sqlite"
    with closing(sqlite3.connect(attached)) as db:
        db.execute("CREATE INDEX IF NOT EXISTS dictionary_label ON shared(label DESC)")
        db.execute("CREATE VIEW IF NOT EXISTS dictionary_view AS SELECT label AS aux_only FROM shared")
        db.commit()
    before = {name: fixture_digest(root / name) for name in ("engine-main.sqlite", "engine-attached.sqlite")}
    evidence = root / "evidence/dictionary-native" / ("capture-" + uuid.uuid4().hex[:12])
    binary = REPO / "build/linux-amd64/nyadb"
    expected_hash = hashlib.sha256(binary.read_bytes()).hexdigest()
    cap = NativeCapture(env["DISPLAY"], evidence, binary)
    app = spawn(root, state, "app", [str(binary)], env)
    window = None

    def click(x, y, target=None):
        cap.actions.append(dict(action="click", client_x=x, client_y=y))
        cap.click(target or window, x, y)
        time.sleep(.3)

    def key(*keys):
        cap.actions.append(dict(action="key", keys=list(keys)))
        cap.key(*keys)
        time.sleep(.3)

    def capture(name):
        cap.capture(name, window, "dictionary", dict(binary_sha256=expected_hash,
            profile_id=profile, namespace="aux", real_sqlite=True, loading=False,
            original_comparison=False, scale="unknown"))

    def export(format):
        path = evidence / ("dictionary.md" if format == "markdown" else "dictionary.html")
        click(280, 666)
        cap.wait_window("^导出数据字典$", pid=app.pid, timeout=8)
        key("ctrl+a")
        cap.run("xdotool", "type", "--clearmodifiers", "--", str(path))
        key("Return")
        deadline = time.monotonic() + 8
        while not path.is_file() and time.monotonic() < deadline:
            time.sleep(.1)
        if not path.is_file():
            raise RuntimeError("Native dictionary export did not create the chosen artifact")
        return verify_artifact(path, format)

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
        click(195, 12)
        capture("tools-menu-1280")
        key("Home", "Down", "Return")
        capture("dictionary-empty-1280")
        click(1095, 148)
        time.sleep(1)
        capture("dictionary-fields-1280")
        markdown_hash = export("markdown")
        capture("dictionary-exported-1280")
        click(150, 666)
        key("Home", "Down", "Return")
        html_hash = export("html")
        capture("dictionary-html-exported-1280")
        cap.resize(window, 840, 560)
        capture("dictionary-fields-840")
        after = {name: fixture_digest(root / name) for name in before}
        if before != after:
            raise RuntimeError("Dictionary acceptance changed fixture schema or rows")
        (evidence / "result.json").write_text(json.dumps(dict(run_id=state["id"],
            binary_sha256=actual_hash, profile_id=profile, namespace="aux",
            markdown_sha256=markdown_hash, html_sha256=html_hash,
            schema_and_rows_unchanged=True, postgres_connected=False,
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
