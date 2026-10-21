# NyaDB Implementation Status

## M0 — MyGo Native UI framework verification

Status: **M0 source implementation and headless/cross compilation checks passed. Native GUI smoke tests pending.**

### Scope

- Go module: `github.com/nyakang/nyadb`, Go 1.27.1+.
- MyGo pinned to v0.5.5 with its CLI declared as a module tool.
- Native window via `cmd/nyadb`, responsive sidebar, SQL editor, and synthetic DataGrid preview.
- SQL language selection, schema-outline mock, and headless UI tests.
- Cross-platform Go CI for Windows, Linux, and macOS.
- No database network access, secrets or persisted user data at M0.

### Verification

Go installed in Repo Code executor: `go1.27.3 linux/amd64`. Commands
were run with `CGO_ENABLED=0` and `GOPATH`, `GOMODCACHE`, `GOCACHE`,
`GOTMPDIR`, `GOENV` explicitly placed under `/data/.cache/golang`.

| Verification | Result |
|---|---|
| `go mod tidy` | PASS |
| `go test ./...` | PASS |
| `go vet ./...` | PASS |
| `go tool mygo vet` | PASS |
| Linux amd64 cross-build | PASS |
| Windows amd64 cross-build | PASS |
| macOS arm64 cross-build | PASS |
| `go tool mygo doctor` | PASS |
| `go tool mygo build` | PASS: Linux executable, .deb and .tar.gz archive |

Go builds validate platform compilation, not interactive UI operation.
Real Windows/Linux/macOS GUI smoke tests and CI job executions remain pending.

### M0 limitations

- Results are synthetic; no database queries are executed.
- The object tree is a mock schema. Database adapters start in M2.
- 100,000-row × 50-column preview is generated lazily in memory, not a DB paging benchmark.
- GUI input/rendering still needs real interactive Windows/Linux/macOS smoke tests.

## UI-1 / UI-2 — Navicat-inspired native UI layout

Status: **Implemented locally, headless UI interactions and compile checks completed**.

- Replaced the simple M0 shell with compact dark in-window menu row, icon toolbar,
  connection tree, object list, multi-tab SQL workspace, inspector and statusbar.
- Explorer, center pane and inspector use adjustable proportional splitters.
- Added a searchable mock connection tree, object filters, mock table browsing,
  independent SQL editor tabs, demo execution status, tab close, and inspector toggle.
- All UI graphics are new simple SVG icons; no third-party application icons.
- Saved a real MyGo headless preview at `docs/ui-preview.png`, generated on demand
  by `NYADB_WRITE_PREVIEW=1 go test ./internal/ui/shell -run TestGenerateUIPreview`.
- Detailed behavior and limitations in `docs/UI_LAYOUT.md`.
- Remaining: physical GUI smoke test on native operating systems, backend M1/M2.

See `docs/M1_PROFILE_STORAGE.md` for the initial M1 local-storage and profile CRUD slice.

Navicat 18 and NyaDB were both launched under isolated Linux Xvfb desktops
for first live GUI comparison. See `docs/NAVICAT_GUI_DIFFERENTIAL_2026-10-20.md`.

## M1-B — Native connection profile manager

Status: **Implemented and locally verified (I)**. NyaDB's real application
now opens a MyGo connection-type chooser and typed profile form, persists
non-secret MySQL/PostgreSQL/SQLite profiles in local SQLite, and shows them
in a searchable explorer with edit/copy/delete/refresh and inspector.
Isolated Xvfb tested profile creation, selection and restart persistence.
No database sessions or credentials are implemented (M2 pending).
Evidence, checks and limitations: `docs/M1_B_VERIFICATION.md`.

### Connection chooser fidelity iteration (2026-10-21)

Connection chooser now uses a native MyGo parent-owned modal child window
instead of an in-workspace overlay. Sidebar type/vendor rows have icons;
additional browsable database/cloud types and icon-only grid/list controls
are implemented. Supported profile kinds are unchanged. See
`docs/CONNECTION_CHOOSER_NATIVE_WINDOW_2026-10-21.md` for differential
screenshots, tests, security boundaries and remaining visual differences.

The connection chooser **list view** was subsequently rebuilt as a compact
expandable vendor tree after live Navicat 18 runtime observation. It includes
25-DIP rows (intentionally roomier than the observed 19px original), independent vendor expansion, full-row selection and two-pane
search filtering. See `docs/NAVICAT_LIST_TREE_DIFFERENTIAL_2026-10-21.md`.
