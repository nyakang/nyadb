# Navicat ELF icon inventory — static research (S)

Date: 2026-10-21. Source files were examined **read-only**:
- `temp/navicat/squashfs-root/usr/bin/navicat`
- `temp/navicat/squashfs-root/usr/lib/libcc.so`

The script `tools/research_navicat_png.py` validates PNG signatures, bounded chunk framing, dimensions and CRCs before extracting samples. Output is restricted to ignored `temp/research/navicat-png-inventory/`: `inventory.csv` lists offsets and pixel sizes, `samples/` contains a bounded set of examples. No Navicat art is placed in distributable `resources/`, `internal/`, `docs/`, or Git history.

| ELF | Validated PNG images | Extracted bounded sample |
|---|---:|---:|
| `usr/bin/navicat` | 3,960 | 96 |
| `usr/lib/libcc.so` | 42 | 32 |

Most common validated dimensions in the main executable: 32×32 (1,360), 16×16 (1,086), 64×64 (605), 128×128 (329). A prior simple signature count of 3,962 for the executable included two candidates failing complete validation. The extraction script uses original ELF offsets, not resource names. **No image-to-database-type attribution has been proved** yet; no icons are included in NyaDB.

Original screenshot observation (O) of the database chooser: a roughly 900×720 window with sidebar filters, search, recent items, multiple icon sections, right-side scrolling and a bottom action bar. The separate MyGo design should recreate layout and behavior using original symbols or openly licensed brand marks, not reuse extracted pixel artwork. Unsupported connection types must not suggest a working driver.
