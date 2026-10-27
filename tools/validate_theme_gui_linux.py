#!/usr/bin/env python3
"""Linux GUI smoke capture for NyaDB theme settings; never touches Navicat/real DB."""
from __future__ import annotations

import os
import hashlib
import json
import sqlite3
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time

from PIL import Image

ROOT = Path(__file__).resolve().parents[1]
OUTPUT = ROOT / "temp/research/theme-gui-2026-10-27"
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


def wait_for_background(light: bool, mode: str | None = None, timeout: float = 45) -> tuple[int, int, int]:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        shot("pending-frame")
        frame = Image.open(OUTPUT / "pending-frame.png").convert("RGB")
        pixel = frame.getpixel((1080, 280))
        ready = min(pixel) > 230 if light else max(pixel) < 100
        dark_selected = frame.getpixel((500, 324)) == (0, 157, 255)
        light_selected = frame.getpixel((900, 324)) == (8, 121, 209)
        if mode == "dark":
            ready = ready and dark_selected
        elif mode == "light":
            ready = ready and light_selected
        elif mode == "system":
            ready = ready and not dark_selected and not light_selected
        if ready:
            return pixel
        time.sleep(0.5)
    raise RuntimeError(f"timed out waiting for {'light' if light else 'dark'} frame: {pixel}")


