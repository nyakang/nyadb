#!/usr/bin/env python3
"""Read-only X11 screen geometry and physical DPI probe; no xdpyinfo dependency."""
import argparse
import ctypes
import ctypes.util
import json
import os


def display_metrics(display_name: str) -> dict:
    library = ctypes.util.find_library("X11")
    if not library:
        raise RuntimeError("libX11 is unavailable")
    x11 = ctypes.CDLL(library)
    x11.XOpenDisplay.argtypes = [ctypes.c_char_p]
    x11.XOpenDisplay.restype = ctypes.c_void_p
    x11.XDefaultScreen.argtypes = [ctypes.c_void_p]
    x11.XDefaultScreen.restype = ctypes.c_int
    for name in ("XDisplayWidth", "XDisplayHeight", "XDisplayWidthMM", "XDisplayHeightMM"):
        fn = getattr(x11, name)
        fn.argtypes = [ctypes.c_void_p, ctypes.c_int]
        fn.restype = ctypes.c_int
    x11.XCloseDisplay.argtypes = [ctypes.c_void_p]
    x11.XCloseDisplay.restype = ctypes.c_int
    display = x11.XOpenDisplay(display_name.encode())
    if not display:
        raise RuntimeError("cannot open X11 display " + display_name)
    try:
        screen = x11.XDefaultScreen(display)
        width = x11.XDisplayWidth(display, screen)
        height = x11.XDisplayHeight(display, screen)
        mm_w = x11.XDisplayWidthMM(display, screen)
        mm_h = x11.XDisplayHeightMM(display, screen)
        return {
            "display": display_name, "screen": screen,
            "width_px": width, "height_px": height,
            "width_mm": mm_w, "height_mm": mm_h,
            "dpi_x": round(width * 25.4 / mm_w, 3) if mm_w else None,
            "dpi_y": round(height * 25.4 / mm_h, 3) if mm_h else None,
            "measurement": "Xlib XDisplayWidthMM/HeightMM: reported virtual dimensions, not monitor calibration",
        }
    finally:
        x11.XCloseDisplay(display)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("display", nargs="?", default=os.environ.get("DISPLAY", ""))
    parser.add_argument("--output", help="Save JSON record at this path")
    args = parser.parse_args()
    data = display_metrics(args.display)
    payload = json.dumps(data, indent=2, ensure_ascii=False) + "\n"
    if args.output:
        with open(args.output, "w", encoding="utf-8") as f:
            f.write(payload)
    print(payload, end="")


if __name__ == "__main__":
    main()
