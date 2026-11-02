#!/usr/bin/env python3
"""Capture structured MySQL design pages inside an owned acceptance fixture.

Only the owned fixture is connected. All edits stay in a draft; SQL is not
confirmed. Run through verify_entry_acceptance.py --run <run> -- <command>.
"""
import hashlib
import os
from pathlib import Path
import subprocess
import time

from capture_alignment_gui_linux import NativeCapture
from verify_entry_acceptance import REPO, environment, manifest, spawn, terminate, validate


def main():
    root, state = validate(Path(os.environ["NYADB_ENTRY_ROOT"]))
    env = environment(root, state)
    if "app" in state["processes"]:
        raise RuntimeError("Designer capture needs its own formal application instance")
    if not (root / "config/NyaDB/config.sqlite").exists():
        subprocess.run(["go", "run", "./tools/entryfixture", "seed"], cwd=REPO, env=env, check=True)
    binary = REPO / "build/linux-amd64/nyadb"
    cap = NativeCapture(env["DISPLAY"], root / "evidence/designer-final", binary)
    app = spawn(root, state, "app", [str(binary)], env)
    window = None

    def click(x, y):
        cap.actions.append({"action": "click", "client_x": x, "client_y": y})
        cap.click(window, x, y)
        time.sleep(.12)

    def key(*keys):
        cap.actions.append({"action": "key", "keys": list(keys)})
        cap.key(*keys)
        time.sleep(.25)

    def capture(name, **extra):
        cap.capture(name, window, name, dict(loading=False, database="entry_source",
                    table="samples", owned_mysql_fixture=True, execution_confirmed=False, **extra))

    try:
        window = cap.wait_window("^NyaDB$", pid=app.pid)
        cap.source_version = "sha256:" + hashlib.sha256(Path(f"/proc/{app.pid}/exe").read_bytes()).hexdigest()
        cap.move_client(window, 30, 50)
        cap.resize(window, 1280, 800)
        time.sleep(1)
        click(105, 170)
        click(105, 170)
        time.sleep(1)
        click(105, 212)
        key("Return")
        time.sleep(1)
        click(165, 60)
        click(519, 137)
        click(370, 190)
        click(360, 137)
        time.sleep(1)
        click(630, 137)
        time.sleep(1)
        capture("designer-fields-1280")
        click(700, 178)
        key("Home", "Down", "Return")
        click(385, 214)
        capture("designer-index-1280", draft_created=True)
        cap.resize(window, 840, 560)
        capture("designer-index-840", formal_minimum_width=840)
        cap.actions.append({"action": "scroll", "client_x": 680, "client_y": 485, "wheel_down": 8})
        for _ in range(8):
            cap.click(window, 680, 485, 5)
            time.sleep(.08)
        time.sleep(.3)
        capture("designer-index-840-scrolled", formal_minimum_width=840)
    finally:
        cap.close()
        if "app" in state["processes"]:
            terminate(state["processes"].pop("app"))
        manifest(root, state)


if __name__ == "__main__":
    main()
