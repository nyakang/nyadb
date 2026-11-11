package contract

import (
	"context"
	"io"
	"slices"
)

// KVSnapshotCapture keeps native source proof independent of privately retained
// file verification and file fsync. It never describes user-file publication.
type KVSnapshotCapture struct {
	Original                      KVSnapshotResult
	SnapshotBytes, JournalBytes   uint64
	SnapshotSHA256, JournalSHA256 []byte
	Stored, FilesSynced, Complete bool
	StorageError, CleanupError    string
}

func (r KVSnapshotCapture) Clone() KVSnapshotCapture {
	r.Original = r.Original.Clone()
	r.SnapshotSHA256 = slices.Clone(r.SnapshotSHA256)
	r.JournalSHA256 = slices.Clone(r.JournalSHA256)
	return r
}

type KVSnapshotCapturePreparation interface {
	Snapshot() KVSnapshotRequest
	Scope() KVSnapshotScope
}

// An artifact exclusively owns its original files and budget. Inspection copies
// do not clone ownership. Close waits for in-flight reads and revokes all readers.
// The read-only streams are independent and never expose writable handles/paths.
type KVSnapshotArtifact interface {
	Receipt() KVSnapshotCapture
	OpenSnapshot() (io.ReadCloser, error)
	OpenJournal() (io.ReadCloser, error)
	Close() error
}

// The original lease/facet is injected. Preparation performs no I/O; capture
// reserves bounded disk space before dispatch and waits for original execution.
type KVSnapshotArtifacts interface {
	Prepare(KVRequestLease, KVSnapshotRequest) (KVSnapshotCapturePreparation, error)
	Capture(context.Context, KVSnapshotCapturePreparation) (KVSnapshotArtifact, KVSnapshotCapture, error)
}
