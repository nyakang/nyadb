NyaDB registers Neo4j as an independent public connection with native Bolt or
Neo4j routing, versioned profiles, URI import, credential-vault integration and
an injectable connection editor. The public workbench offers qualified graph
metadata, Cypher documents, the shared result grid and a shared data grid for
node labels and relationship types, with native typed JSON and property editing.

`Open` binds one official v6 driver to the original client, validates the native
Neo4j agent, and disables managed and auto-commit retries. `Prepare` deeply
captures native parameters. A plan belongs to one client and can be attempted
once. `Execute` performs one auto-commit `Session.Run`, retains bounded typed
rows, and records native completion, counters and bookmarks. Neither Cypher
text nor selected text is split by a SQL parser. Routing retains the configured
scheme and advertised endpoints.

The adapter Session owns one original metadata client and up to 32 independent
query clients. The runtime alone publishes monotonic session generations.
Runtime and workbench wrappers preserve typed graph feature access and original
ownership. A configured/home database is resolved through an original native
operation; namespace identity is ProfileID/database, with no schema. Agent,
protocol and address observations are not proof of database incarnation, cluster
identity or permanent physical socket ownership.

An interrupted request retires its original driver after its executor settles.
Release, Close and cancellation wait for that original operation. Missing native
completion remains uncertain, including native statement errors: arbitrary
procedures and side effects cannot be assumed to have rolled back. The workbench
retains this receipt and query text and requires explicit recovery. Preconnect,
recovery, reconnect and workspace restoration never replay a query. Original
cleanup cannot close a newer published session.

Values retain exact signed integers, special floats, bytes, nested values,
temporal kinds and zones, duration components, spatial references, element IDs,
legacy IDs, node and relationship properties, and ordered paths. The shared
projection keeps exact values; native result JSON keeps graph identity and the
summary. Snapshots deeply copy both. Native JSON display is limited to 256 KiB;
the document retains the complete bounded result. Run-and-export includes typed
JSONL, a manifest and the native graph result in `graph.json`. Result export is
bounded by retained rows; it is not an unlimited graph/database export.

Cypher node results append alias-qualified property columns using only the
retained native rows; projection never issues another request. Original source
ordinals, node identities, query text and completion receipts remain intact.
Columns follow source order and sorted property keys; conflicting names receive
numeric suffixes. Source columns containing only nonempty nodes and NULL are
hidden by default and remain available in the column menu. Mixed values and
empty nodes keep their source columns visible. User layouts survive changes in
observed property types; property identity and default hiding eligibility govern
layout compatibility. Missing properties remain NULL; scalar/binary cells and
complete typed temporal/spatial/duration/list JSON retain their native meanings.

Expansion admits at most 1024 total columns, 1,048,576 cells, a 16 MiB estimated
display budget, 64 KiB of layout descriptors and 4096 bytes per property key.
Exceeding a bound keeps the original node columns with an explicit display notice,
without changing native completion or requiring recovery. Native JSON, snapshots
and complete retained-result exports preserve the original graph alongside the
expanded table. Keys outside retained rows cannot be discovered by projection.

The retained-result graph view follows nodes, directed relationships, paths,
nested lists and maps. Node/relationship element IDs have separate key spaces
inside each original document; missing element IDs are rejected explicitly.
Relationships whose endpoint nodes were not returned show identity-only dashed
placeholders. Returned nodes can fill those placeholders. Different observations
of one identity retain the first complete value with a conflict notice and links
to the original cells; properties are not silently merged. These are observations
of the retained query, not database incarnation or concurrency guarantees.

The view owns at most 1000 nodes, 2000 relationships and an 8 MiB topology budget;
traversal and cell links are each bounded to 65536 items, at depth 32. Display bounds
leave the complete original graph and receipt untouched. Search covers full
name/title/label values, labels, IDs and relationship types. Deterministic spatial
force layout, node dragging, canceled-drag rollback, pinning, hiding/restoring,
pan/zoom, fit, keyboard controls and a paged entity list share the native canvas.
The inspector presents typed property previews with complete bounded JSON and
original-cell links, including expanded property-column provenance. A graph-record
mode uses the existing result grid. Snapshots copy positions, pins, hidden state
and selection while owning separate graph values; replacement/close/disconnect
release derived caches and pending interaction state.

