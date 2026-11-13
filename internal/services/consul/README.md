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

The Agent read foundation adds pure `AgentSelfRequest`,
`AgentServicesRequest` and `AgentChecksRequest` builders. Services/checks retain
omitted or explicitly empty native filters. These endpoints contact the original
agent and do not inherit configured datacenter, browsing prefix or consistency;
explicit namespace, partition, blocking-index and wait parameters are rejected.
An ACL-filtered empty 200 list remains a scoped response, not proof of global
absence or permission; native 403 rejection retains its complete original body.
The complete original map response and HTTP evidence remain independent of any
later projection or unknown write. Local check updates and other Agent
management tools remain unpublished. The Agent read
forms and local-map browser share the existing original KV document lifecycle;
`--agent-browser-probe` requires native workspace/Shell forms, map details,
complete value copy/export, parking, disconnect and evidence-only recovery.
`--agent-read-probe` verifies three Agent parents together with all 26 existing
health Watch/browser and HTTP Watch regression parents; it does not claim the
unrelated complete public Session/grid matrix.

`AgentServiceRegisterRequest` and `AgentServiceDeregisterRequest` use the same
original one-use HTTP/document lease. Registration preserves the entire authored
JSON, checks, sidecar definitions and unknown fields. Replacement omitted,
explicit false, true and empty query values retain native semantics. Embedded
tokens, namespace and partition routing are rejected before I/O. Deregistration
uses the exact original service ID; paths Consul may clean are rejected instead
of using a KV transaction fallback. Neither operation inherits configured
datacenter, prefix or consistency.

Service writes acknowledge only a complete empty HTTP 200 success, the original
403 ACL rejection, or an original deregistration 404. Other failures remain
unknown: registration can add the main service before a later sidecar error.
Complete error receipts survive subsequent reads, publication replacement and
pure recovery, with further writes blocked independently. Agent self/map reads
provide observations, without an incarnation lock or atomic inventory guarantee.
`--agent-service-probe` requires these native service and runtime generation
parents together with the accepted Agent/health/Watch browser regressions;
`--agent-service-native-probe` is a diagnostic subset. Structured service
register/deregister forms retain the complete definition, exact service ID and
omitted/empty/false/true replacement parameters in the original request workspace.
They only generate text; immutable confirmation and the original executor remain
mandatory. Input/receipt copy and export preserve complete original snapshots.
`--public-agent-service-probe` requires actual workspace/Shell service writes,
processed reply faults, parking, evidence-only recovery, original session-tool
fault regressions and all accepted Agent/health/Watch browser parents. Local
check/TTL/maintenance, ACL/catalog and other Agent management tools remain separate.

`AgentCheckRegisterRequest` and `AgentCheckDeregisterRequest` retain complete
native check definitions and exact IDs through the same Client/document lease
and runtime publication. Omitted, empty and null IDs remain authored; the Agent
chooses defaults. Re-registering an existing TTL check can update its definition
while retaining its prior status. A later local-map read cannot confirm a lost
write receipt. Complete empty HTTP 200 responses acknowledge success; proved
403 ACL refusal and deregistration 404 acknowledge denial. Other complete or
partial errors retain the original unknown-write barrier and complete receipts.
Check writes reject unsupported routing/authentication and cleaned paths before
I/O. `--agent-check-probe` requires five native check/runtime parents plus all
46 accepted public service/Agent/health/Watch parents and their 576 images.
The public regression phase finishes before new critical node checks, whose
native status affects every service's passing-only health read in that fixture.
The two structured check tools author complete native definitions or original
removal IDs without dispatching. They use the existing immutable confirmation,
complete receipts, request/input files and unknown-write barrier. Publication
requires `--public-agent-check-probe`: four workspace/Shell parents and 292
bilingual wide/narrow images, with all 51 existing check/service/browser parents
and 576 original images retained. The phases preserve every original assertion
before node-level writes can affect passing-only health filters. TTL updates,
maintenance, ACL/catalog and other Agent management remain separate.

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

