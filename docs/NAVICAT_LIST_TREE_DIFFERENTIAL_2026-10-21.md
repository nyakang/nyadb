# Connection chooser LIST differential: original Navicat runtime vs NyaDB

Date: 2026-10-21. Original Navicat 18 Linux x86-64 ran as a legitimately
available trial under a dedicated Xvfb display, using the *existing isolated*
research HOME `temp/research/gui-compare-20261020/navicat-home/`. No changes to
original ELF/AppImage and no database connections or license bypass attempted.

Evidence: **O** original GUI observation, **I** independent implementation
tested in NyaDB, **V** individually checked behavioral match, **Unknown**
not yet observed/compared.

## Original behaviors directly observed (O)

- Native chooser with title `新建连接`, 900×700 window frame in 1600×960
  Xvfb display (window geometry, not pixel-perfect content measurements).
- Toggling the top-right **list** icon does **not** create a vertical stack of
  grid cards. It switches to a compact **tree**:
  - `最近使用过的`: actual previously used items (SQLite in the observed
    isolated profile). It does not invent a recent item.
  - `全部`: nine leaf database types (MySQL, PostgreSQL, SQL Server,
    Oracle, SQLite, MariaDB, MongoDB, Redis and Snowflake), then
    vendor/managed-service expandable nodes in a stable order.
  - Arrow controls by vendor; clicking Huawei's arrow expands its children,
    independently of the other vendors.
- Measured from screenshot: row height roughly 19 px with 16 px icons; leaf
  icons and text left-aligned, children indented. No boxed row cards, no
  separate `云数据库` heading and no per-row `尚未支持` text.
- Clicking a nested Huawei GaussDB entry selects a **full-width blue row**.
  The original's Next button becomes enabled (NyaDB does not simulate a
  driver for the unsupported database).
- Searching `Gauss` filters **both panes**: the left sidebar retains Huawei
  under vendor filters, and the right tree shows `全部` → expanded Huawei
  with four matching GaussDB/openGauss rows. Search does not require manually
  expanding a collapsed vendor.

## NyaDB behavior implemented (I)

- `connection_catalog_tree.go`: dedicated tree builder for list mode,
  separate from grid view; 25-DIP rows for NyaDB readability (original ~19px); nine core database leaves; ordered
  provider nodes; independently tracked expand/collapse flags; automatic
  temporary expansion for nonempty queries; nested icon/label indentation.
- List rows highlight their full width on selection. The supported profile
  flow remains restricted to MySQL/PostgreSQL/SQLite; other entries retain
  browse-only behavior and report unsupported drivers.
- `catalogSidebarFilterVisible` narrows visible engine/provider options with
  the search query, as observed for `Gauss`.
- The engine checkbox list is restricted to the original nine top-level
  engine categories; extra server/cloud types are not falsely promoted to
  a top-level checkbox category.
- Stored NyaDB bundled PNGs continue to render; no proprietary Navicat
  images were embedded in the application. Local-only research icon loading
  remains opt-in.

## Live differential evidence

Original screenshots, ignored under:
`temp/research/navicat-list-runtime-20261021/`

- `navicat-list-initial.png` — compact provider tree, collapsed
- `navicat-list-huawei-expanded.png` — single vendor expanded
- `navicat-list-selected-leaf.png` — blue full-row selection
- `navicat-list-search-gauss.png` — two-pane search filtering

NyaDB screenshots, ignored under:
`temp/research/nyadb-list-runtime-20261021/`

- `nyadb-list-left-aligned.png` — compact rows and provider tree
- `nyadb-list-selected-gauss.png` — full-width blue selected row
- `nyadb-list-gauss-final.png` — final left/right `Gauss` filtering

Both applications were actually launched under independent Xvfb/Openbox
displays; tests do not substitute for these screenshots.

## Verification

- `go test ./...` PASS, including UI-tester click/expand/collapse,
  selection, grouping and two-pane query filtering.
- `go vet ./...` PASS
- `go tool mygo vet` PASS
- `go tool mygo build` PASS
- Live GUI native tree, scroll, search and row selection observed.

## Remaining differences (Unknown/V not claimed)

- NyaDB's dark palette, search input layout, native titlebar frame,
  checkbox styling, sidebar text and precise spacing differ from Navicat.
- Research HOME contains actual recent SQLite history; fresh NyaDB HOME
  contains no recent types, so the first-list vertical offsets differ.
- NyaDB's provider logos are independently sourced or locally drawn. Several
  additional cloud/catalog entries have only generic fallback symbols.
- Original next-step behavior for unsupported providers cannot be claimed
  implemented without database-specific drivers. No production/test
  database sessions were connected.
- Accessibility semantics, arrow-key navigation, selection persistence across
  sessions and physical OS cross-platform GUI parity remain unverified.

The compact tree structure, provider expansion, row selection and search
filtering were checked against live original evidence (O) and independently
implemented/tested (I). This is **not** a global pixel-identity claim.
