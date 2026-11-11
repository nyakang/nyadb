# Consul original HTTP requests

Consul is a public connection type with independent profiles, versioned local
settings, temporary token/vault handling, `consul://` / `consuls://` URI import
and the existing shared KV request workspace. The runtime publishes only native
KV requests and the `kv` namespace; SQL and relational data tools are unavailable.
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