PNG (2560x1600) and SVG (1280x800) exports capture independent display geometry
before the file picker/writer, fit visible entities and escape labels. They issue
no Cypher and do not borrow later camera/hidden state. Native graph/result export
continues to preserve all retained typed values. Explicit neighbor expansion runs one fixed parameterized
read on the original document lease/database, up to 200 rows and 4 MiB (reduced
when the remaining observation budget is smaller). The query limit is a live
bounded observation, not complete adjacency or an atomic snapshot. Each native
request, namespace and receipt is retained separately from the original result,
within 8 MiB and at most 1000 observations. Payload overflow preserves a receipt
with an explicit missing-payload notice. Missing completion/cancellation/loss
retain the previous graph; cancellation waits for acknowledgement, and a parked
result is checked against the original publication before admission. Explicit
recovery does not rerun either query or expansion.

Merged observations retain the first complete value with conflict notices and
separate neighbor-record provenance. New nodes undergo bounded force relaxation
while all old positions, pins, hidden state and camera remain unchanged. Result
snapshots include owned neighbor values and receipts in their 16 MiB budget, and
provide read-only neighbor JSON and image export without live expansion. Original
query/result JSON continues to contain only original records; separate expansion
JSON retains the complete bounded observations. Supported scalar and homogeneous scalar-list properties, including temporal
and spatial scalars, can be edited from the
node/relationship inspector. A draft captures the original lease, namespace,
qualified target and typed old value, then requires a complete immutable request
preview before one native save. The fixed parameterized query performs a dependent
self-assignment to obtain the entity lock before comparing the selected property
value/type and exact node label set or directed relationship type/endpoints.
Other properties are not compared. Native numeric equality applies to floats,
including signed zero; this is not complete entity or incarnation concurrency.

The save retains its entire native result, counters, bookmarks, request and error
separately. A completed zero-row result means the requested replacement was not
admitted; the lock-acquiring self-assignment remains in counters. Success displays
the returned complete native entity without rewriting the original SQLResult,
Cypher text or table. Separate JSON exports and read-only snapshots own receipts
and display observations. Existing topology bounds still apply. Old/new parameter
capture shares 1 MiB; complete semantic request JSON is also bounded to 1 MiB.
Responses are bounded to two rows/2 MiB, and at most 1000 operation records share
8 MiB with reserved capacity before dispatch. Records count toward snapshot limits.

Each complete saved or verified entity records how many neighbor receipts preceded
it. Rebuilding inserts that entity at this position, retains all earlier source
links and compares subsequent neighbor entities against it. Saving then expanding
neighbors can continue editing without treating the original value as a new
conflict. Later external changes still mark conflicts and block editing until an
explicit full verification of that entity. Node verification cannot reconcile a
relationship or another node. This sequence describes workbench operation order,
not server timestamps or incarnation guarantees; incomplete/truncated verification
never replaces a baseline. Snapshots independently retain both entities and order.

Conflicts retain drafts without resubmitting. Known failures before native dispatch
remain editable and require a new explicit preview; they are not uncertain commits. Cancellation waits for the original
executor; uncertainty and stale publication retain evidence and require explicit
original-profile recovery. Attempted drafts cannot replay, including after recovery.
Disconnect preserves original qualified property drafts and receipts; terminal close
releases them after executor cancellation/acknowledgement. Explicit verification
issues a separate bounded entity read, retains its original native response, and
does not provide missing write confirmation even when values match. Leaving or
closing the workspace guards every unprocessed property draft/operation. Whole-entity
operations are available from the shared data grid through the confirmation path
described below. Extended-type lists and graph import remain pending; Community
fixtures do not validate Enterprise RBAC permissions.

Cypher JSON parameters are temporary per-document drafts. They are not saved in
query text, history or restored workspaces. Duplicate keys, invalid JSON,
overflowing integers and unsupported nesting are rejected before dispatch.
Integers preserve int64; fractional/exponent values use finite native float64.
The editor has Cypher comments/highlighting and native keyword, parameter,
label, relationship, procedure and function completion. SQL formatting and
structured SQL explain tools are unavailable. Cypher files execute one native
source and are limited to 8 MiB.

