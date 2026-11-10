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

`Prepare` accepts bounded native protobuf Range/Put/Delete/Txn/Compact/Status and
lease requests, plus the original Auth/RBAC requests described below. It validates and deep-copies the request; all copies share
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

Native Auth preparation supports AuthStatus, UserList/Get/Add/Delete/
ChangePassword/GrantRole/RevokeRole and RoleList/Get/Add/Delete/GrantPermission/
RevokePermission on the same original KV request lease. AuthEnable/Disable are
not exposed. Names and binary permission ranges remain exact; AuthStatus's
uint64 authRevision stays independent of the KV header revision and public
generation. Acknowledged Auth writes invalidate that physical client's cached
permission observation. Subsequent permission reads are explicit.

Password or hashedPassword is transient in the immutable execution plan.
Recorded requests, early retirement results and workspace operation receipts
omit those fields and retain explicit omission metadata. The original server
protobuf remains complete. Passwordless creation requires its native option.
Lost write acknowledgements retain the original unknown outcome; recovery
does not authenticate, read or replay. These native APIs are a foundation for
the dedicated public user/role management UI, which remains a separate step.
Run `GOWORK=off GOFLAGS=-mod=readonly /usr/bin/python3
tools/verify_etcd_fixture.py --auth-native --race --mode MODE` for six mandatory
native parents, 36 processed-write fault combinations and seven read-receipt
fault cases per mode. Use 3.7.3 anonymous/basic/tls/mtls and 3.5.35 basic.

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
native. The original one-shot KeepAlive method sends one request. Continuous
renewal uses its separate explicit document authorization described below.

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

Maintenance, etcd v2,
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
The raw foundation alone does not establish public Watch/history acceptance.
The native unary request whitelist remains separate from the events feature;
public controls are described in the later public workbench section below.

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
Watch/history workbench acceptance is described in the later public section.

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
private scope alone does not establish graphical Watch/history acceptance.

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
That private stage did not inject events into the public Shell or add a Watch/History
selector. Graphical controls and qualified shared-grid key actions still require
their separate native workbench acceptance.


Public Watch/history workbench
----------------------------

The public Shell now injects the neutral events provider into the existing native
KV document. The method selector exposes Watch/History only for a connection
with this feature. Explicit Start, Stop and Progress controls preserve the original
executor; stopping and cancellation wait for acknowledgement. History retains its
fixed start/end and actual progress proof, with incomplete capture and limited
event projection displayed separately. The receipt shows its original profile and
session generation even after recovery. Large/narrow views scroll receipt details.

Original-key actions in the sole shared data grid capture complete signed row and
page observations, not the clipped key text. A separate original physical read
scope freshly checks grants against the document's immutable grant geometry for
each action. Expired page epochs, changed scopes and publications reject late
results. Watch starts after the original page revision; history fixes its end at
that revision. Opening the tab or recovering the lease never executes a request.

The list shows at most 500 events, with 1024 bytes per key/value preview and a
64 KiB text presentation. Copy/export capture an independent complete receipt
before asynchronous encoding, picker or disk work. Records retain original scope,
request, native frames, exact byte values and quoted native 64-bit numbers, fixed
history projection/proof and terminal/incomplete reasons. Parked clipboard work
waits for its original document activation; cancellation never overwrites the
clipboard. Event receipts remain separate from unknown writes and do not remove
the write barrier. Clear explicitly disposes settled evidence before another run.

Run `GOWORK=off python3 tools/verify_etcd_fixture.py --public-events --race --mode
MODE` for the seven mandatory native public cases and 28 localized Shell images.
Modes are anonymous/basic/tls/mtls on 3.7.3, plus basic on 3.5.35. The local report
is `docs/etcd-public-events-2026-11-10.md`. Auth/RBAC is described below; maintenance UI, cluster acceptance and etcd-v2
remain separate future work.

Private original continuous lease-renewal foundation
--------------------------------------------------

