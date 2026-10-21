# Provider icon assets and connection-chooser sidebar alignment

Date: 2026-10-21. Status: **I** (NyaDB independent implementation tested).

## Behavior

The left connection filter sidebar now uses a 230-DIP pane, 27-DIP
checkbox rows, 18-DIP icons, larger label spacing, and clearer separation
of engine and vendor sections. The two filter sections are still independently
scrollable as one sidebar content area, with the native chooser footer fixed.

Sidebar providers now use the **same order and visible names** as the vendor
groups in the right-hand tree. Internal identifiers remain stable (e.g.
`亚信星辰` internally, `平凯星辰` on screen). A filter for `达梦` now
correctly exposes the existing Dameng connection entry rather than returning
an empty list. Search/filter and expand/collapse behavior is unchanged.

## Provider badges

Fifteen checked-in 64×64 PNG badges are embedded into the normal Go binary
from `internal/assets/providericons/icons/` (not loaded from the working
directory). The corresponding `manifest.json` records each filename, source
and SHA-256. MyGo bitmap decoding is cached once, independently of the
database product image cache.

The following six provider marks are derived from the pinned
[Simple Icons](https://github.com/simple-icons/simple-icons) collection
(commit `c71c69e3a27e4436936b831c72643e6513b9f49b`):

- Alibaba Cloud
- Huawei
- Fujitsu
- Google Cloud
- MongoDB
- Redis

Source SVG copies are under `tools/provider-icon-sources/`. The upstream
[CC0 license copy](../tools/connection-icon-sources/SIMPLE_ICONS_LICENSE.md)
is already stored in this repository. Brand names and trademarks remain
subject to their respective owners' policies.

For Tencent Cloud, Dameng, KingbaseES, Pingkai, HighGo, OceanBase, AWS,
Microsoft and Oracle, this iteration uses **NyaDB-authored colored monograms**,
not claims of official or original brand logos. They are visibly distinct
from database product icons. This is a temporary artwork boundary; replace
only when appropriate brand source assets and reuse terms are established.

**No extracted Navicat PNGs or private application assets are shipped.**

To regenerate vendor PNGs from the committed SVG sources:

```sh
npm install --prefix /tmp/nyadb-icon-build --no-audit --no-fund @resvg/resvg-js@2.6.2
node tools/build_provider_icons.mjs
go test ./internal/assets/providericons ./internal/ui/shell
```

Maintainer-only `--fetch` downloads the listed upstream SVGs from the
pinned commit and must be reviewed before committing source changes.
The standard build, test, and application runtime do **not** need Node.

## Verification

- `go test ./...` PASS (provider manifest hash and image dimensions,
  provider-to-tree order, invalid icon paths, functional Dameng vendor filter,
  checkbox interactions and regression tests).
- `go vet ./...`, `go tool mygo vet`, `go tool mygo build`: PASS.
- Real NyaDB GUI in isolated Linux Xvfb/Openbox at 1600×960: verified
  left sidebar and right tree vendor badges, scrolling through the remaining
  provider filters, fixed header/footer, and `Gauss` filtering in both panes.

Ignored evidence retained in
`temp/research/gui-provider-sidebar-20261021/`:

- `sidebar-list.png`
- `sidebar-vendors-scrolled.png`
- `sidebar-gauss-filtered.png`

These are **I** implementation checks, not pixel-equality (**V**) or a
physical Windows/macOS desktop validation.
