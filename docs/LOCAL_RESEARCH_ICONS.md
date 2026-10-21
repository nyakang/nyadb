# NyaDB — Local-only Navicat icon comparison mode

Status: **Implemented and tested (I)** on Linux Xvfb, 2026-10-21.
Purpose: local, permission-scoped study of Navicat 18's connection chooser.
Not a distributable feature and not part of the standard NyaDB product assets.

## Prepare research images

From the repository root, run the static extraction tool with your own
authorized Navicat 18 Linux files:

```sh
python3 tools/research_navicat_qt_resources.py
```

It writes validated PNGs and `mapping.csv` under the **ignored**
`temp/research/navicat-png-inventory/named/` directory. It does not
modify Navicat original binaries. The mapping includes 11 database types
and 9 cloud-provider variants (20 colored images); grey Off variants for
the primary 11 are retained for research, but the chooser currently
uses only the colorful On variants.

## Enable extracted PNGs in the local GUI

From the repository root:

```sh
NYADB_RESEARCH_ICONS=1 ./build/linux-amd64/nyadb
```

Or point the application to an absolute research directory if running
outside the repository:

```sh
NYADB_RESEARCH_ICONS=1 \
NYADB_RESEARCH_ICON_DIR=/absolute/path/to/nyadb/temp/research/navicat-png-inventory/named \
  ./build/linux-amd64/nyadb
```

The toggle is **explicitly opt-in**. Without `NYADB_RESEARCH_ICONS=1`,
NyaDB renders the **standard committed and embedded project PNGs**, with
NyaDB-authored SVG fallbacks (see `docs/ICON_ASSETS.md`). Extracted
Navicat images are not copied into `resources/` or compiled into releases.
Opt-in research PNGs are decoded once into cached MyGo `ui.Bitmap` objects.

Each approved file must be regular, at most 1 MiB, decodable as PNG,
and exactly 64×64. Missing or invalid files are skipped individually
with an independent SVG fallback. Invalid files report a warning to
stderr only when the user enabled research mode. Explicit filenames
are allowlisted, not constructed from arbitrary UI labels.

## Verification

- `go test ./...`: PASS, including valid/missing/corrupt/oversized/wrong-size
  PNGs, coverage of all 20 catalog names and the MyGo chooser workflow.
- `go vet ./...`, `go tool mygo vet`, `go tool mygo build`: PASS.
- Linux Xvfb/Openbox 1600×960: the native NyaDB process reported
  `NyaDB research icon mode: loaded 20 icons`, then displayed actual
  64×64 source images (scaled to their on-screen content size) in
  the chooser. Manual scrolling to the cloud group showed the mapped
  cloud logos with fixed footer/header and working scroll boundaries.
- Evidence screenshots:
  `temp/research/gui-nyadb-extracted-icons-20261021/chooser-research-20-types.png`
  and `chooser-research-cloud.png`. They remain ignored research evidence,
  not committed or distributed.
- This is real native GUI rendering under Xvfb, **not physical Windows or
  macOS GUI validation**, and not proof of full pixel parity with Navicat.

## Publication boundary

**Do not commit or publish any extracted PNG or bundle it with NyaDB.**
Only the opt-in loader, explicit resource mapping, tests and documentation
belong in Git. The `build/` output uses only NyaDB's ordinary application
resources and does not install a research icon directory. The original
Navicat app remains read-only. Connecting to any production database
was neither necessary nor attempted.

Original resource paths and exact file offsets are recorded in
`docs/NAVICAT_QT_ICON_MAPPING.md`; these offsets are valid only for
the observed Navicat 18 Linux ELF build.
