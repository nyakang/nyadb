package contract

import (
	"context"
	"io"
	"slices"
)

// KVSnapshotRequest describes one native full-backend stream, independent of
// the original connection's browsing prefix and later user-file publication.
// Finite byte/frame limits are immutable request values, never private permits.
type KVSnapshotRequest struct {
	BodyBytes    uint64 `json:"body_bytes,string"`
	JournalBytes uint64 `json:"journal_bytes,string"`
	Frames       uint64 `json:"frames,string"`
}
type KVSnapshotScope struct {
	Original   KVScope
	DocumentID uint64
}

func (s KVSnapshotScope) Clone() KVSnapshotScope { s.Original = s.Original.Clone(); return s }

type KVSnapshotPreparation interface {
	Snapshot() KVSnapshotRequest
	Scope() KVSnapshotScope
}

// Writers are distinct captured original resources. They acknowledge immutable
// buffers synchronously. Execution/Close wait for that acknowledgement; they
// never close a caller resource or publish/fsync a user file.
type KVSnapshotOutputs struct{ Snapshot, Journal io.Writer }

type KVSnapshotResult struct {
	Scope                                                                           KVSnapshotScope
	Request                                                                         KVSnapshotRequest
	Attempted, Ended, Completed                                                     bool
	NativeTerminalObserved, EOF, SourceComplete, ChecksumMatched                    bool
	JournalComplete, OutputAcknowledged, Canceled                                   bool
	Frames, ExpectedBodyBytes, BodyBytes, SnapshotBytesWritten, JournalBytesWritten uint64
	StorageVersion                                                                  string
	BodySHA256, Checksum, JournalSHA256                                             []byte
	NativeStatus, RejectedNative                                                    []byte
	NativeCode, LocalError, CaptureError                                            string
	Before                                                                          KVResult
}

func (r KVSnapshotResult) Clone() KVSnapshotResult {
	r.Scope = r.Scope.Clone()
	r.Before = r.Before.Clone()
	r.BodySHA256 = slices.Clone(r.BodySHA256)
	r.Checksum = slices.Clone(r.Checksum)
	r.JournalSHA256 = slices.Clone(r.JournalSHA256)
	r.NativeStatus = slices.Clone(r.NativeStatus)
	r.RejectedNative = slices.Clone(r.RejectedNative)
	return r
}

// KVSnapshots is optional on the original KV lease. It does not advertise an
// ordinary unary maintenance method, select another endpoint, restore data or
// settle an earlier unknown write. Preparation and recovery perform no I/O.
type KVSnapshots interface {
	PrepareSnapshot(KVSnapshotRequest) (KVSnapshotPreparation, error)
	ExecuteSnapshot(context.Context, KVSnapshotPreparation, KVSnapshotOutputs) (KVSnapshotResult, error)
	LastSnapshotResult() KVSnapshotResult
}
