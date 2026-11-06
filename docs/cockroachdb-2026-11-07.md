# CockroachDB — 2026-11-07

CockroachDB is an independent `cockroachdb` connection product. It shares the
PostgreSQL wire transport, connection options and workspaces, with native
catalog, sequence, optimizer and transaction handling. No additional driver
or parallel data grid is introduced.

## Connection and persistence

- Requires a recognized CockroachDB 25.2+ server, checked before publication.
- Defaults: localhost, port 26257, database `defaultdb`, user `root`, TLS
  `verify-full`. Production clusters require their configured account and CA.
- Supports `cockroachdb://` and `cockroach://` URI import and PostgreSQL-family
  TLS modes, CA/client certificates, application name, default schema, pool
  limits and timeouts. An explicit database and username are required.
- Public options retain their versioned storage envelope and independent kind.
  Credentials use the existing vault; failed vault updates roll back profiles.
  Existing PostgreSQL profiles and credential accounts remain compatible.
- SQL documents retain separate physical leases. Runtime generations revoke
  replaced sessions and their original query/data bindings. Disconnect retains
  SQL text and requires explicit recovery without replaying commands.

## Supported behavior

| Area | Behavior and limits |
| --- | --- |
| Navigation | Fixed database with qualified schemas; tables, views, sequences and routines. Inaccessible sequences are excluded before definition loading. |
| Structures | Native `SHOW COLUMNS`, `SHOW INDEXES` and complete `SHOW CREATE` definitions, with generated fields and foreign keys. Hidden fields do not become edit keys. Native DDL retains storage, family and locality clauses. |
| Row identity | Visible nonnullable unique keys only; partial, expression, invalid and collated keys are excluded. Objects without reliable keys are read-only. |
| Shared grid | Filtering, sorting, offset/keyset paging, layouts, typed row values, draft editing, transactions, clipboard and export. Collated/nullable/unsupported sorts cannot publish keyset cursors. |
| Large values | Explicit previews are bounded to 1024 characters/bytes. Complete editing is limited to 1 MiB. Decimal precision, binary values and SQL NULL remain typed. |
| Plans | `EXPLAIN (OPT, VERBOSE)` yields a native operator tree, row estimates and optimizer costs without executing the statement. Malformed, truncated and oversized plans are rejected. |
| Sequences | Numeric settings come from native `SHOW CREATE SEQUENCE`, retaining START/INCREMENT/MIN/MAX/CACHE/CYCLE and the full server definition. Unsupported emitted option grammar is rejected instead of inventing settings. |
| Import/export | Existing/new table atomic imports, shared format inference and exact typed export. Streaming sources are not replayed automatically. |
| Tools | Graphical table/view designers, object mutations, PostgreSQL backup/restore, account tools, maintenance and SSH/HTTP tunnels are unavailable. Read-only structures and program definitions remain accessible. |

## Serialization and acknowledgement

Managed grid saves own immutable copies of edits and retry the entire batch
only after a server-acknowledged SQLSTATE `40001`. The limit is five attempts,
with 25/50/100/200 ms waits. Every attempt closes its original transaction before
the next begins. Rolled-back saved-row snapshots are discarded, including when
the retry limit is reached. Optimistic original-row comparisons still apply on
every attempt, so an intervening change produces a conflict instead of being
overwritten.

Explicit user transactions settle a serialization error with full rollback,
clearing earlier staged writes. Ordinary batch savepoints cannot recover a
CockroachDB transaction's prior reads. The SQL editor preserves SQLSTATE and a
whole-transaction retry hint; it does not replay user SQL. Confirmed commits,
uncertain acknowledgements, incomplete verification, failed rollback and
cancelled requests are not retried. SQLSTATE `40003` remains uncertain.

CockroachDB normally enables `autocommit_before_ddl`. Connections set it to
`off` so a new-table import and its writes roll back together; a duplicate-key
failure was verified to leave no created table.

## Real-server findings and verification

The disposable TLS fixture uses official `cockroachdb/cockroach:v25.2.4`, image
digest `sha256:46077f1fdacf2ae8f327994564911484dbb7dfdeea96fd838ec3df0f77418798`.
The tested server reports CockroachDB CCL v25.2.4. Verification uses one local
node; multi-node contention and CockroachDB Cloud were not exercised.

Two catalog/protocol differences were verified directly:

- `EXPLAIN (OPT, JSON)` is rejected. DISTSQL JSON contains a processor graph;
  native OPT VERBOSE is used for the optimizer tree.
- `pg_sequence.seqcache` reports 1 for a sequence declared with CACHE 7. Its
  native CREATE definition correctly retains 7, so PostgreSQL placeholder
  sequence settings are not used.

Run fresh owned fixtures from the repository root:

```sh
python3 tools/verify_cockroach_fixture.py
NYADB_COCKROACH_RACE=1 python3 tools/verify_cockroach_fixture.py
```

The script creates a uniquely labelled container, random loopback SQL port,
temporary CA/node/client certificates and account credentials. Cleanup verifies
both product and nonce labels and removes only the inspected container ID.
Credentials stay in a private temporary directory and are removed on exit.
Logs and headless UI captures go to ignored `build/cockroach/`.

Real-server tests cover verified/untrusted TLS, native schemas/DDL/sequences,
generated fields and foreign keys, exact DECIMAL/binary/NULL values, paging,
bounded field reads, edits and stale conflicts, optimizer plans, analysis,
import rollback, export precision, SQL-pool isolation, generation replacement,
query cancellation, the shared-grid save flow and disconnect/recovery. Server
retry injection verifies whole-transaction rollback, bounded exhaustion and a
subsequent successful batch applied exactly once. Commit-result regressions
also verify that exhausted commit-time retries cannot expose rolled-back rows.

The UI captures exercise MyGo components through `ui.NewTester`; they are
headless evidence, not screenshots from a native desktop session.

Final validation passed with Go 1.27.3 and the pinned MyGo fork:

- `CGO_ENABLED=0 go test ./...`, with focused regressions after final changes.
- The repository's 15-package lifecycle `go test -race` suite, plus final
  CockroachDB/toolbar regressions and both disposable real-server fixture modes.
- `go vet ./...`, `go tool mygo vet`, generation and i18n checks (2000 messages).
- All 76 Python tests, including fixture ownership and malformed-inspection
  cleanup cases.
- `CGO_ENABLED=0` amd64 builds for Linux, Windows and macOS.
- Changed-source formatting, `git diff --check` and fixture cleanup checks.
