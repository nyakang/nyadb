#!/usr/bin/env python3
"""Verify color schemes in an isolated X11 desktop with temporary config.

Run with xvfb-run; requires Openbox, xdotool and Pillow with XCB support.
No database sessions or production profiles are used.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import tempfile
import time

from PIL import ImageGrab

ROOT = Path(__file__).resolve().parents[1]
SIDEBARS = {
    "classic": {"light": "f8f9fb", "dark": "232629"},
    "ocean": {"light": "edf3fc", "dark": "1a2c40"},
    "forest": {"light": "ebf3ed", "dark": "203128"},
    "rose": {"light": "f6edf3", "dark": "35283a"},
    "sand": {"light": "f4edde", "dark": "342e24"},
}


def run(*args: str) -> str:
    return subprocess.run(args, capture_output=True, text=True, check=True, timeout=10).stdout.strip()


def stop(process: subprocess.Popen | None) -> None:
    if process is None or process.poll() is not None:
        return
    process.terminate()
    try:
        process.wait(timeout=4)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=4)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=ROOT / "build/linux-amd64/nyadb")
    parser.add_argument("--output", type=Path, default=ROOT / "temp/theme-system-2026-10-31/native")
    args = parser.parse_args()
    binary, output = args.binary.resolve(), args.output.resolve()
    if not binary.is_file() or not os.environ.get("DISPLAY"):
        parser.error("requires a built binary and an isolated X display (xvfb-run)")
    output.mkdir(parents=True, exist_ok=True)

    with tempfile.TemporaryDirectory(prefix="nyadb-theme-xdg-") as config, (output / "app.log").open("w") as log:
        env = {**os.environ, "XDG_CONFIG_HOME": config, "LANG": "zh_CN.UTF-8"}
        env.pop("GTK_THEME", None)
        env.pop("LC_ALL", None)
        wm = subprocess.Popen(["openbox", "--sm-disable"], env=env, stdout=log, stderr=log)
        app = None
        window = ""
        geometry = {}
        db = Path(config) / "NyaDB/config.sqlite"

        def screenshot(label: str):
            frame = ImageGrab.grab().convert("RGB")
            frame.save(output / f"{label}.png")
            return frame

        def launch() -> subprocess.Popen:
            nonlocal window, geometry
            process = subprocess.Popen([str(binary)], cwd=ROOT, env=env, stdout=log, stderr=log)
            deadline = time.monotonic() + 25
            try:
                while time.monotonic() < deadline:
                    if process.poll() is not None:
                        raise RuntimeError(f"app exited: {process.returncode}; see {output / 'app.log'}")
                    try:
                        ids = run("xdotool", "search", "--onlyvisible", "--name", "^NyaDB$")
                    except subprocess.CalledProcessError:
                        ids = ""
                    if ids:
                        window = ids.splitlines()[-1]
                        run("xdotool", "windowsize", window, "1280", "800")
                        run("xdotool", "windowmove", window, "65", "55")
                        run("xdotool", "windowactivate", "--sync", window)
                        time.sleep(0.8)
                        geometry = dict(line.split("=", 1) for line in run("xdotool", "getwindowgeometry", "--shell", window).splitlines())
                        geometry = {k: int(v) for k, v in geometry.items()}
                        return process
                    time.sleep(0.2)
                raise RuntimeError("NyaDB window did not appear")
            except BaseException:
                stop(process)
                raise

        def click(x: int, y: int) -> None:
            run("xdotool", "mousemove", "--window", window, str(x), str(y))
            run("xdotool", "click", "--clearmodifiers", "1")
            time.sleep(0.2)

        def keys(*sequence: str) -> None:
            run("xdotool", "key", "--clearmodifiers", "--delay", "100", *sequence)
            time.sleep(0.2)

        def preferences() -> dict[str, str]:
            with sqlite3.connect(db) as connection:
                return dict(connection.execute("SELECT key, value FROM app_preferences WHERE key IN ('appearance', 'color_scheme')"))

        def wait_for(scheme: str, mode: str, saved_mode: str | None = None):
            expected = tuple(bytes.fromhex(SIDEBARS[scheme][mode]))
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline:
                if app is not None and app.poll() is not None:
                    raise RuntimeError(f"app exited during {scheme} {mode}")
                frame = ImageGrab.grab().convert("RGB")
                pixel = frame.getpixel((geometry["X"] + 10, geometry["Y"] + 420))
                prefs = preferences()
                if pixel == expected and prefs.get("color_scheme", "classic") == scheme and prefs.get("appearance", "dark") == (saved_mode or mode):
                    return pixel
                time.sleep(0.25)
            screenshot("failure")
            raise RuntimeError(f"theme {scheme}/{saved_mode or mode}: expected {expected}, got {pixel}; preferences={prefs}")

        def theme_menu() -> None:
            click(104, 13)
            keys("End", "Right", "Home")

        def select_scheme(index: int) -> None:
            theme_menu()
            keys("Down", "Down", "Down", "Right", "Home", *(["Down"] * index), "Return")

        def select_mode(mode: str) -> None:
            theme_menu()
            keys(*(["Down"] * ["system", "light", "dark"].index(mode)), "Return")

        evidence = {"themes": {}}
        try:
            app = launch()
            screenshot("01-startup")
            click(196, 13)
            keys("End", "Return")
            time.sleep(0.5)
            for index, scheme in enumerate(SIDEBARS):
                select_scheme(index)
                for mode in ("dark", "light"):
                    select_mode(mode)
                    pixel = wait_for(scheme, mode)
                    screenshot(f"{scheme}-{mode}")
                    evidence["themes"][f"{scheme}-{mode}"] = pixel
                    print(f"PASS {scheme} {mode}: {pixel}", flush=True)

            # Exercise a real card click as well as the native menus.
            click(850, 400)
            wait_for("ocean", "light")
            select_mode("system")
            wait_for("ocean", "light", "system")
            screenshot("system-ocean-light")
            evidence["system_preferences"] = preferences()

            # GTK retains native styling while following the chosen mode.
            select_mode("dark")
            wait_for("ocean", "dark")
            click(196, 13)
            dark_menu = screenshot("native-menu-dark")
            dark_pixel = dark_menu.getpixel((geometry["X"] + 230, geometry["Y"] + 40))
            keys("Escape")
            select_mode("light")
            wait_for("ocean", "light")
            click(196, 13)
            light_menu = screenshot("native-menu-light")
            light_pixel = light_menu.getpixel((geometry["X"] + 230, geometry["Y"] + 40))
            keys("Escape")
            if sum(light_pixel) <= sum(dark_pixel) + 200:
                raise RuntimeError(f"GTK menus did not switch: {dark_pixel}, {light_pixel}")
            evidence["native_menu_pixels"] = {"dark": dark_pixel, "light": light_pixel}

            stop(app)
            app = launch()
            wait_for("ocean", "light")
            screenshot("restart-ocean-light")
            evidence["restored_preferences"] = preferences()
            evidence["binary_sha256"] = hashlib.sha256(binary.read_bytes()).hexdigest()
            evidence["git_head"] = run("git", "-C", str(ROOT), "rev-parse", "HEAD")
            source = hashlib.sha256()
            for path in sorted([*ROOT.glob("internal/**/*.go"), *ROOT.glob("cmd/**/*.go"), *ROOT.glob("locales/*.go"), ROOT / "go.mod", ROOT / "go.sum"]):
                source.update(str(path.relative_to(ROOT)).encode())
                source.update(path.read_bytes())
            evidence["source_sha256"] = source.hexdigest()
            (output / "verification.json").write_text(json.dumps(evidence, indent=2) + "\n")
            print("PASS all 10 variants, native menus, system mode, card selection and restart persistence", flush=True)
        finally:
            stop(app)
            stop(wm)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