Native false conditions, transaction 409 and supported endpoint 4xx rejection remain known
failures. HTTP 408, 5xx, redirects, malformed acknowledgements or incomplete bodies
after write dispatch remain unknown. Unknown writes block every later write,
including earlier prepared plans. Later explicit reads do not clear the original
unknown receipt. Transport interruption retires the original client. Recovery
must create an explicitly selected new client and must not replay a request.

Bounds are 8 MiB request JSON, 48 MiB response capture, 512 KiB individual KV value,
4096 UTF-8 bytes per key, 10,000 projected entries and 64 transaction operations.
Incomplete or over-budget captures never become complete acknowledgements.
Continuous renewal, ACL management, registration/config entries, archives,
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
decision after copying/exporting evidence. Prefix-wide conditional set semantics,
TTL renewal, administration, archives, Enterprise/HA and full reference
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


Conditional copy uses a separate `DataKeyCopy` request
and original `PreviewKeyCopy` binding. Ordinary insert/update savers reject copy.
It verifies the signed original source in either payload mode, loads its complete
value and Flags through the original write executor, and creates one immutable
plan containing source index check, destination absence, zero-CAS destination,
complete unchanged source and complete saved destination receipts. Source locks,
source changes and destination races reject atomically. Original indices and
Session/LockIndex are not transferred to the new key. Incomplete source or target
correspondence retains the original unknown receipt and blocks replay.

The runtime publishes `PreviewKeyCopy` to the sole shared grid. Its toolbar and
row menu open an original-source form for an exact destination key. The form
performs no I/O and requires a clean, originally unlocked source. Preview creates
a key-only conditional copy draft; complete bytes come from the original adapter,
including values beyond the bounded grid preview. The complete immutable native
plan and source observation require explicit confirmation. The source is preserved;
the destination becomes editable only after a corresponding complete saved receipt.
Cancel retains the conditional draft; undo/redo and discard use shared draft history.
Pending copy drafts block mixed authoring. Parking, generation replacement,
cancellation, unknown receipts and local leave decisions retain original ownership.

Full public acceptance retains all 56 native/public-write regression parents and
adds six Manager/grid/Shell copy parents (62/configuration, 560 bilingual images).
Use the same five official version/transport configurations as previous acceptance.
The independent default-limit probe requires an actual unchanged server limit and
one unsplit original 413 rejection. Probe-only commands are diagnostic.

```sh
python3 tools/verify_consul_fixture.py --public-key-copy --txn-max-request 8388608 --mode token --race
python3 tools/verify_consul_fixture.py --public-copy-probe --txn-max-request 8388608 --mode token
python3 tools/verify_consul_fixture.py --copy-limit-probe --mode token --race
```


The shared grid offers explicit captured-prefix candidate deletion through a
qualified `DataKeyPrefixDelete` request. The form performs no I/O; preview reads
the complete original prefix and retains full observations, values and Flags.
Confirmation submits that original plan once. Candidates may lie outside the
current grid page; no visible row is substituted for a complete candidate.

The original complete observation and the later native transaction receipt stay
separate and can be copied or exported in full. Cancellation waits for the
original executor; parked completions wait for original activation. Replacement
generations cannot select a new executor. Incomplete, missing or uncertain
receipts retain the original candidate scope and block subsequent writes or
replay; a later successful read does not confirm the original write.

One atomic plan permits at most 21 candidates (three real operations per key).
The extra complete captured body plus decoded values has an 8 MiB budget within
the original plan reservation. Empty roots, foreign prefixes, ACL-filtered or
incomplete observations, Session-locked candidates and excess counts/bytes are
rejected before submission. Original index checks, delete-CAS and corresponding
absence receipts apply to the captured finite set; later-created keys survive.
There is no range-absence or prefix MVCC guarantee, automatic split or replay.
Explicit multiple batches and complete live-prefix delete-tree remain gaps.

