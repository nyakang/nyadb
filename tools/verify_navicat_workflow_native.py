#!/usr/bin/env python3
"""Native Shell workflow evidence over an owned disposable SQLite database.

Build with: CGO_ENABLED=0 go test -c -o build/native-ux ./internal/ui/shell
Fixture commands open real workspaces. This does not claim mouse-driven
connection setup, server integration, or Navicat version equivalence.
"""
from __future__ import annotations
import argparse
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import time

from capture_alignment_gui_linux import NativeCapture

REPO = Path(__file__).resolve().parent.parent


def make_fixture(root: Path):
    with sqlite3.connect(root / "fixture.sqlite") as db:
        db.executescript("""CREATE TABLE customers(id INTEGER PRIMARY KEY,name TEXT NOT NULL,email TEXT,credit NUMERIC,active INTEGER,notes TEXT);
CREATE TABLE orders(id INTEGER PRIMARY KEY,customer_id INTEGER,status TEXT,amount NUMERIC);
CREATE INDEX customer_name ON customers(name);
CREATE VIEW active_customers AS SELECT * FROM customers WHERE active=1;""")
        db.executemany("INSERT INTO customers VALUES(?,?,?,?,?,?)", [(i, f"Customer {i:03}", f"user{i}@example.test", f"{i}.25", i % 2, f"UX fixture row {i}") for i in range(1, 121)])
        db.executemany("INSERT INTO orders VALUES(?,?,?,?)", [(i, i, "paid" if i % 2 else "pending", f"{i}.50") for i in range(1, 31)])


def run_locale(output: Path, locale: str, binary: Path):
    root = output / locale
    root.mkdir()
    (root / ".owned").write_text("NyaDB native workflow fixture\n")
    (root / "command").write_text("")
    make_fixture(root)
    before = hashlib.sha256((root / "fixture.sqlite").read_bytes()).hexdigest()
    display = next(f":{n}" for n in range(250, 290) if not Path(f"/tmp/.X11-unix/X{n}").exists())
    env = dict(os.environ, DISPLAY=display, LIBGL_ALWAYS_SOFTWARE="1", XDG_CONFIG_HOME=str(root / "config"), XDG_CACHE_HOME=str(root / "cache"), NYADB_UX_NATIVE=str(root), NYADB_UX_LOCALE=locale)
    processes, logs, events = [], [], []
    cap = None
    try:
        for name, argv in [("xvfb", ["Xvfb", display, "-screen", "0", "1600x1000x24", "-dpi", "96", "-nolisten", "tcp", "-ac"]), ("openbox", ["openbox"]), ("nyadb", [str(binary)])]:
            log = (root / f"{name}.log").open("w")
            logs.append(log)
            processes.append(subprocess.Popen(argv, cwd=REPO, env=env, stdout=log, stderr=subprocess.STDOUT, start_new_session=True))
            time.sleep(.6)
        cap = NativeCapture(display, root / "screenshots", binary)
        window = cap.wait_window("^NyaDB UX acceptance$", processes[-1].pid)
        cap.move_client(window, 20, 30)
        cap.run("xdotool", "windowactivate", "--sync", str(window))

        def command(cmd):
            (root / "command").write_text(cmd)
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline:
                if processes[-1].poll() is not None:
                    raise RuntimeError("native fixture exited")
                try:
                    state = json.loads((root / "status.json").read_text())
                except (OSError, json.JSONDecodeError):
                    state = {}
                done = state.get("command") == cmd and not state.get("busy", False) and not state.get("pending", "")
                if cmd == "connect":
                    done = done and state.get("connected", False)
                elif cmd == "open":
                    done = done and state.get("database") == "main"
                elif cmd == "data":
                    done = done and state.get("rows", 0) > 0
                elif cmd in ("sql", "rerun"):
                    done = done and state.get("result", False)
                if done:
                    if state.get("error"):
                        raise RuntimeError(state)
                    events.append(state)
                    time.sleep(.3)
                    return
                time.sleep(.1)
            raise RuntimeError((cmd, state))

        def capture(name):
            cap.capture(name, window, name, {"loading": False, "fixture_commands": True, "locale": locale}, locale=locale, theme="light")

        def click(x, y, button=1):
            cap.click(window, x, y, button)
            time.sleep(.3)

        def key(*keys):
            cap.key(*keys)
            time.sleep(.3)

        command("connect")
        command("open")
        command("data")
        cap.resize(window, 1280, 800)
        capture("data-1280")
        # Record form is the first native data toolbar command.
        click(300, 138)
        capture("form-1280")
        cap.resize(window, 840, 560)
        capture("form-840")
        cap.resize(window, 1280, 800)
        command("sql")
        capture("sql-1280")
        cap.resize(window, 840, 560)
        capture("sql-840")
        # Pin and revisit through the native result menu after a real rerun.
        click(338 if locale == "zh-Hans" else 370, 337)
        capture("snapshot-menu-840")
        key("Down", "Return")
        command("rerun")
        click(338 if locale == "zh-Hans" else 370, 337)
        key("Down", "Down", "Return")
        capture("snapshot-after-rerun-840")
        key("Escape")
        # A cached result field viewer opens through the native cell menu.
        click(395, 438, 3)
        key("Down", "Down", "Return")
        capture("sql-field-below-840")
        # The result-only layout keeps both the grid and field viewer usable.
        click(195 if locale == "zh-Hans" else 201, 337)
        key("Down", "Down", "Down", "Return")
        capture("sql-result-only-840")
        click(795, 168)
        capture("sql-cached-export-840")
        key("Escape")
        cap.resize(window, 1280, 800)
        command("design")
        capture("design-fields-1280")
        cap.resize(window, 840, 560)
        capture("design-fields-840")
        cap.resize(window, 1280, 800)
        # Index subpage; ordinary index graph is reached by selecting its row.
        click(380 if locale == "zh-Hans" else 425, 179)
        click(325, 273)
        capture("design-index-1280")
        cap.resize(window, 840, 560)
        capture("design-index-840")
        (root / "command").write_text("quit")
        processes[-1].wait(timeout=20)
        if processes[-1].returncode != 0:
            raise RuntimeError("native fixture failed")
    finally:
        if cap:
            cap.close()
        for process in reversed(processes):
            if process.poll() is None:
                process.terminate()
                process.wait(timeout=15)
        for log in logs:
            log.close()
        after = hashlib.sha256((root / "fixture.sqlite").read_bytes()).hexdigest()
        (root / "verification.json").write_text(json.dumps({"locale": locale, "events": events, "sha256_before": before, "sha256_after": after, "fixture_unchanged": before == after, "processes_stopped": all(p.poll() is not None for p in processes)}, ensure_ascii=False, indent=2) + "\n")
        if before != after:
            raise RuntimeError("fixture data was changed")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, default=REPO / "build/native-ux")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--locale", choices=["zh-Hans", "en", "both"], default="both")
    args = parser.parse_args()
    output = args.output.resolve()
    if not any(output.is_relative_to(REPO / directory) for directory in ("build", "temp")):
        raise SystemExit("output must be in ignored build/ or temp/")
    output.mkdir(parents=True, exist_ok=False)
    for locale in (["zh-Hans", "en"] if args.locale == "both" else [args.locale]):
        run_locale(output, locale, args.binary.resolve())
    print(output)


if __name__ == "__main__":
    main()
