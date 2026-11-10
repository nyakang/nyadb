package etcd

import (
	"errors"
	"math"
	"testing"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func TestLeaseGrantReplyMustAcknowledgeRequestedIdentity(t *testing.T) {
	id := int64(9007199254740993)
	for _, request := range []*pb.LeaseGrantRequest{{TTL: 60}, {ID: id, TTL: 60}} {
		for _, reply := range []proto.Message{
			&pb.LeaseGrantResponse{ID: 0, TTL: 60},
			&pb.LeaseGrantResponse{ID: id, TTL: 0},
			&pb.LeaseGrantResponse{ID: id, TTL: -1},
			&pb.LeaseGrantResponse{ID: id, TTL: 60, Error: "denied"},
			&pb.LeaseRevokeResponse{},
		} {
			if err := validateLeaseReply(request, reply); !errors.Is(err, ErrProtocol) {
				t.Fatal("invalid Grant receipt admitted", err)
			}
		}
		if err := validateLeaseReply(request, &pb.LeaseGrantResponse{ID: id, TTL: 61}); err != nil {
			t.Fatal("service adjusted TTL rejected", err)
		}
	}
	if err := validateLeaseReply(&pb.LeaseGrantRequest{ID: id, TTL: 60}, &pb.LeaseGrantResponse{ID: id + 1, TTL: 60}); !errors.Is(err, ErrProtocol) {
		t.Fatal("foreign requested Grant ID admitted", err)
	}
	for _, id := range []int64{math.MinInt64, math.MaxInt64} {
		if err := validateLeaseReply(&pb.LeaseGrantRequest{ID: id, TTL: 60}, &pb.LeaseGrantResponse{ID: id, TTL: 60}); err != nil {
			t.Fatal("signed native identity narrowed", err)
		}
	}
	if err := validateLeaseReply(&pb.LeaseRevokeRequest{ID: id}, &pb.LeaseRevokeResponse{}); err != nil {
		t.Fatal("native revoke acknowledgement rejected", err)
	}
}