```sh
python3 tools/verify_consul_fixture.py --public-prefix-delete --txn-max-request 8388608 --mode token --race
python3 tools/verify_consul_fixture.py --public-prefix-probe --txn-max-request 8388608 --mode token --race
```

Full acceptance retains every 67-parent prefix foundation/public-copy regression
and adds six original public Manager/grid/Shell parents (73/configuration).
The probe is diagnostic only. Both official default-limit copy probes remain
part of the final matrix. ACL filtering and native write denial are exercised
in configurations with fixture-restricted tokens. Private foundation records
retain their historical unpublished scope.

### Complete captured sets and explicit batches

`captureKeyPrefixDelete` owns one complete original observation and a fixed list
of at most 4096 candidates. It rejects empty/foreign prefixes, ACL-filtered or
locked candidates, and original body plus decoded values over 8 MiB. The public
`DataRuntime.CaptureKeyPrefixDelete` binds the original data-document lease.
Each ordered batch contains at most 21 original keys, needs its
own immutable preview and explicit submission, and never recaptures later keys.
Earlier confirmed batches remain committed when a later batch fails; there is no
cross-batch atomicity. Known failed batches stop the capture; unknown or incomplete
receipts forbid both later batches and replay. Closing cancels the original work;
processed writes still settle with their original acknowledgement and uncertainty.

The capture reserves 64 MiB, each prepared plan reserves 256 MiB, and submission
reserves another 256 MiB for its complete serialized receipt before I/O, alongside
the original executor's 128 MiB pending receipt. The actual serialized batch
receipts remain charged to the original session until capture closure; unspent
reservation is released after acknowledgement. Snapshot inspection cannot mutate
original signatures, candidate values, batch scope or receipt strings. The runtime
boundary validates the original profile/generation, complete candidate identity,
ordered batch and corresponding originals before forwarding to the captured raw
provider. It rejects stopped/in-flight/uncertain batches before provider execution.

`--prefix-delete-set --race --txn-max-request 8388608 --test-timeout 1800
--memory-mib 2048` verifies the five private batch parents plus all 73 retained
public regression parents. `--prefix-set-probe` is a separately labelled diagnostic
and cannot claim complete acceptance or combine with another scope. The default
copy-limit probes continue to use the original 512 KiB transaction limit.

The shared grid opens the complete-capture form without I/O, then keeps the
fixed original candidate set independently of its bounded page. Every batch has
its own immutable confirmation. Only corresponding complete original receipts
advance the ledger or refresh the page; later batches never start automatically.
Observation JSON, the complete ledger and individual receipts remain independently
available for complete clipboard copies and immutable file exports. Parking
consumes only on the original profile; cancellation and document close await the
original executor. Unknown results retain the original close guard and prohibit
later batches, new captures and replay, even after a successful read.

`--public-prefix-set --race --txn-max-request 8388608 --test-timeout 1800
--memory-mib 2048` retains all 78 earlier parents and adds six public
Manager/grid/Shell parents (84/configuration). `--public-prefix-set-probe` selects
only those six parents and is explicitly diagnostic. Final acceptance retains
the five official configurations and two original default-limit copy probes.

The optional `KVHTTPWatchProvider` reserves a separate document lease without
I/O. The existing KV workspace exposes **HTTP Watch** alongside original HTTP
requests; it retains the original request text and independent write receipts. `Prepare`
captures one exact/prefix target, its explicitly authored canonical observation
prefix (including deliberately empty root), unsigned start index, wait, count
and byte limits. The actual read is native indexed HTTP recurse; original target
keys are matched separately without path normalization. Read-only txn does not
supply a blocking index or honour wait and is never substituted for this path.

A run uses one original captured client and at most one in-flight read. Original
wait is 100 ms..10 min and must fit the configured request timeout with native
wait/16 jitter and connect timeout allowance; neither timeout nor wait is changed.
Missing, invalid, zero or backward server indices stop with original evidence.
These are current observations, not etcd events, revisions, historical coverage,
or write acknowledgements. A restricted recurse can return 404 with
ACLFiltered=true; absence is never a deletion event or proof of a complete range.

