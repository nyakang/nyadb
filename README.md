<p align="center">
  <img src="./internal/assets/appicon/icon.svg" alt="NyaDB" width="128" height="128">
</p>


[![Listed on Awesome MyGo](https://awesomemygo.dev/badge.svg)](https://awesomemygo.dev/apps/nyadb)

NyaDB is a cross-platform, native desktop database workbench built with [Go](https://go.dev/) and the [NyaDB fork of MyGo Native UI](https://github.com/nyakang/mygo). It supports live MySQL, MariaDB, PostgreSQL, SQLite, ClickHouse, SQL Server, DuckDB, Oracle, MongoDB and Redis connections, SQL or native MongoDB/Redis commands, and database browsing, with data editing and administration tools for supported engines.

![NyaDB workbench preview](docs/ui-preview.png)

This image is a historical headless UI preview. For recent native-window verification, see the [UI optimization report](docs/ui-global-optimization-2026-11-05.md) and [workbench workflow report](docs/navicat-ux-implementation-2026-11-05.md).

## Features

- Native desktop interface targeting Windows, macOS, and Linux, with shared resizable navigation and inspector panes.
- Consistent Lucide SVG operation icons with a separate set of embedded database brand icons.
- Create, edit, duplicate, delete, group, star, and search connection profiles; import connection URIs and optionally save credentials in the system credential vault.
- A unified navigation tree with lazy database/schema and object loading, favorites, and object search.
- Multi-tab SQL editor with real execution, cancellation, syntax highlighting, completion, formatting, EXPLAIN, multiple results, result snapshots, and export.
- Named queries, SQL files, encrypted workspace recovery, and execution history. Disconnection preserves SQL text and requires explicit session recovery before further execution.
- A shared virtualized data grid with filtering, sorting, paging, column layouts, field previews, and export. MySQL, MariaDB, PostgreSQL, SQLite, SQL Server, DuckDB, and Oracle also support editing, draft undo/redo, transactions, counting, and data analysis where available.
- Table designers, object management, import, backup/restore, and maintenance tools for supported engines, with SQL previews and explicit confirmation for applicable operations.
- MySQL/MariaDB SSH tunnels and a NyaDB HTTPS/WebSocket TCP gateway, with host fingerprint trust and optional saved tunnel credentials.
- Optional AI SQL generation, explanation, and optimization through a configurable OpenAI-compatible service, with request previews and suggestions inserted as unexecuted SQL.
- Read-only data dictionaries with Markdown and HTML export.
- Five [color schemes](docs/THEME_SYSTEM.md)—Classic, Ocean, Forest, Rose and Sand—with light, dark and system appearance, live previews, text scaling, and saved preferences.

## Database support

| Engine | Current capabilities | Main limits |
| --- | --- | --- |
| MySQL | SQL, metadata, data editing, table design, object/account management, import/export, backup/restore, maintenance, TLS and SSH/HTTP tunnels | Tools depend on server capabilities, permissions, and object structure. |
| MariaDB | Independent connection profiles and `mariadb://` URIs; SQL, metadata, data editing, table/view design, object management, import/export, backup/restore, maintenance, TLS and SSH/HTTP tunnels | Requires MariaDB 10.11 or later. Account/role management and MySQL-only check enforcement/index visibility are unavailable. Tools retain complete definitions and reject unsupported changes. |
| PostgreSQL | SQL, schema navigation, data editing, table design, ordinary table/view management, import/export, backup/restore and maintenance | Table design supports standard core PostgreSQL 14–18 ordinary tables; unsupported types and definitions are rejected. Backup/restore requires matching `pg_dump`/`pg_restore` tools. SSH/HTTP tunnels are unavailable. |
| SQLite | SQL, attached databases, data editing, table/view design and management, import/export, online backup/restore and maintenance | Backup/restore operates on one database file at a time. Editing requires a reliable row identity. |
| ClickHouse | Native connections, SQL, metadata/structure browsing, a shared read-only data grid, filtering, sorting, counting and export | No grid editing, transactions, table designer, import, backup/restore or tunnels. Paging uses offsets. |
| SQL Server | SQL authentication and TLS, fixed database/schema navigation, T-SQL batches, XML estimated plans, shared-grid editing, transactions, filtering, counting, analysis and import/export | Verified on SQL Server 2019 and 2022. Imports append atomically to existing tables. SQL requires explicit schema qualification. Table/view design, object mutations, backup/restore, account management, integrated/Entra authentication and tunnels are unavailable. |
| Oracle | Service name or SID connections, schema/object browsing, SQL and PL/SQL, EXPLAIN PLAN, shared-grid editing, transactions, field previews, counting, analysis and append import/export | Uses a pure Go driver; no Instant Client required. Views, unsupported types and unreliable keys are read-only. LOB editing requires DBMS_CRYPTO. No designer, administration, backup/restore, TNS aliases, wallets or tunnels. |
| DuckDB | Local files or `:memory:`, schema/object browsing, SQL and JSON EXPLAIN, shared-grid editing, snapshot transactions, filtering, counting, analysis and append import/export | Requires the verified official DuckDB 1.4.4 runtime. Views and unsupported column types are read-only for editing. No designer, object-management dialogs, backup/restore or tunnels. |
| MongoDB | Hosts/seeds or SRV, SCRAM and TLS, database/collection/index browsing, native Extended JSON commands, shared-grid document editing, counting and export | Verified on MongoDB 7.0.28 standalone and replica-set fixtures. Complete-document editing preserves BSON types and checks concurrent changes. No SQL/mongosh JavaScript, transaction controls, import wizard, designer, backup/restore or tunnels. See the [MongoDB report](docs/mongodb-2026-11-06.md). |
| Redis | Redis 7+ standalone, Cluster and Sentinel; ACL authentication, independently verified Redis/Sentinel TLS, slot routing and master discovery, multi-master SCAN, native command sessions, shared-grid viewing/editing of string/hash/list/set/zset/stream values, TTL, rename, count and export | Cluster uses database 0; multi-key commands, grid renames and transactions require one slot. Failover revokes existing command sessions and requires explicit recovery. Bytes use base64; editing/export is limited to 1 MiB. Pub/Sub, SQL, table design and backup/restore are unavailable. See the [Redis report](docs/redis-2026-11-06.md). |

Grid editing requires a reliable row identity; views, generated fields, and unsupported objects remain read-only. Availability is checked against the engine, adapter, server permissions, and selected object. Unsupported operations are disabled or rejected explicitly.

See the [Oracle report](docs/oracle-2026-11-06.md), [multi-engine delivery report](docs/multi-engine-reliability-2026-11-04.md), [MariaDB report](docs/mariadb-2026-11-06.md), [SQL Server report](docs/sqlserver-2026-11-06.md), [DuckDB report](docs/duckdb-2026-11-06.md), [PostgreSQL designer support boundaries](docs/postgres-designer-save-2026-11-03.md), and [ClickHouse report](docs/clickhouse-native-2026-11-02.md) for detailed limits and verification. Advanced synchronization, modeling, automation, and other planned tools remain in development.

## Requirements

- Go 1.27.3 or later, as specified in `go.mod`.
- MyGo Native UI fork `v0.7.4-nyadb.1` (the `go.mod` replacement for upstream `v0.7.4`, downloaded automatically).
- On Linux, GTK 3 runtime libraries and a graphical desktop session. On Debian/Ubuntu, install `libgtk-3-0`.
- On Windows and macOS, the operating system's native UI libraries.
- A working system credential vault for saved passwords, AI API keys, and encrypted SQL workspace persistence. Linux uses a desktop Secret Service provider; Windows uses Credential Manager and macOS uses Keychain.
- For DuckDB, install the verified runtime with `python3 tools/install_duckdb_runtime.py`, or use the portable package from `python3 tools/package_duckdb.py`. The Go application still builds with `CGO_ENABLED=0`.
- For PostgreSQL backup/restore, `pg_dump` and `pg_restore` matching the server's major version (14–18), available on `PATH` or in the configured tool directory.

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

Translations are maintained in `locales/zh-Hans.json` and `locales/en.json`. After editing the JSON catalogs, regenerate and validate the Go accessors:

```bash
go tool mygo generate
go tool mygo i18n check
```

The generated `locales/catalog_gen.go` is committed and checked for drift on all three CI platforms. Do not edit it by hand; commit the JSON catalogs and regenerated accessors together. `.gitattributes` enforces LF line endings for Go files and locale JSON catalogs.

Some work-in-progress database screens and runtime diagnostic messages have not yet been translated and may display Chinese text.

## Build and test

```bash
go test ./...
go vet ./...
go tool mygo vet
go build ./cmd/nyadb
python3 -m unittest discover -s tests -v
```

To build an application package with MyGo:

```bash
go tool mygo build
```

The Python visual-tool tests require Pillow. Native packaging requirements vary by operating system. [GitHub Actions](.github/workflows/verify.yml) runs Go tests, static checks, and builds with `CGO_ENABLED=0` on Linux, Windows, and macOS, and checks generated translations for drift.

Database integration tests use explicit environment flags and disposable fixtures; ordinary `go test ./...` does not enable them. See the [unified database verification guide](docs/unified-database-verification.md) and engine reports before running fixture tools. For lifecycle or concurrency changes, run the relevant packages with `go test -race`.

Compilation and headless tests do not establish native GUI behavior or visual parity. Native reports document the Linux environments actually observed; Windows/macOS desktop behavior, IME, and multiple-monitor scaling still require separate verification.

## Configuration and privacy

NyaDB stores connection profiles and application preferences in `NyaDB/config.sqlite` under the directory returned by Go's `os.UserConfigDir()`. Profiles contain non-secret connection settings, including engine, host, port, database, username, TLS, and supported advanced options.

Database passwords, tunnel secrets, and AI API keys are stored separately in the operating system's credential vault when saving is enabled. Unsaved database and tunnel credentials remain temporary. Named queries, SQL drafts/workspace snapshots, and history are encrypted in the local database using a separate key held in the system vault. If the vault or workspace key is unavailable, the app reports the affected persistence features as unavailable and retains existing encrypted records.

Connection testing, connecting, and profiles configured for automatic connection can initiate database network access. AI requests go to the configured service only when requested; including SQL text or loaded schema metadata is opt-in, and suggestions are never executed automatically. See the [AI SQL report](docs/ai-sql-native-2026-11-02.md) for request and storage behavior.

On Unix systems, newly created configuration directories and files use restrictive permissions; on Windows, access is governed by filesystem ACLs. Protect configuration files, exports, backups, and your user account accordingly.

## Repository structure

```text
cmd/nyadb/                        Desktop application entry point
cmd/nyadb-gateway/                Optional HTTPS/WebSocket TCP gateway
main.go                           Entry point for MyGo tooling
internal/bootstrap/               Application initialization
internal/assets/appicon/          Application icon SVG, PNG, and native embedding
internal/assets/lucide/           Embedded Lucide UI icons (ISC license)
internal/assets/connectionicons/  Embedded icon assets and provenance
internal/services/connections/    Connection models, validation, and configuration
internal/db/contract/             Database-neutral models and feature contracts
internal/db/                      SQL, schema, import, and tabular format rules
internal/services/dbruntime/      Provider registration and session publication
internal/services/mysql/          MySQL sessions, queries, metadata, and data operations
internal/services/engines/        PostgreSQL and SQLite adapters
internal/services/clickhouse/     Native ClickHouse adapter
internal/services/sqlserver/      SQL Server TDS adapter
internal/services/duckdb/         Embedded DuckDB adapter and purego C API binding
internal/services/credentials/    System vault, workspace keys, and SSH trust
internal/services/queries/        Encrypted SQL assets, workspace, and history
internal/services/aisql/          AI SQL configuration and HTTP/SSE client
internal/transport/               SSH and HTTPS/WebSocket transport
internal/storage/localdb/         SQLite configuration storage and migrations
internal/ui/shell/                Native windows, tabs, and workbench coordination
internal/ui/connectioncatalog/    Connection type chooser
internal/ui/connectioneditor/     Connection forms, credentials, and async checks
internal/ui/navigation/           Connection navigator and filtering
internal/ui/sessionbrowser/       Session workspaces and metadata jobs
internal/ui/sqlworkspace/         SQL documents and execution
internal/ui/dataworkspace/        Data pages, editing, layout, export and information panels
internal/ui/objectworkspace/      Object lists, table actions, structures and program definitions
internal/ui/theme/                Theme, UI icons, and shared bitmap caches
locales/                          Translation catalogs and generated accessors
docs/                             Design and UI documentation
tools/                            Fixture, packaging, and verification tools
tests/                            Python visual-tool tests
.github/workflows/                Cross-platform CI
```

## Documentation

- [Multi-engine reliability, import, backup/restore, and verification](docs/multi-engine-reliability-2026-11-04.md)
- [Unified database runtime and workspace architecture](docs/unified-database-architecture.md)
- [Unified database verification and fixtures](docs/unified-database-verification.md)
- [PostgreSQL table designer and support boundaries](docs/postgres-designer-save-2026-11-03.md)
- [PostgreSQL/SQLite paging, large fields, and analysis](docs/portable-large-data-2026-11-03.md)
- [ClickHouse native driver and read-only workspace](docs/clickhouse-native-2026-11-02.md)
- [DuckDB runtime, capabilities and verification](docs/duckdb-2026-11-06.md)
- [SSH and NyaDB TCP gateway](docs/transport-gateway.md)
- [AI SQL configuration, requests, and verification](docs/ai-sql-native-2026-11-02.md)
- [Workbench workflows and native verification](docs/navicat-ux-implementation-2026-11-05.md)
- [Global UI optimization and acceptance](docs/ui-global-optimization-2026-11-05.md)
- [MyGo v0.7.4 fork upgrade](docs/MYGO_UPGRADE_0_7_4_2026-11-05.md)
- [Implementation history and milestone reports](docs/implementation-status.md)
- [Application icon source and regeneration](internal/assets/appicon/README.md)
- [Lucide general UI icons](docs/LUCIDE_ICONS.md)
- [Connection icon assets and licenses](docs/ICON_ASSETS.md)

NyaDB is an independent implementation. It does not include Navicat source code, branding, or proprietary assets.
