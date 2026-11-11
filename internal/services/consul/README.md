# Consul original HTTP requests

Consul is a public connection type with independent profiles, versioned local
settings, temporary token/vault handling, `consul://` / `consuls://` URI import
and the existing shared KV request workspace. The runtime publishes only native
KV requests, the `kv` namespace and its sole shared key grid with original
confirmed atomic writes; SQL and relational data tools are unavailable.
HTTP defaults to port 8500; verified HTTPS defaults to 8501 for `consuls://`.
Authentication is anonymous or a separate ACL token. Client certificates work
independently of token authorization. Datacenter, exact text prefix, consistency,
CA/server name, mTLS files and finite timeouts stay in the original profile.

Each document owns its original physical lease and immutable one-use plans.
Writes show their actual HTTP method/path before explicit confirmation. Complete
operation records keep body bytes in Base64, parsed headers/trailers, unsigned
indices/flags as strings, session UUIDs, native transaction errors and independent
capture/EOF/ended/acknowledged/succeeded states. Original body exports preserve
complete empty bodies and mark incomplete prefixes separately. The body preview
is bounded to 64 KiB. Request files save text without executing it.

Structured request tools author absolute keys/prefixes, original Base64 bytes,
optional exact uint64 Flags/CAS (including explicit zero), read/list/raw requests,
put/delete and original transaction JSON. A captured neutral builder is immutable
and performs no I/O, connection recovery or dispatch. It generates the same HTTP
JSON consumed by the sole shared document and its existing one-use confirmation.
Incompatible or hidden inputs are rejected; CAS cannot combine with recursive
delete. Form changes and editor changes (including edit/undo) revoke old authority.
Each tool retains its own inputs on modal close, parking and disconnect. Unapplied
inputs participate in leave decisions; request files save only editor text.
Raw GET rejects noncanonical paths because a transaction response is not a raw
HTTP value; ordinary read/list/put/delete use the native transaction fallback.

Disconnect retains text and receipts. Recovery checks the original profile,
namespace, prefix, HTTP identity/configuration and published generation, then
transfers complete original evidence without making requests. An original unknown
write remains independent of successful later reads and blocks further writes.
Only the UI thread consumes private buffered completions; parked results wait for
their original document to activate. Closing cancels and joins the original
executor, releases leases and clears document caches.

`New` captures one original endpoint, datacenter, initial browsing prefix,
consistency policy and temporary ACL token. It performs no HTTP requests.
`Open` observes the contacted agent, leader and initial key listing. KV-only tokens
may be denied agent identity; version and agent identity then remain unavailable.
The browsing prefix is not an authorization boundary. Enterprise namespaces and
partitions are explicitly unsupported in this stage.

Transport uses HTTP/1.1, direct connections, verified TLS 1.2+ and optional PEM CA
and client certificates. Authentication stays in `X-Consul-Token`, outside request
snapshots and URLs. There is no redirect, retry, automatic reconnect or replay.
The package does not depend on Consul SDKs, UI, storage or runtime services.

Supported requests include:

- KV reads, raw bytes, key/prefix listings and explicit index/wait blocking reads.
- Put/delete, exact uint64 CAS and flags, and session acquire/release conditions.
- Native transactions with at most 64 KV operations: reads, comparisons, set/CAS,
  delete, lock and unlock. Consul's complete transaction results and conflicts are
  retained without inventing per-operation result slots.
- Session create, information/list/node reads, one-shot renew and destroy.
- Read-only agent, leader/peer, catalog and health requests.

HTTP paths can clean keys. Noncanonical keys therefore use JSON transactions.
Repeated/trailing slashes keep their exact key identity. Consul 1.22 and 2.0 differ
on traversal and leading-slash keys; a native rejection is retained instead of
silently targeting a cleaned key. A transaction prefix listing contains the full
native entries even when the caller initially requested names only.