`PrepareRenewal` creates a separate one-use authorization for one original
LeaseKeepAlive stream and exact nonzero native int64 Lease ID. It performs no
I/O. `StartRenewal` consumes that authorization once and waits for the first
original acknowledgement; the run stays alive afterward. A complete valid
response's actual TTL schedules the next send at one third of TTL, bounded to
100 ms minimum and 8 hours maximum without integer-duration overflow. Each
request waits for its own captured stream response before another send.

`Poll` transfers bounded complete attempt receipts, including original protobuf
unknown fields, precise ID/TTL/header, attempt sequence, completion, code and
unknown-write outcome. The raw queue retains at most 256 receipts and 1 MiB of
charged capture; a frame is limited to 64 KiB. A retained-receipt admission hook
supports later session/document budgets. Overflow stops the original executor,
keeps earlier receipts and explicitly marks incomplete capture. A valid response
seen by the driver remains acknowledged even when its full capture exceeds the
retention budget; an unvalidated oversized response remains unknown.

Stop/Cancel/Close wait for the original executor. Stopping between acknowledged
requests sends no further renewal and allows natural server expiration. Stopping
with an unacknowledged attempt preserves its unknown outcome and retires that
channel. Permission rejection, TTL<=0, protocol/identity mismatch, original
response loss and timeout never grant a replacement stream or lease. Independent
later TimeToLive reads cannot confirm an earlier lost receipt. No LeaseGrant,
new ID or stream recreation is performed, and the existing one-shot KeepAlive
whitelist and write plans retain their original behavior.

`GOWORK=off python3 tools/verify_etcd_fixture.py --renewal-native --race --mode
MODE` runs four native renewal parents (including 13 first/later processed reply
fault combinations, explicit pending Stop, permission/lease revocation and
capture budgets), plus legacy one-shot lease/expiration and read ownership cases.
Use all four 3.7.3 modes and 3.5.35 basic. This native foundation does not publish
continuous renewal in the GUI or establish neutral session/document budgets;
those remain the next acceptance steps. Local evidence is recorded in
`docs/etcd-renewal-native-2026-11-10.md`.

Private original-session renewal leases
---------------------------------------

`KVRenewalProvider` reserves a separate physical document channel without I/O.
Its pure immutable preparation authorizes exactly one continuous run on the
original native int64 lease ID. Preconnect remains an explicit handshake; Start
captures that channel and one-use authorization. KV one-shot requests, event
streams and data documents retain independent executors. Only dbruntime translates
contextual generation 1 into the original published generation.

Four renewal documents fit within the shared 32-document session limit. Complete
renewal receipts charge up to 8 MiB per document; event and renewal documents
share a 32 MiB retained-session limit. Poll transfers up to 64 receipts without
releasing their charge. Explicit settled disposal releases it. Recovery transfers
the charge for previously delivered and pending receipts to the fresh lease,
including across replacement sessions; insufficient capacity fails without
releasing the original retained charge. Driver acknowledgement and incomplete
capture remain separate.

Session retirement cancels and awaits the original executor while keeping its
parked receipts and budget. Explicit recovery checks the original qualified
scope, prefix, member/cluster and version, transfers remaining receipts and
terminal metadata, and only reserves a fresh channel. It neither opens that
channel nor sends a renewal. Old preparations cannot be transferred. Unknown
renewal outcomes survive Poll, retirement, recovery, independent TTL reads and
further recovery, and block both new preparations and ClearReceipts in that
document. A fresh healthy handshake cannot erase this barrier.

`GOWORK=off python3 tools/verify_etcd_fixture.py --renewal-session --race --mode
MODE` verifies 12 required native parents, including original native and one-shot
regressions, 13 actual processed first/later session reply faults, pending Stop,
retirement/recovery, retained overflow, original public generation and legacy
event retirement/budget regression. Use 3.7.3 anonymous/basic/tls/mtls and 3.5.35
basic. This is an optional neutral runtime foundation; workbench continuous
confirmation, controls and receipt presentation remain a separate acceptance.

Public continuous renewal workbench
----------------------------------

