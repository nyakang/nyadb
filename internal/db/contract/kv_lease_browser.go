package contract

import (
	"context"
	"slices"
)

// A signed exclusive boundary belongs to a client window of an actual cluster
// list. It does not identify a fixed server snapshot or a key-prefix grant.
type KVLeasePageRequest struct {
	Limit int
	After *int64
}

func (r KVLeasePageRequest) Clone() KVLeasePageRequest {
	if r.After != nil {
		id := *r.After
		r.After = &id
	}
	return r
}

type KVLeaseObservation struct {
	ID     int64
	Result KVResult
}

func (r KVLeaseObservation) Clone() KVLeaseObservation {
	r.Result = r.Result.Clone()
	return r
}

// Complete means the selected ID window completed every TTL observation.
// List and each TTL result retain their own complete native receipt and header.
type KVLeasePage struct {
	Scope     KVScope
	Request   KVLeasePageRequest
	List      KVResult
	Rows      []KVLeaseObservation
	More      bool
	NextAfter *int64
	Complete  bool
}

func (p KVLeasePage) Clone() KVLeasePage {
	p.Scope = p.Scope.Clone()
	p.Request = p.Request.Clone()
	p.List = p.List.Clone()
	p.Rows = slices.Clone(p.Rows)
	for i := range p.Rows {
		p.Rows[i] = p.Rows[i].Clone()
	}
	if p.NextAfter != nil {
		id := *p.NextAfter
		p.NextAfter = &id
	}
	return p
}

type KVLeaseDetailRequest struct {
	ID          int64
	IncludeKeys bool
}

type KVLeaseDetail struct {
	Scope   KVScope
	Request KVLeaseDetailRequest
	Result  KVResult
}

func (r KVLeaseDetail) Clone() KVLeaseDetail {
	r.Scope = r.Scope.Clone()
	r.Result = r.Result.Clone()
	return r
}

// This optional read-only facet belongs to the same original KVRequestLease.
// Reservation, recovery, cancellation, physical ownership and Close therefore
// share its existing lifecycle. Recovery does not repeat these observations.
type KVLeaseBrowsing interface {
	BrowseLeases(context.Context, KVLeasePageRequest) (KVLeasePage, error)
	ObserveLease(context.Context, KVLeaseDetailRequest) (KVLeaseDetail, error)
}
