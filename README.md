# NyaDB

NyaDB is a cross-platform, native desktop database workbench built with [Go](https://go.dev/) and [MyGo Native UI](https://github.com/egoist/mygo). It provides a desktop interface for organizing database connections and working with SQL.

![NyaDB workbench preview](docs/ui-preview.png)

## Features

- Native desktop interface for Windows, macOS, and Linux, with resizable explorer, workspace, and inspector panes.
- Consistent Lucide SVG operation icons with a separate set of embedded database brand icons.
- Create, edit, duplicate, delete, and search connection profiles for MySQL, PostgreSQL, and SQLite.
- Persist non-secret connection settings in a local SQLite configuration database.
- Database chooser icons embedded in the application, with documented third-party artwork provenance.
- Multi-tab SQL editor with syntax highlighting.
- Object browser and virtualized table components, with on-demand synthetic data for UI previews.
- Headless UI tests and cross-platform Go build verification.

**Current functionality:** Connection profiles are saved locally, but NyaDB does not yet establish database sessions, load live metadata, or execute SQL against user databases. Object and table previews use demo data; they are not query results.

## Requirements

- Go 1.27.1 or later.
- MyGo Native UI v0.5.5 (declared in `go.mod`).
- On Linux, GTK 3 runtime libraries and a graphical desktop session. On Debian/Ubuntu, install `libgtk-3-0`.
- On Windows and macOS, the operating system's native UI libraries.

No Node.js or web frontend toolchain is required.

## Getting started

Clone the repository and run the application:

```bash
git clone https://github.com/nyakang/nyadb.git
cd nyadb
go mod download
go run ./cmd/nyadb
```

For MyGo development tools:

```bash
go tool mygo doctor
go tool mygo dev
```

## Language

NyaDB includes Simplified Chinese (`zh-Hans`) and English (`en`). The app follows the system's preferred language on first launch. To change it, open **View → Language** and choose **简体中文**, **English**, or **Follow System Language**. MyGo saves an explicit selection between launches.

Translations are maintained in `locales/zh-Hans.json` and `locales/en.json`. After editing the ARB catalogs, regenerate and validate the Go accessors:

```bash
go tool mygo generate
go tool mygo i18n check
```

Some work-in-progress database screens and runtime diagnostic messages have not yet been translated and may display Chinese text.

## Build and test

```bash
go test ./...
go vet ./...
go tool mygo vet
go build ./cmd/nyadb
```

To build an application package with MyGo:

```bash
go tool mygo build
```

Native packaging requirements vary by operating system. The GitHub Actions workflow runs Go tests, static checks, and builds on Linux, Windows, and macOS.

## Configuration and privacy

NyaDB stores connection profiles in `NyaDB/config.sqlite` under the directory returned by Go's `os.UserConfigDir()`. Profiles contain connection names, database types, hosts, ports, database paths or names, usernames, and optional TLS settings.

Passwords, tokens, and credential-bearing connection strings are not stored in the profile database. Creating a profile does not open a network connection to the database. On Unix systems, newly created configuration directories and files use restrictive permissions; on Windows, access is governed by filesystem ACLs. Protect existing files, backups, and your user account accordingly.

## Repository structure

```text
cmd/nyadb/                    Desktop application entry point
internal/bootstrap/           Application initialization
internal/assets/lucide/       Embedded Lucide UI icons (ISC license)
internal/assets/connectionicons/ Embedded icon assets and provenance
internal/services/connections/ Connection profile service and validation
internal/storage/localdb/     SQLite configuration storage and migrations
internal/ui/shell/            Native UI, workspace, dialogs, and tests
internal/ui/theme/            Theme and UI icons
resources/                    Application assets
docs/                         Design and UI documentation
.github/workflows/            Cross-platform CI
```

## Documentation

- [Technical design](docs/NYADB_TECHNICAL_DESIGN.md)
- [UI layout and interactions](docs/UI_LAYOUT.md)
- [Lucide general UI icons](docs/LUCIDE_ICONS.md)
- [Connection icon assets and licenses](docs/ICON_ASSETS.md)

NyaDB is an independent implementation. It does not include Navicat source code, branding, or proprietary assets.
