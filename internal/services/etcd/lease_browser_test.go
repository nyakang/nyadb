package etcd

import (
	"bytes"
	"context"
	"errors"
	"math"
	"slices"
	"testing"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func TestLeaseWindowsKeepSignedExtremeIDsAndExclusiveZeroBoundary(t *testing.T) {
	leases := []*pb.LeaseStatus{{ID: math.MaxInt64}, {ID: -1}, {ID: 9007199254740993}, {ID: math.MinInt64}}
	for _, test := range []struct {
		after    int64
		hasAfter bool
		limit    int
		want     []int64
		more     bool
	}{
		{0, false, 2, []int64{math.MinInt64, -1}, true},
		{-1, true, 2, []int64{9007199254740993, math.MaxInt64}, false},
		{0, true, 1, []int64{9007199254740993}, true},
		{math.MaxInt64, true, 200, nil, false},
	} {
		got, more := leaseWindowIDs(leases, test.limit, test.hasAfter, test.after)
		if !slices.Equal(got, test.want) || more != test.more {
			t.Fatal("signed identity window changed", got, more)
		}
	}
	if leases[0].ID != math.MaxInt64 || leases[3].ID != math.MinInt64 {
		t.Fatal("original native list reordered")
	}
}

func TestLeaseReadsRejectForeignAndMalformedReceipts(t *testing.T) {
	id := int64(9007199254740993)
	request := &pb.LeaseTimeToLiveRequest{ID: id, Keys: true}
	for _, reply := range []proto.Message{
		&pb.LeaseTimeToLiveResponse{ID: id + 1, TTL: 10},
		&pb.LeaseTimeToLiveResponse{ID: id, TTL: -2},
		&pb.LeaseTimeToLiveResponse{ID: id, TTL: 10, GrantedTTL: -1},
		&pb.LeaseTimeToLiveResponse{ID: id, TTL: 10, Keys: [][]byte{nil}},
		&pb.LeaseTimeToLiveResponse{ID: id, TTL: 10, Keys: [][]byte{[]byte("a"), []byte("a")}},
		&pb.LeaseTimeToLiveResponse{ID: id, TTL: 10, Keys: [][]byte{bytes.Repeat([]byte{255}, 4097)}},
		&pb.StatusResponse{},
	} {
		if err := validateLeaseReply(request, reply); !errors.Is(err, ErrProtocol) {
			t.Fatalf("invalid TTL reply accepted: %T, %v", reply, err)
		}
	}
	if err := validateLeaseReply(&pb.LeaseTimeToLiveRequest{ID: id}, &pb.LeaseTimeToLiveResponse{ID: id, TTL: 10, Keys: [][]byte{[]byte("a")}}); err == nil {
		t.Fatal("unrequested attached keys accepted")
	}
	for _, reply := range []*pb.LeaseLeasesResponse{
		{Leases: []*pb.LeaseStatus{nil}},
		{Leases: []*pb.LeaseStatus{{ID: 0}}},
		{Leases: []*pb.LeaseStatus{{ID: id}, {ID: id}}},
	} {
		if err := validateLeaseReply(&pb.LeaseLeasesRequest{}, reply); !errors.Is(err, ErrProtocol) {
			t.Fatal("invalid actual list accepted", err)
		}
	}
	for _, ttl := range []int64{-1, 0, 10} {
		if err := validateLeaseReply(request, &pb.LeaseTimeToLiveResponse{ID: id, TTL: ttl, Keys: [][]byte{bytes.Repeat([]byte{255}, 4096)}}); err != nil {
			t.Fatal("complete exact ID/key or absent TTL rejected", err)
		}
	}
	if err := validateLeaseReply(&pb.LeaseLeasesRequest{}, &pb.LeaseLeasesResponse{Leases: []*pb.LeaseStatus{{ID: math.MinInt64}, {ID: math.MaxInt64}}}); err != nil {
		t.Fatal("signed native ID extremes rejected", err)
	}
}

func TestLeasePageRejectsInvalidAndCanceledPreparationWithoutIO(t *testing.T) {
	c := &Client{}
	for _, r := range []LeasePageRequest{{}, {Limit: -1}, {Limit: 201}} {
		if page, err := c.BrowseLeases(context.Background(), r); !errors.Is(err, ErrInvalid) || page.Complete || page.List.Attempted {
			t.Fatal("invalid page started I/O", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if page, err := c.BrowseLeases(ctx, LeasePageRequest{Limit: 1}); !errors.Is(err, context.Canceled) || page.List.Attempted {
		t.Fatal("canceled page started I/O", err)
	}
	c.operation.Lock()
	page, err := c.BrowseLeases(context.Background(), LeasePageRequest{Limit: 1})
	c.operation.Unlock()
	if !errors.Is(err, ErrBusy) || page.List.Attempted {
		t.Fatal("busy physical executor reused", err)
	}
}

func TestLeasePageCloneRetainsCompleteOriginalProtobufAndIndependentObservations(t *testing.T) {
	native := &pb.LeaseTimeToLiveResponse{ID: 9007199254740993, TTL: 7, GrantedTTL: 10, Keys: [][]byte{{0, 255}}}
	native.ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
	list := &pb.LeaseLeasesResponse{Leases: []*pb.LeaseStatus{{ID: native.ID}}}
	boundary := native.ID
	page := LeasePage{List: Response{Attempted: true, Completed: true, native: list}, Rows: []LeaseObservation{{ID: native.ID, Receipt: Response{Attempted: true, Completed: true, Code: codes.OK, native: native}}}, More: true, NextAfter: &boundary, Complete: true}
	clone := page.Clone()
	clone.List.native.(*pb.LeaseLeasesResponse).Leases[0].ID = 1
	clone.Rows[0].Receipt.native.(*pb.LeaseTimeToLiveResponse).Keys[0][0] = 1
	*clone.NextAfter = 1
	if page.List.Native().(*pb.LeaseLeasesResponse).Leases[0].ID != native.ID || page.Rows[0].Receipt.Native().(*pb.LeaseTimeToLiveResponse).Keys[0][0] != 0 || *page.NextAfter != native.ID {
		t.Fatal("clone mutated original list, complete bytes or boundary")
	}
	if !bytes.Equal(clone.Rows[0].Receipt.Native().ProtoReflect().GetUnknown(), []byte{0xa0, 6, 7}) {
		t.Fatal("unknown protobuf fields dropped")
	}
}