Each start reserves its authored archive limit (up to 256 MiB) plus a 128 MiB
terminal slot before connecting. Up to 256 observations are retained; a complete
result exceeding the authored byte limit occupies that explicit terminal slot
and stops the run. Count exhaustion sends no extra read. Private buffered Poll
transfers immutable copies while Snapshot retains complete originals. Stop and
close await original executor ACK; a Stop caller timeout does not prove ACK.
Session retirement retains receipts; run/document close or explicit settled
ClearObservations releases them. A new start requires a fresh one-use preparation
and explicit release of the earlier archive. Evidence recovery transfers only
original receipts and accounting to a new unconnected lease, never resuming I/O.
Watch does not read or clear any independent unknown write receipt.

`--http-watch --race --txn-max-request 8388608 --test-timeout 1800 --memory-mib
2048` appends eight private adapter/runtime parents to all 84 public batch
regression parents (92/configuration), retaining their exact image sets.
`--http-watch-probe` runs only those eight private parents and cannot combine with
another acceptance or diagnostic scope. The public workspace acceptance below
adds original tab parking, indexed preview, complete exports and explicit recovery.

A settled old run's late Close validates its original archive owner without
waiting on a newer run's executor. Evidence transfer and release are atomic under
that same owner, preserving one charged archive during concurrent recovery/Close.


## Public original HTTP Watch workspace

Choose **HTTP Watch** in the existing native KV document and author the target,
exact/prefix matching, actual canonical observation prefix (including an explicitly
empty root), unsigned starting index, native wait, observation count and byte
budget. The actual read range is displayed separately from the target. Preparation
and reservation do no I/O; **Start HTTP Watch** authorizes one captured request.
The original connection timeout and jitter constraints are validated without
changing the wait or connection settings.

The original run owns complete evidence. Indexed status polling consumes its
private pending buffer only on the active original document's UI thread; selecting
an observation clones that ordinal rather than the entire archive. Preview stays
bounded. Adjacent observed key/value/Flags/session differences preserve empty,
missing and NULL distinctions and do not claim all intervening events. ACL-filtered
or incomplete ranges do not produce inferred deletion comparisons.

Stop, cancellation, disconnect and close wait for the original executor's
acknowledgement. Parked results await their original tab. Explicit recovery
transfers only settled original evidence into a fresh unconnected lease; even a
cancelled late transfer retains the archive. Copy/export capture immutable complete
original status, headers, body, typed entries, match ordinals and original scope
before clipboard/picker/disk waits. Indices and Flags retain uint64 text precision.
New reads never confirm unknown writes. Clearing the original archive and starting
again are separate explicit actions; stopping or recovering does not resume reads.

`--public-http-watch --race --txn-max-request 8388608 --test-timeout 1800
--memory-mib 2048` retains all 92 original private Watch/public regression parents
and 808 original images per configuration, adds four required public Watch parents
and 80 original-scope UI images. `--public-http-watch-probe` is a separate diagnostic
scope. Final acceptance uses official 2.0.2 anonymous/token/TLS/mTLS and 1.22.3 token
fixtures, plus the two original default transaction-limit probes. HTTP Session
renewal is described below. Live-prefix deletion, management/registration, archives,
Enterprise/HA and the complete database/middleware alignment remain separate work.


## Bounded HTTP Session renewal foundation

`KVHTTPSessionRenewalProvider` reserves a distinct lease for one original profile,
configuration and document. Its immutable one-use preparation authorizes a UUID,
an interval (100 ms–1 hour), at most 256 PUTs, and at most 256 MiB of evidence.
Start first reads that UUID's native session info and preserves the complete HTTP
response. Missing/invalid UUID, TTL, node, lock delay or behavior prevents renewal;
the native TTL must exceed the authored interval. The first PUT follows that
observation; each subsequent interval begins after the preceding response, with
at most one request in flight. No timer claims an exact server expiry time.