Graph schemas retain native labels, relationship types, inferred node/relationship
properties, indexes, constraints, procedures and all function overload rows.
Qualified labels and relationship types open shared data documents. A separate
read-only definition action retains graph metadata snapshots; graph indexes,
constraints, procedures and functions open those snapshots directly. Schema retention shares a 16 MiB budget;
these are live observations, not atomic or relational schemas. Native node/
relationship objects are not accepted as request parameters.

The data grid includes alias-qualified `property.<key>` columns alongside the
complete native entity and property-map columns. An initial or explicit refresh
reads the complete qualified key inventory and native `valueType()` observations
on its original driver; cursor pages reuse that inventory. Property keys are
returned as native data and never interpolated into Cypher. Distinct observed
types are hints, not constraints. Newly introduced keys remain in the full native
JSON and appear as independent columns after another explicit refresh.

Missing properties remain NULL and differ from empty strings, empty lists and
empty bytes. Integers/floats/booleans/strings preserve their native cell kinds;
bytes remain binary, with bounded hex previews and chunks counted in bytes.
Temporal, spatial, duration and list columns keep complete typed native JSON.
Original signed row identities capture their property inventory, so a field
request cannot adopt a later page's column meanings. Complete entity fingerprints
still reject changed values. The inventory admits at most 1024 total columns,
4096 bytes per key, 16 observed types per key and 64 KiB of encoded descriptors;
signed row references admit at most 128 KiB and share the existing 16 MiB retained
page budget. Wide inventories can require a smaller page size. Inventory scans
and entity reads observe live data without a common atomic snapshot.

Default layouts present the entity ID and independent properties before the
full JSON columns. Saved user layouts override this order; source ordinals and
existing layout keys/formats remain unchanged. The shared record form follows
column order while retaining all fields, including hidden grid fields.

The graph data binding reserves an immutable label or relationship-type
range through the shared `DataDocumentLease` contract. Reservation performs no
I/O; each read captures its own original driver. Pages use ordered native
`elementId()` boundaries signed for that document and refresh epoch. Node rows
retain element IDs, labels, complete typed properties and the native node;
relationship rows also retain their type and both endpoint identities. Preview
cells contain at most 1024 characters, complete fields at most 1 MiB and native
responses at most 16 MiB. Count and explicit field reads have independent
cancellation scopes. Complete row/field reads compare the original entity
fingerprint; changed or missing entities require another explicit page read.
These fingerprints and label/type identities are value observations, with no
incarnation or atomic CAS guarantee. Complete native JSONL and typed tabular
export scan the full qualified range in bounded native reads without restarting
after a failed read or writer. A canceled or lost page request revokes that
original range; explicit reopening never replays it. Session/document close
waits for captured executors and late opens to acknowledge cancellation.

The shared `dataworkspace.State/Document` owns both graph kinds. Qualified
navigation, object actions and search retain the kind, original profile/database
and published generation, including a label and relationship type with the same
name. Canceled pages remain busy until executor acknowledgement. A retired range
keeps its last successful page and requires the explicit Reopen data action;
refresh and session replacement cannot replay it. Parked completions wait for
the original profile, and late generations cannot replace cached data. Disconnect
and close dispose scoped graph documents and preserve existing Cypher text.

The public engine advertises Query, Metadata, Documents, Keyset, Count,
FieldChunks, Export, JSONL and EditableTables. Identity, label/type, endpoints
and whole-entity columns are read-only. Property updates use the direct Saver;
node creation and node/relationship deletion require explicit original-plan
confirmation. Relationship creation, SQL transactions, import, relational filters,
arbitrary sorting and SQL export are unavailable. `--grid` requires the public
grid cases, original read failure/cancellation/close, explicit reopen, full range
exports, independent property columns/original fields and Chinese/English grid
images at 1280x800 and 720x520, together with all foundation/profile/session/
Cypher cases. `--data` continues to select the
adapter read and native transaction foundation separately from the public grid tests.

