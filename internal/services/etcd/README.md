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
close releases their leases and caches. Public member-targeted Defragment is
described below; public Alarm controls are described at the end of this document. Public Snapshot is described below; Restore and membership changes remain pending.

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

### Original member native protocol

The native `Client` accepts MemberList and a one-use Defragment request.
`OpenMember` requires an explicitly observed cluster/member/version and verifies
Status on the selected transport without a KV probe or endpoint fallback.
Before dispatching Defragment it captures a separate complete Status reply and
validates the same identity again. This observation is not a native CAS or an
incarnation guarantee. A failed pre-dispatch check sends no Defragment request.

MemberList can legitimately return revision zero. Defragment normally returns
an empty protobuf with no header; its actual RPC success acknowledges the
original request without borrowing a Status header. Present headers must match
the original identity, and native extensions remain intact. Processed reply
loss/cancellation retains an unknown outcome; later Status or DB sizes cannot
settle it. Learners reject Defragment. Official 3.7 uses FailedPrecondition;
3.5 returns Unavailable for the exact unsupported-learner error and Unknown for
the exact maintenance permission denial. These two observed 3.5 rejections are
definite; the original native codes/messages remain unchanged.

This native foundation uses separate original-member authority rather than
ordinary KV request codecs. Public member batches are described below.
Run `tools/verify_etcd_fixture.py --member-native
--race --mode MODE` under the five existing version/transport configurations.
`tools/verify_etcd_member_fixture.py --race --version VERSION` verifies three
voters and one learner, explicit mapped endpoint aliases and real leader
transfer using independently owned official 3.7.3/3.5.35 clusters.

### Original document member leases

An optional neutral `KVMembers` facet on the original KV document owns opaque
directory authority and explicitly selected member transports. Copies of its
inspection snapshots cannot authorize another bind. Each candidate supplies
its own expected version and endpoint; failed native Status handshakes remain
available for inspection. Authenticated learners use the original token for
Status without another Authenticate RPC or a KV probe.

Each pure, one-use member preparation checks a fresh linearizable directory on
the original parent transport, compares the complete selected member metadata,
then records fresh Status on the captured child before its single empty RPC.
An optional follower preparation additionally stops before dispatch if that
final Status reports the target as leader or cannot establish a leader. This
gate does not lock leadership, reorder or replay the consumed preparation.
The runtime preserves optional facets and translates nested public generations
only at its boundary. All operations serialize with the original document.

Full directory, handshake, Before and native ACK/error receipts have separate
bounded document/session reservations before dispatch. Member unknown writes
block subsequent document writes and survive other reads, child Close and pure
recovery. Recovery transfers original evidence and generations without opening
or executing a member request. Parent/session retirement waits for the original
executor; explicit document disposal releases its transports and caches.

Run `tools/verify_etcd_fixture.py --member-session --race --mode MODE` and
`tools/verify_etcd_member_fixture.py --member-session --race --mode MODE
--version VERSION` for the original session/runtime matrices. The latter also
verifies an actual raft leadership transfer after follower preparation and a
successful original follower ACK. Public member selection and checked batches
are described below.

### Public original Members and checked Defragment batches

The sole `kvworkspace.State/Document` offers the `nyadb://etcd/members` method.
Its strict JSON contains at most eight explicit targets, each with an exact
unsigned decimal member ID, endpoint and independently expected version. The
original opaque MemberList observation authorizes each binding; JSON, archived
DTOs and request files restore text only. Endpoint aliases are explicitly
entered and verified using the captured original authentication/TLS settings.
Full failed handshakes remain in the bounded binding history. Learners can bind
for Status inspection but cannot enter a Defragment batch.

The checked immutable confirmation captures original profile/public generation,
document, directory, scopes, child leases and pure one-use follower/final plans.
Before each step it records fresh complete Status replies for every remaining
target and requires one consistent known voter leader and raft term. Followers
precede the selected leader. The final native pre-dispatch Status independently
checks a known voter leader; a non-final target that became leader stops before
send. These observations provide no CAS, lock or member-incarnation guarantee.
No failed preparation is reordered, retired as unknown or replayed.

