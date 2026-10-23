#!/usr/bin/env python3
"""Linux GUI smoke capture for NyaDB theme settings; never touches Navicat/real DB."""
from __future__ import annotations

import os
import sqlite3
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time

from PIL import Image

ROOT = Path(__file__).resolve().parents[1]
OUTPUT = ROOT / "temp/research/theme-gui-2026-10-23"
OUTPUT.mkdir(parents=True, exist_ok=True)
BINARY = ROOT / "build/linux-amd64/nyadb"


def run(*args: str, timeout: float = 10) -> str:
    p = subprocess.run(args, capture_output=True, text=True, timeout=timeout, check=True)
    return p.stdout.strip()


def click(window: str, x: int, y: int) -> None:
    run("xdotool", "mousemove", str(x), str(y))
    run("xdotool", "click", "--clearmodifiers", "1")
    time.sleep(0.55)


def key(*keys: str) -> None:
    run("xdotool", "key", "--clearmodifiers", *keys)
    time.sleep(0.5)


def shot(label: str) -> None:
    run("scrot", "-z", "-o", str(OUTPUT / f"{label}.png"))


def main() -> int:
    if not BINARY.is_file():
        print("ERROR: Linux build missing; run go tool mygo build first", file=sys.stderr)
        return 2
    if not os.environ.get("DISPLAY"):
        print("ERROR: requires an isolated X display (xvfb-run)", file=sys.stderr)
        return 2
    with tempfile.TemporaryDirectory(prefix="nyadb-theme-xdg-") as home:
        env = {**os.environ, "HOME": home, "XDG_CONFIG_HOME": str(Path(home) / ".config")}
        # Avoid a production profile and OS keychain. Do not start any DB session.
        wm = subprocess.Popen(["openbox", "--sm-disable"], env=env,
                              stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        log = open(OUTPUT / "nyadb-stderr.log", "w", encoding="utf-8")
        app = subprocess.Popen([str(BINARY)], cwd=str(ROOT), env=env,
                               stdout=log, stderr=subprocess.STDOUT)
        try:
            window = None
            for _ in range(55):
                if app.poll() is not None:
                    raise RuntimeError(f"NyaDB terminated (exit {app.returncode}); see log")
                try:
                    ids = run("xdotool", "search", "--onlyvisible", "--name", "NyaDB")
                    if ids:
                        window = ids.splitlines()[-1]
                        break
                except subprocess.CalledProcessError:
                    pass
                time.sleep(0.3)
            if not window:
                raise RuntimeError("NyaDB top-level window not found")
            run("xdotool", "windowsize", window, "1280", "800")
            run("xdotool", "windowmove", window, "65", "55")
            run("xdotool", "windowactivate", "--sync", window)
            time.sleep(3.0)
            shot("01-main-dark")
            # Absolute X11 coordinates include the Openbox titlebar.
            click(window, 293, 88)  # Tools menu
            shot("02-tools-menu")
            key("End", "Return")  # final Tools entry: Settings / 选项…
            time.sleep(1.0)
            shot("03-settings-dark")
            click(window, 785, 332)  # Light theme preview header
            key("space")  # Native button keyboard activation as a fallback
            time.sleep(4.0)
            shot("04-settings-light")
            # Exercise a full tab rebuild in the actual native event loop.
            click(window, 383, 180)  # Objects
            click(window, 472, 180)  # Settings
            time.sleep(2.0)
            shot("04b-settings-light-reopened")
            before = Image.open(OUTPUT / "03-settings-dark.png").convert("RGB").getpixel((670, 280))
            after = Image.open(OUTPUT / "04b-settings-light-reopened.png").convert("RGB").getpixel((670, 280))
            db = Path(home) / ".config" / "NyaDB" / "config.sqlite"
            with sqlite3.connect(db) as conn:
                saved = conn.execute("SELECT value FROM app_preferences WHERE key='appearance'").fetchone()
            print("APPEARANCE:", saved, "pixels =", before, after)
            if sum(after) <= sum(before) + 220:
                raise RuntimeError(f"theme did not visibly switch: dark={before}, light={after}")
            if not saved or saved[0] != "light":
                raise RuntimeError(f"light preference was not persisted: {saved}")
            # Scroll native settings workspace to show the actual SQL editor.
            run("xdotool", "mousemove", "910", "704")
            run("xdotool", "click", "--repeat", "4", "--delay", "130", "5")
            time.sleep(0.75)
            shot("05-sql-editor-light")
            run("xdotool", "mousemove", "1088", "478")
            run("xdotool", "mousedown", "1")
            run("xdotool", "mousemove", "1088", "685")
            run("xdotool", "mouseup", "1")
            time.sleep(1.5)
            shot("06-sql-editor-scrolled-light")
            print("SUCCESS: native Linux GUI / dark -> light -> persisted preference")
            print("PIXEL: dark =", before, "light =", after)
            print("WINDOW_ID:", window)
            for item in sorted(OUTPUT.glob("0*.png")):
                print("SCREENSHOT:", item.relative_to(ROOT))
            return 0
        finally:
            app.terminate()
            try: app.wait(timeout=4)
            except subprocess.TimeoutExpired: app.kill()
            wm.terminate()
            try: wm.wait(timeout=2)
            except subprocess.TimeoutExpired: wm.kill()
            log.close()


if __name__ == "__main__":
    raise SystemExit(main())
