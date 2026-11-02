#!/usr/bin/env python3
"""Replay URI import, SQLite object search, and favorites in an owned Linux run.

Use verify_entry_acceptance.py --run <run> --keep -- this tool. Screenshots
are NyaDB native evidence only; the SQL query is never executed by this replay.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3
import subprocess
import time

from PIL import Image

from capture_alignment_gui_linux import NativeCapture
from verify_entry_acceptance import REPO, environment, manifest, spawn, terminate, validate


def read_directory(config):
    with sqlite3.connect(config.as_uri() + "?mode=ro", uri=True) as db:
        row = db.execute("SELECT value FROM app_preferences WHERE key=?",
                         ("workbench_favorites_v1",)).fetchone()
    return json.loads(row[0]) if row else {"version": 1, "entries": [], "groups": []}


def check_directory(value, phase, identity=None):
    if value.get("version") != 1:
        raise RuntimeError("Unexpected favorites format")
    entries, groups = value["entries"], value["groups"]
    if phase == "empty":
        if entries or groups:
            raise RuntimeError("Replay requires an empty owned favorites directory")
        return None
    if phase == "removed":
        if entries or groups:
            raise RuntimeError("Confirmed removal did not persist")
        return None
    if len(entries) != 1 or len(groups) != 1:
        raise RuntimeError("Expected one native favorite and group")
    entry, group = entries[0], groups[0]
    obj = entry.get("object", {})
    if (entry.get("kind") != "object" or entry.get("name") != "原生收藏表"
            or obj.get("Kind") != "table" or obj.get("Name") != "fixture"
            or obj.get("Catalog") != "main" or obj.get("Schema") != "main"
            or obj.get("ProfileID") != entry.get("profile_id")
            or group.get("name") != "原生收藏分组"
            or entry.get("group_id") != group.get("id")):
        raise RuntimeError("Native favorite lost its original identity or group")
    current = (entry["id"], group["id"], entry["profile_id"])
    if identity is not None and current != identity:
        raise RuntimeError("Restart changed favorite or group identity")
    return current


def primary_button_center(client):
    """Locate the sole filled primary button in a centered favorite dialog."""
    width, height = client.size
    left, top = max(0, width//2-270), max(0, height//2-130)
    right, bottom = min(width, width//2+270), min(height, height//2+130)
    pixels = client.convert("RGB").load()
    candidates = set()
    for y in range(top, bottom):
        for x in range(left, right):
            r,g,b = pixels[x,y]
            if r < 75 and g > 85 and b > 180:
                candidates.add((x,y))
    rectangles = []
    while candidates:
        point = candidates.pop()
        pending = [point]
        xs, ys = [point[0]], [point[1]]
        while pending:
            x,y = pending.pop()
            for neighbor in ((x-1,y),(x+1,y),(x,y-1),(x,y+1)):
                if neighbor in candidates:
                    candidates.remove(neighbor)
                    pending.append(neighbor)
                    xs.append(neighbor[0]); ys.append(neighbor[1])
        bounds = min(xs),min(ys),max(xs)+1,max(ys)+1
        if bounds[2]-bounds[0]>=50 and bounds[3]-bounds[1]>=20 and len(xs)>800 and len(xs)/((bounds[2]-bounds[0])*(bounds[3]-bounds[1]))>.3:
            rectangles.append(bounds)
    if len(rectangles)!=1:
        raise RuntimeError("Cannot uniquely locate favorite confirmation button")
    x1,y1,x2,y2 = rectangles[0]
    return ((x1+x2)//2, (y1+y2)//2)


def main():
    root, state = validate(Path(os.environ["NYADB_ENTRY_ROOT"]))
    env = environment(root, state)
    config = root / "config/NyaDB/config.sqlite"
    if "app" in state["processes"]:
        raise RuntimeError("Replay requires its own formal application process")
    if not config.exists():
        subprocess.run(["go", "run", "./tools/entryfixture", "seed"],
                       cwd=REPO, env=env, check=True)
    check_directory(read_directory(config), "empty")
    binary = REPO / "build/linux-amd64/nyadb"
    cap = NativeCapture(env["DISPLAY"], root / "evidence/common-native", binary)
    window, expected_hash, pid = None, None, None

    def start():
        nonlocal window, expected_hash, pid
        app = spawn(root, state, "app", [str(binary)], env)
        pid = app.pid
        window = cap.wait_window("^NyaDB$", pid=pid)
        digest = hashlib.sha256(Path(f"/proc/{pid}/exe").read_bytes()).hexdigest()
        if expected_hash is not None and expected_hash != digest:
            raise RuntimeError("Formal binary changed during restart")
        expected_hash = digest
        cap.source_version = "sha256:" + digest
        cap.move_client(window, 30, 50)
        cap.resize(window, 1280, 800)
        time.sleep(1)

    def stop():
        terminate(state["processes"].pop("app"))
        manifest(root, state)

    def click(x, y, target=None, button=1):
        cap.actions.append({"action": "click", "client_x": x, "client_y": y, "button": button})
        cap.click(target or window, x, y, button=button)
        time.sleep(.3)

    def key(*keys):
        cap.actions.append({"action": "key", "keys": list(keys)})
        cap.key(*keys)
        time.sleep(.3)

    def paste(text):
        # Only public fixture labels and a password-free owned file URI.
        subprocess.run(["xclip", "-selection", "clipboard"], input=text.encode(), env=env, check=True)
        key("ctrl+v")

    def capture(name, target=None, **observed):
        cap.capture(name, target or window, "common-entries", dict(
            loading=False, real_vault=True, binary_sha256=expected_hash, **observed))

    def manage():
        click(150, 12)
        key("Down", "Return")
        time.sleep(.6)

    def confirm():
        path = root / "runtime/common-confirmation.png"
        cap.screenshot(path)
        geometry = cap.geometry(window)
        with Image.open(path) as frame:
            center = primary_button_center(frame.crop(tuple(geometry["client_crop"])))
        path.unlink(missing_ok=True)
        click(*center)

    def directory_wait(phase, identity=None):
        until = time.monotonic() + 8
        while time.monotonic() < until:
            try:
                return check_directory(read_directory(config), phase, identity)
            except (RuntimeError, KeyError, sqlite3.Error):
                time.sleep(.2)
        raise RuntimeError("Native favorites publication did not reach the expected state")

    try:
        start()
        click(35, 55)
        child = cap.wait_window("^新建连接$", pid=pid)
        click(120, 677, child)
        child = cap.wait_window("^通过 URI 新建连接$", pid=pid)
        click(330, 90, child)
        paste((root / "fixture.sqlite").as_uri().replace("file:", "sqlite:", 1))
        capture("uri-masked", child, uri_password_free=True)
        click(824, 676, child)
        child = cap.wait_window("^" + re.escape("新建连接 (SQLite)") + "$", pid=pid)
        capture("uri-imported", child, saved=False, connected=False)
        click(850, 676, child)
        with sqlite3.connect(config.as_uri() + "?mode=ro", uri=True) as db:
            if db.execute("SELECT COUNT(*) FROM connection_profiles").fetchone()[0] != 3:
                raise RuntimeError("URI import silently saved another connection")
        click(110, 128)
        key("Return")
        time.sleep(1)
        key("ctrl+shift+f")
        click(400, 138)
        paste("fixture")
        click(1040, 138)
        time.sleep(.8)
        capture("search-result-1280", real_sqlite=True)
        click(430, 279)
        key("Return")
        time.sleep(1)
        capture("search-open-data", real_sqlite=True)
        manage()
        click(222, 630)
        paste("原生收藏分组")
        confirm()
        time.sleep(.6)
        click(924, 170)
        key("End", "Return")
        click(1030, 630)
        click(150, 12)
        key("Down", "Down", "Return")
        key("ctrl+a")
        paste("原生收藏表")
        confirm()
        identity = directory_wait("grouped")
        capture("favorites-grouped-1280", real_sqlite=True, directory_verified=True)
        click(400, 170)
        paste("不存在")
        capture("favorites-search-empty", expected_rows=0)
        key("ctrl+a")
        paste("原生收藏表")
        capture("favorites-search-match", expected_rows=1)
        cap.resize(window, 840, 560)
        capture("favorites-grouped-840", real_sqlite=True, directory_verified=True)
        stop()
        start()
        manage()
        directory_wait("grouped", identity)
        subprocess.run(["go", "run", "./tools/entryfixture", "native-common"], cwd=REPO, env=env, check=True)
        capture("favorites-restart-offline", restarted=True, directory_verified=True)
        # Group deletion retains the favorite, then removing it retains the table.
        click(924, 170)
        key("End", "Return")
        click(445, 630)
        confirm()
        time.sleep(.6)
        value = read_directory(config)
        if value["groups"] or len(value["entries"]) != 1 or value["entries"][0].get("group_id"):
            raise RuntimeError("Deleting a group removed or stranded its favorite")
        click(230, 229)
        key("Delete")
        capture("favorites-remove-confirmation", confirmed=False)
        confirm()
        directory_wait("removed")
        with sqlite3.connect(root / "fixture.sqlite") as db:
            if db.execute("SELECT label FROM fixture WHERE id=1").fetchone()[0] != "native fixture":
                raise RuntimeError("Removing a favorite changed fixture data")
        capture("favorites-removed", directory_verified=True, fixture_retained=True)
        (root / "evidence/common-native-result.json").write_text(json.dumps(dict(
            run_id=state["id"], binary_sha256=expected_hash, favorite_id=identity[0],
            group_id=identity[1], profile_id=identity[2], restarted=True,
            uri_saved=False, removed=True, fixture_retained=True), indent=2) + "\n")
    finally:
        cap.close()
        if "app" in state["processes"]:
            stop()
        manifest(root, state)


if __name__ == "__main__":
    main()
