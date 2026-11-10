# Native etcd v3 workbench

This package is registered as a builtin etcd v3 provider. The public chooser,
generic URI import, persisted profiles, sessionbrowser, native request documents
and sole shared key grid bind the original profile and published generation.
The earlier reports preserve their original private-stage acceptance scope.

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
generic URI routing, chooser publication and builtin provider registration
now forward to this same configuration.

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

`Manager.KVRequests` binds the builtin provider and translates contextual to
published generations only at the runtime boundary. Lease results retain full protobuf bytes and
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

The `kvworkspace` UI binds original request documents through the neutral
provider. Writes need a separate checked confirmation of the captured request;
reads and preconnect run once. Documents retain authored and canonical text,
complete original receipts, independent unknown-write evidence and explicit
request files. Disconnect preserves text; recovery reserves a new lease without
dispatch. Later reads cannot resolve the original unknown write or enable replay.
Shell owns profile activation, tabs, leave guards and native file dialogs. The
public File/Window/toolbar commands create native KV documents and open/save
versioned native request envelopes through kvworkspace. SQL workspace recovery
does not reconstruct those requests or execute them.

Run `python3 tools/verify_etcd_fixture.py --workspace --profile --race --mode basic`
for native request/UI/Shell acceptance alongside the foundation, editor and
session tests. The original receipt fault proxy first receives a real server Put
receipt, then writes a different value and withholds the original reply; loss,
cancel and publication replacement retain the original unknown outcome while
recovery/readback preserve the subsequent value. Localized wide/small MyGo tester
images are UI regression evidence, not native desktop execution on other OSes.

The shared data adapter reserves original key documents through
`DataDocumentsProvider`, with independent page/count/field/write/export channels.
Authenticated byte cursors retain a fixed original revision; healthy explicit
Refresh starts a new revision only for that document. Pages aggregate one key per
Range RPC at that revision from the outset; complete previews and fingerprints
are published only after the entire page succeeds. Actual page failure or
compaction retires the range and requires explicit reopening. Complete field
reads retain the original fingerprint representation for the mature grid;
returned chunks are bounded to 1024 bytes, but native Range transports the full
current value. Multi-chunk latest reads do not promise MVCC consistency during
concurrent modifications. Full editing is bounded to 1 MiB.

One original immutable preview authorizes one native Txn for insert/update/delete
or rename. Five source comparisons retain original create/mod revision, version,
lease and bytes; destination create=0 guards inserts and renames. Ordinary updates
use native IgnoreValue/IgnoreLease for omitted fields, while saved Range outcomes
verify the complete expected value and lease. Explicit empty values and lease=0
remain distinct. Inserts and renames send their full destination values. Smaller
server request limits still apply, including to the five original comparisons.
Overlapping keys,
unsupported SQL semantics and batches beyond 128 comparisons/operations are
rejected before dispatch. Actual complete Txn outcomes generate saved snapshots;
unknown/incomplete acknowledgements preserve drafts and block document writes.
CSV/JSON export streams complete binary values and decimal metadata through its
own fixed-revision executor, one value per Range to bound response batching. A 4 KiB key with more results uses a two-key keys-only observation to advance without skipping longer server keys. Unsupported keys beyond 4 KiB fail explicitly; maximum prefixes remain ranges and exact grants remain exact RPCs.

Run `python3 tools/verify_etcd_fixture.py --data-adapter --workspace --profile
--race --mode basic` for adapter, builtin Manager-bound shared grid and previous workspace/profile
regression. Public capabilities enable KeyValues, Metadata, Keyset, Count,
FieldChunks, EditableTables and Export; SQL/query, transactions, design and
administration capabilities remain disabled.
It proves complete editing, key confirmation, cancellation, original parked
completion, healthy refresh and generation replacement. Native service tests cover
all mutation kinds, conflicts, reply faults and auxiliary cancellation.

Public workbench acceptance uses `python3 tools/verify_etcd_fixture.py --public
--race --mode basic`. Each full mode requires 42 original native cases and 172
localized wide/small images. The chooser → generic URI/editor → separate vault →
SQLite reopen → sessionbrowser → qualified kv/keyspace → shared grid path verifies
insert, empty binary value, lease detach, omitted-value preservation, rename,
delete, complete native receipts and dirty/disconnect/exit guards. Native request
File/Window commands verify original envelope open/save/save-as. Real successful
Txn reply loss, cancellation and terminal close preserve the write barrier and
never overwrite a later independent value; ten-comparison conflicts leave the
entire original batch unapplied. `--public-smoke` checks six public/grid cases
with 64 images and is explicitly insufficient for full acceptance.

ACL-aware browsing captures self UserGet and assigned RoleGet observations and
intersects the normalized byte read grants with the original profile prefix.
Overlapping/adjacent grants and disjoint ranges share one fixed MVCC revision,
authenticated cursor, complete count and export scope. Read-only rows cannot be
edited/deleted; grid writes require both readable and writable original/source
and destination keys. Empty intersections and users without read grants open a
read-only empty grid without probing forbidden keys. Explicit native requests
still use the server's authorization for each operation.

Basic authentication selects the exact configured username even with mTLS;
pure certificate authentication selects the certificate CN. TLS without client
certificate authentication does not establish a CN identity. Captures bound role
and permission counts and aggregate metadata size. Each independently opened
page/count/field/write/export channel checks the original permission fingerprint.
When available, any change in the global auth revision invalidates that original
snapshot conservatively, even when its normalized key ranges remain equal.
Restricted etcd 3.5 users cannot read AuthStatus: auth revision remains unavailable
(zero), role membership and role permission observations are read again during
capture, and no atomic auth snapshot or unchanged-permission guarantee is claimed.
Every actual KV operation remains server-authorized. Changed captures retire the
original range; healthy Refresh preserves cached data until successful completion,
and failed/retired reads require explicit reopening without replaying writes.

