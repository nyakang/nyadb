# Original snapshot files

This package provides injected neutral file operations. `Operations` implements
Qdrant's existing inspected input/output contract. `NewKVSnapshotStore` implements
private disk capture for an original KV document's optional `KVSnapshots` facet.
The KV store has no database driver, runtime manager or UI. Its separate file
preparation accepts an explicit user choice from injected host operations.

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
User-file publication is described below. Public KV/Shell Snapshot dialogs
remain a separate acceptance stage.

Verification uses focused race/fault/lifetime tests and the owned fixture's
`--snapshot-artifact` mode, including raw/session/runtime regressions and exact
original file/frame equality, failed-source/cancellation/parking/cleanup,
restricted identities and a real committed Put followed by lost acknowledgement.
The accepted native source matrix and local repository validation are recorded
under ignored `docs/` and `build/`, independently of public acceptance.


## Original user-file publication

`PrepareFile` captures the exact owned artifact, cloned original document/profile/
public generation/receipt, selected backend or native journal, explicit existing-
file replacement choice, original parent root/handle and file fingerprint. An
absent file requires a new-file choice; an existing file requires explicit
replacement. Inspections cannot mint authority. Closed/foreign/copied plans and
closed artifacts cannot dispatch output I/O. Preparation reads the original old
file using its own captured size, so a large old backup can replace a smaller new
snapshot without extending the output quota. Symlinks and changed identities,
sizes, timestamps or hashes are rejected.

Backend publication requires a complete source/artifact. A stored, fsynced and
complete original native journal can be saved independently after a failed
backend capture. Saving the failure journal does not complete the backend source
or settle any native write receipt. Export performs no connection recovery,
Snapshot request or request replay.

Publication reserves exactly the selected output size against the shared finite
quota. It creates an exclusive 0700 sibling directory and a 0600 output file,
checks exact read/write counts and source hash, fsyncs/closes the output and
independently rehashes it. Linux uses `renameat2(RENAME_NOREPLACE)`, macOS uses
`renameatx_np(RENAME_EXCL)` and Windows uses relative native handle renaming with
replacement disabled. Unsupported systems/filesystems return an error without
a destructive fallback. The Windows/macOS implementation is cross-compiled;
actual platform runtime semantics remain a separate verification scope.

New targets are published without replacing an appeared file. Replacement first
moves the target into the exclusive `displaced` name without overwriting any name,
then checks the actual displaced identity and complete hash against the original
choice. It publishes the new name with another no-replace move. Any intervening
change/conflict preserves every file; restoration uses no-replace semantics.
Successful replacement also preserves the old file for the user. The receipt
records its location and whether it was observed retained. Neither cleanup nor
Close deletes a displaced file or published destination. A replacement involves
two atomic moves and can temporarily leave the target absent; it is not an atomic
exchange of the two files.

Actual move acknowledgements, later destination hash/path observations, file
fsync, parent/private-directory fsync, cancellation and cleanup are independent.
Failed move acknowledgement remains unknown even after a matching observation.
`PathMatched` verifies the original parent pathname still selects the captured
directory; `AuxiliaryPathMatched` additionally checks the private directory. The
recorded auxiliary paths refer to the original directory choice and cannot be
presented as current locations when their identity checks fail. Published files
survive later cancellation, path changes, fsync/verification/cleanup failure.

Cancellation and Close wait for original synchronous copy, read, write and fsync
operations. Artifact closure revokes publication; store Close cancels/waits exports
before closing their readers and artifacts. Successful publication transfers the
new file to user ownership and releases its reservation. Failed temporary cleanup
retains the exact reservation and error; foreign private names are preserved.
Displaced user backup bytes are never charged as snapshot-owned temporary data.
Owned native fixture mode `--snapshot-file` covers both formats, replacement,
original post-disconnect saving, earlier actual unknown Put barriers and complete
failure-journal export, in addition to all prior stream/session/artifact cases.
