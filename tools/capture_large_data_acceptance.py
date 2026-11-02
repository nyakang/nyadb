#!/usr/bin/env python3
"""Capture the formal shared SQLite grid inside an owned acceptance run.

Use verify_entry_acceptance.py; all writes stay in its isolated fixture files.
Screenshots record requested actions. Numeric results require visual review.
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


def seed(path):
    with closing(sqlite3.connect(path)) as db:
        if db.execute("SELECT 1 FROM sqlite_schema WHERE name='advanced'").fetchone():
            raise RuntimeError("Large-data acceptance needs a fresh fixture")
        db.execute("CREATE TABLE advanced(id INTEGER PRIMARY KEY,n INTEGER,note TEXT,payload BLOB)")
        db.executemany("INSERT INTO advanced VALUES(?,?,?,?)", [
            (i, 9223372036854775805 + i % 3, "Unicode fixture: " + "数据🙂" * 1200, b"\x00\xff" * 4096)
            for i in range(1, 251)])
        db.commit()


def fingerprint(path):
    with closing(sqlite3.connect(path.as_uri() + "?mode=ro", uri=True)) as db:
        h = hashlib.sha256()
        for row in db.execute("SELECT id,n,note,hex(payload) FROM advanced ORDER BY id"):
            h.update(json.dumps(row, ensure_ascii=False).encode())
        return dict(rows=db.execute("SELECT COUNT(*) FROM advanced").fetchone()[0], sha256=h.hexdigest())


def expected_fingerprint():
    h = hashlib.sha256()
    for i in range(1, 251):
        h.update(json.dumps((i, 9223372036854775805 + i % 3,
            "Unicode fixture: " + "数据🙂" * 1200, "00FF" * 4096), ensure_ascii=False).encode())
    return dict(rows=250, sha256=h.hexdigest())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--explore", action="store_true")
    parser.add_argument("--resume-fixture", action="store_true", help="reuse only the exact previously seeded owned data")
    opts = parser.parse_args()
    root, state = validate(Path(os.environ["NYADB_ENTRY_ROOT"]))
    env = environment(root, state)
    if "app" in state["processes"]:
        raise RuntimeError("Formal large-data capture requires its own process")
    config = root / "config/NyaDB/config.sqlite"
    if not config.exists():
        subprocess.run(["go", "run", "./tools/entryfixture", "seed-engine-options"], cwd=REPO, env=env, check=True)
    profile = check_attachment(config, root)
    fixture = root / "engine-attached.sqlite"
    if not opts.resume_fixture:
        seed(fixture)
    before = fingerprint(fixture)
    if before != expected_fingerprint():
        raise RuntimeError("Owned fixture differs from the exact seeded data")
    binary = REPO / "build/linux-amd64/nyadb"
    digest = hashlib.sha256(binary.read_bytes()).hexdigest()
    evidence = root / "evidence/large-data-native" / ("capture-" + uuid.uuid4().hex[:12])
    cap = NativeCapture(env["DISPLAY"], evidence, binary)
    app = spawn(root, state, "app", [str(binary)], env)
    manifest(root, state)
    (evidence / "app-identity.json").write_text(json.dumps(dict(state["processes"]["app"], run_id=state["id"], binary_sha256=digest), indent=2) + "\n")
    window = None

    def click(x, y, button=1):
        cap.actions.append(dict(action="click", client_x=x, client_y=y, button=button))
        cap.click(window, x, y, button)
        time.sleep(.4)

    def key(*keys):
        cap.actions.append(dict(action="key", keys=list(keys)))
        cap.key(*keys)
        time.sleep(.4)

    def capture(name, **requested):
        cap.capture(name, window, "portable-large-data", dict(binary_sha256=digest,
            profile_id=profile, namespace="aux", real_sqlite=True, original_comparison=False,
            source=before, loading=False, **requested))

    try:
        window = cap.wait_window("^NyaDB$", pid=app.pid)
        if hashlib.sha256(Path(f"/proc/{app.pid}/exe").read_bytes()).hexdigest() != digest:
            raise RuntimeError("Formal running binary differs from build")
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
        click(350, 190)
        key("Return")
        time.sleep(1)
        capture("grid-1280", table_open_requested=True)
        cap.resize(window, 840, 560)
        capture("grid-840", table_open_requested=True)
        cap.resize(window, 1280, 800)
        if opts.explore:
            return
        # Coordinates are established from the formal-window exploration and
        # checked by the saved action stream and visual review of each image.
        click(1102, 116)  # table information dock
        capture("inspector-1280", inspector_requested=True)
        click(1127, 152)  # exact count
        time.sleep(1)
        capture("count-1280", count_requested=True)
        click(992, 733)  # next page
        time.sleep(1)
        capture("next-page-1280", next_page_requested=True)
        click(926, 733)  # previous page
        time.sleep(1)
        click(800, 213)  # select the first row's binary field
        click(1155, 116)  # explicit field information
        capture("field-1280", field_information_requested=True)
        click(1159, 152)  # read the next 1024 bytes
        time.sleep(1)
        capture("field-chunk-1280", field_chunk_requested=True)
        click(769, 138)  # data analysis icon
        capture("analysis-settings-1280", analysis_settings_requested=True)
        click(440, 203)  # start analysis over all 250 rows
        time.sleep(1)
        capture("analysis-1280", analysis_requested=True)
        click(971, 172)  # selected-column statistics
        capture("analysis-column-1280", column_statistics_requested=True)
        click(500, 418)  # numeric field in the shared analysis grid
        capture("analysis-numeric-1280", numeric_statistics_requested=True)
        cap.resize(window, 840, 560)
        capture("analysis-840", analysis_requested=True)
    finally:
        terminate(state["processes"].pop("app"))
        manifest(root, state)
        after = fingerprint(fixture)
        (evidence / "source-check.json").write_text(json.dumps(dict(before=before, after=after, unchanged=before == after), indent=2) + "\n")
        if before != after:
            raise RuntimeError("Read-only acceptance changed fixture data")


if __name__ == "__main__":
    main()