Shared data-grid saves capture the signed original complete entity snapshot and
all edit maps/bytes before waiting. Each batch (at most 200 rows and 1 MiB of
captured edits) uses one explicit native transaction with no managed retries.
Entity locks are acquired in element-ID order through a dependent property SET,
including absent properties. The locked complete entity and both supported
values/fingerprints validation payloads are checked before SET += bound native
properties. A later-row conflict rolls back all earlier provisional changes.
Only actual COMMIT confirmation and a final bookmark admit saved rows; each row
receives a new signed snapshot for continued editing. Unsupported kind, list
subtype and spatial-dimension changes are rejected explicitly.

The graph editor exposes NULL, text, integer, finite float, boolean, binary and
complete native GraphValue JSON. It supports four scalar kinds, bytes, four
homogeneous scalar-list kinds and eight temporal/spatial scalar kinds. NULL
removes a property; missing values can be added only for keys in the original
qualified column inventory. JSON rejects duplicate/unknown fields, invalid
Unicode and case aliases which could overwrite native kinds/components.

One latest original transaction receipt is retained per data document, at most
8 MiB. It contains captured edits, native parameters, provisional typed results
and summaries, and separate BEGIN/COMMIT/ROLLBACK confirmation flags. The modal
previews at most 64 KiB; copy and atomic file export keep the complete original
bytes and capture them before a picker or disk wait. A later receipt replaces
this receipt; Close cancels late exports and releases owned data.

The driver's Rollback can return nil without sending a server ROLLBACK after a
native transaction failure. Such nil cannot confirm rollback. Canceled/lost or
otherwise uncertain/incomplete saves retain drafts and prohibit replay, including
queued writes on the original lease. Independent read scopes remain available.
Readback cannot supply a missing COMMIT acknowledgement. Unknown writes offer
an explicit original-document close decision which discards only local drafts
and receipts; it does not resend, confirm or roll back the original transaction.
Cancellation of that decision retains the guard. The close action waits for
executor acknowledgement and remains available even if receipt bytes are missing.
Unsupported verification commands are omitted. Parked results wait for
the original profile, and late generations retain evidence without replacing the
grid. Tests withhold an actual native COMMIT response, independently observe the
committed property, write a distinctive later value and prove that loss, cancel,
close and queued writes cannot replay over that value.

TLS uses the URI hostname, custom CA roots and optional client certificates.
The driver derives TLS ServerName from the URI, so a conflicting SNI override is
rejected. `+ssc` explicitly bypasses server certificate verification and cannot
be combined with custom CA roots. Row, byte, depth and item budgets limit retained
values; the driver can allocate a large value before adapter retention checks.

Run the opt-in verifier in all supported fixture modes:

```sh
python3 tools/verify_neo4j_fixture.py --mode anonymous --race --grid
python3 tools/verify_neo4j_fixture.py --mode basic --race --grid
python3 tools/verify_neo4j_fixture.py --mode tls --race --grid
python3 tools/verify_neo4j_fixture.py --mode mtls --race --grid
```

The verifier creates its own digest-pinned official Neo4j 5.26.21 Community Docker
node and private configuration/credentials. It requires all native foundation,
profile/editor, session and public-workbench cases to pass without skips. Real
acceptance includes saved-profile reopening, native typed values/metadata,
published generations, committed writes with lost/canceled responses, explicit
recovery, original close/termination, retained-node property expansion, original
node visibility, native snapshots/exports, bounded fallback and localized
workbench/form images. Four grid modes require 208 native groups and 1376 images, including retained
graph paths, parallel/reverse/self-loop relationships, missing endpoints, original
record links, full property JSON, snapshots, image export and 1001-node display
bounds, explicit one/second-hop reads, 200-row neighbor limits, scalar property saves and lock-blocked typed conflicts, committed-write response loss, independent verification, separate record
links/exports, conflict observations, parked generation rejection and native
response-loss/cancellation/original-close recovery without replay. Save/expand/verify/edit
combinations cover nodes and directed relationships, preserve later external conflicts,
reconcile only the explicitly verified entity and retain independent snapshots. Cleanup
checks immutable container ID and nonce labels. Evidence and sanitized logs stay
under ignored `build/neo4j/`.