def main() -> int:
    if not BINARY.is_file():
        print("ERROR: Linux build missing; run go tool mygo build first", file=sys.stderr)
        return 2
    if not os.environ.get("DISPLAY"):
        print("ERROR: requires an isolated X display (xvfb-run)", file=sys.stderr)
        return 2
    with tempfile.TemporaryDirectory(prefix="nyadb-theme-xdg-") as home:
        env = {**os.environ, "XDG_CONFIG_HOME": str(Path(home) / ".config")}
        env.pop("GTK_THEME", None)  # Exercise NyaDB's reversible native fallback.
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
            click(window, 261, 88)  # Tools menu
            shot("02-tools-menu")
            key("End", "Return")  # final Tools entry: Settings / 选项…
            time.sleep(1.0)
            shot("03-settings-dark")
            click(window, 785, 332)  # Light theme preview header
            wait_for_background(True, "light")
            shot("04-settings-light")
            # Exercise a full tab rebuild in the actual native event loop.
            click(window, 355, 184)  # Objects
            click(window, 429, 184)  # Settings
            time.sleep(2.0)
            shot("04b-settings-light-reopened")
            before = Image.open(OUTPUT / "03-settings-dark.png").convert("RGB").getpixel((1080, 280))
            after = Image.open(OUTPUT / "04b-settings-light-reopened.png").convert("RGB").getpixel((1080, 280))
            db = Path(home) / ".config" / "NyaDB" / "config.sqlite"
            with sqlite3.connect(db) as conn:
                saved = conn.execute("SELECT value FROM app_preferences WHERE key='appearance'").fetchone()
            print("APPEARANCE:", saved, "pixels =", before, after)
            if sum(after) <= sum(before) + 220:
                raise RuntimeError(f"theme did not visibly switch: dark={before}, light={after}")
            if not saved or saved[0] != "light":
                raise RuntimeError(f"light preference was not persisted: {saved}")
            click(window, 261, 88)
            time.sleep(1.0)
            shot("04c-native-menu-light")
            key("Escape")
            click(window, 440, 332)  # Return to the dark preview card.
            wait_for_background(False, "dark")
            click(window, 261, 88)
            time.sleep(1.0)
            shot("04d-native-menu-dark")
            light_menu = Image.open(OUTPUT / "04c-native-menu-light.png").convert("RGB").getpixel((300, 118))
            dark_menu = Image.open(OUTPUT / "04d-native-menu-dark.png").convert("RGB").getpixel((300, 118))
            if sum(light_menu) <= sum(dark_menu) + 220:
                raise RuntimeError(f"native GTK menu did not switch: light={light_menu}, dark={dark_menu}")
            print("GTK_MENU:", "light =", light_menu, "dark =", dark_menu)
            key("Escape")
            click(window, 371, 499)  # Follow system; restore the Xvfb desktop theme.
            wait_for_background(True, "system")
            shot("04e-system-theme")
            with sqlite3.connect(db) as conn:
                system_saved = conn.execute("SELECT value FROM app_preferences WHERE key='appearance'").fetchone()
            if system_saved != ("system",):
                raise RuntimeError(f"system preference was not saved: {system_saved}")
            click(window, 261, 88)
            time.sleep(1.0)
            shot("04f-native-menu-system")
            system_menu = Image.open(OUTPUT / "04f-native-menu-system.png").convert("RGB").getpixel((300, 118))
            if sum(system_menu) <= sum(dark_menu) + 220:
                raise RuntimeError(f"system mode did not restore the native desktop menu: {system_menu}")
            key("Escape")
            click(window, 785, 332)  # Restore light for the editor captures.
            wait_for_background(True, "light")
            # Scroll native settings workspace to show the actual SQL editor.
            run("xdotool", "mousemove", "910", "704")
            run("xdotool", "click", "--repeat", "4", "--delay", "130", "5")
            time.sleep(0.75)
            shot("05-sql-editor-light")
            run("xdotool", "mousemove", "1134", "478")
            run("xdotool", "mousedown", "1")
            run("xdotool", "mousemove", "1134", "813")
            run("xdotool", "mouseup", "1")
            time.sleep(1.5)
            shot("06-sql-editor-scrolled-light")
            app.terminate()
            try:
                app.wait(timeout=4)
            except subprocess.TimeoutExpired:
                app.kill()
                app.wait(timeout=4)
            app = subprocess.Popen([str(BINARY)], cwd=str(ROOT), env=env,
                                   stdout=log, stderr=subprocess.STDOUT)
            restarted = None
            for _ in range(55):
                if app.poll() is not None:
                    raise RuntimeError(f"restarted NyaDB terminated: {app.returncode}")
                try:
                    ids = run("xdotool", "search", "--onlyvisible", "--name", "NyaDB")
                    if ids:
                        restarted = ids.splitlines()[-1]
                        break
                except subprocess.CalledProcessError:
                    pass
                time.sleep(0.3)
            if not restarted:
                raise RuntimeError("restarted NyaDB window not found")
            run("xdotool", "windowsize", restarted, "1280", "800")
            run("xdotool", "windowmove", restarted, "65", "55")
            run("xdotool", "windowactivate", "--sync", restarted)
            wait_for_background(True)
            shot("07-restarted-light")
            with sqlite3.connect(db) as conn:
                restored = conn.execute("SELECT value FROM app_preferences WHERE key='appearance'").fetchone()
            if restored != ("light",):
                raise RuntimeError(f"restart lost appearance preference: {restored}")
            source = hashlib.sha256()
            for path in sorted([*ROOT.glob("internal/**/*.go"), *ROOT.glob("cmd/**/*.go"), *ROOT.glob("locales/*.go"), ROOT / "main.go", ROOT / "go.mod", ROOT / "go.sum"]):
                source.update(str(path.relative_to(ROOT)).encode())
                source.update(path.read_bytes())
            (OUTPUT / "build-identity.json").write_text(json.dumps({
                "git_head": run("git", "-C", str(ROOT), "rev-parse", "HEAD"),
                "binary_sha256": hashlib.sha256(BINARY.read_bytes()).hexdigest(),
                "source_sha256": source.hexdigest(),
                "workspace": "contains uncommitted theme changes",
                "display": os.environ["DISPLAY"],
                "workbench_pixels": {"dark": before, "light": after},
                "menu_pixels": {"dark": dark_menu, "light": light_menu, "system": system_menu},
                "restored_appearance": restored[0],
            }, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
            print("SUCCESS: native Linux GUI / dark -> light -> dark -> system -> light / GTK menus / restart restored light")
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
