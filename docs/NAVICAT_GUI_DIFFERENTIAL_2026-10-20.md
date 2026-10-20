# NyaDB / Navicat 18 GUI Differential — Connection Management

**Observed:** 2026-10-20 (Linux x86_64, 1600 × 960 virtual X11 desktop, Xvfb + Openbox).
**Status:** First live GUI comparison; connection creation is **not yet V-passed**.
**Evidence codes:** O = original runtime observed; I = NyaDB implemented and observed; V = verified functional equivalence; Unknown = not examined.

## Environment and safeguards

- Original launched from `temp/navicat/squashfs-root/AppRun` (read-only source).
- Original was run with an isolated `HOME` under
  `temp/research/gui-compare-20261020/navicat-home/`. This avoids
  changing the user's regular Navicat configuration.
- NyaDB launched from `build/linux-amd64/nyadb`.
- Both ran under separate Xvfb displays: NyaDB `:101`, Navicat `:102`.
- No production or remote database connections were attempted. No connection
  tests were clicked. No credentials were supplied.
- Original trial reminder displayed 14 days; trial button was used normally
  without license bypass. First-run theme was set to Dark; telemetry sharing
  was declined.
- Evidence screenshots are kept under `temp/research/gui-compare-20261020/`.
  They are research evidence only and must not be copied into NyaDB product assets.
- This is real application rendering through Xvfb, **not** physical
  Windows/Linux/macOS desktop smoke testing.

## Screenshots (original runtime O)

| File (relative to the evidence directory) | Observation |
|---|---|
| `navicat-workbench-dark.png` | Original dark workbench, initially empty connection list |
| `navicat-new-connection-menu.png` | New connection type chooser |
| `navicat-select-mysql.png` | Selected type state (MySQL card highlighted) |
| `navicat-mysql-settings.png` | MySQL connection configuration, General tab |
| `navicat-postgres-settings.png` | PostgreSQL connection configuration, General tab |
| `navicat-sqlite-settings.png` | SQLite connection configuration, General tab |
| `navicat-mysql-advanced.png` | Advanced options |
| `navicat-mysql-database.png` | Database-selection options |
| `navicat-mysql-ssl.png` | SSL options |
| `navicat-mysql-ssh.png` | SSH tunneling options |
| `navicat-mysql-http.png` | HTTP options |
| `navicat-mysql-notes.png` | Notes tab |
| `navicat-profile-context.png` | Saved connection right-click menu and inspector |
| `navicat-edit-sqlite.png` | Editing a saved profile; profile list pane becomes visible |

NyaDB screenshots: `nyadb-initial.png`,
`nyadb-connection-action.png`. The latter shows the Connection toolbar
action currently updates the statusbar only.

## Original behavior observations (O)

1. The original main window uses a menu row, icon toolbar, connection explorer,
   central object page and right inspector. Initial state has no connections
   in the isolated profile home. The measured main-client window was about
   1066 × 640 in this session; different initial dimensions prevent pixel
   parity claims with NyaDB's 1280 × 800 window.
2. Selecting toolbar **连接** opens a large modal titled **新建连接**.
   Its left list filters by database/vendor, with a scrollable icon-card grid
   and a separate **下一步** action. The type picker is approximately
   900 × 720 on the 1600 × 960 display.
3. Selecting MySQL and **下一步** opens **新建连接 (MySQL)**.
   Its tabs are **常规 / 高级 / 数据库 / SSL / SSH / HTTP / 备注**.
   General controls: connection name, host, port, username, password and
   a save-password checkbox; bottom actions include URI, test connection,
   previous step, confirm and cancel.
4. The observed MySQL initial defaults were host `localhost`, port `3306`,
   user `root`. The observed PostgreSQL initial defaults were host
   `localhost`, port `5432`, initial database `postgres`, user
   `postgres`. Both screenshots show a checked save-password box.
5. The SQLite General tab differs: it offers **existing database file**,
   **new SQLite 3**, **new SQLite 2**, database file picker, username and
   password. Its tabs are **常规 / 高级 / 附加的数据库 / HTTP / 备注**;
   no MySQL-style SSL/SSH tab was visible.
