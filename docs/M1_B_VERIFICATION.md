# NyaDB M1-B Verification — Connection Profiles

**Date:** 2026-10-21.
**Implementation:** MyGo v0.5.5, Go 1.27.3, Linux amd64.
**Baseline original observations:** `docs/NAVICAT_GUI_DIFFERENTIAL_2026-10-20.md`.
**Git stages:** M1-A `4d81bcf`, M1-B1 `7a2f980`, M1-B2 `4685e2a`, M1-B3 `afa5034`.

## Scope delivered (I)

- Original icon-free connection type chooser implemented in MyGo with
  MySQL, PostgreSQL and SQLite selection, Next/Back/Cancel and validation.
- Each connection type has an editable metadata form. MySQL defaults:
  localhost, port 3306, root; PostgreSQL: localhost, port 5432,
  postgres initial database and username. SQLite uses an existing-file
  path and native file-picker command (host-specific picker unverified).
- Form submission uses `connections.Service` -> SQLite profile store,
  without issuing SQL against remote/user databases, Ping or network calls.
- Real application now uses `NewWithProfiles` instead of five demo roots.
  Saved profiles support list, search, edit, copy, delete confirmation and
  refresh; selected profile metadata displays in the right inspector.
- Bootstrap stores app configuration at `os.UserConfigDir()/NyaDB/config.sqlite`.
  Database handles close on application exit. Native dialog plumbing is
  asynchronous, with main-thread updates through `Window.Update`.
- No passwords, tokens, secret references or unredacted DSNs are persisted;
  authentication, SSH, HTTP and TLS connection behavior are not represented
  as functional.

## Live GUI evidence (I)

A separate Xvfb/Openbox session at 1600x960 was used with both `HOME`
and `XDG_CONFIG_HOME` pointed at an isolated temporary research directory.
The application was built and launched as a real Linux native window,
not merely rendered by the headless tester.

Screenshots (under `temp/research/gui-compare-m1b-20261021/`,
not shipped or committed):

| Screenshot | Captured state |
|---|---|
| `nyadb-after-wait.png` | Fresh profile store: zero real connections |
| `nyadb-choose.png` | MyGo modal with three connection types |
| `nyadb-sqlite-form.png` | SQLite connection metadata form |
| `nyadb-saved-profile.png` | Saved profile visible with inspector |
| `nyadb-restarted-profile-ready.png` | Profile restored after process restart |
| `nyadb-reloaded-selected.png` | Restored profile selected after restart |
| `nyadb-edit-form.png` | Existing SQLite metadata prefilled in edit modal |

The isolated run created profile `SQLite Lab` with a *configuration
string* `/tmp/m1b-lab.sqlite`. It did **not** create or access that
SQLite user database. The app's own config SQLite database was created
with owner-only (0600) permissions.

A screenshot captured too early during launch was black; after the
software renderer initialized, window rendering was normal. Xvfb
reported no GPU, not a native desktop GPU-test result.

## Differential status vs original

| Behavior | Original (O) | NyaDB (I) | Outcome |
|---|---|---|---|
| Open new-connection chooser | Large type grid | Three native type cards | Same high-level flow; **not pixel V** |
| MySQL/PG/SQLite General form | Type-specific tabbed dialog | Compact type-specific modal | Function subset; **not full V** |
| Persist connection profile | Saved profile appears in tree | Saved profile appears in tree and survives restart | Both observed independently; **not a full V suite** |
| Context actions | Original right-click menu | NyaDB explicit side-panel buttons | Behavioral alternative, visual gap |
| SQLite blank file accepted | Original accepted incomplete profile | NyaDB rejects empty file path | Intentional stricter input policy |
| Password storage | Original save-password checkbox observed | NyaDB persists none | Deliberate security boundary |
| File chooser | Original SQLite browse control | Native picker wired; callback tested via injected command | Actual OS picker result **Unknown** |
| Physical GUI and cross-platform | Not evaluated | Linux Xvfb only | **Unknown** |

## Automated verification

The following commands were executed with `CGO_ENABLED=0` and
Go module/cache data rooted at `/data/.cache/golang`:

| Command | Status |
|---|---|
| `go test ./...` | PASS |
| `go vet ./...` | PASS |
| `go tool mygo vet` | PASS |
| `go tool mygo build` | PASS: linux-amd64 executable, .deb and .tar.gz |

New tests exercise the chooser, validation, create/read/update/delete,
duplicate-name errors, back/cancel, refresh, delete confirmation,
native file-picker callback injection and SQLite reopen. This is
headless MyGo UI automation plus one manual Xvfb GUI flow; not a
replacement for physical desktop tests.

## Remaining work

- Implement M2 database drivers, sessions and connection lifecycle
  (connect, disconnect, Ping, error handling and cancel).
- Add secure OS keychain-backed authentication before handling passwords
  in the connection form. Credentials must never enter local profile DB.
- Add native connection right-click context actions and keyboard commands,
  advanced/tunneled connection options, and multi-profile configuration.
- Provide SQLite file creation (currently only stores a path to an
  existing file), validate OS picker behavior on Linux/Windows/macOS.
- Match modal sizing, visuals, icon detail, responsive small-window
  behavior, DPI/IME and keyboard accessibility to measured original UX.
- Cross-platform physical GUI testing and quantitative screenshot
  diff are still required before any broad `V` equivalence claim.
