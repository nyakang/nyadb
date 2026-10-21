# Lucide general-purpose UI icons

The NyaDB native interface uses Lucide SVGs for general actions, toolbar
buttons, database-object symbols, and menu/explorer icons.

- **Source:** [lucide-icons/lucide](https://github.com/lucide-icons/lucide)
- **Pinned upstream release:** `0.468.0`
- **License:** ISC, with Feather/MIT copyright notice as in the upstream file.
- **Attribution:** `internal/assets/lucide/LICENSE`.
- **SVGS:** `internal/assets/lucide/icons/*.svg`, compiled with Go `embed`.

The small checked-in subset is deterministic, works without a network
connection or node dependencies, and requires no WebView. MyGo parses SVGs
once at initialization and handles theme-dependent `currentColor`.

To refresh the pinned icons intentionally:

```sh
python3 tools/update_lucide_icons.py
go test ./internal/assets/lucide ./internal/ui/theme ./internal/ui/shell
```

Review the updated SVGs and LICENSE before committing. Build and runtime do
not invoke this tool.

## Scope

The semantic names used throughout the UI (e.g. `connection`, `query`,
`refresh`) are centralized in `internal/ui/theme/icons.go`. They map to
checked-in Lucide glyphs. Changing an icon style is a single mapping change
rather than modifications throughout the shell.

**Database brands remain separate:** `internal/assets/connectionicons`
contains the MySQL/PostgreSQL/SQLite/etc. branded PNGs. Independently
authored brand/type fallback geometry is retained for missing PNGs and
local-only comparison mode. Lucide does not replace brand identity icons.

Do not copy Navicat's proprietary resource artwork into NyaDB's shipping
icon bundle.

## Verification — 2026-10-21 (I)

The following checks passed on Linux amd64 using Go 1.27.3, MyGo 0.5.5:

- `go test ./...` (including Lucide resource/semantic icon tests and
  existing shell interaction tests)
- `go vet ./...`
- `go tool mygo vet`
- `go tool mygo build`

A real NyaDB window was launched under an isolated 1600×960 Xvfb/Openbox
desktop, using separate test HOME and XDG_CONFIG_HOME paths. The toolbar,
database explorer and connection chooser rendered with the new Lucide
operation symbols; database brand icon tiles stayed unchanged.
Screenshots are retained only as untracked research evidence in
`temp/research/gui-lucide-20261021/lucide-main-window.png` and
`lucide-connection-chooser.png`.

This is **I** evidence of local implementation and GUI rendering, not a
physical desktop smoke test or Navicat pixel-equivalence (**V**) claim.
