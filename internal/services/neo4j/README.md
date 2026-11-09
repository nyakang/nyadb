This package is the private native Neo4j Bolt foundation. It does not register a
connection type, persist a profile, publish a runtime provider, or add a workbench
view. Those are separate integration stages.

`Open` binds one official v6 driver to the original client, validates the native
Neo4j agent, and disables both managed and auto-commit retries. `Prepare` captures
immutable Cypher parameters; its plan belongs to that client and can be attempted
once. `Execute` uses auto-commit `Session.Run`, records typed bounded rows and the
native completion summary, and carries acknowledged bookmarks into subsequent
sessions. Routing retains the configured scheme and advertised endpoints.

An interrupted request retires its original driver after the session settles.
`Close` cancels and waits for that operation; it cannot close a replacement client.
Missing native completion remains uncertain, including native statement errors:
arbitrary Cypher procedures and side effects cannot be inferred to have rolled
back. Explicit recovery must never replay the original plan. A successful native
summary remains observable when row retention is truncated; unread rows are
discarded through `Consume`, allowing the server to complete the request.

Values retain exact signed integers, special floats, bytes, nested values,
temporal kinds and zone/offset information, duration components, spatial SRIDs,
node/relationship element IDs and legacy IDs, properties, and ordered paths.
Graph objects are result values and are rejected as parameters. Retained row
count, encoded data size, recursive depth, and item count are bounded. The driver
can allocate a large native value while decoding it before this package captures
it; these are retention bounds, not transport allocation bounds.

TLS uses the URI hostname, custom CA roots, and optional client certificates.
The pinned driver overrides `TlsConfig.ServerName` from the URI, so a conflicting
SNI override is explicitly rejected. `+ssc` selects the driver's explicit
certificate-verification bypass and cannot be combined with custom CA roots.

Run unit and concurrency checks with:

```sh
go test -race ./internal/services/connections ./internal/services/neo4j
```

The opt-in native verifier deploys its own digest-pinned official Neo4j 5.26.21
Community Docker node, writes secrets only to private temporary files, requires
every acceptance case to pass, and validates immutable ID and random labels
before cleanup. Run each mode:

```sh
python3 tools/verify_neo4j_fixture.py --mode anonymous --race
python3 tools/verify_neo4j_fixture.py --mode basic --race
python3 tools/verify_neo4j_fixture.py --mode tls --race
python3 tools/verify_neo4j_fixture.py --mode mtls --race
```

Reports, sanitized logs and source hashes are written under ignored
`build/neo4j/`. Native acceptance covers real authentication, direct and routing
schemes, TLS CA/hostname and mTLS rejection, parameterized graph writes/readback,
typed values, native summaries/errors, bounded results, independently observed
committed writes with lost/canceled responses, blocked transactions, original
close/termination, cleanup and replacement survival. It does not establish
support for other Neo4j versions, Enterprise/cluster administration, graph UI,
data editing or public connection/profile/session integration.

The private profile/editor stage lives in `connections`, `localdb` and
`connectioneditor`. Add `--profile` to the verifier to exercise versioned native
settings, pure URI import, SQLite restart/copy, injected credential-vault save,
closed original connection checks, canceled real handshakes, and 48 localized
form screenshots per mode. It retains a private Kind without registering a
descriptor or runtime provider. Password-free basic profiles may be saved for
an explicit later prompt; the editor requires a password before testing.