Each actual PUT has its own full status/headers/trailers/body and acknowledgement.
A changed or incomplete session projection stops further requests without
rewriting a known native ACK. Actual lost/partial/408/5xx/timeout/cancellation or
mismatched-UUID replies retain an unknown-write terminal and prohibit reuse,
clearing receipts, or further writes on that lease. A later read cannot confirm
that original write. The archive includes initial GET and every PUT, with local
receipt ordinals/time kept separate from native fields. Indexed polling never
evicts the original archive; copying a snapshot cannot mutate it.

Start reserves the entire authored budget plus a 128 MiB terminal slot before
connecting. A complete or partial final receipt above the authored budget is
retained and terminates the run, including when the initial GET exhausts the
budget. Request, data, Watch and renewal leases share the 32-lease limit and the
original session receipt budget. Stop/close/retirement wait for the original
executor; stop never destroys the native Session. Recovery transfers only settled
original evidence, accounting and the unknown barrier into an unconnected lease,
with no new GET/PUT and no transferred preparation. Late original run Close
cannot delete a transferred archive or cancel a newer run.

`--session-renewal --race --txn-max-request 8388608 --test-timeout 1800
--memory-mib 2048` requires the seven new original adapter/runtime parents plus all
96 retained public Watch/regression parents. `--session-renewal-probe` is only a
diagnostic. Verification uses official 2.0.2 anonymous/token/TLS/mTLS and 1.22.3
token, including actual TTL-spanning renewal, native read/write ACL denial, gone
sessions, processed reply faults, original generation replacement, and evidence
transfer. The two original default transaction-limit probes remain separate.

## Public HTTP Session renewal documents

The existing KV workspace exposes HTTP Session Renewal through a separately
injected optional provider. Form and JSON share one authored request; partial
inputs can be saved without executing. Preview captures the original UUID,
endpoint/DC, generation, interval, count, byte budget and provider preparation.
Only an explicit checked confirmation starts the original GET and bounded PUTs.
Editing, loading another request, changing method, replacing the lease or an
unknown write invalidates that authority.

Original indexed records remain on the original run. The UI consumes parked
results only when their owning document returns. Stop, disconnect, replacement
and close wait for executor acknowledgement, including cancellation while the
start result is pending. Unknown renewal outcomes block all other writes in the
same document, and existing unknown writes block renewal. Later reads and Watch
observations never settle those writes. Clearing known receipts and starting
another run are separate explicit actions.

Copy/export capture complete immutable evidence before clipboard/picker waits.
The native body is retained separately from the optional Session projection;
LockDelay uses precise unsigned decimal text. Recovery retains the original
scope and records, reserving a new unconnected lease without executing. Cancelled
late transfers and providers returning only a complete snapshot preserve evidence.

`--public-session-renewal --race --txn-max-request 8388608 --test-timeout 1800
--memory-mib 2048` retains all 103 foundation/regression parents and 888 existing
images per configuration, adding five required public workspace/Shell parents
and 128 renewal images. `--public-session-renewal-probe` is only a diagnostic.
The official five configurations and two default transaction-limit probes remain
required for full acceptance. HTTP Watch authorization never authorizes renewal.
Service, health, registration and ACL management, live-prefix deletion, full
archives, Enterprise/HA and every remaining Navicat/DBX integration gap remain
independent.

## HTTP Session and lock request tools

The immutable request builder can author Session info/create/destroy and HTTP
KV acquire/release requests. The existing request-tools menu exposes all five
forms alongside its six KV tools. They carry text only; generation does not reserve a
lease, read credentials, connect, prepare a write or dispatch a request.

Creation preserves omitted fields separately from an explicitly empty Name or
Node, zero LockDelay, and an empty NodeChecks list. UUIDs remain lowercase native
strings. Durations, behavior, checks, bounds and incompatible/hidden fields pass
the existing native request validator. Lock requests retain absolute encoded
keys, exact value bytes and optional uint64 Flags, including explicit zero.
Noncanonical HTTP paths are rejected because substituting transaction lock/unlock
would change the original HTTP true/false response and conflict semantics.

