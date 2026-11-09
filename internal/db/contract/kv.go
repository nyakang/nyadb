package contract

import (
	"context"
	"errors"
	"slices"
)

var (
	ErrKVLeaseLimit = errors.New("native KV document lease limit reached")
	ErrKVLeaseLost  = errors.New("original KV lease lost; recover explicitly without replay")
	ErrKVPlanUsed   = errors.New("KV request preparation already used or belongs to another lease")
	ErrKVBusy       = errors.New("original KV lease has an active request")
)

// Prefix is a byte scope for browsing, not a database name or permission claim.
// Native requests contain absolute keys; the adapter never prepends Prefix.
// Generation is contextual in adapters and public only at the runtime boundary.
type KVScope struct {
	Namespace  NamespaceRef
	Generation uint64
	Prefix     []byte
	Header     KVHeader
	Version    string
}

func (s KVScope) Clone() KVScope { s.Prefix = slices.Clone(s.Prefix); return s }

type KVHeader struct {
	ClusterID, MemberID uint64
	Revision            int64
	RaftTerm            uint64
}
type KVRequest struct{ Method, JSON string }
type KVPreparation interface {
	Snapshot() KVRequest
	Writes() bool
}
type KVEntry struct {
	Key, Value                                  []byte
	CreateRevision, ModRevision, Version, Lease int64
}

func (e KVEntry) Clone() KVEntry {
	e.Key = slices.Clone(e.Key)
	e.Value = slices.Clone(e.Value)
	return e
}

type KVLeaseReceipt struct {
	ID, TTL, GrantedTTL int64
	Keys                [][]byte
	IDs                 []int64
	Error               string
}
type KVStatus struct {
	Version                                       string
	DBSize, DBSizeInUse                           int64
	Leader, RaftIndex, RaftTerm, RaftAppliedIndex uint64
	Errors                                        []string
	IsLearner                                     bool
}

// Kind identifies range/put/delete/txn/lease/status. Entries are Range KVs or
// previous KVs; Operations retain original Txn response order and headers.
type KVReply struct {
	Kind            string
	Header          KVHeader
	Entries         []KVEntry
	Count, Deleted  int64
	More, Succeeded bool
	Operations      []KVReply
	Lease           *KVLeaseReceipt
	Status          *KVStatus
}

func (r KVReply) Clone() KVReply {
	r.Entries = slices.Clone(r.Entries)
	for i := range r.Entries {
		r.Entries[i] = r.Entries[i].Clone()
	}
	r.Operations = slices.Clone(r.Operations)
	for i := range r.Operations {
		r.Operations[i] = r.Operations[i].Clone()
	}
	if r.Lease != nil {
		x := *r.Lease
		x.IDs = slices.Clone(x.IDs)
		x.Keys = slices.Clone(x.Keys)
		for i := range x.Keys {
			x.Keys[i] = slices.Clone(x.Keys[i])
		}
		r.Lease = &x
	}
	if r.Status != nil {
		x := *r.Status
		x.Errors = slices.Clone(x.Errors)
		r.Status = &x
	}
	return r
}

// Native contains the complete protobuf reply, including fields unknown to the
// typed projection. JSON and Reply are bounded optional projections; capture
// errors never turn an acknowledged native write into an unconfirmed write.
type KVResult struct {
	Request                                   KVRequest
	Attempted, Completed, WriteOutcomeUnknown bool
	Code                                      string
	Header                                    KVHeader
	Native                                    []byte
	JSON, JSONCaptureError, ProjectionError   string
	Reply                                     *KVReply
}

func (r KVResult) Clone() KVResult {
	r.Native = slices.Clone(r.Native)
	if r.Reply != nil {
		x := r.Reply.Clone()
		r.Reply = &x
	}
	return r
}

type KVRecovery struct {
	Lease KVRequestLease
	Last  KVResult
}
type KVRequestLease interface {
	Scope() KVScope
	CheckKVLease() error
	Prepare(KVRequest) (KVPreparation, error) // pure; captures an immutable one-use request
	Preconnect(context.Context) error
	Execute(context.Context, KVPreparation) (KVResult, error)
	LastResult() KVResult
	Close() error // revokes and waits for the original executor, then clears caches
}
type KVRequestsProvider interface {
	KVScope() KVScope
	ReserveKV(uint64) (KVRequestLease, error)     // no I/O; document identity is nonzero
	RecoverKV(KVRequestLease) (KVRecovery, error) // settles old work; new reads remain explicit
}
