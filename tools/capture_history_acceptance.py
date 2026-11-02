#!/usr/bin/env python3
"""Exercise formal SQL history under the owned real keyring/SQLite fixture.

Run through verify_entry_acceptance.py --run <owned-run> -- <command>.
No Navicat, production service, passwords or keys are used in GUI input.
"""
import argparse
import hashlib
import os
from pathlib import Path
import subprocess
import sys
import time

from capture_alignment_gui_linux import NativeCapture
from verify_entry_acceptance import REPO, environment, manifest, spawn, terminate, validate


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--clear-only", action="store_true",
                        help="resume the clear step without running SQL again")
    parser.add_argument("--with-data", action="store_true",
                        help="also open the owned SQLite table and verify global read activity")
    parser.add_argument("--with-designer", action="store_true",
                        help="also capture the structured MySQL designer")
    opts = parser.parse_args()
    root, state = validate(Path(os.environ["NYADB_ENTRY_ROOT"]))
    env = environment(root, state)
    if "app" in state["processes"]:
        raise RuntimeError("History replay needs its own formal application instance")
    if not (root / "config/NyaDB/config.sqlite").exists():
        subprocess.run(["go", "run", "./tools/entryfixture", "seed"], cwd=REPO, env=env, check=True)
    binary = REPO / "build/linux-amd64/nyadb"
    cap = NativeCapture(env["DISPLAY"], root / "evidence/history-native", binary)
    app = None
    window = None
    launched_sha = None

    def start():
        nonlocal app, window, launched_sha
        app = spawn(root, state, "app", [str(binary)], env)
        window = cap.wait_window("^NyaDB$", pid=app.pid)
        actual_sha = hashlib.sha256(Path(f"/proc/{app.pid}/exe").read_bytes()).hexdigest()
        if launched_sha is not None and launched_sha != actual_sha:
            raise RuntimeError("Formal binary changed during the restart replay")
        launched_sha = actual_sha
        cap.source_version = "sha256:" + launched_sha
        cap.move_client(window, 30, 50)
        cap.resize(window, 1280, 800)
        time.sleep(1)

    def click(x, y):
        cap.actions.append({"action": "click", "client_x": x, "client_y": y})
        cap.click(window, x, y)
        time.sleep(.25)

    def key(*keys):
        cap.actions.append({"action": "key", "keys": list(keys)})
        cap.key(*keys)
        time.sleep(.25)

    def check(phase):
        subprocess.run(["go", "run", "./tools/entryfixture", phase], cwd=REPO, env=env, check=True)

    def capture(name, **observed):
        cap.capture(name, window, name, dict(loading=False, real_vault=True,
                    binary_sha256=launched_sha, **observed))

    try:
        start()
        if not opts.clear_only:
            click(105, 128)
            click(105, 128)
            time.sleep(2)
            key("ctrl+n")
            click(425, 225)
            subprocess.run(["xclip", "-selection", "clipboard"], input=b"SELECT 'history-native-fixture' AS marker", env=env, check=True)
            key("ctrl+a", "ctrl+v", "ctrl+Return")
            time.sleep(1)
            capture("history-execution-attempt", execution_requested=True)
            check("native-history")
            capture("history-sql-result", real_sqlite=True, shortcuts=["Ctrl+N from connection navigator", "Ctrl+Enter"])
            if opts.with_data:
                click(105, 150)
                click(105, 150)
                time.sleep(1)
                capture("global-history-database", real_sqlite=True)
                click(50, 170)  # disclose the table category
                click(120, 191)
                key("Return")
                time.sleep(1)
                capture("global-history-data", real_sqlite=True)
                check("native-global-history")
                # Restore the SQL tab so the history shortcut remains a
                # document action with an original-profile opener.
                click(493, 109)
            key("ctrl+l")
            capture("history-list", shortcut="Ctrl+L")
            cap.resize(window, 840, 560)
            capture("history-840", formal_minimum_width=840)
            cap.resize(window, 1280, 800)
            # No SQL is replayed on restart.
            terminate(state["processes"].pop("app"))
            app = None
            start()
        key("ctrl+l")
        check("native-global-history" if opts.with_data else "native-history")
        capture("history-restart", process_restart=True, no_automatic_execution=True)
        click(835, 631)
        capture("history-clear-confirm")
        key("Return")
        time.sleep(1)
        check("native-history-cleared")
        capture("history-cleared", durable_clear=True)
    finally:
        cap.close()
        if "app" in state["processes"]:
            terminate(state["processes"].pop("app"))
        manifest(root, state)
    if opts.with_designer:
        subprocess.run([sys.executable, "tools/capture_designer_acceptance.py"], cwd=REPO, env=env, check=True)


if __name__ == "__main__":
    main()