Relationship creation, graph import, extended-type lists,
structured Cypher plans, administration/RBAC, Enterprise/cluster validation and
full database archives and other versions remain future work. Their capabilities are not inferred from a
successful Community connection or the ability to execute arbitrary Cypher.

For a focused property-write and close regression after the complete workbench
matrix, run `python3 tools/verify_neo4j_fixture.py --grid-writes --race --mode MODE`.
This scope requires three native transaction cases and all five shared-grid
read/write/close cases, with 64 localized grid images per mode. It does not claim
the complete foundation/profile/Cypher matrix was rerun by this focused command.

The private entity foundation shares the same original data lease, captured edit
values, cancellation and explicit native transaction executor. It creates
label-qualified nodes (including propertyless nodes), deletes nodes without
implicitly detaching, deletes directed relationships, and separately performs
bounded detached node deletion. Creation retains exact supported scalar, bytes,
homogeneous scalar-list and eight temporal/spatial scalar kinds; labels and
property keys are bound native data. New complete signed identities are admitted
only after actual COMMIT confirmation and its final bookmark. A later conflict
rolls back both provisional creation and deletion without recreating identities.

Deletion locks and validates the original complete entity and either values or
fingerprints payload. Detach collects at most 2001 distinct incident edges,
rejects more than 2000, locks them in element-ID order and materializes complete
typed identity/endpoints/properties before deleting. Reverse/parallel edges and
self-loops are retained distinctly. A missing lock key still acquires the native
lock but reports zero property updates; an existing key reports one. Oversized
edge evidence rolls back the entire provisional deletion. This is bounded live
evidence, not complete database/incarnation concurrency or an unlimited archive.

Original transaction receipts remain within 8 MiB; captured edits remain within
1 MiB/200 rows and each statement response within 2 MiB. Failed native statement
rollback no-ops cannot acknowledge a server rollback. Missing COMMIT responses
poison original queued/future mutations, keep the original evidence and wait for
executor cancellation/close acknowledgement. Independent original reads remain
usable. Native acceptance independently confirms actual creation/deletion, then
changes remaining values before introducing response loss/cancellation/close;
old operations must not replay or damage a replacement client.

`--entity-native --race --mode MODE` selects four private entity cases, including
16 original COMMIT-fault combinations and original node/edge lock comparisons.
The complete `--grid` matrix also requires those cases. Public entity operations
now require captured draft decisions, affected-edge previews and explicit
confirmation through the original shared-grid/runtime publication.
Relationship creation requires separately qualified endpoint input and remains
pending alongside graph import, archives, administration and other full scope.

Private original-document entity previews now retain complete captured edits,
native read parameters and typed node/incident-edge observations. Their neutral
DataMutationPlan accessor returns deep copies, independently of executor values.
Each original lease owns at most one preview (8 MiB native evidence, 1 MiB edits,
2 MiB per read). New previews replace the previous preview; Close releases it.
The original write queue claims a preview once before native dispatch. Caller
implementations, other document/profile/generation handles, replaced previews
and repeated submissions cannot authorize a mutation. Runtime wrappers reject
late previews after publication replacement and close the captured raw handle.

Plain connected-node deletion is blocked by its preview before a native write.
For detached deletion, Submit locks and compares the original complete node and
each distinct directed/self-loop/parallel relationship, including typed properties
and endpoints. It deletes only the explicitly previewed edges, followed by plain
node DELETE. A changed edge set or value rolls back earlier provisional rows;
an unobserved concurrent edge cannot be implicitly detached. Element IDs still
represent live observations, not incarnation or a complete database snapshot.
Adjacent selected nodes can share edges: later rows exclude only this batch's
confirmed provisional edge deletions, without changing the original preview or
accepting external changes. Shared/reverse/parallel/self-loop edges are deleted
once, while unselected endpoints remain intact.

