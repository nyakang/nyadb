package contract

import (
	"context"
	"slices"
)

const (
	KVHTTPACLImpactMaxRows        = 10000
	KVHTTPACLImpactMaxReads       = 128
	KVHTTPACLImpactMaxBytes int64 = 32 << 20
)

// Impact reads one existing original target and its native inbound references.
// It does not predict dynamic binding-rule evaluation, future logins, or tokens
// in other datacenters. Separate native reads never form an atomic snapshot.
type KVHTTPACLImpactRequest struct {
	Resource, Identity string
	MaxRows, MaxReads  int
	MaxBytes           int64
}

// Receipt and body fingerprint stay together, including unmatched rows from
// stale native membership indices. All bodies remain private ACL evidence.
type KVHTTPACLImpactRead struct {
	Result     KVHTTPACLReadResult
	BodySHA256 string
}

func (r KVHTTPACLImpactRead) Clone() KVHTTPACLImpactRead {
	r.Result = r.Result.Clone()
	return r
}

// A resource is deduplicated by original native identity. Locations retain
// every observed membership path without duplicating secret-bearing rows.
type KVHTTPACLImpactLocation struct{ Read, Row int }
type KVHTTPACLImpactReference struct {
	Resource, Identity string
	Locations          []KVHTTPACLImpactLocation
}

type KVHTTPACLImpact struct {
	Scope   KVScope
	Request KVHTTPACLImpactRequest
	// Creation retains the exact original native create definition. An existing
	// AuthMethod can be upserted through that same native endpoint; its complete
	// original detail and inbound references are read when UpsertIdentity is set.
	Creation       *KVHTTPACLMutationRequest
	UpsertIdentity string
	Reads          []KVHTTPACLImpactRead // target first; all complete and partial receipts retained
	References     []KVHTTPACLImpactReference
	Dependencies   []KVHTTPACLImpactReference // requested policy/role/auth-method definitions
	Rows           int                        // includes every returned row, including duplicates and unmatched rows
	Bytes          int64
	Complete       bool // only the requested bounded native observations, never atomic CAS
}

func (o KVHTTPACLImpact) Clone() KVHTTPACLImpact {
	o.Scope = o.Scope.Clone()
	if o.Creation != nil {
		v := o.Creation.Clone()
		o.Creation = &v
	}
	o.Reads = slices.Clone(o.Reads)
	for i := range o.Reads {
		o.Reads[i] = o.Reads[i].Clone()
	}
	o.References = slices.Clone(o.References)
	for i := range o.References {
		o.References[i].Locations = slices.Clone(o.References[i].Locations)
	}
	o.Dependencies = slices.Clone(o.Dependencies)
	for i := range o.Dependencies {
		o.Dependencies[i].Locations = slices.Clone(o.Dependencies[i].Locations)
	}
	return o
}

type KVHTTPACLImpactBrowsing interface {
	ObserveHTTPACLImpact(context.Context, KVHTTPACLImpactRequest) (KVHTTPACLImpact, error)
}