6. Clicking **确定** on a blank SQLite configuration closed the dialog and
   added a connection named **SQLite** to the explorer. The inspector then
   showed a SQLite profile with an empty/unavailable file path and zero
   sessions. This demonstrates that Navicat accepted that incomplete profile
   in the observed flow, **not** that it could connect successfully.
7. Selecting this profile updated the right inspector and changed some
   context-sensitive toolbar functions. Right-click exposed **打开连接**,
   **编辑连接**, **新建连接**, **删除连接**, **复制连接** and **刷新**,
   among other actions. Visible shortcuts included Enter (open), Del
   (delete), F5 (refresh).
8. **编辑连接** opened a different form from the new connection wizard:
   a left **连接配置文件** pane appeared with **主要配置文件 (Active)**,
   and the saved connection name was prefilled. Whether a masked password
   represents a real stored secret or a sentinel was **not established**.

## NyaDB implementation observations (I)

- NyaDB successfully launched with the native MyGo workbench, connection
  sidebar, object tab, toolbar, inspector and statusbar.
- It displayed five explicit demo connection roots and a UI-preview label.
- Clicking **连接** changed the statusbar to indicate unavailable mock
  functionality; **no connection-type modal opened**.
- NyaDB M1-A backend does persist profiles through
  `internal/services/connections` and SQLite
  `internal/storage/localdb`, but it is not wired to this UI.
- NyaDB's profile validation currently requires a nonempty SQLite file path;
  the original accepted a blank profile. Treat this as a **behavior gap and
  product-policy decision**, not as evidence that NyaDB is defective.
- NyaDB intentionally does not persist raw passwords; do not emulate the
  original checked save-password default without an OS keychain and explicit
  user consent.

## Differential findings

| Area | Original (O) | NyaDB (I) | Result |
|---|---|---|---|
| Main workbench | Three panels, menu and toolbar | Three panels, menu and toolbar | Similar structure, **not V-passed** |
| Initial connection list | Empty with clean isolated home | Five demo roots | Gap |
| New connection action | Type selector modal | Status-only placeholder | Gap |
| MySQL/PG/SQLite field sets | Type-specific forms | No interactive forms | Gap |
| Connection profile CRUD | GUI entry points visible | Backend only | Gap |
| Profile selection and inspector | Live profile metadata | Mock connection metadata | Gap |
| Database operations | Not exercised | Not implemented | Unknown |
| Physical desktop tests | Not performed | Not performed | Unknown |

## M1-B implementation and acceptance scope

1. Add a native MyGo connection-type chooser for **MySQL, PostgreSQL and SQLite**.
   It must have selection state, Back/Next/Cancel flow, keyboard focus and
   accessible labels. Do not copy database brand icons from Navicat; use
   NyaDB-original icons.
2. Add type-specific native connection forms. MVP General fields:
   - MySQL: name, host (default localhost), port (3306), username, session
     password, optional database.
   - PostgreSQL: name, host (default localhost), port (5432), initial database
     (postgres), username, session password.
   - SQLite: name, existing file path and file picker; creating new files
     can be a later explicitly marked capability.
   Common optional advanced/TLS values should be exposed only if implemented.
   SSH/HTTP can be disabled with clear not-yet-supported messaging.
3. Validate inputs without emitting SQL, Ping or attempting network access.
   Confirmation of a profile stores **non-secret metadata only** and returns
   to the explorer with the new item selected.
4. Populate left tree from the profile store (not mock roots) and support
   edit, duplicate, delete confirmation and refresh; inspector reflects the
   selected profile. Deleting a profile must not affect user database files.
5. Add `ui.NewTester` tests for new/next/back/cancel, input validation,
   CRUD persistence, selection restoration, refresh and duplicate-name errors.
6. Later, run side-by-side screenshot tests at matched window dimensions and
   repeat on a physical desktop before marking **V**. Do not claim real
   connections until the M2 driver implementation is also verified.

## Not examined / unknown

- Credential persistence or decryption in Navicat.
- Actual connection handshake, error text, timeout handling or connection
  cancellation.
- SQLite file creation or table browsing; no file was opened.
- Navicat Delete confirmation outcomes, multiple profile identities or
  full SSH/HTTP form validation.
- Exact font metrics, pixel-perfect spacing, scaling and IME outside Xvfb.

Research images and isolated trial settings are left in
`temp/research/gui-compare-20261020/`; formal NyaDB code must remain
separate from that material.
