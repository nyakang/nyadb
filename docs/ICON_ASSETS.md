# Connection selector icon assets

These icons are specifically for **database brand identities**; NyaDB's
general toolbar, explorer, and action symbols use pinned **Lucide SVGs**
in `internal/assets/lucide/` (see `docs/LUCIDE_ICONS.md`).

NyaDB ships connection-type icons as **ordinary embedded project assets**.
Normal use does not require a research directory, any environment variable,
a network connection, an external icon pack, or a post-installation setup.

## Repository layout

- `internal/assets/connectionicons/icons/*.png`: 20 committed 64×64 PNGs.
- `internal/assets/connectionicons/manifest.json`: exact catalog names,
  filenames, provenance, colors and SHA-256 checksums.
- `internal/assets/connectionicons/assets.go`: Go `//go:embed` filesystem.
- `internal/ui/theme/brand_icons.go`: shared single-decode MyGo bitmap caches.
- `tools/connection-icon-sources/*.svg`: source artwork used to render the
  PNG set, pinned to upstream revisions; upstream license copies are retained.
- `tools/build_connection_icons.mjs`: optional deterministic asset generator.
- `internal/assets/providericons/`: 15 separate, embedded provider filter badges,
  including provenance and SHA-256 manifest (see `docs/PROVIDER_ICON_SIDEBAR_2026-10-21.md`).

Normal builds embed all PNGs in the NyaDB executable. Changing the working
directory cannot cause the connection icons to disappear. Missing or invalid
icons in a corrupt build fall back to NyaDB's original SVG symbols.

## Artwork provenance and publication boundary

These **are not copied from Navicat's ELF or Qt resources**. NyaDB uses
independently authored geometry where needed, plus source art from:

- [Simple Icons](https://github.com/simple-icons/simple-icons), pinned at
  `c71c69e3a27e4436936b831c72643e6513b9f49b` — SVG collection published
  under CC0 1.0. The source copy is
  `tools/connection-icon-sources/SIMPLE_ICONS_LICENSE.md`. The project's
  [disclaimer](https://github.com/simple-icons/simple-icons/blob/develop/DISCLAIMER.md)
  notes that individual icons may carry separate terms.
- [Devicon](https://github.com/devicons/devicon), pinned at
  `6a37858047b0e8d9c9c0e68f4a071fc410652e89` — the standalone MySQL
  dolphin silhouette, from its MIT-licensed icon project. A copy of the
  MIT license is `tools/connection-icon-sources/DEVICON_LICENSE.txt`.
- NyaDB-original graphics for SQL Server, Oracle, Dameng, OceanBase,
  cloud-database connector variants, AWS and Azure representations.

The per-image `source` / `artwork` fields in `manifest.json` document each
file. Brand names, logos and trademarks belong to their respective owners.
These identification icons do not imply endorsement, affiliation, or a
license to use any vendor trademark beyond applicable law and brand policy.
Review vendor branding rules before commercial redistribution. No Navicat
private icon files are committed or included in release bundles.

## Rebuild the PNG assets (maintainer workflow)

The generated PNGs are already committed, so the steps below are **not**
needed to build or run NyaDB:

```sh
npm install --prefix /tmp/nyadb-icon-build --no-audit --no-fund @resvg/resvg-js@2.6.2
node tools/build_connection_icons.mjs
go test ./internal/assets/connectionicons ./internal/ui/theme ./internal/ui/connectioncatalog ./internal/ui/shell
```

To deliberately refresh the pinned Simple Icons SVG sources, execute
`node tools/build_connection_icons.mjs --fetch` with network access.
After reviewing updated images and provenance, commit the SVG source,
PNG and manifest changes together.

## Local-only Navicat visual study

For differential research, the optional
`NYADB_RESEARCH_ICONS=1` launcher setting overlays locally extracted Navicat
icons from the ignored `temp/research/` directory on top of these normal
project assets. This is **only for local comparison** and never part of the
Git-tracked product resources. Without that opt-in, ordinary bundled icons
are displayed on every OS.

Research procedure: `docs/LOCAL_RESEARCH_ICONS.md`.