The existing kvworkspace State/Document receives the optional original-session
renewal provider. Its separate LeaseRenewal method accepts a complete nonzero
signed int64 ID and version-1 request files without floating-point conversion.
The initial template ID=0 cannot authorize a run. Starting opens a confirmation
of the immutable original text, lease ID, profile and generation. A checked
continuous authorization is required; editing the text, switching method or
replacing the original lease invalidates it. The original LeaseKeepAlive method
remains a one-shot request.

Start captures the original one-use preparation and executor. Stop/Cancel,
disconnect, tab/workspace close and exit wait for the original executor. A pending
first Stop cancels its captured request context; a Stop after acknowledgement
settles that original run. Stopping may allow the original lease and attached
keys to expire naturally. Parking pauses UI consumption while the authorized
original executor continues within its capture budgets. Recovery transfers
original receipts and retained charges without opening or starting a renewal.
A new known run requires explicit receipt disposal and a new confirmation.

The run's original profile/generation, exact ID, actual TTL, attempts, complete
protobuf (including unknown fields), optional JSON and separate acknowledgement,
unknown outcome and incomplete capture are retained. Presentation shows at most
32 recent receipts and 64 KiB. Asynchronous complete copy/export captures the
original scope/request/receipts before clipboard, picker or disk waiting. Canceled
or parked file completions cannot overwrite another active document. Unknown
one-shot and renewal write receipts remain separate and prohibit all new writes
in that document, including after independent reads and explicit recovery.

`GOWORK=off /usr/bin/python3 tools/verify_etcd_fixture.py --public-renewal --race
--mode MODE` requires 24 native parents plus 20 native workspace and 60 public
Shell images per mode (including old Watch/history). Use 3.7.3 anonymous/basic/
tls/mtls and 3.5.35 basic. Local evidence is
`docs/etcd-public-renewal-2026-11-10.md`. The original-document lease browser is described below. Dedicated lease management and Auth/RBAC are described below. Maintenance
administration, cluster and etcd-v2 remain
separate acceptance steps.

Private original lease-browsing foundation
-----------------------------------------

`Client.BrowseLeases` captures one actual native cluster lease list and selects
at most 200 signed-int64 IDs after an explicit exclusive boundary. It owns the
original physical executor across the list and sequential current TTL reads.
There is no server pagination, fixed MVCC snapshot or previously known-ID
fallback. The complete original list receipt remains separate from its bounded
window; more than 10,000 listed IDs rejects browsing while retaining the full
native receipt. Each returned TTL row keeps its own full success/failure receipt.
An interrupted window is incomplete and does not publish a continuation.
The aggregate native receipt budget is 32 MiB; another read starts only after
reserving a complete 16 MiB transport-bounded reply. Exhaustion retains every
already captured full receipt and stops before the next call.

`Client.ObserveLease` reads an exact nonzero signed-int64 ID, current TTL and
GrantedTTL, and complete binary attached keys only when explicitly requested.
It preserves native TTL=-1 for missing leases and the original protobuf including
unknown fields. These native lease APIs have cluster scope; they do not imply
the connection's browse prefix or a shared-grid row identity. Actual server
permissions still apply. A foreign ID, duplicate/zero list ID, invalid or
duplicate key, unexpected keys or invalid TTL is rejected with the original
receipt retained. These read-only APIs never grant, renew or revoke a lease.

`GOWORK=off /usr/bin/python3 tools/verify_etcd_fixture.py --lease-browser-native
--race --mode MODE` requires nine native parents, including complete attached
keys, exact ID windows, actual processed list/TTL reply faults, permissions,
malformed/bounded replies, aggregate receipt budgets and original
one-shot/continuous renewal regressions.
Use 3.7.3 basic/anonymous/tls/mtls and 3.5.35 basic. Neutral document/runtime
publication and the dedicated lease/attached-key browser were subsequent acceptance
steps described below; this foundation did not change public chooser or URI capabilities.

Private original-document lease-browsing facet
----------------------------------------------

The neutral `KVLeaseBrowsing` facet is optional on the original
`KVRequestLease`. It reuses that document's physical channel, operation lock,
request cancellation, pure reservation/recovery and idempotent Close. Captured
page/detail requests and scopes, signed IDs, full native list/TTL/attached-key
receipts and optional projections are copied independently. Browsing leaves
the original one-shot LastResult and unknown write receipt unchanged.

