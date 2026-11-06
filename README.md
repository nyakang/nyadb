<p align="center">
  <img src="./internal/assets/appicon/icon.svg" alt="NyaDB" width="128" height="128">
</p>


[![Listed on Awesome MyGo](https://awesomemygo.dev/badge.svg)](https://awesomemygo.dev/apps/nyadb)

NyaDB is a cross-platform, native desktop database workbench built with [Go](https://go.dev/) and the [NyaDB fork of MyGo Native UI](https://github.com/nyakang/mygo). It supports live MySQL, MariaDB, TiDB, OceanBase (MySQL mode), PostgreSQL, CockroachDB, SQLite, ClickHouse, SQL Server, DuckDB, Oracle, MongoDB and Redis connections, SQL or native MongoDB/Redis commands, and database browsing, with data editing and administration tools for supported engines.

![NyaDB workbench preview](docs/ui-preview.png)

This image is a historical headless UI preview. For recent native-window verification, see the [UI optimization report](docs/ui-global-optimization-2026-11-05.md) and [workbench workflow report](docs/navicat-ux-implementation-2026-11-05.md).

## Features

- Native desktop interface targeting Windows, macOS, and Linux, with shared resizable navigation and inspector panes.
- Consistent Lucide SVG operation icons with a separate set of embedded database brand icons.
- Create, edit, duplicate, delete, group, star, and search connection profiles; import connection URIs and optionally save credentials in the system credential vault.
- A unified navigation tree with lazy database/schema and object loading, favorites, and object search.
- Multi-tab SQL editor with real execution, cancellation, syntax highlighting, completion, formatting, EXPLAIN, multiple results, result snapshots, and export.
- Named queries, SQL files, encrypted workspace recovery, and execution history. Disconnection preserves SQL text and requires explicit session recovery before further execution.
- A shared virtualized data grid with filtering, sorting, paging, column layouts, field previews, and export. MySQL, MariaDB, TiDB, OceanBase (MySQL mode), PostgreSQL, CockroachDB, SQLite, SQL Server, DuckDB, and Oracle also support editing, draft undo/redo, transactions, counting, and data analysis where available.
- Table designers, object management, import, backup/restore, and maintenance tools for supported engines, with SQL previews and explicit confirmation for applicable operations.
- MySQL/MariaDB/TiDB/OceanBase SSH tunnels and a NyaDB HTTPS/WebSocket TCP gateway, with host fingerprint trust and optional saved tunnel credentials.
- Optional AI SQL generation, explanation, and optimization through a configurable OpenAI-compatible service, with request previews and suggestions inserted as unexecuted SQL.
- Read-only data dictionaries with Markdown and HTML export.
- Five [color schemes](docs/THEME_SYSTEM.md)—Classic, Ocean, Forest, Rose and Sand—with light, dark and system appearance, live previews, text scaling, and saved preferences.

## Database support

| Engine | Current capabilities | Main limits |
| --- | --- | --- |
| MySQL | SQL, metadata, data editing, table design, object/account management, import/export, backup/restore, maintenance, TLS and SSH/HTTP tunnels | Tools depend on server capabilities, permissions, and object structure. |
| MariaDB | Independent connection profiles and `mariadb://` URIs; SQL, metadata, data editing, table/view design, object management, import/export, backup/restore, maintenance, TLS and SSH/HTTP tunnels | Requires MariaDB 10.11 or later. Account/role management and MySQL-only check enforcement/index visibility are unavailable. Tools retain complete definitions and reject unsupported changes. |
| TiDB | Independent profiles, port 4000 and `tidb://` URIs; SQL, TiDB JSON plans, table/view/sequence browsing, shared-grid editing and transactions, table/view design, accounts/roles/grants, import/export, backup/restore, ANALYZE/ADMIN CHECK, TLS and SSH/HTTP tunnels | Requires TiDB 8.5+. CHECK operations require `tidb_enable_check_constraint=ON`. TiDB executable DDL attributes are preserved; unsupported routines, triggers, events, spatial/fulltext/descending indexes and unsafe attribute edits are rejected. Sequence backups contain definitions and restart values, not allocated/current values. TiDB archives require a TiDB target. See the [TiDB report](docs/tidb-2026-11-06.md). |
| OceanBase (MySQL mode) | Independent `oceanbase://` profiles, tenant usernames and port 2881; SQL, native JSON plans, tables/views/routines/events, reconstructed sequence definitions, shared-grid editing and transactions, import/export, full table copies, owner-preserving view edits, ANALYZE/CHECK, TLS and shared MySQL-family tunnels | Requires OceanBase 4.3.5+. Oracle tenants are rejected. Table design, account management and logical backup/restore are unavailable. View algorithm, definer, SQL SECURITY and CHECK OPTION changes are rejected before execution. Sequence browsing needs tenant metadata privileges. See the [OceanBase report](docs/oceanbase-2026-11-07.md). |
| PostgreSQL | SQL, schema navigation, data editing, table design, ordinary table/view management, import/export, backup/restore and maintenance | Table design supports standard core PostgreSQL 14–18 ordinary tables; unsupported types and definitions are rejected. Backup/restore requires matching `pg_dump`/`pg_restore` tools. SSH/HTTP tunnels are unavailable. |
| CockroachDB | Independent `cockroachdb://` profiles, port 26257 and verified TLS; fixed database/schema navigation, tables/views/sequences/routines, native optimizer plans, shared-grid editing and transactions, bounded serialization retries, count/analysis and import/export | Requires CockroachDB 25.2+. Grid saves retry complete managed batches only after acknowledged `40001`; explicit transactions and SQL are never replayed. Table/view design, object-management dialogs, backup/restore, accounts, maintenance and tunnels are unavailable. See the [CockroachDB report](docs/cockroachdb-2026-11-07.md). |
| SQLite | SQL, attached databases, data editing, table/view design and management, import/export, online backup/restore and maintenance | Backup/restore operates on one database file at a time. Editing requires a reliable row identity. |
| ClickHouse | Native connections, SQL, metadata/structure browsing, a shared read-only data grid, filtering, sorting, counting and export | No grid editing, transactions, table designer, import, backup/restore or tunnels. Paging uses offsets. |
| SQL Server | SQL authentication and TLS, fixed database/schema navigation, T-SQL batches, XML estimated plans, shared-grid editing, transactions, filtering, counting, analysis and import/export | Verified on SQL Server 2019 and 2022. Imports append atomically to existing tables. SQL requires explicit schema qualification. Table/view design, object mutations, backup/restore, account management, integrated/Entra authentication and tunnels are unavailable. |
| Oracle | Service name or SID connections, schema/object browsing, SQL and PL/SQL, EXPLAIN PLAN, shared-grid editing, transactions, field previews, counting, analysis and append import/export | Uses a pure Go driver; no Instant Client required. Views, unsupported types and unreliable keys are read-only. LOB editing requires DBMS_CRYPTO. No designer, administration, backup/restore, TNS aliases, wallets or tunnels. |
| DuckDB | Local files or `:memory:`, schema/object browsing, SQL and JSON EXPLAIN, shared-grid editing, snapshot transactions, filtering, counting, analysis and append import/export | Requires the verified official DuckDB 1.4.4 runtime. Views and unsupported column types are read-only for editing. No designer, object-management dialogs, backup/restore or tunnels. |
| MongoDB | Hosts/seeds or SRV, SCRAM and TLS, database/collection/index browsing, native Extended JSON commands, shared-grid document editing, counting and export | Verified on MongoDB 7.0.28 standalone and replica-set fixtures. Complete-document editing preserves BSON types and checks concurrent changes. No SQL/mongosh JavaScript, transaction controls, import wizard, designer, backup/restore or tunnels. See the [MongoDB report](docs/mongodb-2026-11-06.md). |
| Redis | Redis 7+ standalone, Cluster and Sentinel; ACL authentication, independently verified Redis/Sentinel TLS, slot routing and master discovery, multi-master SCAN, native command sessions, shared-grid viewing/editing of string/hash/list/set/zset/stream values, TTL, rename, count and export | Cluster uses database 0; multi-key commands, grid renames and transactions require one slot. Failover revokes existing command sessions and requires explicit recovery. Bytes use base64; editing/export is limited to 1 MiB. Pub/Sub, SQL, table design and backup/restore are unavailable. See the [Redis report](docs/redis-2026-11-06.md). |

Grid editing requires a reliable row identity; views, generated fields, and unsupported objects remain read-only. Availability is checked against the engine, adapter, server permissions, and selected object. Unsupported operations are disabled or rejected explicitly.

See the [Oracle report](docs/oracle-2026-11-06.md), [multi-engine delivery report](docs/multi-engine-reliability-2026-11-04.md), [MariaDB report](docs/mariadb-2026-11-06.md), [SQL Server report](docs/sqlserver-2026-11-06.md), [DuckDB report](docs/duckdb-2026-11-06.md), [PostgreSQL designer support boundaries](docs/postgres-designer-save-2026-11-03.md), and [ClickHouse report](docs/clickhouse-native-2026-11-02.md) for detailed limits and verification. Advanced synchronization, modeling, automation, and other planned tools remain in development.
