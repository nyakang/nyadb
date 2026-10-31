#!/usr/bin/env python3
"""Compose the ten rendered SQL workspaces and draft grids into one PNG.

Generate inputs with NYADB_THEME_CAPTURE and the theme visual matrix tests.
Requires Pillow; all tiles come from application renders, with no mock UI.
"""
from __future__ import annotations

import argparse
from pathlib import Path

from PIL import Image, ImageDraw


def main() -> None:
    root = Path(__file__).resolve().parents[1]
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--capture", type=Path, default=root / "temp/theme-optimization")
    parser.add_argument("--output", type=Path, default=root / "docs/theme-system-preview.png")
    args = parser.parse_args()
    families = ("classic", "ocean", "forest", "rose", "sand")
    width, sql_height, grid_height, gap, label = 840, 525, 140, 12, 26
    tile_height = label + sql_height + gap + grid_height
    canvas = Image.new("RGB", (2 * width + 3 * gap, 5 * tile_height + 6 * gap), "#e8ebef")
    draw = ImageDraw.Draw(canvas)
    for row, family in enumerate(families):
        for col, mode in enumerate(("light", "dark")):
            x, y = gap + col * (width + gap), gap + row * (tile_height + gap)
            draw.text((x + 6, y + 7), f"{family.title()} / {mode.title()}", fill="#202733")
            with Image.open(args.capture / f"{family}-{mode}-sql.png") as source:
                canvas.paste(source.convert("RGB").resize((width, sql_height), Image.Resampling.LANCZOS), (x, y + label))
            with Image.open(args.capture / f"{family}-{mode}-data.png") as source:
                # Keep native grid pixels: modified value, selected NULL, and
                # the literal string NULL are all visible in the first rows.
                canvas.paste(source.convert("RGB").crop((0, 0, width, grid_height)), (x, y + label + sql_height + gap))
    args.output.parent.mkdir(parents=True, exist_ok=True)
    canvas.save(args.output, optimize=True)
    print(args.output)


if __name__ == "__main__":
    main()
