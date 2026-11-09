NyaDB registers Neo4j as an independent public connection with native Bolt or
Neo4j routing, versioned profiles, URI import, credential-vault integration and
an injectable connection editor. The public workbench offers qualified graph
metadata, Cypher documents and the shared result grid plus native typed JSON.

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
Qualified graph objects open read-only metadata snapshots rather than relational
structures or implicit data queries. Schema retention shares a 16 MiB budget;
these are live observations, not atomic or relational schemas. Native node/
relationship objects are not accepted as request parameters.

The private graph data binding reserves an immutable label or relationship-type
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

This binding does not yet enable public graph data tools. Shared grid UI,
property editing and graph visualization require their own subsequent acceptance;
the public engine continues to advertise only Query/Metadata. Add `--data` to
the verifier commands below to check the private reads together with the public
Cypher workbench. This records existing form/Cypher images, not graph data-grid
screenshots.

TLS uses the URI hostname, custom CA roots and optional client certificates.
The driver derives TLS ServerName from the URI, so a conflicting SNI override is
rejected. `+ssc` explicitly bypasses server certificate verification and cannot
be combined with custom CA roots. Row, byte, depth and item budgets limit retained
values; the driver can allocate a large value before adapter retention checks.

Run the opt-in verifier in all supported fixture modes:

```sh
python3 tools/verify_neo4j_fixture.py --mode anonymous --race --public
python3 tools/verify_neo4j_fixture.py --mode basic --race --public
python3 tools/verify_neo4j_fixture.py --mode tls --race --public
python3 tools/verify_neo4j_fixture.py --mode mtls --race --public
```

The verifier creates its own digest-pinned official Neo4j 5.26.21 Community Docker
node and private configuration/credentials. It requires all native foundation,
profile/editor, session and public-workbench cases to pass without skips. Real
acceptance includes saved-profile reopening, native typed values/metadata,
published generations, committed writes with lost/canceled responses, explicit
recovery, original close/termination and localized workbench/form images. Cleanup
checks immutable container ID and nonce labels. Evidence and sanitized logs stay
under ignored `build/neo4j/`.

Graph visualization/expansion, graph data editing/import, unlimited export,
structured Cypher plans, administration/RBAC, Enterprise/cluster validation and
other versions remain future work. Their capabilities are not inferred from a
successful Community connection or the ability to execute arbitrary Cypher.
