# NyaDB Implementation Status

## Current snapshot — 2026-10-25

The current baseline is **Go 1.27.1+ and MyGo `v0.6.3-nyadb.2`**. `go.mod`
requires upstream `github.com/egoist/mygo v0.6.3` and replaces it with the
NyaDB fork. This UI alignment iteration does not change the driver interface,
profile format, or SQLite schema.

| Area | Current implementation |
|---|---|
| Profiles | Local MySQL, PostgreSQL, and SQLite profile CRUD, grouping, stars, search, and non-secret persistence. Selecting a profile does not connect. |
| MySQL sessions | Explicit asynchronous connect/cancel/disconnect and connection testing; direct TCP and verified TLS; stale-result protection and credential reentry on reconnect. Unsupported connection settings fail before dialing. |
| MySQL objects | Real database and table/view metadata, lazy loading, refresh/retry, and a unified native virtual tree using stable group/profile/database/category/object identities. |
| MySQL structure | Real read-only fields, indexes, foreign keys, and field properties. “Design Table” opens this viewer; it does not edit schema. |
| MySQL data | Real read-only table/view rows, virtualized cells, bounded paging, supported keyset traversal, server-side filtering/sorting, and large-field chunk previews. NULL, empty strings, and truncated values remain distinct. |
| SQL editor | Multi-tab editing and syntax highlighting; execution and result previews are explicitly simulated and do not run user SQL against the database. |
| PostgreSQL / SQLite | Local profiles and their existing forms only; no managed sessions or live database browser. The application's own SQLite configuration storage is separate. |
| Unimplemented capabilities | Arbitrary SQL execution, data/schema writes, import/export, SSH/HTTP tunnels, and additional database drivers. Unimplemented commands are disabled. |

The current Linux alignment target is Navicat 18, Simplified Chinese, dark
theme, and compact density. Shared theme metrics now provide 19-DIP navigation
and chooser list rows with 16-DIP icons, low-emphasis toolbar selection, and
initial navigation/inspector content widths of 250/200 DIP shared across workspace
pages. The calibrated dark workspace/panel fill is `#232629`, and toolbar,
status bar, and connection-editor surfaces use `#31363b`.

The chooser and MySQL editor use a 900×700 reference client area. General and
Advanced use a centered 604-DIP form; SSL, SSH, and HTTP use the available
client width minus 40 DIP (860 DIP at the reference size). These forms share
a 166-DIP label column, 31-DIP row rhythm, and 26-DIP input height. The current
19-DIP chooser rows and full-width tunnel forms supersede the roomier chooser
and propagated 604-DIP form assumptions described in the historical milestones
below. Fixed editor footers and scrolling keep actions reachable in smaller
windows; cancel/back/close retain draft and credential-cleanup boundaries.

### Evidence and current acceptance

Implementation, headless UI tests, native GUI observations, and original/runtime
comparisons are separate evidence levels. **O** means original runtime
observation, **S** static original evidence, **I** independent implementation
and tests, **V** a passed comparison for the stated scene and measurements,
and **Unknown** insufficient evidence. Historical V results do not establish
current whole-page or whole-application parity.

Current differential tooling accepts workbench and connection-form UI-state
evidence without inventing database results. Objects, structure, and data
scenes still require matching real service, fixture, read-result, and selected
state evidence. Without a valid original read, those scenes remain **BLOCKED**.
The gate records client crops, actual scale, fonts, source identity, readiness,
selection/expansion/menu state, and explicit geometry/color measurements;
see [the current gate specification](UI_VISUAL_GATE_2026-10-25.md).

The [iteration report](UI_ALIGNMENT_2026-10-25.md) records passing tests, race,
static checks, packaging and cross-compilation, plus native screenshots and
the remaining strict visual BLOCKED conditions. Windows/macOS runtime behavior
and any unobserved minimum-window, scale, theme, or language combinations remain
**Unknown**, even when compilation or headless tests pass.

## Historical milestones

The entries below describe the implementation and verification **at their
recorded milestone**. Version pins, pending features, UI measurements, and
limitations in those entries are historical; the current snapshot above takes
precedence. Linked reports retain their original evidence scope.

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
expandable vendor tree after live Navicat 18 runtime observation. At this
milestone it used 25-DIP rows (roomier than the observed 19px original;
superseded by 19 DIP on 2026-10-25), independent vendor expansion, full-row selection and two-pane
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

### MySQL Notes editor and full-window refinement baseline (2026-10-21)

The MyGo-native Notes tab now provides the observed large multiline input,
label and footnote. User-authored notes are saved on successful profile
confirmation, reloaded on edit, preserved on duplication and discarded when
editing is canceled. Notes live in a new general Profile.Notes field, backed
by a backward-compatible SQLite v3 migration (v2 → v3); existing records
start with empty notes. Notes are stored as plain text and must not contain
credentials. See `docs/MYSQL_NOTES_TAB_DIFFERENTIAL_2026-10-21.md`.

All seven MySQL editor tabs now have a first independent UI implementation (I).
Visual and behavior parity (V) remains incomplete. The next prioritized work
is tracked in `docs/MYSQL_EDITOR_VISUAL_REFINEMENT_PLAN_2026-10-21.md`.

### Shared MySQL connection-window chrome refinement (2026-10-21)

The common 900×700 MyGo editor has a locally scoped blue-gray palette,
updated main-tab border, normalized footer-button height/spacing, and
re-centered independent connection/tunnel diagrams. All seven tabs were
visited in a Linux Xvfb GUI session. A headless UI regression test checks
shared controls and cross-tab tunnel state. Unimplemented profile-list
navigation is visibly disabled rather than made functional without evidence.
See `docs/MYSQL_CONNECTION_CHROME_DIFFERENTIAL_2026-10-21.md`.

