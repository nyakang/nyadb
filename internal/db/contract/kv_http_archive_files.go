package contract

import (
	"context"
	"io"
	"slices"
)

type KVHTTPArchiveCapture struct {
	Original                             KVHTTPArchiveResult
	Stored, FilesSynced, DirectorySynced bool
	ArchiveBytes, ReceiptBytes           uint64
	ArchiveSHA256, ReceiptSHA256         []byte
	StorageError                         string
}

func (r KVHTTPArchiveCapture) Clone() KVHTTPArchiveCapture {
	r.Original = r.Original.Clone()
	r.ArchiveSHA256 = slices.Clone(r.ArchiveSHA256)
	r.ReceiptSHA256 = slices.Clone(r.ReceiptSHA256)
	return r
}

type KVHTTPStoredArchive interface {
	Receipt() KVHTTPArchiveCapture
	OpenArchive() (io.ReadCloser, error)
	OpenReceipt() (io.ReadCloser, error)
	Close() error
}
type KVHTTPArchiveStoragePreparation interface {
	Scope() KVHTTPArchiveScope
	Snapshot() KVHTTPArchiveRequest
}
type KVHTTPArchiveArtifacts interface {
	PrepareHTTPArchiveStorage(KVRequestLease, KVHTTPArchiveRequest) (KVHTTPArchiveStoragePreparation, error)
	CaptureHTTPArchiveStorage(context.Context, KVHTTPArchiveStoragePreparation) (KVHTTPStoredArchive, KVHTTPArchive, KVHTTPArchiveCapture, error)
	PrepareHTTPArchiveFile(context.Context, KVHTTPStoredArchive, KVSnapshotFileChoice) (KVSnapshotFilePreparation, error)
	PublishFile(context.Context, KVSnapshotFilePreparation) (KVSnapshotFileResult, error)
}