Run `python3 tools/verify_etcd_fixture.py --access --race --mode basic` for all
47 public/native parents and 196 localized images per full mode. `--access-smoke`
checks only the four service parents; `--access --public-smoke` adds the restricted
Shell case to the six public/grid regression parents and requires 88 images.
These smaller scopes do not establish full acceptance.

Watch/history, automatic renewal, maintenance/auth management, etcd v2,
additional versions and cluster operation remain pending. Historical public
acceptance is recorded in `docs/etcd-public-workbench-2026-11-10.md`; ACL scope and
verification are recorded separately in `docs/etcd-access-browsing-2026-11-10.md`.

A private raw Watch foundation owns one original protobuf stream per Client,
retains complete bounded native frames and event bytes, validates the original
WatchID and member identity, and handles explicit progress and zero-revision
compaction receipts separately. Stop waits for the actual original cancellation
receipt; cancellation/Close and actual processed response loss never recreate
that stream. The buffer admits 256 frames, 10000 events and 8 MiB of conservative
owned byte estimates; overflow preserves prior receipts and ends the original
executor. Fragmented Watch requests are explicitly unsupported in this stage.

`--watch-smoke --race --mode MODE` runs four private raw-service parents, including
actual upstream creation/event response loss and the 4 KiB binary key boundary.
It does not establish public Watch/history acceptance. Graphical controls remain
pending; the raw Watch API is not published by the native request whitelist.

Private `Client.History` captures an exact-key Range receipt and a fixed revision
window, then transfers the same Client's uninterrupted execution ownership to
one original historical Watch. Completion requires an actual original progress
acknowledgement reaching the captured end; an empty result or deadline alone
cannot prove completion. The default start covers 10000 revisions ending at the
captured key's last modification (or the captured end for an absent key).
Explicit start/end revisions are never moved after compaction or interruption.

Complete original frames, including later events outside the window, remain
separate from the newest-at-most-500-event window projection. Projection limits
set Truncated without evicting frames; a capture frame/event/byte budget failure
sets CaptureIncomplete and terminates the original executor. PUT/DELETE and
previous values retain exact bytes and MVCC/lease metadata. Progress requests
are explicitly counted, sent at most 64 times, and bounded by the original
overall request deadline. Close and cancellation await the original executor;
lost Range/creation/event/progress receipts never cause replay or reconnection.

`--history-smoke --race --mode MODE` requires seven private native history
parents. It establishes the raw fixed-window foundation only; the public
Watch/history workbench remains pending.

A separate private `contract.KVEventsProvider` supplies original physical
document leases through Session and the runtime publication boundary. Requests,
complete native frames, typed binary events and original fixed-window history
receipts own independent snapshots. Public generations are translated only by
dbruntime. Existing native write preparations and unknown write receipts remain
on their separate KV request lease.

Sessions allow four event documents, included in the existing 32-document total.
Each original document has a 16 MiB conservative receipt reservation and shares
a 32 MiB session budget. Reservations include raw buffers, neutral/retained
copies and optional JSON; Poll does not release charges for receipts already
transferred to the document. Overflow stops only its captured original stream
and retains prior frames. Complete binary values are never silently truncated.
Frame JSON presentation is independently bounded to 1 MiB; complete original
protobuf remains available when that optional presentation cannot fit.

Stop waits for actual cancellation acknowledgement. A stopped Watch retains its
original run until explicit ClearReceipts or document disposal. Session
retirement closes original physical channels and clears credentials while
parked frames/history remain owned by their original document. Document Close
clears caches and releases reservations. Recovery settles only the old captured
lease, transfers remaining original evidence, and reserves a new lease without
opening a channel, subscribing or executing history. Preconnect remains explicit.

`--events-smoke --race --mode MODE` requires seven private Session/Manager
parents, including actual processed Watch/progress response loss, original
cancel/retirement, generation replacement, precise lease metadata, unknown-write
receipt independence and shared-budget competition after Poll transfers. This
scope does not publish or establish graphical Watch/history acceptance.

The private KV workspace document core now accepts an injected events provider
alongside its original native request lease. It captures exact Watch/History
input, keeps the Watch context alive through the original receiver, and sends
bounded private update/final packets to the active document's UI thread. Parked
packets stay with their original document; stop/cancel/close wait for the original
executor, and explicit recovery never subscribes or executes another request.
Restart requires explicit settled receipt disposal. Native unknown-write replies
remain independent of all event reads and recovery.

Event request files retain the version-one envelope and unfinished JSON text.
Strict decoding rejects unknown fields, duplicate snake/camel aliases, imprecise
or overflowing integers, invalid binary ranges and fragmented Watches. Separate
event archives retain original public scope, authored input, fixed window,
complete protobuf frames (including unknown fields), exact typed binary values,
native int64/uint64 strings, progress proof and terminal/incomplete reasons.
Presentation limits (500 events, 1024-byte value previews, 64 KiB text) do not
truncate these archives. Export captures the complete original receipt before
the native picker; close/cancellation prevents a late picker from writing.

`--events-workspace-smoke --race --mode MODE` validates three private document
parents against the real Manager and official server: same-revision event order,
binary/old values, precise leases, fixed history/progress and archives; parked
publication replacement, explicit recovery/restart/stop and files; and History
after an actual processed Put reply loss without resolving the unknown write.
This stage does not inject events into the public Shell or add a Watch/History
selector. Graphical controls and qualified shared-grid key actions still require
their separate native workbench acceptance.