The batch stops at the first failure, cancellation or unknown outcome. Complete
records keep succeeded/failed/unknown/not-executed targets, all ordering rounds,
DirectoryCheck, Before, independent original headers and native protobuf/JSON.
A successful empty ACK has `NativeCaptured=true`, empty bytes and no fabricated
header. A completed native ACK may remain succeeded when publication retires,
while the remaining targets stay unexecuted. Member unknown receipts separately
block KV, Auth, renewal and member writes; later Status, child Close or recovery
cannot settle them. Parked outcomes wait for their original document activation.
Recovery carries full original member evidence with old public generations and
revokes directory/binding authority without any new discovery, bind or write.

The UI keeps editable selection apart from retained results and displays step
results, native ACK, Before Status, current member Status and handshakes. It
copies/exports full original archives; only the full-record preview is bounded
to 64 KiB. The archive cap is 512 MiB with conservative pre-dispatch reservations;
binding history holds at most 32 targets. Close drains original pending outcomes
and releases transports, authority, plans, callbacks and caches. Request files
restore the selection text and revoke live authority while retaining receipts.

Run `GOWORK=off /usr/bin/python3 tools/verify_etcd_member_fixture.py
--public-members --race --version VERSION --mode MODE`. The supported matrix is
3.7.3 anonymous/basic/tls/mtls plus 3.5.35 basic. Each independently owned three-
voter/one-learner fixture requires nine native parent cases and 36 workspace plus
36 Shell localized images. Real upstream reply-loss/cancellation/close faults,
partial batches, restricted-principal denial, learner inspection, original
publication/recovery, text files and full archives are mandatory. Local evidence:
`docs/etcd-public-members-2026-11-11.md`. Public Alarm/Disarm is described below. Public Snapshot is described below. Restore, membership changes,
broader cluster management, etcd-v2 and AuthEnable/AuthDisable remain separate.

### Original Alarm native requests and leases

The native Client, neutral complete KV result, original document request codec
and optional maintenance inventory now support `/etcdserverpb.Maintenance/Alarm`.
GET is read-only and accepts all alarms or a known alarm-type filter. Official
3.5/3.7 ignore GET's member_id field, so nonzero member filters are explicitly
rejected. Exact DEACTIVATE captures an unsigned member ID and NOSPACE/CORRUPT
pair in one immutable one-use request. The adapter sends one direct protobuf RPC,
without ACTIVATE, implicit all-member disarm or synthesized helper replies.

Complete AlarmResponse headers, protobuf/JSON, exact uint64 member IDs, native
unknown enum numbers and per-alarm/header/root extensions remain inspectable.
Removed entries in the original DEACTIVATE reply are separate from the current
alarm list. Already-absent exact DEACTIVATE can succeed with a real header and
empty list; later absence never acknowledges an earlier lost native write.
Malformed/wrong original headers, entries or exact target replies retain native
evidence and interrupt that original transport. Projection limits preserve the
complete acknowledged protobuf and an explicit projection error.

Both official 3.7.3 and 3.5.35 allow authenticated restricted users to GET alarms;
DEACTIVATE requires native maintenance permission and returns the original
PermissionDenied code/message. Anonymous and certificate principals use their
captured original transport/authentication settings. The native list is cluster
scope, independent of the connection's KV browsing prefix, and may contain stale
member IDs; an alarm ID is not proof of a current physical member transport.

Original document Prepare/Execute serializes requests and keeps native ACKs and
independent write uncertainty. Actual processed Disarm reply loss/cancellation,
close or invalid native reply stops without replay. Pure recovery carries the
original receipt and write barrier, revokes old preparations and never opens or
executes another request. Later explicit Alarm reads cannot release an original
KV/member/Disarm unknown barrier. Public generation translation remains only at
the runtime boundary; another profile's activation never changes original work.