Generated text uses the existing one-use request preparation and complete HTTP
receipt path. Native false is an acknowledged conflict. A processed write with a
lost, partial, 408/5xx or cancelled reply keeps its unknown outcome, including a
creation whose UUID was not captured. Recovery transfers original evidence
without guessing that UUID or executing again; subsequent reads cannot promote
the original unknown write. Explicit Session destruction and natural expiry
remain distinct from stopping renewal or releasing one key.

`--session-tools --race --txn-max-request 8388608 --test-timeout 1800
--memory-mib 2048` retains all 108 public renewal/regression parents and 1016
images per configuration, adding four private Session/lock parents. The
`--session-tools-probe` scope is diagnostic only. Final acceptance uses official
2.0.2 anonymous/token/TLS/mTLS and 1.22.3 token fixtures plus both default
transaction-limit probes.

Public forms preserve partial input, explicit name/node inclusion, native UUIDs,
original Base64 bytes and optional decimal Flags. Unchecking a name/node field
clears its value. Tool input copy/export uses a separate version-1 JSON envelope
and captures the original complete draft before a clipboard or file-picker job.
These files do not authorize requests; generated HTTP text continues to use the
existing request-file format. Tool-input import is not supported.

Generating HTTP text selects its ordinary receipt view even when an older Watch
or renewal record was selected under the same editor method. Those original
records remain available. Unknown generic writes and unknown renewal attempts
both prohibit further writes in the document; reads, generation, file export and
explicit recovery cannot clear that barrier. Lock replies describe this one HTTP
request and do not promise continued lock ownership.

`--public-session-tools` adds five native workspace/Shell parents and 220
bilingual images to the complete private 112-parent/1016-image regression.
`--public-session-tools-probe` is diagnostic only. Native acceptance covers
original forms, UUIDs, true/false, ACL filtering/denials, immutable confirmations,
files, parking, executor acknowledgement, retirement, recovery and close. The
32 processed-write faults use actual Consul upstream effects, independently
observed without upgrading the document's unknown receipt. All remaining
integration gaps persist.


### Session response projection foundation

Complete successful original `GET /v1/session/list`, `/v1/session/node/<node>`
and `/v1/session/info/<UUID>` receipts may carry a typed Session observation.
The optional projection retains original order, exact unsigned indices and
nanosecond LockDelay, distinct legacy/Node/Service checks, and field presence
(including explicit null and unknown field names). Unknown field values remain
in the original HTTP body. Missing, null, empty and zero values are distinct.
An info response must contain zero or one row matching the authored UUID.

Malformed or excessive projections are unavailable as a whole; original HTTP
status, body, headers and acknowledgement remain authoritative. Bounds are
10,000 rows/checks, 256 fields per object and 32 MiB total projection cost; the
complete original response remains subject to the existing 48 MiB limit.
Nested projections are cloned and included in the owning lease receipt budget.
Later reads and evidence-only recovery never acknowledge an unknown write.

Session list search/paging, selected detail, held-key observation and guarded
destruction are available in the workbench. The original health browser and
finite blocking health Watch have separate native acceptance. Agent browser,
registration/check writes, catalog/ACL management and remaining reference gaps
require separate publication and verification.
`--session-projection` retains every public Session tool parent and screenshot;
`--session-projection-probe` is a distinct diagnostic scope only.

Agent services/checks reads can carry an optional local-map projection. Native
map keys, original order, missing/null/empty fields and complete original entry
JSON remain distinct. Local checks may omit Node. Invalid identities, duplicate
keys/fields or exceeded row/32 MiB projection bounds publish no partial map and
retain the original HTTP acknowledgement. ACL-filtered maps remain explicitly
incomplete; they do not establish catalog absence or registration permission.
Clones and original lease receipt budgets include all projected entry bytes.
`--agent-projection-probe` verifies the projection through the original Agent
read parents and all existing health/browser/HTTP Watch parents. It rejects
mixed scopes, retains unknown write evidence and never retries a damaged read.
