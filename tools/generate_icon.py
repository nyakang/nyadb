#!/usr/bin/env python3
"""Render the committed NyaDB SVG as its transparent 1024px application icon.

Requires ImageMagick 7 (magick) only when regenerating the asset. Ordinary
Go/MyGo builds use the committed PNG and do not run this script.
"""
import shutil
import subprocess
import tempfile
from pathlib import Path

SIZE = 1024
ARTWORK_SIZE = 840
ROOT = Path(__file__).resolve().parents[1]
ASSETS = ROOT / "internal" / "assets" / "appicon"


def main():
    magick = shutil.which("magick")
    if magick is None:
        raise SystemExit("ImageMagick 7 (magick) is required to regenerate the icon")

    # Render the SVG at four times its declared size before trimming and
    # downsampling. Keep transparency, original colors and aspect ratio.
    result = subprocess.run(
        [
            magick, "-background", "none", "-density", "288",
            str(ASSETS / "icon.svg"), "-trim", "+repage",
            "-filter", "Lanczos", "-resize", f"{ARTWORK_SIZE}x{ARTWORK_SIZE}",
            "-gravity", "center", "-extent", f"{SIZE}x{SIZE}",
            "-depth", "8", "-strip", "PNG32:-",
        ],
        check=True,
        stdout=subprocess.PIPE,
    )
    target = ASSETS / "icon.png"
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(dir=ASSETS, suffix=".tmp", delete=False) as output:
            temporary = Path(output.name)
            output.write(result.stdout)
        temporary.chmod(0o644)
        temporary.replace(target)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)
    print(f"Wrote {target.relative_to(ROOT)} ({len(result.stdout)} bytes)")


if __name__ == "__main__":
    main()
