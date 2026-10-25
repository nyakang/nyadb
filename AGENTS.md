# Repository Guidelines

## Project Structure & Module Organization

NyaDB is a Go desktop database workbench using MyGo UI. `cmd/nyadb/` is the development entry point; root `main.go` supports MyGo tooling. `internal/bootstrap/` wires startup, `internal/ui/shell/` contains views and interaction state, and `internal/ui/theme/` defines appearance and icons. Profiles and MySQL sessions live in `internal/services/connections/`; SQLite storage and migrations live in `internal/storage/localdb/`.

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

## Commit & Pull Request Guidelines

Follow history's `type(scope): imperative summary` convention, e.g. `fix(mysql): preserve nullable field metadata`. Use scopes such as `ui`, `theme`, or `deps`. PRs should explain behavior changes, link relevant issues, report validation, and include screenshots for visible UI changes.

## Configuration & Assets

Keep credentials out of profiles, logs, and commits. Configuration lives under `os.UserConfigDir()/NyaDB/config.sqlite`. Preserve asset licenses and provenance manifests; keep proprietary research assets in ignored local directories.
