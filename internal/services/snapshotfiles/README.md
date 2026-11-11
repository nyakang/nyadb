# Original snapshot files

This package provides injected neutral file operations. `Operations` implements
Qdrant's existing inspected input/output contract. `NewKVSnapshotStore` implements
private disk capture for an original KV document's optional `KVSnapshots` facet.
The KV store has no database driver, runtime manager, user destination or UI.

KV capture preparation retains the exact original physical lease, scope,
document and finite request. Preparation performs no file/network I/O. Capture
reserves body + native 32-byte checksum + complete frame journal limits against
one finite disk quota before native dispatch. Arithmetic stays exact and bounded
by signed file offsets. The one-use store preparation cannot be copied into fresh
authority or submitted to another store. The native lease owns dispatch and its
original operation/cancellation lifetime.

Each capture creates an exclusive 0700 directory under the captured `os.Root`
and two 0600 files. Writes are synchronous, bounded and retain exact acknowledged
counts. After original execution, writable handles are fsynced/closed and the
original files reopened read-only. Bounded context-aware reads validate original
identity, sizes and full digests; a complete source also requires the original
body/checksum/EOF evidence. Local cancellation during verification preserves the
native completed-source receipt but removes this capture's files. A failed source
may retain a verified partial file and complete original native journal; it never
becomes a complete artifact. Local disk faults retain original receipts and clean
up only this capture's original files.

An artifact owns its disk charge and read-only file handles. Its independent
readers use original file offsets and cannot expose writable handles or paths.
Inspection deep-copies receipts. Close waits for in-flight reads, revokes all
readers, closes captured handles and checks exact original file/directory identity
before cleanup. Charges shrink to actual retained bytes and release exactly once
on successful cleanup. Failed cleanup remains charged/registered and is reported
by both artifact receipts and store Close; replacement files are preserved.
Store Close cancels and waits for original captures, then closes all artifacts.

Native source proof, output acknowledgement, private artifact verification,
file fsync and user-file publication are separate facts. `FilesSynced` records
successful fsync of the two private files; it does not prove directory or user
file durability. This API does not publish Snapshot controls, choose a user file,
restore data, replay a stream or settle an earlier unknown KV/member/Alarm write.
User-file permits/temporary writes/exclusive publication and public KV/Shell
Snapshot dialogs remain separate acceptance stages.

Verification uses focused race/fault/lifetime tests and the owned fixture's
`--snapshot-artifact` mode, including raw/session/runtime regressions and exact
original file/frame equality, failed-source/cancellation/parking/cleanup,
restricted identities and a real committed Put followed by lost acknowledgement.
The accepted native source matrix and local repository validation are recorded
under ignored `docs/` and `build/`, independently of public acceptance.
