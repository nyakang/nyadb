#!/usr/bin/env python3
"""Capture settled native X11 client windows with measured geometry.

Attach only to an explicitly supplied research window, or launch the supplied
NyaDB binary in a disposable XDG configuration. No database fixtures, passwords,
or Navicat launch/license operations are built into this utility. Optional JSON
steps describe ordinary mouse/keyboard actions on that application's windows.
"""
from __future__ import annotations

import argparse
import ctypes
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import time
from datetime import datetime, timezone

from PIL import Image, ImageChops, ImageStat

from x11_display_metrics import display_metrics


class NativeCapture:
    def __init__(self, display: str, output: Path, source_path: Path | None = None,
                 app_name: str = "nyadb", actual_scale=None, font_family=None,
                 font_size_dip=None, measurement_evidence: list[str] | None = None):
        self.display = display
        self.output = output.resolve()
        self.output.mkdir(parents=True, exist_ok=True)
        self.env = dict(os.environ, DISPLAY=display)
        self.app_name = app_name
        self.actual_scale = actual_scale if actual_scale is not None else "unknown"
        self.font_family = font_family or "unknown"
        self.font_size_dip = font_size_dip if font_size_dip is not None else "unknown"
        self.measurement_evidence = measurement_evidence or []
        self.source_version = "unknown"
        if source_path:
            self.source_version = "sha256:" + hashlib.sha256(source_path.read_bytes()).hexdigest()
        self.metrics = display_metrics(display)
        self.actions: list[dict] = []
        self.expected_client_sizes: dict[int, tuple[int, int]] = {}
        self.x11 = ctypes.CDLL("libX11.so.6")
        self.x11.XOpenDisplay.argtypes = [ctypes.c_char_p]
        self.x11.XOpenDisplay.restype = ctypes.c_void_p
        self.x11.XDefaultRootWindow.argtypes = [ctypes.c_void_p]
        self.x11.XDefaultRootWindow.restype = ctypes.c_ulong
        self.x11.XGetGeometry.argtypes = [ctypes.c_void_p, ctypes.c_ulong,
                                         ctypes.POINTER(ctypes.c_ulong), ctypes.POINTER(ctypes.c_int),
                                         ctypes.POINTER(ctypes.c_int), ctypes.POINTER(ctypes.c_uint),
                                         ctypes.POINTER(ctypes.c_uint), ctypes.POINTER(ctypes.c_uint),
                                         ctypes.POINTER(ctypes.c_uint)]
        self.x11.XTranslateCoordinates.argtypes = [ctypes.c_void_p, ctypes.c_ulong, ctypes.c_ulong,
                                                  ctypes.c_int, ctypes.c_int, ctypes.POINTER(ctypes.c_int),
                                                  ctypes.POINTER(ctypes.c_int), ctypes.POINTER(ctypes.c_ulong)]
        self.x11.XCloseDisplay.argtypes = [ctypes.c_void_p]
        self.connection = self.x11.XOpenDisplay(display.encode())
        if not self.connection:
            raise RuntimeError(f"Cannot open display {display}")

    def close(self):
        if self.connection:
            self.x11.XCloseDisplay(self.connection)
            self.connection = None

    def run(self, *args: str) -> str:
        result = subprocess.run(args, env=self.env, capture_output=True, text=True,
                                check=True, timeout=10)
        return result.stdout.strip()

    def geometry(self, window: int) -> dict:
        root, child = ctypes.c_ulong(), ctypes.c_ulong()
        x, y, rx, ry = (ctypes.c_int() for _ in range(4))
        width, height, border, depth = (ctypes.c_uint() for _ in range(4))
        if not self.x11.XGetGeometry(self.connection, window, ctypes.byref(root), ctypes.byref(x),
                                    ctypes.byref(y), ctypes.byref(width), ctypes.byref(height),
                                    ctypes.byref(border), ctypes.byref(depth)):
            raise RuntimeError(f"Cannot read window geometry {window}")
        if not self.x11.XTranslateCoordinates(self.connection, window,
                                             self.x11.XDefaultRootWindow(self.connection), 0, 0,
                                             ctypes.byref(rx), ctypes.byref(ry), ctypes.byref(child)):
            raise RuntimeError(f"Cannot translate window {window} to root coordinates")
        return {"x": rx.value, "y": ry.value, "width": width.value, "height": height.value,
                "client_crop": [rx.value, ry.value, rx.value + width.value, ry.value + height.value],
                "measurement": "XGetGeometry + XTranslateCoordinates(client, root, 0, 0)"}

    def active_window(self) -> int:
        return int(self.run("xdotool", "getactivewindow"))

    def window_pid(self, window: int) -> int:
        return int(self.run("xdotool", "getwindowpid", str(window)))

    def wait_window(self, pattern: str, pid: int | None = None, timeout: float = 30) -> int:
        until = time.monotonic() + timeout
        while time.monotonic() < until:
            try:
                ids = self.run("xdotool", "search", "--onlyvisible", "--name", pattern).splitlines()
                for text in reversed(ids):
                    window = int(text)
                    if pid is None or self.window_pid(window) == pid:
                        return window
            except subprocess.CalledProcessError:
                pass
            time.sleep(0.25)
        raise RuntimeError(f"Window did not appear: {pattern}")

    def resize(self, window: int, width: int, height: int):
        requested_w, requested_h = width, height
        for _ in range(4):
            self.run("xdotool", "windowsize", str(window), str(requested_w), str(requested_h))
            # GTK/MyGo and the window manager can apply a second configure
            # event after initially reporting the requested outer dimensions.
            # Wait for several stable observations, not the first matching one.
            previous, stable = None, 0
            started = time.monotonic()
            while time.monotonic() - started < 3:
                time.sleep(0.35)
                geometry = self.geometry(window)
                size = (geometry["width"], geometry["height"])
                stable = stable + 1 if size == previous else 0
                previous = size
                if stable >= 3 and time.monotonic() - started >= 1.4:
                    break
            if stable >= 3 and (geometry["width"], geometry["height"]) == (width, height):
                self.expected_client_sizes[window] = (width, height)
                return
            requested_w += width - geometry["width"]
            requested_h += height - geometry["height"]
        raise RuntimeError(f"Window refuses requested {width}x{height} client size: {geometry}")

    def move_client(self, window: int, x: int, y: int):
        # xdotool reports/sets decorated coordinates inconsistently under some
        # window managers; reconcile against Xlib's actual client origin.
        requested_x, requested_y = x, y
        for _ in range(4):
            self.run("xdotool", "windowmove", str(window), str(requested_x), str(requested_y))
            time.sleep(0.25)
            actual = self.geometry(window)
            if (actual["x"], actual["y"]) == (x, y):
                return
            requested_x += x - actual["x"]
            requested_y += y - actual["y"]
        raise RuntimeError(f"Window refuses client position {x},{y}: {actual}")

    def click(self, window: int, x: int, y: int, button: int = 1):
        geometry = self.geometry(window)
        if not (0 <= x < geometry["width"] and 0 <= y < geometry["height"]):
            raise ValueError("Click is outside the target client window")
        self.run("xdotool", "mousemove", str(geometry["x"] + x), str(geometry["y"] + y))
        self.run("xdotool", "click", "--clearmodifiers", str(button))

    def key(self, *keys: str):
        self.run("xdotool", "key", "--clearmodifiers", *keys)

    def capture(self, name: str, window: int, scene: str, ui_state: dict,
                timeout: float = 35, locale: str = "zh-Hans", theme: str = "dark") -> dict:
        if not re.fullmatch(r"[A-Za-z0-9_-]+", name):
            raise ValueError("Capture name must contain only letters, digits, '_' and '-'")
        if ui_state.get("loading") is not False:
            raise ValueError("Capture requires an explicitly settled UI state")
        # Keep incidental hover/tooltips out of a default-state capture. Moving
        # the pointer does not alter keyboard focus or activate another window.
        self.run("xdotool", "mousemove", str(self.metrics["width_px"] - 1), str(self.metrics["height_px"] - 1))
        temporary = self.output / ".pending-frame.png"
        previous, stable, samples = None, 0, 0
        until = time.monotonic() + timeout
        started = time.monotonic()
        while time.monotonic() < until:
            geometry = self.geometry(window)
            expected = self.expected_client_sizes.get(window)
            if expected and (geometry["width"], geometry["height"]) != expected:
                # A first frame can arrive seconds after window creation. Honor
                # the requested client size even if that delayed configure came
                # after resize() settled; never label the smaller frame 1280x800.
                self.resize(window, *expected)
                previous, stable = None, 0
                continue
            self.run("scrot", "-z", "-o", str(temporary))
            with Image.open(temporary) as desktop:
                crop = geometry["client_crop"]
                if crop[0] < 0 or crop[1] < 0 or crop[2] > desktop.width or crop[3] > desktop.height:
                    raise RuntimeError(f"Target client extends beyond the captured desktop: {geometry}")
                client = desktop.convert("RGB").crop(tuple(crop))
            samples += 1
            black_fraction = sum(client.convert("L").histogram()[:4]) / (client.width * client.height)
            if black_fraction >= 0.995:
                stable = 0
                previous = None
            elif previous is not None and previous.size == client.size:
                delta = ImageChops.difference(client, previous)
                mae = sum(ImageStat.Stat(delta).mean) / 3
                stable = stable + 1 if mae <= 0.05 else 0
                if stable >= 3 and time.monotonic() - started >= 1.2:
                    screenshot = self.output / f"{name}.png"
                    shutil.copyfile(temporary, screenshot)
                    client.save(self.output / f"{name}-client.png")
                    observation = self.output / f"{name}.json"
                    record = {
                        "application": self.app_name, "scene": scene,
                        "source_version": self.source_version,
                        "captured_at_utc": datetime.now(timezone.utc).isoformat(),
                        "platform": "linux-x11", "locale": locale, "theme": theme,
                        "actual_scale": self.actual_scale, "font_family": self.font_family,
                        "font_size_dip": self.font_size_dip,
                        "dpi_x": self.metrics["dpi_x"], "dpi_y": self.metrics["dpi_y"],
                        "viewport_width": client.width, "viewport_height": client.height,
                        "client_crop": geometry["client_crop"], "window_id": window,
                        "client_geometry": geometry, "display_metrics": self.metrics,
                        "render_ready": True,
                        "render_stability": {"samples": samples, "consecutive_stable_comparisons": stable,
                                             "last_frame_rgb_mae": mae, "black_fraction": black_fraction},
                        "ui_state": ui_state,
                        "ui_state_source": "operator-observed state and recorded ordinary GUI actions",
                        "actions": list(self.actions), "screenshot": screenshot.name,
                        "screenshot_sha256": hashlib.sha256(screenshot.read_bytes()).hexdigest(),
                        "observation_evidence": [observation.name] + self.measurement_evidence,
                        "unknowns": [key for key, value in (("actual_scale", self.actual_scale),
                                                            ("font_family", self.font_family),
                                                            ("font_size_dip", self.font_size_dip))
                                     if value == "unknown"],
                    }
                    observation.write_text(json.dumps(record, ensure_ascii=False, indent=2) + "\n")
                    temporary.unlink(missing_ok=True)
                    print(f"CAPTURE {screenshot}", flush=True)
                    return record
            previous = client if black_fraction < 0.995 else None
            time.sleep(0.4)
        temporary.unlink(missing_ok=True)
        raise RuntimeError(f"No settled non-black frame after {timeout}s: {name}")