Run `GOWORK=off /usr/bin/python3 tools/verify_etcd_fixture.py --alarm-native --race
--version VERSION --mode MODE` for 18 mandatory native parent cases per fixture:
3.7.3 anonymous/basic/tls/mtls plus 3.5.35 basic. Actual NOSPACE/CORRUPT setup exists
only on independently owned test fixtures. The suites include type-filter and
ignored-member-filter behavior, exact unsigned stale-member IDs, empty/removed
native ACKs, restricted/certificate principals, complete extensions, actual
processed read/write reply faults, cancellation/ownership, original publication
and pure recovery, plus existing KV/Compact/member regressions. TLS/mTLS fault
relays keep the original transport configuration and owned certificates.
Local evidence: `docs/etcd-alarm-native-2026-11-11.md`. This is native foundation
acceptance; public checked Alarm selection/forms/batches and Snapshot are described below. Restore,
membership changes, full cluster management, etcd-v2 and AuthEnable/AuthDisable
remain outside this stage.

### Private checked Alarm batch document foundation

The sole KV State/Document holds a private actual GET authority for its original
physical lease, document and public generation. Exported observation DTOs and
request files cannot restore that handle. Authored selections contain only the
known alarm type filter and exact decimal uint64 member/type pairs, with at most
eight distinct targets. GET never accepts a member filter; unknown observed enum
numbers remain inspectable. A removed member ID is an alarm identity, not an
endpoint or proof of current membership.

Pure preparation captures immutable one-use full-GET and exact-DEACTIVATE plans
for every selected pair. Confirmation validates the same private observation,
lease, original input and snapshots. Each worker step obtains a fresh complete
GET on that original lease and checks the selected pair and its native extension
bytes before sending a single original Disarm. Missing/changed pairs, failed or
incomplete reads, cancellation, write failures or unknown ACKs stop the sequence.
No later target is inspected after that stop. A fresh GET is an observation, not
CAS or alarm-incarnation protection; a target can disappear after that check and
still produce a genuine already-absent empty native ACK.

Original succeeded/failed/unknown/not-executed outcomes retain their own full
GET and native ACK records. Independent unknown evidence blocks ordinary KV,
Auth, renewal, member and Alarm writes and survives later reads and recovery.
Receipt DTOs deep-copy native bytes/extensions. Request-file open revokes local
authority while keeping original observations/receipts; export captures complete
original data before opening the picker. Original private buffered completions
wait for activation. Recovery transfers original evidence without GET/Disarm I/O;
close drains the original executor and clears all caches. Leave permissions
include the Alarm receipt revision. Archive capacity is reserved before GET,
Disarm and recovery; complete captured native outcomes remain separate from any
preview or JSON projection bounds.

`GOWORK=off /usr/bin/python3 tools/verify_etcd_fixture.py --alarm-workspace --race
--version VERSION --mode MODE` adds four mandatory original-workspace parents
to the native Alarm/session/runtime/KV/Compact/member regressions. Owned official
3.7.3 anonymous/basic/TLS/mTLS and 3.5.35 basic verify stale pairs, type windows,
original simultaneous pairs, restricted GET versus exact-write denial, actual
processed read/partial-write faults, parking, cancellation, close, replacement,
disconnect and explicit recovery. TLS/mTLS fault relays preserve the original
fixture certificate paths and require client certificate verification in mTLS.
That acceptance covers the private document foundation. The separately accepted public controls are described below.


### Public original Alarm forms and checked Disarm batches

The inventory-gated `nyadb://etcd/alarms` method uses the same KV State/Document,
original physical lease and private GET authority. Read the native cluster list,
choose all alarms or NOSPACE/CORRUPT, then select up to eight exact observed
member/type pairs. Changing the filter or opening a request file revokes old
confirmation authority. Unknown native type numbers remain visible but cannot
be selected for Disarm; a member ID does not prove current cluster membership.

Preview and confirmation retain the original complete GET, selected pairs and
one-use GET/DEACTIVATE plans. Every step reads the full list again and checks its
original target before sending. Missing/changed pairs, failed reads, cancellation,
failed writes or unknown ACKs stop the sequence and preserve its unexecuted
remainder. Reading a list does not lock an alarm or prevent it from recurring.
No ACTIVATE, all-target helper, automatic retry or rollback is published.