Every prepared plan owns a deep copy of bytes/query fields and grants one use to
its original client. Inspection copies grant no execution authority. Only one
request executes per client. Close cancels and joins that original executor, then
clears the client's temporary token and cached responses. The caller's returned
response remains independent of cache disposal.

HTTP capture/EOF, parsed headers, native acknowledgement, success and projections
are distinct. Original body bytes, response protocol and parsed header/trailer
values stay available even on protocol/projection errors. Uint64 indices and
flags retain exact precision. Field presence, JSON null and empty values remain
distinct; unknown fields stay in the original body.

Native false conditions, transaction 409 and explicit 4xx rejection remain known
failures. HTTP 408, 5xx, redirects, malformed acknowledgements or incomplete bodies
after write dispatch remain unknown. Unknown writes block every later write,
including earlier prepared plans. Later explicit reads do not clear the original
unknown receipt. Transport interruption retires the original client. Recovery
must create an explicitly selected new client and must not replay a request.

Bounds are 8 MiB request JSON, 48 MiB response capture, 512 KiB individual KV value,
4096 UTF-8 bytes per key, 10,000 projected entries and 64 transaction operations.
Incomplete or over-budget captures never become complete acknowledgements.
Continuous watch/renewal, ACL management, registration/config entries, archives,
multi-agent/Enterprise support remain future scope.

Run isolated official acceptance with:

```sh
python3 tools/verify_consul_fixture.py --public --mode anonymous --race
python3 tools/verify_consul_fixture.py --public --mode token --race
python3 tools/verify_consul_fixture.py --public --mode tls --race
python3 tools/verify_consul_fixture.py --public --mode mtls --race
python3 tools/verify_consul_fixture.py --public --version 1.22.3 --mode token --race
```

The wrapper pins official image digests, checks version/node identity, requires
the exact 22-parent native/document/runtime/public set, 76 bilingual images
and matching source hashes, and removes only its own
original container and temporary certificates/tokens. Receipts and sanitized
diagnostics are written to ignored `build/consul`. `--probe` is diagnostic only.

Structured tool acceptance uses `--tools --race` with the same five modes above.
It retains all 22 original parents and adds three mandatory runtime/workspace tool
parents, 204 bilingual images per fixture, actual processed reply-fault scenarios
and checks for offline generation, exact bytes/zero/CAS, transaction conflicts,
original recovery/parking, file text and unapplied-input leave guards.

The public shared grid reserves four independent original
HTTP executors for pages, count, fields and export. Reservation performs no I/O.
Each page reads one complete bounded recurse/get-tree observation, orders exact
UTF-8 key bytes and applies a document-signed last-key cursor. Pages are live;
HTTP indices do not provide a fixed MVCC snapshot. Refresh invalidates old cursors.
Count and export each read an independent complete visible-prefix observation.
The complete observation must stay within 10,000 entries, 32 MiB of original
values and the 48 MiB HTTP capture limit; oversized export fails before emitting
rows. Field previews preserve at most 1024 bytes, while explicit full-value reads
retain the original bytes up to Consul's 512 KiB limit. Unsigned indices/flags and
missing, null and empty session/value states remain distinct in captured evidence.
Close joins original work and clears cursors and receipts. Failed page reads
require explicit original-document reopening; requests never select a replacement
executor or replay automatically. Original metadata publishes the single qualified
`kv/keyspace/keys` object. Navigation, object-toolbar/context/search and View
entries use the existing shared grid, alongside retained native HTTP documents.
The seven-column structure is read-only and presents native nullability and
generated fields, without relational default/autoincrement/index/foreign-key
controls. SQL, explicit multi-step transactions, relational import,
filtering and user-defined sorting remain unavailable.

Private data acceptance uses `--data-session --race` with the same five modes.
It keeps all 25 native/document/runtime/public/tool parents and adds four data
parents, including actual processed-read cancellation/reply loss and a native
prefix exceeding the complete-value budget. The existing 204 bilingual request
workspace images are regression evidence; they do not claim a public data grid.

