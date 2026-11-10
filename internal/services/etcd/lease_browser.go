package etcd

import (
	"context"
	"sort"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

const (
	leaseBrowserLimit     = 200
	leaseBrowserByteLimit = 32 << 20
)

// LeasePageRequest selects a bounded window of one actual cluster lease list.
// After is an exclusive signed int64 boundary, not a server/MVCC cursor.
type LeasePageRequest struct {
	Limit int
	After *int64
}

type LeaseObservation struct {
	ID      int64
	Receipt Response
}

// LeasePage keeps the complete original list receipt and every attempted TTL
// receipt, including failures. Rows are signed-ID ordered. Each TTL is a
// separate live observation; this is not a transaction or a fixed snapshot.
type LeasePage struct {
	List      Response
	Rows      []LeaseObservation
	More      bool
	NextAfter *int64
	Complete  bool
}

func (p LeasePage) Clone() LeasePage {
	p.List = p.List.clone()
	p.Rows = append([]LeaseObservation(nil), p.Rows...)
	for i := range p.Rows {
		p.Rows[i].Receipt = p.Rows[i].Receipt.clone()
	}
	if p.NextAfter != nil {
		id := *p.NextAfter
		p.NextAfter = &id
	}
	return p
}

// BrowseLeases performs read-only calls on this original physical executor.
// It never falls back to previously known IDs or sends Grant/KeepAlive/Revoke.
// LeaseLeases itself has no pagination and retains its full bounded receipt.
func (c *Client) BrowseLeases(parent context.Context, request LeasePageRequest) (page LeasePage, err error) {
	if request.Limit < 1 || request.Limit > leaseBrowserLimit {
		return page, ErrInvalid
	}
	var after int64
	hasAfter := request.After != nil
	if hasAfter {
		after = *request.After
	}
	if err = parent.Err(); err != nil {
		return page, err
	}
	if !c.operation.TryLock() {
		return page, ErrBusy
	}
	defer c.operation.Unlock()
	ctx, cancel := context.WithTimeout(parent, c.config.RequestTimeout)
	defer cancel()
	p, err := c.Prepare(&pb.LeaseLeasesRequest{})
	if err != nil {
		return page, err
	}
	page.List, err = c.executeOwned(ctx, p)
	if err != nil {
		return page, err
	}
	list := page.List.native.(*pb.LeaseLeasesResponse)
	if len(list.Leases) > 10000 {
		return page, ErrBounds
	}
	ids, more := leaseWindowIDs(list.Leases, request.Limit, hasAfter, after)
	page.More = more
	captured := proto.Size(list)
	for _, id := range ids {
		// Reserve a complete transport-bounded reply before making another read.
		// Earlier receipts are never evicted, and an incomplete window has no
		// continuation. Projection/serialization budgets remain separate.
		if leaseBrowserByteLimit-captured < responseLimit {
			return page, ErrBounds
		}
		p, e := c.Prepare(&pb.LeaseTimeToLiveRequest{ID: id})
		if e != nil {
			return page, e
		}
		r, e := c.executeOwned(ctx, p)
		page.Rows = append(page.Rows, LeaseObservation{ID: id, Receipt: r})
		if r.native != nil {
			captured += proto.Size(r.native)
		}
		if e != nil {
			return page, e
		}
	}
	if page.More && len(page.Rows) > 0 {
		id := page.Rows[len(page.Rows)-1].ID
		page.NextAfter = &id
	}
	page.Complete = true
	return page, nil
}

func leaseWindowIDs(leases []*pb.LeaseStatus, limit int, hasAfter bool, after int64) ([]int64, bool) {
	ids := make([]int64, 0, len(leases))
	for _, lease := range leases {
		if !hasAfter || lease.ID > after {
			ids = append(ids, lease.ID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	more := len(ids) > limit
	return ids[:min(len(ids), limit)], more
}

// ObserveLease reads the current TTL and, only when requested, complete native
// attached keys. Lease APIs are cluster scoped; no prefix/MVCC claim is made.
func (c *Client) ObserveLease(ctx context.Context, id int64, includeKeys bool) (Response, error) {
	p, err := c.Prepare(&pb.LeaseTimeToLiveRequest{ID: id, Keys: includeKeys})
	if err != nil {
		return Response{}, err
	}
	return c.Execute(ctx, p)
}

func validateLeaseReply(request, native proto.Message) error {
	switch r := request.(type) {
	case *pb.LeaseGrantRequest:
		v, ok := native.(*pb.LeaseGrantResponse)
		if !ok || v == nil || v.ID == 0 || v.TTL <= 0 || v.Error != "" || r.ID != 0 && v.ID != r.ID {
			return ErrProtocol
		}
	case *pb.LeaseRevokeRequest:
		v, ok := native.(*pb.LeaseRevokeResponse)
		if !ok || v == nil {
			return ErrProtocol
		}
	case *pb.LeaseTimeToLiveRequest:
		v, ok := native.(*pb.LeaseTimeToLiveResponse)
		if !ok || v == nil || v.ID != r.ID || v.TTL < -1 || v.GrantedTTL < 0 || !r.Keys && len(v.Keys) > 0 {
			return ErrProtocol
		}
		seen := make(map[string]bool, len(v.Keys))
		for _, key := range v.Keys {
			if !validKey(key) || seen[string(key)] {
				return ErrProtocol
			}
			seen[string(key)] = true
		}
	case *pb.LeaseLeasesRequest:
		v, ok := native.(*pb.LeaseLeasesResponse)
		if !ok || v == nil {
			return ErrProtocol
		}
		seen := make(map[int64]bool, len(v.Leases))
		for _, lease := range v.Leases {
			if lease == nil || lease.ID == 0 || seen[lease.ID] {
				return ErrProtocol
			}
			seen[lease.ID] = true
		}
	}
	return nil
}