The evidence selector separates full archives, step results, latest GET, each
original Disarm ACK, its before-dispatch GET and recovered original receipts.
Complete copies/exports retain native bytes and exact unsigned IDs independently
of the bounded preview. Later empty lists do not replace removed-alarm ACKs or
settle an earlier unknown write. The original unknown remains accessible across
method changes and blocks further writes. Parked completion waits for its owning
request tab; recovery creates a new lease while retaining old evidence and sends
no request. Request files restore only authored selection text.

Run:

```sh
GOWORK=off GOFLAGS=-mod=readonly /usr/bin/python3 tools/verify_etcd_fixture.py --public-alarms --race --version VERSION --mode MODE
```

Each owned configuration requires 27 native
parents and 136 Chinese/English wide/narrow workspace/Shell screenshots. Use
3.7.3 anonymous/basic/TLS/mTLS and 3.5.35 basic. Tests cover actual simultaneous
NOSPACE/CORRUPT, restricted native GET versus denied exact writes, injected future
enums after a real GET, complete files, processed partial ACK loss, actual tab
parking, cross-method write barriers and pure recovery. TLS/mTLS relays preserve
the original certificate configuration and verify client certificates in mTLS.
Local evidence: `docs/etcd-alarm-public-2026-11-11.md`. Snapshot and complete files
are described below. Restore, membership changes, broader cluster management, etcd-v2 and
AuthEnable/AuthDisable remain separate scope.

### Original native Snapshot stream foundation

`Client.PrepareSnapshot` captures the original empty protobuf request, finite
body/journal/frame limits and a private one-use owner. `ExecuteSnapshot` uses
that client's raw disabled-retry transport, captures a separate fresh native
Status observation, then receives and validates each original Snapshot frame.
No public method, document facet or restore capability is installed by this
foundation.

The final data frame and independent 32-byte SHA-256 frame can both have zero
remaining bytes and nil headers. Completeness requires consistent body counts,
the complete matching checksum and actual original EOF. Frame headers remain
nullable; they never borrow the Status header. The server's storage version
remains distinct from the running product version. Original unknown extensions
are preserved.

Two distinct caller-owned writers receive the native body plus checksum and a
bounded-memory, length-prefixed journal of all complete protobuf frames. The
journal also retains the separate original Status, native terminal status and
local/output outcomes. `ReadSnapshotJournal` reads one bounded record at a time
and rejects truncated records, missing terminal, duplicate origin and trailing
bytes. A failed frame's original bytes remain separately available if journal
or output writing fails. Short writes preserve their acknowledged byte counts;
a partial journal cannot be repaired by appending a footer.

Source completeness, output acknowledgement, journal completeness and overall
execution success are separate outcomes. Neither `Completed` nor a complete
journal proves fsync, publication or destination ownership. The original file
owner must independently handle its temporary files, durability and publication.
The executor never closes a caller's writer, switches endpoints, resumes or
replays. Cancellation and Close wait for original receive/write acknowledgement;
Close releases the duplicate client receipt cache. Snapshot evidence does not
settle any earlier unknown write and represents whole-cluster backend state,
independent of a connection's browsing prefix.

```sh
GOWORK=off GOFLAGS=-mod=readonly python3 tools/verify_etcd_fixture.py --snapshot-native --race --version VERSION --mode MODE
```

Run official 3.7.3 anonymous/basic/TLS/mTLS and 3.5.35 basic. Each configuration
requires nine native parents, including the existing binary MVCC, cancellation
and original write-receipt regressions. Snapshot cases validate multi-frame
native disk output against every journal blob, actual restricted-token and
certificate permissions, real complete upstream streams followed by partial
loss, missing/wrong checksum, changed count/header, trailing frames, withheld
EOF, extensions, cancellation and original close without resumption. Unit tests
cover local short writes, byte budgets, truncated journals and blocked-writer
close acknowledgement. Original session/document facets, complete owned
artifacts, durable user-file publication and public controls remain subsequent
acceptance stages.

