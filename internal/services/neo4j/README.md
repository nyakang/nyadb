NyaDB registers Neo4j as an independent public connection with native Bolt or
Neo4j routing, versioned profiles, URI import, credential-vault integration and
an injectable connection editor. The public workbench offers qualified graph
metadata, Cypher documents, the shared result grid and a read-only shared data
grid for node labels and relationship types, with native typed JSON.

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
FieldChunks, Export and JSONL. Editing, transactions, import, relational filters,
arbitrary sorting and SQL export are unavailable. `--grid` requires the public
grid cases, original read failure/cancellation/close, explicit reopen, full range
exports, independent property columns/original fields and Chinese/English grid
images at 1280x800 and 720x520, together with all foundation/profile/session/
Cypher cases. `--data` continues to select the
adapter read foundation separately from the public grid tests.

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
recovery, original close/termination and localized workbench/form images. Cleanup
checks immutable container ID and nonce labels. Evidence and sanitized logs stay
under ignored `build/neo4j/`.

Graph visualization/expansion, graph data editing/import,
structured Cypher plans, administration/RBAC, Enterprise/cluster validation and
full database archives and other versions remain future work. Their capabilities are not inferred from a
successful Community connection or the ability to execute arbitrary Cypher.
