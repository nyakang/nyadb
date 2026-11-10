package etcd

import (
	"context"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func leaseBrowserRequest(native proto.Message) contract.KVRequest {
	method, _, _ := validateRequest(native)
	raw, _ := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(native)
	return contract.KVRequest{Method: method, JSON: string(raw)}
}

// Browsing shares the original document channel and operation lock. It keeps
// its returned receipts separate from the existing one-shot LastResult.
func (l *kvLease) BrowseLeases(parent context.Context, request contract.KVLeasePageRequest) (out contract.KVLeasePage, err error) {
	request = request.Clone()
	out.Scope, out.Request = l.Scope(), request
	if request.Limit < 1 || request.Limit > leaseBrowserLimit {
		return out, ErrInvalid
	}
	if !l.ops.TryLock() {
		return out, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	ctx, done, err := l.request(parent)
	if err != nil {
		return out, err
	}
	defer done()
	if err = l.connect(ctx); err != nil {
		return out, err
	}
	page, err := l.client.BrowseLeases(ctx, LeasePageRequest{Limit: request.Limit, After: request.After})
	out.List = projectKVResult(leaseBrowserRequest(&pb.LeaseLeasesRequest{}), page.List)
	out.Rows = make([]contract.KVLeaseObservation, len(page.Rows))
	for i, row := range page.Rows {
		out.Rows[i] = contract.KVLeaseObservation{ID: row.ID, Result: projectKVResult(leaseBrowserRequest(&pb.LeaseTimeToLiveRequest{ID: row.ID}), row.Receipt)}
	}
	out.More, out.NextAfter, out.Complete = page.More, page.NextAfter, page.Complete
	if out.NextAfter != nil {
		id := *out.NextAfter
		out.NextAfter = &id
	}
	if !l.client.Alive() {
		err = l.markLost(err)
	}
	return out, err
}

func (l *kvLease) ObserveLease(parent context.Context, request contract.KVLeaseDetailRequest) (out contract.KVLeaseDetail, err error) {
	out.Scope, out.Request = l.Scope(), request
	if request.ID == 0 {
		return out, ErrInvalid
	}
	if !l.ops.TryLock() {
		return out, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	ctx, done, err := l.request(parent)
	if err != nil {
		return out, err
	}
	defer done()
	if err = l.connect(ctx); err != nil {
		return out, err
	}
	r, err := l.client.ObserveLease(ctx, request.ID, request.IncludeKeys)
	out.Result = projectKVResult(leaseBrowserRequest(&pb.LeaseTimeToLiveRequest{ID: request.ID, Keys: request.IncludeKeys}), r)
	if !l.client.Alive() {
		err = l.markLost(err)
	}
	return out, err
}

var _ contract.KVLeaseBrowsing = (*kvLease)(nil)
