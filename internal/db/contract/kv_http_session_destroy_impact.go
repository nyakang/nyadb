package contract

import (
	"context"
	"slices"
)

type KVHTTPSessionDestroyImpactRequest struct {
	ID       string
	MaxRows  int
	MaxBytes int64
}

// These receipts were read sequentially through the original document lease.
// Complete describes the captured scoped observations, never an atomic view
// or permission to destroy a Session. No write is performed by this facet.
type KVHTTPSessionDestroyImpact struct {
	Scope    KVScope
	Request  KVHTTPSessionDestroyImpactRequest
	Session  KVResult
	Keys     KVHTTPSessionKeysObservation
	Bytes    int64
	Complete bool
}

func (o KVHTTPSessionDestroyImpact) Clone() KVHTTPSessionDestroyImpact {
	o.Scope = o.Scope.Clone()
	o.Session = o.Session.Clone()
	o.Keys = o.Keys.Clone()
	return o
}

type KVHTTPSessionDestroyHeldKey struct {
	Key         string
	ModifyIndex uint64
}

// An expectation captures the original profile, generation, UUID, Behavior
// and observed held-key identities. Later comparison does not close the race
// between a preflight read and a native destroy request.
type KVHTTPSessionDestroyExpectation struct {
	Scope        KVScope
	ID, Behavior string
	HeldKeys     []KVHTTPSessionDestroyHeldKey
}

func (e KVHTTPSessionDestroyExpectation) Clone() KVHTTPSessionDestroyExpectation {
	e.Scope = e.Scope.Clone()
	e.HeldKeys = slices.Clone(e.HeldKeys)
	return e
}

type KVHTTPSessionDestroyImpactBrowsing interface {
	ObserveHTTPSessionDestroyImpact(context.Context, KVHTTPSessionDestroyImpactRequest) (KVHTTPSessionDestroyImpact, error)
}

// A plan owns one original lease and one captured preview. Execution consumes
// it even when revalidation or cancellation prevents the native write. The
// caller keeps a cloned preview for presentation; Close revokes and waits.
type KVHTTPSessionDestroyPreparation interface {
	KVPreparation
	LocalRecordID() string
	Impact() KVHTTPSessionDestroyImpact
	Close() error
}

type KVHTTPSessionDestroyPlanning interface {
	PrepareHTTPSessionDestroy(context.Context, KVHTTPSessionDestroyImpactRequest) (KVHTTPSessionDestroyPreparation, error)
}

// Preview and Recheck are sequential original observations, not an atomic
// transaction. The containing KVResult alone records the original PUT outcome.
type KVHTTPSessionDestroyEvidence struct {
	// LocalRecordID identifies the original client operation record through
	// recovery. It is neither a Consul identity nor server write authority.
	LocalRecordID string
	Preview       KVHTTPSessionDestroyImpact
	Recheck       KVHTTPSessionDestroyImpact
	Revalidated   bool
}

func (e KVHTTPSessionDestroyEvidence) Clone() KVHTTPSessionDestroyEvidence {
	e.Preview = e.Preview.Clone()
	e.Recheck = e.Recheck.Clone()
	return e
}
