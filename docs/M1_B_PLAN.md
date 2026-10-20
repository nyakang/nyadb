# NyaDB M1-B — Connection Management Delivery Plan

Plan date: 2026-10-20. Target: Go 1.27+ / MyGo Native UI v0.5.5.
Baseline research: `docs/NAVICAT_GUI_DIFFERENTIAL_2026-10-20.md`.
All screenshots are research evidence, not NyaDB resources.

## Delivery stages (each independently tested, committed and pushed)

1. **M1-A carry-over:** Audit and preserve existing uncommitted profile storage,
   migrations and original live GUI research. Verify baseline, commit and push.
2. **M1-B1 application service:** Provide a framework-independent profile coordinator
   for list/create/update/delete/duplicate. Make local storage an explicit app
   dependency, with lifecycle/error handling, no credentials persisted. Unit-test.
3. **M1-B2 MyGo native flow:** Build connection-type choice, per-kind profile editor,
   validation/error display, next/back/cancel using MyGo widgets. No real network calls,
   test connections or password persistence. Add headless UI interaction tests.
4. **M1-B3 connection explorer:** Replace mock roots with persisted profiles;
   edit/duplicate/delete with confirmation, refresh, selection/inspector integration;
   regression tests for empty state and persistence.
5. **M1-B4 verification:** Run `go test ./...`, `go vet ./...`,
   `go tool mygo vet`, `go tool mygo build`; produce isolated GUI screenshots
   and compare against Navicat observation; document O/I/V distinctions.
   Commit and push verification artifacts/doc updates (not private screenshot assets).

## Acceptance criteria and boundaries

- MySQL default localhost:3306; PostgreSQL localhost:5432; SQLite file path.
- Profile form does not send SQL, Ping, or connect to production databases.
- Passwords and secrets never persist to local SQLite, logs or research artifacts.
- Unsupported SSH/HTTP/advanced features are labeled pending, not simulated as working.
- Names and profile IDs remain consistent across edit, duplicate, refresh and restart.
- No Navicat proprietary source, icons, screenshots or brand assets in shipped UI.
- GUI headless verification is not a substitute for physical GUI smoke tests.
- Commit only relevant NyaDB project changes; do not reset or rewrite Git history.
