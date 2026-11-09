# Native etcd v3 foundation

This package is private transport work. It is not registered as a public
connection, and does not yet provide persisted profiles or a workbench page.

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

Profile/URI/vault/editor, neutral runtime leases and generation binding, KV
workbench/permissions/confirmation flows, Watch/history, maintenance/auth
management, etcd v2, additional versions and cluster operation remain pending.
