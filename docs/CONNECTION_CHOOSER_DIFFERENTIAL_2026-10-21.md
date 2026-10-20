# NyaDB connection chooser redesign — live GUI comparison

Date: 2026-10-21. Evidence classification: original application observation (O), ELF static evidence (S), NyaDB implementation/test observation (I); **no pixel-differential V claim**.

## Source of comparison

- Original GUI: `temp/research/gui-compare-20261020/navicat-new-connection-menu.png` and the user-provided October 21 chooser screenshot. The original chooser is approximately 900×720, with a left database/vendor filter list, search/view controls above an icon grid, recent/all/cloud sections, a scroll area and a bottom action row.
- Independent NyaDB screenshots (Linux Xvfb + Openbox 1600×960):
  `temp/research/gui-chooser-20261021/catalog-final-icons.png`,
  `catalog-list-mode.png`, `catalog-scrolled.png`, `catalog-fixed-grid.png`.
  These research screenshots are not application assets or committed deliverables.
- S: `docs/NAVICAT_ICON_INVENTORY.md` records validated PNG data found in the original executable. No extracted PNGs or Qt resources are used in the NyaDB UI.

## Implementation (I)

- Replaced the ~720px, 3-card modal with an approximately 890×678 native dialog (at 1280×800 application dimensions) using an independent two-pane structure.
- Left sidebar: a scrollable database-engine and cloud-provider checkbox filter list plus filter reset.
- Right: in-dialog search, grid/list view controls, recent selection history (session-local), common types when there is no history, all engine types and cloud database examples.
- Implemented scroll-clipped icon grid/list content with a persistent top toolbar and footer. At narrower app widths, icon columns reduce to four or three.
- Added distinct original geometric SVG symbols, locally authored in `internal/ui/theme/icons.go`; these are **not** third-party trademark logos or extracted Navicat icons.
- Supported connection types remain MySQL, PostgreSQL and SQLite. Other catalog entries explicitly say **尚未支持**; clicking one prevents proceeding and explains the absence of a driver. No real connections or authentication were added.
- The URI-import button is a visible placeholder which reports that URI import is unsupported. It does not parse or execute credentials.
- The existing next/back/cancel workflow and local non-secret profile persistence remain unchanged.

## Verification

| Check | Result |
|---|---|
| `go test ./...` | Passed, including chooser search/filter, supported and unsupported choice, grid/list, min 840×560 viewport and old CRUD tests |
| `go vet ./...` | Passed |
| `go tool mygo vet` | Passed |
| `go tool mygo build` | Passed for Linux amd64 |
| Xvfb GUI view | Opened chooser and captured real screenshot |
| Xvfb grid/list | Both layouts rendered; header/footer stayed visible |
| Xvfb right-pane scroll | Scrolled to cloud entries without spilling outside modal |
| Xvfb final symbol render | Newly authored symbols render in app |

The first GUI attempt exposed a real overflow bug: a vertically growing scroll container pushed content over the header/footer. Replacing implicit growth with a bounded body height, left/right scroll heights and a responsive column count resolved it. Do not treat that initial screenshot as a passing verification result.

## Remaining differences

- Native NyaDB typography, individual card spacing, buttons and layout geometry are **not pixel-identical** to Navicat.
- Database-specific symbols are generic newly authored vector signs rather than exact vendor-branded icons. No third-party logo licensing was assumed.
- Connection list and vendor catalog intentionally contain fewer entries; unsupported entries do not open fake connection forms.
- Recent list is truthful (session history) rather than showing hard-coded examples of operations the user has never performed.
- More responsive sizes and accessibility need physical Linux/Windows/macOS GUI validation. This run was a real window under Xvfb, not a physical desktop session.

This report does not assert Navicat source-code equivalence or claim to implement the original application's proprietary internals.
