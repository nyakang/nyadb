#!/usr/bin/env python3
"""Update pinned Lucide SVG assets (maintainer-only, network required).

Regular NyaDB builds never run this script; SVGs are stored in Git.
Verify upstream license and diffs before changing the pinned version.
"""
from pathlib import Path
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parents[1]
ICONS = ROOT / "internal/assets/lucide/icons"
VERSION = "0.468.0"
NAMES = (
    "plug-zap", "file-code-2", "table-2", "eye", "square-function",
    "user-round", "wrench", "database-backup", "calendar-clock",
    "workflow", "info", "folder", "database", "plus", "x", "refresh-cw",
    "layout-grid", "list", "list-filter", "building-2",
    "bot", "binoculars", "folder-plus", "folder-open", "pencil", "copy",
    "trash-2", "file-plus-2", "file-down", "file-up", "table-properties",
)
BASE = f"https://raw.githubusercontent.com/lucide-icons/lucide/{VERSION}"

def main():
    ICONS.mkdir(parents=True, exist_ok=True)
    for name in NAMES:
        with urlopen(f"{BASE}/icons/{name}.svg", timeout=15) as response:
            data = response.read()
        if b"<svg" not in data or b"currentColor" not in data:
            raise ValueError(f"unexpected upstream SVG: {name}")
        (ICONS / f"{name}.svg").write_bytes(data)
        print(f"Updated {name}.svg")
    with urlopen(f"{BASE}/LICENSE", timeout=15) as response:
        (ICONS.parent / "LICENSE").write_bytes(response.read())
    print(f"Lucide {VERSION}: {len(NAMES)} assets and upstream license")

if __name__ == "__main__":
    main()
