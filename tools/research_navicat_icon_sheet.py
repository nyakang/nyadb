#!/usr/bin/env python3
"""Build read-only visual index sheets for validated embedded PNGs.

This tool is for manual attribution: source offsets are stable within this
original ELF, not resource names. Never use research images as shipped assets.
"""
import argparse
import csv
from hashlib import sha256
from io import BytesIO
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--inventory", type=Path, default=Path("temp/research/navicat-png-inventory/inventory.csv"))
    ap.add_argument("--elf", type=Path, default=Path("temp/navicat/squashfs-root/usr/bin/navicat"))
    ap.add_argument("--dimensions", default="64,128,50,100,48,80")
    ap.add_argument("--per-page", type=int, default=70)
    ap.add_argument("--out", type=Path, default=Path("temp/research/navicat-png-inventory/contact"))
    args = ap.parse_args()
    if not args.out.resolve().is_relative_to(Path("temp/research").resolve()):
        ap.error("contact sheets must be under temp/research/")
    args.out.mkdir(parents=True, exist_ok=True)
    dims = set(map(int, args.dimensions.split(",")))
    rows = [r for r in csv.DictReader(args.inventory.open()) if r["elf"] == args.elf.name and int(r["width"]) == int(r["height"]) and int(r["width"]) in dims]
    data = args.elf.read_bytes()
    seen = set()
    unique = []
    for r in rows:
        start = int(r["offset_hex"], 16)
        content = data[start:start+int(r["byte_length"])]
        h = sha256(content).hexdigest()
        if h in seen:
            continue
        seen.add(h)
        unique.append((r, content, h))
    print("filtered", len(rows), "unique-byte-images", len(unique), "dims", args.dimensions)
    metadata = []
    columns = 10
    cellw, cellh = 110, 106
    rows_per_page = (args.per_page + columns - 1)//columns
    for page_start in range(0, len(unique), args.per_page):
        sheet = Image.new("RGB", (columns*cellw, rows_per_page*cellh), (47,47,47))
        draw = ImageDraw.Draw(sheet)
        batch = unique[page_start:page_start+args.per_page]
        for idx, (r, content, h) in enumerate(batch):
            global_index = page_start + idx
            x, y = idx%columns*cellw, idx//columns*cellh
            draw.rectangle((x+4,y+4,x+cellw-4,y+cellh-4), fill=(66,66,66), outline=(107,107,107))
            try:
                im = Image.open(BytesIO(content)).convert("RGBA")
                im.thumbnail((72,72), Image.Resampling.LANCZOS)
                sheet.paste(im, (x+(cellw-im.width)//2, y+5+(72-im.height)//2), im)
            except Exception as e:
                print("decode failed",r["offset_hex"],repr(e))
            label = f'{global_index:04d} {r["offset_hex"]}'
            draw.text((x+4,y+79), label, fill="white")
            draw.text((x+4,y+92), f'{r["width"]}x{r["height"]}', fill=(200,200,200))
            metadata.append({"index":global_index, "offset_hex":r["offset_hex"],"size":r["width"],"sha256":h})
        path=args.out / f"sheet-{page_start//args.per_page:02d}.png"
        sheet.save(path)
        print(path)
    with (args.out/"index.csv").open("w",newline="") as f:
        writer=csv.DictWriter(f,fieldnames=["index","offset_hex","size","sha256"]);writer.writeheader();writer.writerows(metadata)

if __name__ == "__main__":
    main()