External writers can use an inverted endpoint/relationship lock order and cause
a native deadlock. The exact server failure and missing rollback acknowledgement
are retained without retries; independent checks verify preserved entities and
the committed concurrent edge value. Missing COMMIT responses keep the original
write scope blocked, including queued previews. Independent reads remain usable.
Signed edit capture is registered before validation, so original Close waits
for preparation and cannot clear its signature key before acknowledgement.

`--entity-preview --race --mode MODE` requires 11 native cases: three property
transaction cases, four entity foundation cases and four preview cases. The
preview cases include six original node/edge/adjacency changes, bounds, read
cancellation/close and 16 actual COMMIT response-fault combinations per mode.
This focused scope does not rerun the complete profile/Cypher/workbench image
matrix. Public entity operations are bound through original preview/Submit contracts;
ordinary Saver remains limited to property update.

The sole shared data workspace now has a capability-gated original-plan
confirmation path. Its separate publication/revocation ticket closes unconsumed
and late preview handles, parks original-profile notifications, and waits for
preview cancellation acknowledgement. Confirmation checks the captured drafts,
object, public generation and complete observations; cancellation preserves drafts
and clears leave continuations. Generated identities and row removal require
actual COMMIT confirmation with completely corresponding original outcomes.
Delete and detach-delete drafts are both protected from editing and included in
history/removal. The UI owns a deep copy of evidence after the one-attempt handle
has been consumed. English/Chinese confirmation controls fit wide/narrow windows.

The public Runtime binds label update/insert/delete/detach-delete and relationship
update/delete. Entity mutations use original previews and explicit confirmation;
direct Saver remains update-only. New nodes omit untouched inventory properties,
show derived fields as generated, and distinguish explicit empty strings and NULL.
Labels without properties can create propertyless nodes. Node deletion captures
the original selection before choosing ordinary deletion or deletion with the
explicitly previewed relationships. Cancel retains all original drafts, including
mixed uncommitted inserts. Relationships cannot be created without a qualified
endpoint model and do not offer a node detach decision.

`--entity-grid --race --mode MODE` includes public entity creation, original
selection decisions, blocked connected-node deletion, adjacent selected-node
bulk deletion, after-preview conflicts, actual COMMIT reply faults and original
preview parking/cancellation/close. Its final acceptance also requires native
property/entity/preview foundations, existing public data regressions and each
localized creation/deletion/preview/receipt/lifecycle image. The entity matrix
requires 21 native cases and 112 localized images per mode. Its report retains
source hashes, exact original fixture identity and verified private-file cleanup.
Complete repository verification and 16 package lifecycle race checks use a
20-minute test timeout for the full Shell suite. Node/relationship identities
remain live observations; successful entity writes refresh the original page,
and later selection or sorting cannot redirect a captured deletion or generated
identity. A retired original read range requires explicitly reopening its document
before further property/entity editing.

Private qualified relationship creation now has a separate endpoint observation
contract. The original relationship document reads complete source/target nodes,
including typed properties, labels and native completion, and signs that evidence
with its own key and a purpose distinct from row tokens. Endpoint input is deeply
captured inside the existing 200-row/1 MiB edit budget. The preview rereads both
nodes and rejects changed observations. Submit locks every unique endpoint in
sorted element-ID order before any batch mutation, compares complete content,
then uses directed CREATE. Parallel, reverse and self-loop requests stay distinct.
Generated identities require exact creation counters, typed properties/endpoints
and actual COMMIT acknowledgement. A later row conflict rolls back provisional
creation; canceled or uncertain requests cannot reuse the original plan.

`--relationship-native --race --mode MODE` requires 15 native cases: four
relationship cases plus the existing three property, four entity and four preview
cases. All four modes passed against Neo4j 5.26.21 Community, including actual
COMMIT-response withholding, blocked endpoint locks and original read/close
cancellation. This is private transaction groundwork. Public Runtime still omits
ReadGraphEndpoints and relationship insert; the shared grid still needs explicit
endpoint input, owned draft/history/confirmation integration and public lifecycle
and localized UI acceptance before relationship creation is enabled. Graph import,
full archives, extended temporal/spatial lists, plans, RBAC and other version,
Enterprise/cluster and full reference scope remain pending.