### Shared MySQL form geometry (2026-10-21)

At this milestone native MyGo connection forms shared a 604-DIP centered layout, 166-DIP
label column, 31-DIP row rhythm and 26-DIP text/select controls. Browse
buttons use 22 DIP with 7 DIP spacing. General and Advanced were compared
against original screenshots before propagating these rules to SSL, SSH and
HTTP. Runtime observation on 2026-10-25 superseded that propagation: those
three pages use the full client width minus 40 DIP. At the earlier milestone,
existing MyGo checkboxes retained their native 16-DIP size. Headless
rectangular-geometry tests and isolated Linux GUI screenshots confirm
consistent positioning, without claiming pixel-perfect parity. See
`docs/MYSQL_FORM_GEOMETRY_DIFFERENTIAL_2026-10-21.md`.

### MySQL form fine-visual refinement (2026-10-21)

The editor now uses a window-scoped MyGo theme for its input background,
focused border and muted/disabled text. A shared 14-DIP native CheckboxBase
renderer replaces blue-fill checkboxes with the observed dark-square/light-tick
style on General, Advanced, Compatibility, SSL, SSH and HTTP. Native focus,
keyboard and accessibility behavior remain intact. GUI and headless regression
evidence is recorded in `docs/MYSQL_FORM_FINE_VISUAL_DIFFERENTIAL_2026-10-21.md`.

### MySQL new/edit lifecycle and configuration sidebar (2026-10-21)

MySQL editing now has a native, collapsible 258-DIP configuration sidebar,
with a selected main configuration and up to 16 named alternative drafts.
Alternatives can be added, switched, renamed, duplicated and removed.
The editor separates new/create from edit/update, omits the Back action when
editing, preserves unfinished settings across configuration changes, and
discards changes on Cancel or OS window close. Confirm validates and saves
non-secret main and alternative settings atomically in one profile row.
SQLite schema v4 adds a backward-compatible variants JSON column; real
database-session activation of an alternative remains unimplemented.
See `docs/MYSQL_EDITOR_PROFILE_LIFECYCLE_DIFFERENTIAL_2026-10-21.md`.

### Active MySQL configuration and future session resolution (2026-10-22)

Selecting an editor row no longer changes the Active target. A context-menu
action marks the primary or an alternative as Active in the window draft;
Confirm persists the selection with the alternatives. SQLite schema v5
adds `active_variant` (empty for primary) and migrates older profiles
without changing their default. The non-secret `ActiveSettings` resolver
is available to a future MySQL session factory and fails closed on invalid
references. The workbench inspector now shows the resolved Active
configuration but does not claim to be connected. The sidebar has keyboard
up/down navigation and one-time name focus. See
`docs/MYSQL_ACTIVE_CONFIGURATION_DIFFERENTIAL_2026-10-22.md`.

### MySQL direct TCP connection test (2026-10-22)

The native Go MySQL driver can establish, authenticate, Ping and explicitly
close a direct TCP session from the selected draft configuration. The editor's
Test button starts asynchronous, context-cancellable work, shows completion
and errors without storing credentials, and rejects unsupported SSL/SSH/HTTP
or other advanced settings *before* dialing. Closing the editor cancels work
and ignores stale results. The driver is also exposed as OpenDirectMySQL for
a future managed session layer, which is not yet connected to the workbench.
Protocol tests use a disposable local mock MySQL peer rather than a real
production database. Full database compatibility, TLS and encrypted credential
transport remain unverified/unimplemented. See
`docs/MYSQL_DIRECT_CONNECT_PHASE1_2026-10-22.md`.

### MySQL verified TLS and managed workbench sessions (2026-10-22)

The native MySQL driver now supports strict certificate/hostname-verified TLS,
optional CA and paired client key material. Remote plaintext connections are
rejected even in editor Test Connection. A session manager owns asynchronous
connection setup, cancellation, disconnect and cleanup, with stale-result
prevention and real SHOW DATABASES metadata queries. The workbench exposes
explicit Connect, Cancel, Disconnect and credential reentry before Reconnect;
its object view shows real database names rather than demo tables in profile
mode. This does not yet implement schema/table browsing or SQL execution.
See docs/MYSQL_TLS_MANAGED_SESSION_2026-10-22.md.

### MySQL database tree and real table/view metadata (2026-10-22)

Connected MySQL workspaces now offer a native MyGo database tree with lazy
per-schema object loading, real INFORMATION_SCHEMA table/view metadata,
engine/comment columns, database switching, type filters and explicit refresh.
Bound schema-name parameters and session-scoped cancellation prevent unsafe SQL
construction and late-query UI corruption; caches clear on disconnect.
The service enforces a bounded 10,000-object response and MyGo renders the
metadata list with its native Table. This does not execute arbitrary SQL or
load table rows. See docs/MYSQL_OBJECT_BROWSER_PHASE1_2026-10-22.md.

### Native connection editor shared palette (2026-10-22)

The native child window and type chooser now initialize the common NyaDB
dark theme. The editor's body, tabs, footer, form controls, checkboxes and
edit-profile sidebar use the same workbench color tokens rather than a
separate blue-grey palette. The shared layout, native MyGo focus and IME
behavior and existing connection functionality remain unchanged. Headless
all-tab raster checks and Xvfb desktop screenshots are recorded in
`docs/CONNECTION_EDITOR_SHARED_THEME_2026-10-22.md`.
