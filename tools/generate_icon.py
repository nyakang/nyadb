#!/usr/bin/env python3
"""Generate NyaDB's placeholder 1024px PNG without external image packages."""
import math
import struct
import zlib
from pathlib import Path

SIZE = 1024

def chunk(kind: bytes, data: bytes) -> bytes:
    return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data) & 0xffffffff)

def rgb(x: int, y: int):
    # Dark navy background with a blue rounded-rectangle tile.
    bg = (17, 24, 39)
    dx = max(abs(x - 511.5) - 350, 0)
    dy = max(abs(y - 511.5) - 350, 0)
    if dx * dx + dy * dy > 78 * 78:
        return bg
    color = (37, 99, 235)
    # Three curved database disks, modeled as horizontal white rings.
    nx = (x - 512) / 245
    if abs(nx) > 1:
        return color
    ellipse_y = 62 * math.sqrt(max(0, 1 - nx * nx))
    for mid in (367, 510, 653):
        if abs(y - (mid + ellipse_y)) < 11:
            return (232, 243, 255)
    if abs(abs(x - 512) - 245) < 11 and 367 <= y <= 655:
        return (232, 243, 255)
    return color

def main():
    payload = bytearray()
    for y in range(SIZE):
        payload.append(0)
        for x in range(SIZE):
            payload.extend(rgb(x, y))
    png = b"\x89PNG\r\n\x1a\n"
    png += chunk(b"IHDR", struct.pack(">2I5B", SIZE, SIZE, 8, 2, 0, 0, 0))
    png += chunk(b"IDAT", zlib.compress(payload, 9))
    png += chunk(b"IEND", b"")
    target = Path("resources/icon.png")
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(png)
    print(f"Wrote {target} ({len(png)} bytes)")

if __name__ == "__main__":
    main()
