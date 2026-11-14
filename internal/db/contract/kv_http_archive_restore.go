package contract

import (
	"context"
	"slices"
)

// Restoration replaces the full datacenter backend, including ACL secrets.
// The bytes and target are immutable; a later read never acknowledges a restore.
type KVHTTPArchiveRestoreRequest struct {
	Archive       []byte
	ExpandedBytes uint64
}

func (r KVHTTPArchiveRestoreRequest) Clone() KVHTTPArchiveRestoreRequest {
	r.Archive = slices.Clone(r.Archive)
	return r
}

type KVHTTPArchiveRestorePreparation interface {
	Request() KVHTTPArchiveRestoreRequest
	Scope() KVHTTPArchiveScope
	TargetURL() string
	LocalRecordID() string
	Source() KVHTTPArchiveResult
	Close() error
}
type KVHTTPArchiveRestoreResult struct {
	Scope                                            KVHTTPArchiveScope
	Request                                          KVHTTPArchiveRestoreRequest
	Source                                           KVHTTPArchiveResult
	LocalRecordID, TargetURL                         string
	HTTP                                             KVHTTPResult
	Attempted, Ended, Completed, WriteOutcomeUnknown bool
	RevokedProfiles                                  []int64
}

func (r KVHTTPArchiveRestoreResult) Clone() KVHTTPArchiveRestoreResult {
	r.Scope = r.Scope.Clone()
	r.Request = r.Request.Clone()
	r.Source = r.Source.Clone()
	r.HTTP = r.HTTP.Clone()
	r.RevokedProfiles = slices.Clone(r.RevokedProfiles)
	return r
}

type KVHTTPArchiveRestoring interface {
	PrepareHTTPArchiveRestore(KVHTTPArchiveRestoreRequest) (KVHTTPArchiveRestorePreparation, error)
	ExecuteHTTPArchiveRestore(context.Context, KVHTTPArchiveRestorePreparation) (KVHTTPArchiveRestoreResult, error)
	LastHTTPArchiveRestore() KVHTTPArchiveRestoreResult
}

// The runtime revokes all other affected handles before dispatch. The original
// provider drains every other executor in this session while excluding this lease.
type KVHTTPArchiveRestoreQuiescing interface{ QuiesceHTTPArchiveRestore(KVRequestLease) error }
