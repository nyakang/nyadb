#!/usr/bin/env python3
"""Capture actual MyGo import/maintenance/backup windows over disposable SQLite.

UI-thread fixture commands invoke the normal workspace actions. This is native
workspace evidence, not a claim of mouse-driven end-to-end shell acceptance.
"""
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import tempfile
import time

from capture_alignment_gui_linux import NativeCapture

REPO = Path(__file__).resolve().parents[1]


def run():
    base = REPO / "build/multi-engine-native"
    base.mkdir(parents=True, exist_ok=True)
    root = Path(tempfile.mkdtemp(prefix="run-", dir=base))
    (root / ".owned").write_text("native workspace acceptance\n")
    xvfb = subprocess.Popen(["Xvfb", "-displayfd", "1", "-screen", "0", "1600x1000x24", "-ac", "-nolisten", "tcp"], stdout=subprocess.PIPE, stderr=(root / "xvfb.log").open("w"), text=True)
    display = ":" + xvfb.stdout.readline().strip()
    env = dict(os.environ, DISPLAY=display, XDG_CONFIG_HOME=str(root / "config"), XDG_DATA_HOME=str(root / "data"), GTK_THEME="Adwaita:dark")
    events = []
    try:
        fixtures = [
            ("maintenance", "NYADB_MAINTENANCE_NATIVE", "NyaDB maintenance acceptance", ["maintenance-plan", "maintenance-run", "backup-plan", "backup-run", "restore-plan", "restore-run", "overwrite-plan", "overwrite-run"]),
            ("import", "NYADB_IMPORT_NATIVE", "NyaDB import acceptance", ["import-read", "import-plan", "import-run"]),
        ]
        for fixture, flag, title, commands in fixtures:
            (root / "command").write_text("")
            (root / "status.json").unlink(missing_ok=True)
            binary = REPO / ("build/native-" + fixture)
            log = (root / (fixture + ".log")).open("w")
            process = subprocess.Popen([str(binary)], env=dict(env, **{flag: str(root)}), cwd=REPO, stdout=log, stderr=subprocess.STDOUT)
            cap = NativeCapture(display, root / "screenshots", binary, app_name="nyadb-" + fixture)
            try:
                window = cap.wait_window("^" + title + "$", pid=process.pid)
                cap.move_client(window, 20, 20)
                for command in commands:
                    (root / "command").write_text(command)
                    deadline = time.monotonic() + 45
                    while time.monotonic() < deadline:
                        if process.poll() is not None:
                            raise RuntimeError("native fixture exited; see " + str(log.name))
                        try:
                            status = json.loads((root / "status.json").read_text())
                        except (OSError, json.JSONDecodeError):
                            status = {}
                        if status.get("command") == command and not status.get("busy", True):
                            break
                        time.sleep(.1)
                    else:
                        raise RuntimeError("native action timed out: " + command)
                    events.append(status)
                    if command.endswith("-plan") and not status.get("plan"):
                        raise RuntimeError("preview failed: " + repr(status))
                    if command.endswith("-run") and status.get("state") != "completed" and not status.get("completed"):
                        raise RuntimeError("execution failed: " + repr(status))
                    if command == "import-run":
                        if status.get("processed") != 2 or status.get("prepared") != 2 or status.get("failed") != 0:
                            raise RuntimeError("import rows failed: " + repr(status))
                        if not env.get("NYADB_NATIVE_POSTGRES_PORT"):
                            with sqlite3.connect(root / "native-import.sqlite") as db:
                                rows = db.execute("SELECT id,note,amount FROM imported_records ORDER BY id").fetchall()
                            if rows != [(1, "native import", "9007199254740993.123456789"), (2, "second row", "0.01")]:
                                raise RuntimeError("imported values changed: " + repr(rows))
                    for width, height in [(1280, 800), (840, 560)]:
                        cap.resize(window, width, height)
                        cap.capture(f"{command}-{width}", window, command, {"loading": False, "fixture_commands": True, **status})
                (root / "command").write_text("quit")
                process.wait(timeout=15)
                if process.returncode != 0:
                    raise RuntimeError("native fixture verification failed; see " + str(log.name))
                events.append({"fixture": fixture, "verification": json.loads((root / (fixture + "-verified.json")).read_text())})
                events.append({"fixture": fixture, "exit": process.returncode})
            finally:
                cap.close()
                if process.poll() is None:
                    process.terminate()
                    process.wait(timeout=15)
                log.close()
    finally:
        xvfb.terminate()
        xvfb.wait(timeout=15)
        (root / "results.json").write_text(json.dumps({"events": events, "display_stopped": xvfb.poll() is not None}, indent=2, ensure_ascii=False) + "\n")
        print(root, flush=True)


if __name__ == "__main__":
    run()
