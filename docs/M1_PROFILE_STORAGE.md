# M1-A — Local SQLite storage and connection profiles

**Status:** Implemented and tested as a backend slice on 2026-10-20.
**Evidence:** NyaDB implementation (I), not an observation of Navicat.

## Scope

- `internal/storage/localdb` opens a dedicated on-disk SQLite database and
  runs an idempotent schema migration tracked with `PRAGMA user_version`.
- `internal/services/connections` defines typed profiles and a UI-independent
  service for `Create`, `Get`, `List`, `Update`, and `Delete`.
- Supports non-secret connection metadata for MySQL, PostgreSQL and SQLite.
- All user-supplied data is bound as SQL parameters.
- New database files are created with 0600 permissions; new parent
  directories use 0700. Existing permissions are not modified.
- The table intentionally has no password, token, secret reference or raw DSN
  column. Never pass credentials into profile fields.

`modernc.org/sqlite` is pinned to v1.60.1; no CGO requirement.

## Database schema v1

`connection_profiles` has `id`, `name` (unique), `kind`, `host`, `port`,
`database_name`, `username`, `tls_mode`, `created_at_ms`,
`updated_at_ms`. Schema changes must use explicit forward-only migrations.
Newer unknown schema versions cause an error, not implicit downgrade.

## API integration (not yet wired into bootstrap/UI)

```go
db, err := localdb.Open(configDBPath)
if err != nil {
    return err
}
defer db.Close()

profiles := connections.NewStore(db)
created, err := profiles.Create(ctx, connections.Profile{
    Name: "Local DB",
    Kind: connections.SQLite,
    Database: "/path/to/database.sqlite",
})
```

The caller owns `db.Close()`; services honor provided contexts.
A future application service will own this database and provide profiles
to the MyGo UI without importing UI packages into the storage layer.

## Verification

Environment: Go 1.27.3, Linux amd64, CGO disabled, Go cache at
`/data/.cache/golang`.

| Command | Result |
|---|---|
| `go mod tidy` | PASS |
| `go test ./...` | PASS |
| `go vet ./...` | PASS |
| `go tool mygo vet` | PASS |
| `go tool mygo build` | PASS — Linux build and packages |

Added tests cover create/reopen/migration, rejection of unknown future
schema versions, private initial file permissions, CRUD, type validation,
SQL-parameter binding and not-found cases.

## Remaining work and limitations

- No connection dialog or live connection tree integration yet. Existing
  demo connections are still mock UI data.
- No persistent application workspace, SQL draft recovery or splitter state.
- No password handling, OS keychain integration, real database sessions,
  cancellation, metadata access, or result paging.
- File permissions are only enforced for newly created files. Operators must
  also protect backups, parent directories and existing configuration files.
- GUI smoke tests, macOS/Windows builds and original-product differential
  verification were not performed in this phase.
