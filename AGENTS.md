# Repository Guidelines

## Project Structure & Module Organization

NyaDB is a Go desktop database workbench using MyGo UI. `cmd/nyadb/` is the development entry point; root `main.go` supports MyGo tooling. `internal/bootstrap/` wires startup, `internal/ui/shell/` contains workbench views and interaction state, `internal/ui/connectioncatalog/` owns the connection type chooser, `internal/ui/connectioneditor/` owns connection forms and editing state, `internal/ui/navigation/` owns the connection navigator, and `internal/ui/theme/` defines appearance and shared icon caches. Connection models, validation, and configuration services live in `internal/services/connections/`; authenticated MySQL sessions, SQL execution, metadata, and data operations live in `internal/services/mysql/`. SQLite profile storage, application preferences, and migrations live in `internal/storage/localdb/`.

Both `localdb` and the MySQL service depend on connection models; `connections` must not depend on storage, database drivers, or UI. Bootstrap and shell compose these packages. Import the MySQL service as `mysqlsvc` and the MySQL driver as `mysqldriver`.

`connectioncatalog` owns chooser search, filters, presentation, selection and in-session recents. Shell hosts its view and opens the connection editor through selection callbacks. The chooser must not depend on shell, storage or the MySQL runtime service. Shared brand bitmap caches belong to theme; callers must not modify their maps.

`connectioneditor` owns raw form inputs, alternative configurations, temporary secrets, vault save/rollback and asynchronous connection checks. Configuration services, vaults, MySQL validation/testing and host callbacks are injected. It must not import shell, storage or database drivers/runtime services. Shell owns native windows, chooser navigation, saved-profile list refresh and real session invalidation; bootstrap-facing picker methods remain on shell and forward to the editor.

`navigation` owns qualified node keys, profile/group snapshots, tree selection and disclosure, search/engine/star filters, temporary filter restoration, inline group editing and navigation icons. It consumes read-only session metadata and workbench callbacks; it must not import shell, storage or MySQL runtime services. Shell supplies those callbacks in the existing profile view and keeps profile/session activation, asynchronous metadata loading, object actions, context-menu commands and document coordination. A database's opened state belongs to its session and remains separate from tree disclosure. Single click selects an unopened database; explicit open, double click or Enter schedules metadata.

Go tests sit beside source files. `internal/assets/` contains embedded icons, `resources/` contains artwork, and `locales/` contains translations. `docs/` records design and verification; `tools/` and `tests/` contain utilities and Python visual-comparison tests. Builds and research belong in ignored `build/` and `temp/` directories.

## Build, Test, and Development Commands

Use Go 1.27.1 or later and the MyGo fork pinned in `go.mod`. Linux requires GTK 3 runtime libraries (`libgtk-3-0`) and a desktop session for native execution.

- `go mod download`: fetch dependencies.
- `go run ./cmd/nyadb`: launch locally.
- `go tool mygo doctor` / `go tool mygo dev`: diagnose prerequisites or start development mode.
- `go test ./...`: run Go tests, including headless UI tests.
- `go vet ./...` and `go tool mygo vet`: run static and UI checks.
- `go build ./cmd/nyadb`: compile; `go tool mygo build`: package into `build/`.

CI runs tests, checks, and compilation with `CGO_ENABLED=0` on Linux, Windows, and macOS.

## Coding Style & Naming Conventions

Format Go with `gofmt` (tabs). Use lowercase packages, exported `PascalCase` identifiers, unexported `camelCase` identifiers, and descriptive snake_case filenames. Keep persistence, services, and views in their respective packages. Edit translation JSON catalogs, then run `go tool mygo generate` and `go tool mygo i18n check`; do not hand-edit `locales/catalog_gen.go`.

## Testing Guidelines

Use Go's `testing` package, `*_test.go` files, and `TestBehavior` names; UI tests use MyGo's `ui.NewTester`. No numeric coverage threshold is configured. Cover changed behavior and migration compatibility. Python visual-tool tests require Pillow: `python3 -m unittest discover -s tests -v`. MySQL integration tests require disposable fixtures and explicit `NYADB_MYSQL_*_IT` flags; consult the relevant tests and docs before enabling them.

Configuration and service tests use `connections_test`; SQLite persistence and legacy profile decoding tests live beside `localdb`, and MySQL runtime tests live beside the MySQL service. Chooser state and view tests live beside `connectioncatalog`; editor form, configuration, credential and async-check tests live beside `connectioneditor`; navigator filtering, node identity, category order, inline editing, virtualization and icon tests live beside `navigation`. Shell keeps native-window, navigation/workbench wiring and real-session integration tests. Disposable MySQL SQL fixtures live in `internal/services/mysql/testdata/`. Legacy-data fixtures hold their database handles explicitly rather than exposing them through production storage APIs. For focused regression checks, run `go test ./internal/services/connections ./internal/storage/localdb ./internal/services/mysql ./internal/ui/shell ./internal/ui/connectioncatalog ./internal/ui/connectioneditor ./internal/ui/navigation ./internal/ui/theme`; run the same package list with `go test -race` for lifecycle and concurrency changes.

## Commit & Pull Request Guidelines

Follow history's `type(scope): imperative summary` convention, e.g. `fix(mysql): preserve nullable field metadata`. Use scopes such as `ui`, `theme`, or `deps`. PRs should explain behavior changes, link relevant issues, report validation, and include screenshots for visible UI changes.

## Configuration & Assets

Keep credentials out of profiles, logs, and commits. Configuration lives under `os.UserConfigDir()/NyaDB/config.sqlite`. Preserve asset licenses and provenance manifests; keep proprietary research assets in ignored local directories.
