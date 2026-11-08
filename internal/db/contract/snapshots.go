package contract

import (
	"bytes"
	"context"
	"io"
	"slices"
	"time"
)

type SnapshotScope string

const (
	SnapshotCollection SnapshotScope = "collection"
	SnapshotShard      SnapshotScope = "shard"
	SnapshotStorage    SnapshotScope = "storage"
)

// Storage targets have an empty Collection. A shard belongs to a qualified
// physical collection; neither aliases nor presentation labels authorize I/O.
type SnapshotTarget struct {
	Scope      SnapshotScope
	Collection NamespaceRef
	Shard      uint32
}
type SnapshotDescription struct {
	Name, CreationTime, Checksum string
	Size                         uint64
	HasCreationTime              bool
	Raw                          []byte
}
type SnapshotRequest struct {
	Method, Path, ContentType string
	Body                      []byte
}
type SnapshotResponse struct {
	Status      int
	ContentType string
	Body        []byte
	Incomplete  bool
}
type SnapshotRead struct {
	Response          SnapshotResponse
	BodyTruncated     bool
	OriginalBodyBytes int
}
type SnapshotMetadata struct {
	Object        ObjectRef
	Target        SnapshotTarget
	Collections   []NamespaceRef
	Aliases       []CollectionAlias
	Configuration string
	Snapshots     []SnapshotDescription
	Reads         []SnapshotRead
}
type SnapshotSource struct {
	Name, SHA256 string
	Size         uint64
}
type SnapshotRecovery struct {
	Location, Checksum, Priority string
}
type SnapshotDraft struct {
	Action   string
	Wait     bool
	Selected SnapshotDescription
	Source   SnapshotSource
	Recovery SnapshotRecovery
}
type SnapshotTransferOptions struct {
	MaxBytes uint64
	Timeout  time.Duration
}
type SnapshotReceipt struct {
	Action            string
	Target            SnapshotTarget
	State             string
	Response          SnapshotResponse
	HasResult, Result bool
	HasNullResult     bool
	Description       *SnapshotDescription
	SourceBytesRead   uint64
	SourceSHA256      string
}
type SnapshotTransfer struct {
	Target                          SnapshotTarget
	Name, Path                      string
	Response                        SnapshotResponse
	ExpectedSize, Received, Written uint64
	ExpectedChecksum, SHA256        string
	ContentLength                   int64
	Prefix                          []byte
	Complete                        bool
}
type SnapshotResult struct {
	Object   ObjectRef
	Action   string
	Target   SnapshotTarget
	State    string
	Receipt  SnapshotReceipt
	Transfer SnapshotTransfer
	Metadata SnapshotMetadata
	Matched  bool // Explicit archive create/delete observation only.
	// Recovery exposes a live physical collection count, also for shard targets.
	// It cannot prove archive/data equivalence, and is never a shard count.
	Count    uint64
	HasCount bool
}

// Adapter-private handles retain immutable observations and one-attempt state.
// Accessors return copies. Caller-defined handles do not authorize execution.
type SnapshotObservation interface {
	Snapshot() SnapshotMetadata
}
type SnapshotPlan interface {
	Action() string
	Target() SnapshotTarget
	Preview() SnapshotRequest
	UploadSource() (SnapshotSource, bool)
	RestoresData() bool
}
type SnapshotLease interface {
	Object() ObjectRef
	Generation() uint64
	Read(context.Context, SnapshotTarget) (SnapshotObservation, error)
	Prepare(SnapshotObservation, SnapshotDraft) (SnapshotPlan, error)
	Submit(context.Context, SnapshotPlan) (SnapshotResult, error)
	// Admission owns the stream, including rejected requests and preflight.
	// Cancellation/Close waits for the original stream's Close acknowledgement.
	Upload(context.Context, SnapshotPlan, io.ReadCloser, SnapshotTransferOptions) (SnapshotResult, error)
	Download(context.Context, SnapshotPlan, io.WriteCloser, SnapshotTransferOptions) (SnapshotResult, error)
	Verify(context.Context) (SnapshotResult, error)
	Close() error
}
type SnapshotProvider interface {
	SupportsSnapshots(ObjectRef, SnapshotTarget, string) bool
	ReserveSnapshotManagementDocument(ObjectRef, uint64) (SnapshotLease, error)
}

func CloneSnapshotDescription(d SnapshotDescription) SnapshotDescription {
	d.Raw = bytes.Clone(d.Raw)
	return d
}
func CloneSnapshotRequest(r SnapshotRequest) SnapshotRequest {
	r.Body = bytes.Clone(r.Body)
	return r
}
func CloneSnapshotMetadata(m SnapshotMetadata) SnapshotMetadata {
	m.Collections, m.Aliases = slices.Clone(m.Collections), slices.Clone(m.Aliases)
	m.Snapshots, m.Reads = slices.Clone(m.Snapshots), slices.Clone(m.Reads)
	for i := range m.Snapshots {
		m.Snapshots[i] = CloneSnapshotDescription(m.Snapshots[i])
	}
	for i := range m.Reads {
		m.Reads[i].Response.Body = bytes.Clone(m.Reads[i].Response.Body)
	}
	return m
}
func CloneSnapshotDraft(d SnapshotDraft) SnapshotDraft {
	d.Selected = CloneSnapshotDescription(d.Selected)
	return d
}
func CloneSnapshotResult(r SnapshotResult) SnapshotResult {
	r.Receipt.Response.Body = bytes.Clone(r.Receipt.Response.Body)
	if r.Receipt.Description != nil {
		d := CloneSnapshotDescription(*r.Receipt.Description)
		r.Receipt.Description = &d
	}
	r.Transfer.Response.Body = bytes.Clone(r.Transfer.Response.Body)
	r.Transfer.Prefix = bytes.Clone(r.Transfer.Prefix)
	r.Metadata = CloneSnapshotMetadata(r.Metadata)
	return r
}