Public grid acceptance uses `--public-grid --race` with the same five official
modes. It retains all 29 native/document/runtime/public/tool/data parents and adds
six mandatory runtime, shared-grid and Shell parents. Each configuration requires
56 Chinese/English wide/narrow grid images and the existing 204 HTTP-workspace
regression images. Original cursor proofs and cached row observations are signed
per document; independent HTTP index changes do not invalidate unchanged-row
export verification. Failed page reads leave count, field and export executors
independent. Failed public handshakes return no session for runtime cleanup.


Public grid mutations validate both signed original snapshot
payload modes and captures complete before-images and readable native transaction
parameters in a bounded, immutable, one-attempt plan. A dormant write executor
belongs to the same original logical document; four read executors remain
independent. Insert/update/delete and rename batches use original ModifyIndex
checks, CAS/delete-CAS, destination absence and complete in-transaction receipts.
Originally Session-locked rows are rejected: Consul CAS can modify a locked key.
Lock acquisition/release/destruction/expiration changes ModifyIndex, so an
intervening lock change rejects the original batch atomically. These observations
are not a claim of agent/database incarnation or a frozen prefix range.

Only key, complete binary value and exact uint64 Flags are authored. Explicit
NULL/default writes are rejected; authored empty bytes are saved as Consul's
native `Value: null`. Generated indices/Session/LockIndex stay read-only. Limits
are 64 actual native operations, 512 KiB/value and 8 MiB for each encoded HTTP
request and document envelope. The server has a separate limit: official default
`limits.txn_max_req_len` is 512 KiB. Large values or batches can receive a known
413 rejection; the adapter never changes that configuration or splits/replays a
batch. Complete 512 KiB value acceptance explicitly configures this fixture limit
to 8 MiB. Original plans reserve a conservative 256 MiB in the existing session
budget for all captured values, snapshots, JSON and processing copies.

Malformed or incomplete successful receipts retain their original transport ACK
and a separate application uncertainty barrier. Auxiliary reads cannot replace
that receipt or authorize another write. Cancel/close waits for the original
executor and receipt classification before releasing caches and plan budget.
The public grid uses the existing sole shared editor, immutable complete key
confirmation and native receipt viewer. Original Session-locked rows are visibly
read-only. Complete corresponding transaction ACK/body confirms a commit even
when optional HTTP index/leader metadata is malformed; those original warnings
remain in the receipt. Incomplete or noncorresponding bodies remain unknown.
Parked writes wait for the original profile, generation replacement never selects
a new executor, and explicit unknown-document close requires a local discard
decision after copying/exporting evidence. Conditional copy and prefix-wide
conditional set semantics,
continuous Watch/TTL, administration, archives, Enterprise/HA and full reference
alignment remain outstanding.

```sh
python3 tools/verify_consul_fixture.py --data-mutations --txn-max-request 8388608 --mode token --race
python3 tools/verify_consul_fixture.py --mutation-limit-probe --mode token --race
```

The mutation fixture adds six mandatory adapter parents to original native,
document/runtime and read-document regression (35 parents total). Use the same
five official version/transport configurations as public acceptance. The default
request-limit probe keeps the official server configuration and independently
requires a genuine 413, zero partial writes and no splitting/replay. These commands independently verify the adapter foundation and server limits.

Full public write acceptance uses `--public-grid-writes --txn-max-request 8388608
--race` in the same five official modes. It retains the complete original
regression, adds nine public Manager/grid/Shell mutation parents (50 mandatory parents
total), requires 196 grid and 204 HTTP-workspace images per mode, and checks complete
preview/receipt copies and files, actual processed write reply loss and no replay,
and requires corresponding bilingual wide/narrow images. `--grid-write-probe`
is diagnostic only and does not replace complete regression acceptance.
