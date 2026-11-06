# OceanBase MySQL mode — 2026-11-07

OceanBase is an independent `oceanbase` connection product, using the shared
MySQL transport and the existing SQL, metadata, object and data workspaces.
It does not introduce another grid or a new runtime generation authority.

## Connection contract

- Baseline: OceanBase 4.3.5 LTS or later; Oracle compatibility mode is rejected.
- Direct observer port: **2881**. Proxy endpoints can use their configured port.
- Default user: `root@test`; replace `test` with the actual tenant. The existing
  username field retains the entire tenant/proxy-qualified account string.
- URI example: `oceanbase://user%40tenant@db.example:2881/app?sslmode=verify-full`.
- The provider checks `SELECT VERSION(), @@ob_compatibility_mode` before
  publishing a session. The `5.7.25` protocol prefix does not establish product
  identity or capabilities. Failed checks close their original pools.
- Named configurations, TLS certificates, SSH/HTTP tunnel settings, temporary
  secrets, vault rollback, product filters and native icons use the existing
  MySQL-family configuration and lifecycle contracts. Stored profiles contain
  no password. Real verification below covers direct verified TLS.

## Delivered behavior and boundaries

| Area | Behavior |
| --- | --- |
| SQL | Independent physical leases, cancellation with driver acknowledgement, preserved text on disconnect and explicit recovery; no automatic replay. |
| Execution plans | Native `EXPLAIN FORMAT=JSON`; joins its line-per-row result and retains ordered `CHILD_n` operators, row estimates and estimated time in microseconds. Estimated time is not presented as optimizer cost. Invalid, duplicate, oversized, deep or truncated plans are rejected. |
| Browsing | Qualified databases, tables, views, functions, procedures and events; read-only column/index/foreign-key metadata and complete table/view DDL. Native storage, compression and replica attributes remain visible. |
| Sequences | Read-only definitions reconstructed from `DBA_OB_SEQUENCE_OBJECTS` and tenant database IDs. Stored START/INCREMENT/MIN/MAX/CACHE/CYCLE/ORDER attributes are preserved; cached `LAST_NUMBER` is never presented as the original start or exact current state. |
| Limited accounts | Table browsing works with a schema-only SELECT grant. Without SELECT privileges on `oceanbase.DBA_OB_SEQUENCE_OBJECTS` and `oceanbase.__all_database`, sequence metadata is omitted. Only those specific permission errors are handled as limited visibility; other query errors propagate. |
| Shared grid | Typed decimals and binary values, NULLs, keyset/offset paging, filters, sorting, complete-value and bounded field reads, table information, counting, editing, stale-fingerprint rejection, transactions, import into existing/new tables, export and analysis. |
| Table actions | Shared create/rename/drop/delete/truncate actions and complete structure/data copies. Copying retains native table options and generated/check definitions. The graphical table designer is unavailable; its legacy planner cannot verify native OceanBase storage attributes. |
| View edits | Complete existing normalized query definitions can be edited and verified by the original owner. OceanBase omits security headers from SHOW CREATE VIEW; its metadata owner is included in the optimistic fingerprint and checked before replacement. ALGORITHM, DEFINER, SQL SECURITY and CHECK OPTION requests are rejected before DDL. |
| Programs | Complete function/procedure definitions and SQL modes are retained; replacement and reload verification use the existing object executor. Events remain browsable through their server metadata. |
| Maintenance | ANALYZE and CHECK. ANALYZE returns no status rows; its acknowledged completion is adapted explicitly. OPTIMIZE/REPAIR are not offered by this integration. |
| Administration | MySQL account/role management and logical backup/restore are unavailable. OceanBase uses product-specific authentication and native archive/storage semantics. A foreign OceanBase archive is also rejected by the MySQL restore planner. Database creation/attribute editing uses native SQL; the MySQL database designer cannot verify OceanBase replica attributes. |

## Real fixture and verification

`python3 tools/verify_oceanbase_fixture.py` creates a unique, labelled container,
publishes only a random loopback SQL port, creates a random dedicated account,
configures a temporary certificate and requires SSL for that account. Cleanup
checks both ownership labels and removes the inspected immutable container ID.
Certificates and private environment files are deleted with the fixture.

Pinned image:
`oceanbase/oceanbase-ce:4.3.5.6-106000012026040916`, downloaded digest
`sha256:31086a6900c21c479c2bcd942b6a28c53b17a51f4e9b9eb8eafcc596adfcd2e3`.
The tested server reports `5.7.25-OceanBase_CE-v4.3.5.6` and `MYSQL`.

The tests exercise direct verified TLS and rejection of an untrusted CA,
product mismatch, schema-only metadata permissions, qualified sequences,
exact DECIMAL(40,20) values, binary/NULL/large fields, paging and filtering,
stale edits, generated-column protection, transactions, import/export/analysis,
native plan parsing, full table copies, owner-preserving view changes and
rejection of ignored view security changes, function/procedure replacements,
ANALYZE/CHECK, SQL cancellation and revocation of original leases and backends.
Shell tests exercise the chooser, credential/configuration lifecycle, native
grid save, sequence documents, SQL execution and disconnect recovery.

OceanBase reports an empty `Ssl_cipher` status even for TLS connections. The
tests verify certificate trust during the actual driver handshake rather than
using that unsupported status variable as evidence of encryption.

Normal and race fixture logs and native screenshots are written beneath the
ignored `build/oceanbase/` directory. Run the real fixture with race detection:

```sh
NYADB_OCEANBASE_RACE=1 python3 tools/verify_oceanbase_fixture.py
```

Validation passed: the full CGO-disabled Go suite, the focused lifecycle race
suite, Go/MyGo vet, MyGo catalog generation and i18n checks, all 73 Python
ownership and visual-tool tests, and CGO-disabled Linux amd64, Windows amd64
and macOS arm64 builds. Both the normal and race real-server fixtures passed.
The Chinese and English native editor captures show the MySQL tenant and
username hint. Owned fixture containers and temporary credential directories
were removed. Oracle-mode enterprise runtime behavior is outside this fixture.

## References

- [Official OceanBase Docker image and tenant connection instructions](https://github.com/oceanbase/docker-images/tree/main/oceanbase-ce)
- [Official release images](https://hub.docker.com/r/oceanbase/oceanbase-ce/tags)
- [OceanBase SSL wallet filenames](https://github.com/oceanbase/oceanbase/blob/develop/deps/oblib/src/lib/ob_define.h)
- [OceanBase SSL reload implementation](https://github.com/oceanbase/oceanbase/blob/develop/src/observer/ob_srv_network_frame.cpp)
- [OceanBase system variables](https://github.com/oceanbase/oceanbase/blob/develop/src/share/system_variable/ob_system_variable_factory.cpp)

Compatibility decisions use the pinned real instance above; references to the
development branch explain source behavior and do not expand the tested scope.