The runtime binds this facet only when the original raw lease implements it.
It checks the captured namespace, prefix, handshake identity/version and raw
generation, translating only that original generation into the public scope.
An original completion remains intact when publication retirement adds an
error; the original document decides admission. Recovery accepts the same
bound KV document, preserves its one-shot receipt and reserves a new original
lease without reading, granting, revoking or restarting renewal.

`GOWORK=off /usr/bin/python3 tools/verify_etcd_fixture.py --lease-browser-session
--race --mode MODE` requires 16 native parents. It includes the nine native
browsing/renewal regressions, original document/one-shot ownership, actual partial
read cancellation/Close, actual unknown-write recovery followed by independent
list/detail reads, and four runtime publication/recovery/processed-write/late-open
cases. Use 3.7.3 basic/anonymous/tls/mtls and 3.5.35 basic. Public list/detail
controls follow below; original-action confirmations remain a later step.

Public original-document lease browser
--------------------------------------

The existing KV workspace discovers browsing on its original KVRequestLease.
LeaseBrowse accepts limit 1–200 and an optional exclusive signed-int64 after;
LeaseDetail requires an exact nonzero signed ID and explicit keys selection.
These methods read the actual cluster list and independent live TTLs; the client
window is unrelated to server pagination, a prefix grant or a fixed MVCC snapshot.
Selecting a row never starts renewal or grants/revokes a lease. Dedicated actions
and immutable management confirmations remain separate work.

The sole State/Document retains a valid cached page, a valid detail and the latest
original attempt. Complete raw list/TTL/detail bytes, all attached keys, authored
requests and original scope/generation survive bounded presentation. The native
32 MiB page capture budget, 10,000 list IDs and 200 selected IDs still apply.
Tables virtualize attached keys; per-cell Base64 preview is 256 bytes, while
explicit selected-key copy and original records retain complete keys. Receipt
preview is at most 64 KiB. There is no additional browser history or lease family.

Cancellation, parking, retirement, recovery, files, leave decisions and Close use
the original document lifecycle. Invalid or retired results settle loading and
retain complete original evidence without replacing the cached page. Recovery
never repeats a read or resumes renewal, and old selections cannot authorize a
read in a new generation. Browsing and explicit browser receipt disposal do not
change one-shot LastResult or either unknown write barrier. Copy/export captures
a deep original receipt before clipboard, picker or disk waits. Close releases
physical ownership, jobs, editors and browser caches.

`GOWORK=off /usr/bin/python3 tools/verify_etcd_fixture.py --public-lease-browser
--race --mode MODE` requires 22 native parents and 56 localized workspace/Shell
images per mode. It includes the original native/session/runtime regression,
261 complete attached keys with a 4096-byte key, actual processed list/second-TTL/
detail receipt loss/cancellation/disconnect/retirement/Close/parking, actual
one-shot and renewal unknown followed by pure recovery and independent browsing,
public client windows, complete archives and original parked publication recovery.
Use 3.7.3 anonymous/basic/tls/mtls and 3.5.35 basic. The previous public-renewal
matrix also verifies the existing request/events/renewal/file flows after these
controls. Local evidence: docs/etcd-public-lease-browser-2026-11-10.md.

## Original Auth management

The optional `KVAuthManagement` facet advertises the original request lease's
native Auth inventory. Runtime binding preserves all four Auth/browser facet
combinations; UI support is never inferred from an engine name or a method prefix.
The public method selector exposes `AuthManagement`, a pure users/roles view.
Opening its `.nyakv` file or recovering its lease never reads or writes anything.

Explicit users/roles directory and name-detail reads preserve original native
receipts, complete names and independent uint64 Auth revision. Permission tools
retain exact byte keys, exclusive-end ranges and the single-NUL unbounded end;
text and strict Base64 inputs are supported up to the native 4096-byte key limit.
An arbitrary range is never labeled as a prefix. Unknown native permission
extensions remain in complete copies; the selected revocation shortcut refuses
to silently discard them.

