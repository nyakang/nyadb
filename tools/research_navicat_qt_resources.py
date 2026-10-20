#!/usr/bin/env python3
"""Resolve Qt resource *names* to validated embedded PNG offsets in Navicat ELF.

This is a static research utility for the inspected Navicat 18 Linux build,
not a generic Qt RCC parser. All writes are restricted to temp/research.
No proprietary image may be redistributed as an NyaDB application resource.
"""
import argparse
import csv
from pathlib import Path
import struct

from research_navicat_png import SIGNATURE, inspect_png

# Determined from the Qt resource name/tree and PNG framing of the provided
# Linux executable. These addresses are build-specific; failures are explicit.
NAME_BASE = 0xD0EB20
DATA_BASE = 0xD43180

DRIVERS = (
    ("MySQL", "MySQL"),
    ("PostgreSQL", "PostgreSQL"),
    ("SQL Server", "SQLserver"),
    ("Oracle", "Oracle"),
    ("SQLite", "SQLite"),
    ("MariaDB", "MariaDB"),
    ("MongoDB", "MongoDB"),
    ("Redis", "Redis"),
    ("Snowflake", "Snowflake"),
    ("Dameng", "Dameng"),
    ("OceanBase", "OceanBase"),
)

# Additional resource paths identified in the same Qt name/tree structures.
# Original spelling (including SQLsever) is preserved for exact matching.
CLOUD_RESOURCES = (
    ("阿里云 RDS MySQL 版", "OV_64_Alibaba_Cloud_MySQL_On.png"),
    ("阿里云 RDS PostgreSQL 版", "OV_64_Alibaba_Cloud_PostgreSQL_On.png"),
    ("阿里云 RDS SQL Server 版", "OV_64_Alibaba_Cloud_SQLsever_On.png"),
    ("阿里云 MongoDB 版", "OV_64_Alibaba_Cloud_MongoDB_On.png"),
    ("阿里云 Redis 版", "OV_64_Alibaba_Cloud_Redis_On.png"),
    ("腾讯云 MySQL", "OV_64_Tencent_Cloud_MySQL_On.png"),
    ("华为云 PostgreSQL", "OV_64_Huawei_Cloud_PostgreSQL_On.png"),
    ("AWS RDS MySQL", "OV_64_AmazonRDS_MySQL_On.png"),
    ("Azure SQL", "OV_64_Microsoft_Azure_SQLserver_On.png"),
)

def decode_resource(binary: bytes, resource_name: str):
    needle = resource_name.encode("utf-16-be")
    pos = binary.find(needle)
    if pos < 6 or binary.find(needle, pos + 1) != -1:
        raise ValueError(f"Qt name not uniquely present: {resource_name}")
    record_pos = pos - 6
    declared_len = struct.unpack_from(">H", binary, record_pos)[0]
    if declared_len != len(resource_name):
        raise ValueError(f"Qt name length mismatch: {resource_name}")
    tree_name_off = record_pos - NAME_BASE
    tree_key = struct.pack(">I", tree_name_off)
    matches = []
    pos = 0
    while True:
        tree_pos = binary.find(tree_key, pos)
        if tree_pos < 0:
            break
        pos = tree_pos + 4
        if binary[tree_pos+4:tree_pos+6] != b"\x00\x00":
            continue
        data_relative = struct.unpack_from(">I", binary, tree_pos + 10)[0]
        payload_offset = DATA_BASE + data_relative
        if payload_offset + 4 > len(binary):
            continue
        length = struct.unpack_from(">I", binary, payload_offset)[0]
        png_offset = payload_offset + 4
        png_end = png_offset + length
        if length > 12*1024*1024 or png_end > len(binary) or binary[png_offset:png_offset+8] != SIGNATURE:
            continue
        parsed = inspect_png(binary, png_offset)
        if parsed and parsed[0] == png_end:
            matches.append((tree_pos, png_offset, png_end, length, parsed[1]))
    if len(matches) != 1:
        raise ValueError(f"expected exactly one valid PNG for {resource_name}, got {len(matches)}")
    tree_pos, png_offset, png_end, length, (width, height) = matches[0]
    return {
        "resource": ":/images/" + resource_name,
        "tree_offset": f"0x{tree_pos:x}",
        "png_offset": f"0x{png_offset:x}",
        "png_length": str(length),
        "dimensions": f"{width}x{height}",
        "png_bytes": binary[png_offset:png_end],
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--elf", type=Path, default=Path("temp/navicat/squashfs-root/usr/bin/navicat"))
    parser.add_argument("--out", type=Path, default=Path("temp/research/navicat-png-inventory/named"))
    args = parser.parse_args()
    if not args.out.resolve().is_relative_to(Path("temp/research").resolve()):
        parser.error("must write beneath temp/research")
    data = args.elf.read_bytes()
    if data[NAME_BASE:NAME_BASE+18] != (b"\x00\x06\x07\x03\x7d\xc3" + "images".encode("utf-16-be")):
        parser.error("this is not the known Qt resource name layout; rebuild-specific addresses needed")
    args.out.mkdir(parents=True, exist_ok=True)
    output_rows = []
    for display, ident in DRIVERS:
        for variant in ("On", "Off"):
            resource = f"OV_64_{ident}_{variant}.png"
            info = decode_resource(data, resource)
            dst = args.out / resource
            dst.write_bytes(info.pop("png_bytes"))
            output_rows.append({"type": display, "state": variant, **info})
            print(display, variant, info["png_offset"], info["dimensions"], resource)
    for display, resource in CLOUD_RESOURCES:
        info = decode_resource(data, resource)
        dst = args.out / resource
        dst.write_bytes(info.pop("png_bytes"))
        output_rows.append({"type": display, "state": "On", **info})
        print(display, "On", info["png_offset"], info["dimensions"], resource)
    with (args.out / "mapping.csv").open("w", newline="", encoding="utf-8") as f:
        w=csv.DictWriter(f, fieldnames=["type","state","resource","tree_offset","png_offset","png_length","dimensions"])
        w.writeheader()
        w.writerows(output_rows)
    # This local-only visual index is not distributable as an app asset.
    try:
        from PIL import Image, ImageDraw
    except ImportError:
        print("Pillow not available; mapping.csv and named PNG evidence are complete")
        return
    sheet = Image.new("RGB", (690, 70 * len(DRIVERS) + 48), (42, 42, 42))
    draw = ImageDraw.Draw(sheet)
    draw.text((16, 15), "Qt resource name and PNG offsets - Navicat research only", fill="white")
    for index, (display, ident) in enumerate(DRIVERS):
        y = 48 + index * 70
        draw.text((16, y + 19), display, fill="white")
        for j, state in enumerate(("On", "Off")):
            x = 195 + 250 * j
            path = args.out / f"OV_64_{ident}_{state}.png"
            icon = Image.open(path).convert("RGBA")
            sheet.paste(icon, (x, y), icon)
            row = output_rows[index * 2 + j]
            draw.text((x + 70, y + 12), state, fill="white")
            draw.text((x + 70, y + 30), row["png_offset"], fill=(185, 185, 185))
    preview = args.out / "resource-map-preview.png"
    sheet.save(preview)
    print("Research preview:", preview)

if __name__ == "__main__":
    main()
