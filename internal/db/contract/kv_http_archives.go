package contract

import (
	"context"
	"io"
	"maps"
	"slices"
)

// An HTTP archive is a native full-scope save, independent of browsing prefixes.
// HTTP reader chunking has no native frame semantics. Restore is not part of
// this facet, and a later archive cannot settle an earlier unknown write.
type KVHTTPArchiveRequest struct {
	CompressedBytes uint64 `json:"compressed_bytes,string"`
	ExpandedBytes   uint64 `json:"expanded_bytes,string"`
}

type KVHTTPArchiveScope struct {
	Original   KVScope
	DocumentID uint64
}

func (s KVHTTPArchiveScope) Clone() KVHTTPArchiveScope { s.Original = s.Original.Clone(); return s }

type KVHTTPArchivePreparation interface {
	Snapshot() KVHTTPArchiveRequest
	Scope() KVHTTPArchiveScope
	TargetURL() string
	LocalRecordID() string
}

// This separate private receipt never enters a generic HTTP or KV result cache.
// Metadata/checksum files and original headers may contain private information.
// SourceComplete proves archive integrity only, never fsync or user publication.
type KVHTTPArchiveResult struct {
	Scope                                                         KVHTTPArchiveScope
	Request                                                       KVHTTPArchiveRequest
	LocalRecordID, TargetURL, Protocol                            string
	Status                                                        int
	Header, Trailer                                               map[string][]string
	Attempted, Ended, EOF, HTTPComplete, SourceComplete, Canceled bool
	GZIPComplete, TARComplete, ChecksumsMatch, MetadataValid      bool
	OriginalBytes, ExpandedBytes, StateBytes                      uint64
	OriginalSHA256, StateSHA256                                   []byte
	Metadata, Checksums                                           []byte
	Version, Index, Term                                          uint64
	SnapshotID, LocalError                                        string
}

func (r KVHTTPArchiveResult) Clone() KVHTTPArchiveResult {
	r.Scope = r.Scope.Clone()
	r.Header = maps.Clone(r.Header)
	for k, v := range r.Header {
		r.Header[k] = slices.Clone(v)
	}
	r.Trailer = maps.Clone(r.Trailer)
	for k, v := range r.Trailer {
		r.Trailer[k] = slices.Clone(v)
	}
	r.OriginalSHA256, r.StateSHA256 = slices.Clone(r.OriginalSHA256), slices.Clone(r.StateSHA256)
	r.Metadata, r.Checksums = slices.Clone(r.Metadata), slices.Clone(r.Checksums)
	return r
}

// The caller exclusively owns this bounded private source resource. It survives
// lease retirement/recovery without connecting or repeating any request. Its
// readers never expose writable aliases. Close revokes readers and releases the
// original budget; the document must close it when replacing/discarding evidence.
// OpenOriginal also exposes incomplete/error response prefixes, whose receipt
// explicitly distinguishes them from complete valid native archives.
type KVHTTPArchive interface {
	Receipt() KVHTTPArchiveResult
	OpenOriginal() (io.ReadCloser, error)
	Close() error
}

// Preparation performs no I/O. Capture waits for the one original executor and
// reserves finite private retention before connecting. A non-nil resource on
// failure still belongs to the caller and must be closed. File artifacts and
// explicit publication require a separate injected service.
type KVHTTPArchiveCapturing interface {
	PrepareHTTPArchive(KVHTTPArchiveRequest) (KVHTTPArchivePreparation, error)
	CaptureHTTPArchive(context.Context, KVHTTPArchivePreparation) (KVHTTPArchive, KVHTTPArchiveResult, error)
}