The nine write actions cover user add/delete/password changes and role grants,
and role add/delete and permission grants. Creating a user with initial roles,
or a role with an initial permission, prepares separate immutable native RPCs.
Each step requires the original document/profile/generation/physical lease and
an explicit checked confirmation. Complete earlier step receipts remain intact;
a failed, canceled, incomplete or unknown step stops subsequent writes. Native
names/ranges have no CAS or incarnation precondition. A retained unknown write
blocks further writes in that document even after later reads or pure recovery.

Temporary passwords only enter the original execution plans. Preview and receipt
records explicitly identify omitted password fields; cancel, disconnect, retirement
and close clear temporary form secrets. A `.nyakv-auth-preview.json` export is a
redacted record, not a runnable request file. Complete operation export/copy retains
all planned redacted requests and each original native step reply. The pure view
file cannot contain password or other management-form fields. Native step capture
reserves a complete next transport-bounded reply before dispatch, and stops before
another RPC when its retained native budget is exhausted.

Run `GOWORK=off /usr/bin/python3 tools/verify_etcd_fixture.py --public-auth --race
--mode MODE` for 13 required native parents and 80 bilingual wide/narrow captures
per mode. Use official 3.7.3 anonymous/basic/tls/mtls and 3.5.35 basic. These include
all 14 original native Auth RPC regressions, actual password/certificate/restricted
identities, real processed partial-write faults, original cancel/close/parking and
publication replacement, complete permissions and redacted records. AuthEnable/
AuthDisable, maintenance/member administration and etcd-v2 remain separate scope.
Local evidence: `docs/etcd-auth-public-2026-11-11.md`.

## Original compaction request foundation

The optional `KVMaintenance` facet advertises `KV/Compact` on the original
request lease. Runtime binding preserves all eight maintenance/Auth/browser
combinations. Preparation captures a positive signed int64 revision and the
logical/physical option without network activity; execution uses the original
one-use plan and native RPC. Compact affects cluster history regardless of the
connection's browse prefix. It does not change current values or create a new
KV revision. Observations of the current revision are not a CAS precondition.

The complete original CompactionResponse, header, protobuf extensions and JSON
remain separate from later Range/Watch/history observations. Native rejection of
a future/already-compacted revision or insufficient permission is definite;
processed-reply loss, cancellation and an invalid header retain an unknown
write outcome. Recovery never executes Compact and cannot transfer its old plan.
The public KV method selector includes a dedicated Compact form. Its explicit
Status/Range probe captures the original document/cluster/member/public generation,
revision and native read receipts. A separate immutable checked confirmation
uses one original Compact plan; a successful Range at the compaction boundary
does not guarantee that repeating Compact will succeed. Copies and exports keep
complete original observations and replies. Request files restore text only.
Original observation/confirmation/unknown receipts participate in leave decisions;
close releases their leases and caches. Defragment, Alarm, Snapshot and member
management are pending.

Run `GOWORK=off /usr/bin/python3 tools/verify_etcd_fixture.py --compaction-native
--race --mode MODE` for 16 required native parents. Use official 3.7.3
anonymous/basic/tls/mtls and 3.5.35 basic. These cover original native compaction
and publication, logical/physical receipt faults, exact MVCC pagination,
compacted Watch/history, restricted identity and original Auth/browser recovery.
This stage does not establish public maintenance UI acceptance. Local evidence:
`docs/etcd-compaction-native-2026-11-11.md`.

Public form acceptance: `GOWORK=off /usr/bin/python3 tools/verify_etcd_fixture.py
--public-compaction --race --mode MODE` requires 25 native parents, 48 workspace
and 48 Shell Chinese/English wide/narrow images. Use the same five official
configurations. Actual processed logical/physical reply loss, cancellation,
close, parking, replacement, disconnect and invalid headers keep their original
receipts, while explicit recovery/Range/Watch/history never replay Compact or
settle its missing acknowledgement. Local evidence:
`docs/etcd-compaction-public-2026-11-11.md`.
