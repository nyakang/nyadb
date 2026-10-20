#!/usr/bin/env python3
"""Read-only inventory and bounded extraction of PNGs embedded in an ELF.

Research output is deliberately stored only under temp/research. The originals
are never opened for writing. These images are proprietary study evidence and
must never be copied into NyaDB's distributable assets.
"""
import argparse
import collections
import csv
from pathlib import Path
import struct
import zlib

SIGNATURE = b"\x89PNG\r\n\x1a\n"
MAX_BYTES = 12 * 1024 * 1024
MAX_DIMENSION = 4096


def inspect_png(blob: bytes, pos: int):
    end_limit = min(len(blob), pos + MAX_BYTES)
    cursor = pos + len(SIGNATURE)
    dimensions = None
    seen_ihdr = False
    while cursor + 12 <= end_limit:
        size = struct.unpack_from(">I", blob, cursor)[0]
        kind = blob[cursor + 4:cursor + 8]
        after = cursor + 12 + size
        if size > MAX_BYTES or after > end_limit or not all(65 <= x <= 90 or 97 <= x <= 122 for x in kind):
            return None
        content = blob[cursor + 8:cursor + 8 + size]
        expected = struct.unpack_from(">I", blob, cursor + 8 + size)[0]
        if zlib.crc32(kind + content) & 0xffffffff != expected:
            return None
        if not seen_ihdr:
            if kind != b"IHDR" or size != 13:
                return None
            width, height = struct.unpack_from(">II", content)
            if not 0 < width <= MAX_DIMENSION or not 0 < height <= MAX_DIMENSION:
                return None
            dimensions = (width, height)
            seen_ihdr = True
        cursor = after
        if kind == b"IEND":
            return cursor, dimensions
    return None


def scan(source: Path, dest: Path, cap: int):
    binary = source.read_bytes()
    dest.mkdir(parents=True, exist_ok=True)
    records = []
    dimensions = collections.Counter()
    offset = 0
    extracted = 0
    while True:
        pos = binary.find(SIGNATURE, offset)
        if pos < 0:
            break
        offset = pos + len(SIGNATURE)
        parsed = inspect_png(binary, pos)
        if not parsed:
            continue
        end, (width, height) = parsed
        dimensions[(width, height)] += 1
        records.append((source.name, pos, end - pos, width, height))
        if extracted < cap and (width, height) in ((16, 16), (32, 32), (48, 48), (64, 64), (128, 128)):
            file = dest / f"{source.stem}-{pos:08x}-{width}x{height}.png"
            file.write_bytes(binary[pos:end])
            extracted += 1
    return records, dimensions, extracted


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True, nargs="+", type=Path)
    parser.add_argument("--out", default=Path("temp/research/navicat-png-inventory"), type=Path)
    parser.add_argument("--sample-limit", type=int, default=120)
    args = parser.parse_args()
    if not args.out.resolve().is_relative_to(Path("temp/research").resolve()):
        parser.error("output must be contained inside temp/research/")
    if not (0 <= args.sample_limit <= 512):
        parser.error("sample-limit must be within 0..512")
    all_rows = []
    for source in args.input:
        if not source.is_file():
            parser.error(f"missing ELF: {source}")
        rows, dims, extracted = scan(source, args.out / "samples", args.sample_limit)
        all_rows.extend(rows)
        print(f"{source}: validated PNGs={len(rows)}, extracted examples={extracted}")
        print("  most common dimensions:", dims.most_common(12))
    with (args.out / "inventory.csv").open("w", newline="", encoding="utf-8") as f:
        writer = csv.writer(f)
        writer.writerow(("elf", "offset_hex", "byte_length", "width", "height"))
        for name, start, length, w, h in all_rows:
            writer.writerow((name, hex(start), length, w, h))


if __name__ == "__main__":
    main()