### Original document Snapshot leases and runtime binding

The optional `contract.KVSnapshots` facet runs through the existing original
KV document transport and operation lock. Its pure preparation captures finite
limits, original profile/contextual generation/document and one-use authority.
Copied or foreign preparations cannot create another request. Snapshot keeps
`LastSnapshotResult` separate from ordinary KV/member/Alarm receipts and never
settles an earlier unknown write, including after explicit recovery or later
reads.

Runtime preserves all 32 exact Auth/Browse/Maintenance/Member/Snapshot facet
combinations. It translates only the original receipt's contextual generation
to its owning publication; late results remain associated with that original
publication. Recovery creates a new original document lease and transfers the
old complete receipt without sending Snapshot or restoring plan authority.

The lease reserves bounded metadata capacity before opening or dispatching:
a conservative 200 MiB reservation against a dedicated 512 MiB session cap
shrinks to the retained receipt's actual charge. Full native error/failed-frame
bytes and original Status remain independent of bounded local diagnostics.
The raw client's duplicate snapshot cache is released after neutral capture.
Original close waits for receives and caller writer acknowledgement and releases
its own metadata; publication retirement preserves original evidence for explicit
recovery. Snapshot still uses distinct caller-owned body/journal writers. Owned
immutable disk artifacts and user-file publication are provided by the neutral
`internal/services/snapshotfiles` service. Public controls are described below.

```sh
GOWORK=off GOFLAGS=-mod=readonly /usr/bin/python3 tools/verify_etcd_fixture.py --snapshot-session --race --version VERSION --mode MODE
```

This scope requires 20 native parents in official 3.7.3 anonymous/basic/TLS/mTLS
and 3.5.35 basic. It includes all raw Snapshot, original KV scope/cancellation and
runtime write-fault/generation regressions, then real session/runtime same-lease
streams, full frame bytes, exact publication/document scope, restricted native
permissions, actual source/EOF faults, short writes, blocked writer close,
unknown original Put receipts, replacement/disconnect, another parked profile
and pure recovery. TLS/mTLS relays retain the original transport configuration;
mTLS verifies client certificates. Injected tests also verify all 32 facet
combinations, copied/foreign plans, quota admission before I/O, original deep
copies, late results and independent unknown-write barriers.


### Public Snapshot capture and original file publication

Select Snapshot in the existing KV request document. Exact positive integer
strings bound backend bytes, complete native-journal bytes and frame count. The
whole-backend confirmation captures the original connection, cluster/member,
session generation, document and limits; the browsing prefix does not limit
Snapshot scope. Request files restore inputs without executing capture.

The source, private artifact, user-file publication and complete record views
keep independent evidence. Saving a retained backend or complete failure journal
needs no active connection. A separately inspected and confirmed existing file
replacement preserves its old bytes; a newly appeared target is rejected until
explicitly inspected and confirmed again. Publication acknowledgement loss,
later content/path matches, durability and cleanup remain separate. Full exports
preserve original authored input, exact identities, limits and native/path bytes.

Cancellation and close wait for original executors. Parked outcomes stay with
the original document, and recovery sends neither Snapshot nor file publication.
Earlier unknown KV/member/Alarm writes retain their own receipts and barriers.
Bootstrap supplies the bounded private store and Shell supplies native pickers;
kvworkspace uses only injected neutral contracts.

`GOWORK=off GOFLAGS=-mod=readonly /usr/bin/python3 tools/verify_etcd_fixture.py --public-snapshot --race --version VERSION --mode MODE`
requires 34 native parents and 156 bilingual wide/narrow workspace/Shell images
per configuration. Use official 3.7.3 anonymous/basic/TLS/mTLS and 3.5.35 basic.
Restore, membership changes, broader cluster management, etcd-v2,
AuthEnable/AuthDisable and actual Windows/macOS filesystem runtime retain their
separate scopes. Local verification: `docs/etcd-snapshot-public-2026-11-11.md`.
