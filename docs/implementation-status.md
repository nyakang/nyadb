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

A subsequent sidebar iteration unified vendor filter ordering with the tree,
added a separate source-attributed provider badge pack, adjusted spacing, and
fixed the Dameng vendor checkbox. See `docs/PROVIDER_ICON_SIDEBAR_2026-10-21.md`.

### MySQL native connection editor (2026-10-21)

First UI reconstruction pass (I): original MySQL General tab observed in isolated
Linux Xvfb; NyaDB now displays native tabs, General fields, password masking,
and a fixed footer in a 900×700 editor window. Unimplemented tabs and connection
testing remain explicitly pending. Build/test checks and live GUI comparison:
`docs/MYSQL_CONNECTION_EDITOR_DIFFERENTIAL_2026-10-21.md`.

### MySQL advanced settings (2026-10-21)

The Advanced tab has native MyGo controls for driver, charset, timing,
compression, auto-connect, named pipe and session settings. Its non-secret
settings are validated and persisted with a backward-compatible local SQLite
schema migration (v1 to v2). Original behavior was observed in an isolated
Navicat session; driver execution is not implemented.
See `docs/MYSQL_ADVANCED_DIFFERENTIAL_2026-10-21.md`.

### MySQL compatibility and initial query (2026-10-21)

The Compatibility subtab now has native MyGo switches, dropdowns, exact
observed defaults and individually enabled overrides. Its validated settings
are persisted in the existing advanced JSON. The Initial Query overlay uses a
MyGo multiline editor with confirmation, cancellation and clipboard copying.
Because initialization SQL can contain credentials, its text is session-only,
not saved or executed. Details and original observation evidence:
`docs/MYSQL_COMPATIBILITY_INITIAL_QUERY_2026-10-21.md`.

### MySQL Database tab offline reconstruction (2026-10-21)

The Database tab now matches the observed custom database list design using a
native MyGo Table with Database and Auto Open columns. The custom-list toggle
is persisted; add/remove stay disabled until real MySQL metadata retrieval is
available. Original and NyaDB Linux GUI observations:
`docs/MYSQL_DATABASE_TAB_DIFFERENTIAL_2026-10-21.md`.

### MySQL SSL editor (2026-10-21)

The native MyGo SSL tab reproduces the original two-stage enablement for SSL,
verification, certificate paths, CA checking and an obscured key passphrase.
Only flags and certificate paths persist in the existing MySQL advanced JSON.
The passphrase is session-only and is cleared on close, cancel, save or back.
Real TLS handshakes remain unimplemented. Original UI screenshots, MyGo GUI
evidence and the unresolved behavior boundary are documented in
`docs/MYSQL_SSL_TAB_DIFFERENTIAL_2026-10-21.md`.

### MySQL SSH connection tab (2026-10-21)

The native MyGo SSH tab reproduces the observed tunnel toggle, SSH server
diagram, host/port/username inputs and three authentication modes: password,
public key, and password plus key. Non-secret metadata is validated and saved
inside the existing MySQL advanced JSON. Passwords and private-key passphrases
remain session-only and are cleared on editor lifecycle boundaries. The key
path browser uses a native file picker with stale-result protection. Real
SSH tunneling and host-key verification are not yet implemented. See
`docs/MYSQL_SSH_TAB_DIFFERENTIAL_2026-10-21.md`.

### MySQL HTTP tunnel tab (2026-10-21)

The MyGo-native HTTP tab now includes the original tunnel URL, Base64 query
setting, Authentication / Proxy subtabs, password and certificate sub-controls,
native certificate file browsing, and proxy defaults (port 8080). HTTP and SSH
tunnels are mutually exclusive. Non-secret settings persist in the MySQL
advanced JSON; authentication passwords and certificate passphrases remain
session-only. Actual HTTP transport, secure credentials storage and tunnel
script export are not implemented. Original and NyaDB evidence:
`docs/MYSQL_HTTP_TAB_DIFFERENTIAL_2026-10-21.md`.
