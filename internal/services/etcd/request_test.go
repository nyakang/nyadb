package etcd

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func TestPreparationRejectsUnsupportedAndUnboundedRequests(t *testing.T) {
	c := &Client{}
	for _, r := range []proto.Message{
		nil, (*pb.PutRequest)(nil), &pb.PutRequest{}, &pb.PutRequest{Key: []byte("k"), Value: make([]byte, (1<<20)+1)},
		&pb.RangeRequest{Key: []byte("a"), RangeEnd: []byte("z")}, &pb.RangeRequest{Key: []byte("a"), RangeEnd: []byte("a"), Limit: 1},
		&pb.RangeRequest{Key: []byte("a"), Revision: -1}, &pb.RangeRequest{Key: []byte("a"), SortTarget: 99},
		&pb.TxnRequest{}, &pb.TxnRequest{Success: []*pb.RequestOp{nil}},
		&pb.TxnRequest{Compare: []*pb.Compare{{Key: []byte("a"), Target: pb.Compare_MOD, TargetUnion: &pb.Compare_Version{Version: 1}}}, Success: []*pb.RequestOp{putOp([]byte("a"), nil, 0)}},
		&pb.TxnRequest{Success: []*pb.RequestOp{{Request: &pb.RequestOp_RequestTxn{RequestTxn: &pb.TxnRequest{}}}}},
		&pb.PutRequest{Key: []byte("a"), Lease: 1, IgnoreLease: true}, &pb.LeaseGrantRequest{TTL: -1}, &pb.LeaseRevokeRequest{},
		&pb.AuthDisableRequest{}, &pb.CompactionRequest{}, &pb.CompactionRequest{Revision: -1},
	} {
		if _, err := c.Prepare(r); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted invalid native request %T: %v", r, err)
		}
	}
	unknown := &pb.RangeRequest{Key: []byte("a")}
	unknown.ProtoReflect().SetUnknown([]byte{0xa0, 6, 1})
	if _, err := c.Prepare(unknown); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown native semantics accepted")
	}
	cycle := &pb.TxnRequest{}
	cycle.Success = []*pb.RequestOp{{Request: &pb.RequestOp_RequestTxn{RequestTxn: cycle}}}
	if _, err := c.Prepare(cycle); !errors.Is(err, ErrInvalid) {
		t.Fatal("cyclic nested transaction accepted")
	}
	if _, err := c.Prepare(&pb.TxnRequest{Success: []*pb.RequestOp{{Request: (*pb.RequestOp_RequestPut)(nil)}}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("nil native oneof accepted")
	}
	count := PrefixRange(nil, 0, 0)
	count.CountOnly = true
	if p, err := c.Prepare(count); err != nil || p.Writes() {
		t.Fatal("bounded count-only scan rejected", err)
	}
	readTxn := &pb.TxnRequest{Success: []*pb.RequestOp{{Request: &pb.RequestOp_RequestRange{RequestRange: &pb.RangeRequest{Key: []byte("a")}}}}}
	if p, err := c.Prepare(readTxn); err != nil || p.Writes() {
		t.Fatal("read-only transaction classified as write")
	}
}

func TestBinaryPrefixAndExclusiveContinuation(t *testing.T) {
	for _, test := range []struct{ prefix, start, end []byte }{
		{nil, []byte{0}, []byte{0}}, {[]byte{0xff}, []byte{0xff}, []byte{0}},
		{[]byte{'a', 0xff}, []byte{'a', 0xff}, []byte{'b'}}, {[]byte{'a', 0}, []byte{'a', 0}, []byte{'a', 1}},
	} {
		r := PrefixRange(test.prefix, 10, 9007199254740993)
		if !bytes.Equal(r.Key, test.start) || !bytes.Equal(r.RangeEnd, test.end) || r.Revision != 9007199254740993 {
			t.Fatal("binary prefix or exact revision changed")
		}
		if _, _, err := validateRequest(r); err != nil {
			t.Fatal("valid binary range rejected", err)
		}
	}
	key := []byte{255, 0}
	next := AfterKey(key)
	next[0] = 1
	if key[0] != 255 || !bytes.Equal(AfterKey(key), []byte{255, 0, 0}) {
		t.Fatal("exclusive binary boundary aliased input")
	}
}

func TestConfigRejectsAmbiguousCredentialAndTransportInputs(t *testing.T) {
	for _, cfg := range []Config{
		{Endpoint: "http://user:secret@localhost"}, {Endpoint: "http://localhost/path"}, {Endpoint: "http://localhost?x=1"},
		{Endpoint: "http://localhost?"}, {Endpoint: "http://localhost#fragment"}, {Endpoint: "http://localhost:0"},
		{Endpoint: "unix:///socket"}, {Endpoint: "localhost:2379"}, {Endpoint: "http://localhost", CAFile: "ignored"},
		{Endpoint: "http://localhost", Auth: "none", Username: "unexpected"}, {Endpoint: "http://localhost", ClientCertFile: "cert"},
		{Endpoint: "https://localhost", ClientKeyFile: "key"}, {Endpoint: "http://localhost", RequestTimeout: time.Millisecond},
	} {
		if _, _, err := normalizeConfig(cfg, ""); err == nil {
			t.Fatal("invalid etcd configuration accepted")
		}
	}
	cfg := Config{Endpoint: "https://[::1]:2379", Auth: "basic", Username: " exact name "}
	out, tc, err := normalizeConfig(cfg, " exact secret ")
	if err != nil || out.Username != cfg.Username || tc.ServerName != "::1" {
		t.Fatal("valid original configuration changed", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, Config{Endpoint: "http://localhost"}, ""); !errors.Is(err, context.Canceled) {
		t.Fatal("opening ignored pre-cancellation", err)
	}
}
