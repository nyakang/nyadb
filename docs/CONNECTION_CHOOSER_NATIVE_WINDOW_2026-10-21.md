# Connection chooser: native-window and catalog differential

Date: 2026-10-21 (Linux x86-64, MyGo v0.5.5 / Go 1.27.3).
Evidence notation: **O** = original GUI observed, **I** = NyaDB implemented and tested,
**V** = matched behavior differential verified, **Unknown** = untested.

## Scope and results

| Requested change | Original evidence (O) | NyaDB implementation and check (I) |
|---|---|---|
| A dialog-like **native window**, not an in-app overlay | Navicat shows a decorated standalone connection window with system close control, parent window behind it | MyGo `NewWindow` with `Parent`, `Modal:true`, `902×732` content size; verified in live Linux Xvfb with Openbox title bar and window geometry |
| Category names **with icons** | Left database/vendor filter list contains a small icon alongside checkbox and label | Rows now render small embedded database/provider icons where available, otherwise Lucide/native fallback icons; separate filter and vendor heading icons |
| Extended **choice catalog** | Screenshot shows many cloud/platform choices beyond MySQL/PostgreSQL/SQLite | Extended browse-only catalog in `connection_catalog_extended.go`; engine/vendor filtering and search tested |
| Grid/list as **icons**, not text buttons | Top-right square-grid and row-list controls | Pinned Lucide `layout-grid` and `list`, with selected-state background; visual and MyGo tester interaction validated |

The native window is **not** a WebView or a mocked title bar. A fallback
`ui.Modal` remains exclusively for headless MyGo `ui.NewTester` callers
with no registered host. In a normal application, the overlay is suppressed
and the parent-owned window is used. The native child closes through the
window footer, profile save, or OS titlebar; when closed it clears temporary
session-secret state. Editing an existing profile also opens the native
editor, with an Edit title.

## Catalog implementation boundary

Supported NyaDB connection profile types remain only MySQL,
PostgreSQL and SQLite. Every additional server/vendor item is browse-only,
explicitly refused before entering a connection editor. Choosing one
preserves the visible selected-card state, while the footer reports
that its driver is not yet supported.

Base built-in database icons and 20 approved existing provider assets remain
bundled in `internal/assets/connectionicons/`. More catalog entries use
NyaDB-original fallback SVGs where separately sourced brand images are not
yet available. **No Navicat proprietary bitmap is shipped**.

Recent items are based on actual selection history, not fabricated.
When no selection history exists, NyaDB displays **常用类型** rather than
invented entries under **最近使用过的**.

## Verification evidence

- `go test ./...`: PASS, including native-window lifecycle view,
  extended choices, duplicate-name checks, cloud/vendor filters, unsupported
  driver rejection and icon-only list/grid interactions
- `go vet ./...`: PASS
- `go tool mygo vet`: PASS
- `go tool mygo build`: PASS (Linux amd64)
- Real Xvfb/Openbox GUI (1600×960) at `:113`: the chooser launched as a
  separate system window, and `xdotool` reported content geometry
  `WIDTH=902 HEIGHT=732` and title `新建连接`.
  Grid/list views both rendered, the chooser scrolled through the longer
  cloud list with fixed toolbar/footer, and choosing MySQL / Next displayed
  the native connection editing form.
- Private research screenshots:
  `temp/research/gui-native-chooser-20261021/native-window-ready.png`,
  `native-list-mode.png`, `native-catalog-scrolled.png`,
  `native-mysql-editor.png`. These are ignored evidence, not product assets.

## Remaining difference / not verified

- **Not pixel V:** Windows/Linux/macOS titlebar decorations differ by OS;
  selected-card colors, text truncation, exact measurements, search focus and
  some brand assets still differ from Navicat.
- The original's large catalog has vendor-specific images for more items
  than NyaDB currently has approved brand resources.
- Recent type selection is session-local and only reflects choices actually
  made; cross-restart recency persistence is not implemented.
- No real database session establishment, credential storage, cloud service
  configuration, URI import or database query execution was tested or added.
- Physical native Windows/macOS/Linux desktop validation remains pending;
  Xvfb confirms an actual native Linux window, not cross-platform parity.

This stage is **I** evidence of a real native UI implementation and tested
subset of behaviors. Original observation is **O**; no broad equivalence
claim (V) is made.
