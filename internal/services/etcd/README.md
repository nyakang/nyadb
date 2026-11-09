# Native etcd v3 foundation

This package is private transport work. It is not registered as a public
connection. Its private profile/editor phase supports persisted configuration
and injected native checks; the public workbench remains pending.

`Open` owns one gRPC channel to an explicit HTTP/HTTPS endpoint. Credentials
are passed separately. Native Status and an explicitly readable probe key
establish the observed version, cluster/member IDs and access. Exact basic
credentials, TLS verification and client certificate authentication are
supported. A successful probe does not imply full-range or administrative
permission. Cluster/member IDs are observations, not incarnation guarantees.

`Prepare` accepts bounded native protobuf Range/Put/Delete/Txn/Status and
lease requests. It validates and deep-copies the request; all copies share
one attempt flag. Transactions reject nesting and preserve native comparisons,
both branches and atomic response semantics. Native results retain int64,
uint64 and binary values; inspection returns deep copies. `PrefixRange` and
`AfterKey` preserve binary ordering. Fixed-revision paging is explicit and
compaction errors never fall back to a newer revision.

The package uses official API stubs, without clientv3 retry/auth interceptors.
gRPC service-config retries and hedging are disabled. The pinned gRPC runtime
can transparently retry calls not processed remotely. Processed writes are
not replayed. Expired/revoked tokens require explicit recovery. A channel may
reconnect internally to the same endpoint; it is not a dedicated physical
database session or a failover/endpoint-discovery guarantee.

Cancellation waits for the local original RPC executor and retires its channel.
It is not server rollback. Missing acknowledgement of a possibly processed
write is retained as unknown. Definite native permission, authentication,
validation and comparable rejection statuses remain distinguishable. Close
cancels first, waits for that executor, and clears tokens and retained results.

Limits are 4 KiB keys, 1 MiB Put/compare values, 128 combined transaction
comparisons/operations, 8 MiB encoded requests and 16 MiB received messages.
Range scans require an explicit limit up to 10,000 or count-only mode. These
are encoded-message/retention limits, not total protobuf/transport heap bounds.
Lease TTL, generated/requested IDs, attached keys and absent TTL=-1 remain
native. Keepalive sends one request; no background keepalive is started.

Run `GOWORK=off go test -race ./internal/services/etcd` for local regression.
Run `python3 tools/verify_etcd_fixture.py --mode basic --race` for an owned,
official digest-pinned Docker node. Modes are anonymous/basic/tls/mtls;
`--version 3.5.35` selects the additional compatibility fixture. Native
acceptance covers 3.7.3 in all four modes and 3.5.35 basic on Linux. The fixture
checks immutable ID and ownership labels before removing only its resources.

`OpenProfile` accepts the pure version-1 etcd profile and a separate temporary
secret. The editor and SQLite storage keep exact text/Base64 key prefixes,
none/basic/certificate authentication, strict TLS and 1..300/1..600 second
connect/request timeouts. Username spaces are retained. Basic with mTLS may
use a certificate CN for native KV permission checks. Empty prefixes probe
one zero-byte key; a successful check cannot prove access to all prefix keys.
The product-specific `ParseEtcdURI` accepts etcd/etcds schemes and a root path;
generic URI routing, chooser publication and provider registration are absent.

Run `python3 tools/verify_etcd_fixture.py --profile --race --mode basic` for
native profile save/reopen/duplicate, original check closure, vault rollback,
actual upstream handshake cancellation, parked success isolation and 72
bilingual wide/small form images alongside the native foundation cases.

`OpenSession` binds the immutable original profile, prefix bytes and native
identity. Neutral `KVRequestsProvider` document leases reserve without I/O;
each lazily opens an independent original channel. At most 32 nonzero, unique
document IDs are reserved. Native method paths and strict protobuf JSON are
captured by pure one-use preparations; foreign, used and busy requests never
queue. Canceling before dispatch does not consume a plan. Prefix remains a
browsing scope; absolute request keys are never rewritten.

`Manager.KVRequests` binds an explicitly injected provider and translates
contextual to published generations only at the runtime boundary. Builtin
registration remains absent. Lease results retain full protobuf bytes and
bounded protobuf JSON (32 MiB) and typed projections (10,000 entries/operations).
Int64/uint64 and bytes remain exact. Projection capture limits do not change an
acknowledged native write's completion. These are capture bounds, not total
heap bounds.

`RecoverKV` revokes and settles the original executor, returns its cached last
result when available, and reserves a fresh lease without authentication,
reading or replay. Recovery checks profile, namespace, prefix and observed
cluster/member/version. Explicit Close clears cached results after waiting;
recovery of an already closed lease cannot resurrect those caches. Callers
retain the immutable result returned by the original Execute, including late
completion or uncertainty after publication changed. Readback is a separate
observation, not proof of the original completion or incarnation.

Run `python3 tools/verify_etcd_fixture.py --session --profile --race --mode basic`
for private native session/runtime and editor regression. The fixture executes
packages sequentially because native token-revocation cases alter that same
owned server's authentication state. The same four 3.7.3 modes and 3.5.35 basic
remain the acceptance matrix.

Public KV workbench/permissions/confirmation flows, Watch/history,
maintenance/auth management, etcd v2, additional versions and cluster operation
remain pending.