def stop_owned(process):
    if process is None or process.poll() is not None:
        return
    process.terminate()
    try:
        process.wait(timeout=4)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    target = parser.add_mutually_exclusive_group(required=True)
    target.add_argument("--binary", type=Path, help="Launch this NyaDB binary in disposable XDG configuration")
    target.add_argument("--window", type=int, help="Attach to this explicitly authorized research window")
    parser.add_argument("--display", help="Existing X11 research display; binary mode otherwise creates its own")
    parser.add_argument("--dpi", type=int, default=96, help="Xvfb reported DPI when creating a display")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--source-path", type=Path, help="Read-only executable used to identify an attached app")
    parser.add_argument("--app-name", default="nyadb")
    parser.add_argument("--locale", choices=("zh-Hans", "en"), default="zh-Hans")
    parser.add_argument("--steps", type=Path, help="JSON list: resize, move, click, key, text, window, capture")
    parser.add_argument("--actual-scale", type=float)
    parser.add_argument("--font-family")
    parser.add_argument("--font-size-dip", type=float)
    parser.add_argument("--measurement-evidence", type=Path, action="append", default=[])
    args = parser.parse_args()
    if args.window and not args.display:
        parser.error("--window requires an explicit --display")
    if args.window and not args.steps:
        parser.error("--window requires --steps describing the observed UI state")
    if args.dpi <= 0:
        parser.error("--dpi must be positive")
    if any(value is not None for value in (args.actual_scale, args.font_family, args.font_size_dip)) and not args.measurement_evidence:
        parser.error("Explicit scale/font values require --measurement-evidence; otherwise leave them Unknown")
    args.output.mkdir(parents=True, exist_ok=True)
    evidence = []
    for source in args.measurement_evidence:
        target_path = args.output / source.name
        if source.resolve() != target_path.resolve():
            shutil.copyfile(source, target_path)
        evidence.append(source.name)
    session = None
    app = wm = xvfb = log = None
    configuration = tempfile.TemporaryDirectory(prefix="nyadb-gui-capture-")
    try:
        temporary = configuration.name
        display = args.display
        if not display:
            reader, writer = os.pipe()
            xvfb = subprocess.Popen(["Xvfb", "-displayfd", str(writer), "-screen", "0", "1600x1000x24", "-dpi", str(args.dpi)],
                                    pass_fds=(writer,), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            os.close(writer)
            with os.fdopen(reader) as pipe:
                display = ":" + pipe.readline().strip()
            wm = subprocess.Popen(["openbox", "--sm-disable"], env=dict(os.environ, DISPLAY=display),
                                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        session = NativeCapture(display, args.output, args.binary or args.source_path, args.app_name,
                                args.actual_scale, args.font_family, args.font_size_dip, evidence)
        if args.binary:
            env = dict(os.environ, DISPLAY=display, XDG_CONFIG_HOME=str(Path(temporary) / "config"),
                       XDG_CACHE_HOME=str(Path(temporary) / "cache"))
            language = "zh_CN.UTF-8" if args.locale == "zh-Hans" else "en_US.UTF-8"
            env.update(LANG=language, LC_ALL=language)
            env.pop("NYADB_RESEARCH_ICONS", None)
            log = (args.output / "application.log").open("w")
            app = subprocess.Popen([str(args.binary.resolve())], env=env, stdout=log, stderr=log)
            session.source_version = "sha256:" + hashlib.sha256(Path(f"/proc/{app.pid}/exe").read_bytes()).hexdigest()
            window = session.wait_window("^NyaDB$", app.pid)
        else:
            window = args.window
        pid = session.window_pid(window)
        steps = json.loads(args.steps.read_text()) if args.steps else [
            {"action": "resize", "width": 1280, "height": 800},
            {"action": "move", "x": 160, "y": 100},
            {"action": "capture", "name": "main", "scene": "workbench",
             "ui_state": {"database_backed": False, "loading": False, "selection": [],
                          "expanded": [], "menu_state": "closed"}}]
        for step in steps:
            action = step["action"]
            if action == "window":
                window = session.wait_window(step["title"], pid)
                session.run("xdotool", "windowactivate", "--sync", str(window))
            elif action == "active_window":
                expected_title = step.get("title")
                deadline = time.monotonic() + 10
                while time.monotonic() < deadline:
                    selected = session.active_window()
                    if session.window_pid(selected) == pid:
                        observed_title = session.run("xdotool", "getwindowname", str(selected))
                        if not expected_title or re.search(expected_title, observed_title):
                            window = selected
                            break
                    time.sleep(0.2)
                else:
                    raise RuntimeError(f"No active application window matches: {expected_title!r}")
                session.run("xdotool", "windowactivate", "--sync", str(window))
            elif action == "resize":
                session.resize(window, step["width"], step["height"])
            elif action == "move":
                session.move_client(window, step["x"], step["y"])
            elif action == "click":
                session.click(window, step["x"], step["y"], step.get("button", 1))
            elif action == "key":
                if session.window_pid(session.active_window()) != pid:
                    raise RuntimeError("Keyboard focus moved outside the captured application")
                session.key(*step["keys"])
            elif action == "text":
                if session.window_pid(session.active_window()) != pid:
                    raise RuntimeError("Keyboard focus moved outside the captured application")
                session.run("xdotool", "type", "--clearmodifiers", "--", step["text"])
            elif action == "capture":
                session.capture(step["name"], window, step["scene"], step["ui_state"], locale=args.locale)
            else:
                raise ValueError(f"Unknown GUI step: {action}")
            session.actions.append(step)
    finally:
        stop_owned(app)
        if session:
            session.close()
        stop_owned(wm)
        stop_owned(xvfb)
        if log:
            log.close()
        configuration.cleanup()


if __name__ == "__main__":
    main()
