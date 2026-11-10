package etcd

import (
	"bytes"
	"context"
	"errors"
	"math"
	"testing"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func renewalObservationFixture() *RenewalRun {
	return &RenewalRun{client: &Client{identity: Identity{Header: Header{ClusterID: 1, MemberID: 2}}}, request: &pb.LeaseKeepAliveRequest{ID: 9007199254740993}}
}
func TestRenewalPlanKeepsExactOneRunAuthorizationWithoutIO(t *testing.T) {
	c := &Client{}
	request := &pb.LeaseKeepAliveRequest{ID: math.MaxInt64}
	p, err := c.PrepareRenewal(request)
	if err != nil || p.Request().ID != math.MaxInt64 {
		t.Fatal("native renewal ID narrowed", err)
	}
	request.ID = 1
	copy := p.Request()
	copy.ID = 2
	if p.Request().ID != math.MaxInt64 {
		t.Fatal("plan aliases caller")
	}
	for _, r := range []*pb.LeaseKeepAliveRequest{nil, {}, {ID: 0}} {
		if _, err := c.PrepareRenewal(r); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid ID authorized", err)
		}
	}
	request = &pb.LeaseKeepAliveRequest{ID: 7}
	request.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
	if _, err := c.PrepareRenewal(request); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown request semantics authorized")
	}
	if _, err := (&Client{}).StartRenewal(context.Background(), p); !errors.Is(err, ErrUsed) {
		t.Fatal("foreign authorization consumed", err)
	}
	c.operation.Lock()
	if _, err := c.StartRenewal(context.Background(), p); !errors.Is(err, ErrBusy) || p.used.Load() {
		t.Fatal("busy original channel consumed authorization", err)
	}
	c.operation.Unlock()
}
func TestRenewalReceiptsKeepExactUnknownFieldsAndSeparateExpiredFromForeign(t *testing.T) {
	r := renewalObservationFixture()
	response := &pb.LeaseKeepAliveResponse{Header: watchTestHeader(9), ID: r.request.ID, TTL: math.MaxInt64}
	response.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x07})
	receipt := RenewalReceipt{Attempted: true, Sequence: 1}
	if err := r.observe(&receipt, response); err != nil || !receipt.Completed || receipt.TTL != math.MaxInt64 {
		t.Fatal("original renewal response changed", err)
	}
	response.ID = 1
	decoded := &pb.LeaseKeepAliveResponse{}
	if proto.Unmarshal(receipt.Native, decoded) != nil || decoded.ID != r.request.ID || !bytes.Equal(decoded.ProtoReflect().GetUnknown(), []byte{0xa0, 0x06, 0x07}) {
		t.Fatal("complete original protobuf changed")
	}
	response = &pb.LeaseKeepAliveResponse{Header: watchTestHeader(0), ID: r.request.ID, TTL: 0}
	receipt = RenewalReceipt{}
	if err := r.observe(&receipt, response); !errors.Is(err, ErrLeaseExpired) || !receipt.Completed || receipt.Header.Revision != 0 {
		t.Fatal("original expiry zero header replaced or marked uncertain", err)
	}
	for _, bad := range []string{"id", "member", "cluster", "header", "revision", "bytes"} {
		response = &pb.LeaseKeepAliveResponse{Header: watchTestHeader(9), ID: r.request.ID, TTL: 3}
		switch bad {
		case "id":
			response.ID++
		case "member":
			response.Header.MemberId++
		case "cluster":
			response.Header.ClusterId++
		case "header":
			response.Header = nil
		case "revision":
			response.Header.Revision = 0
		case "bytes":
			response.ProtoReflect().SetUnknown(bytes.Repeat([]byte{0xa0, 0x06, 0x07}, renewalFrameBytes))
		}
		receipt = RenewalReceipt{}
		if err := r.observe(&receipt, response); err == nil || receipt.Completed {
			t.Fatal("foreign or unbounded renewal admitted", bad, err)
		}
	}
}
func TestRenewalBoundsRetainEarlierReceiptsAndTTLArithmeticCannotOverflow(t *testing.T) {
	r := renewalObservationFixture()
	for i := 0; i < renewalFrameLimit; i++ {
		if err := r.append(RenewalReceipt{Sequence: uint64(i + 1), Native: []byte{byte(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	before := r.bytes
	if err := r.append(RenewalReceipt{Native: []byte{255}}); !errors.Is(err, ErrBounds) || r.bytes != before {
		t.Fatal("overflow evicted original receipts")
	}
	for page := 0; page < 4; page++ {
		got := r.Poll()
		if len(got) != 64 {
			t.Fatal("bounded original transfer changed")
		}
		for i, v := range got {
			if v.Sequence != uint64(page*64+i+1) || len(v.Native) != 1 || v.Native[0] != byte(page*64+i) {
				t.Fatal("receipt sequence or bytes changed")
			}
		}
	}
	if r.bytes != 0 || len(r.receipts) != 0 {
		t.Fatal("poll retained transferred buffers")
	}
	r.reserve = func(int) bool { return false }
	if err := r.append(RenewalReceipt{}); !errors.Is(err, ErrBounds) || len(r.receipts) != 0 {
		t.Fatal("external retained-receipt budget bypassed")
	}
	for _, ttl := range []int64{1, 2, 3, 9007199254740993, math.MaxInt64} {
		if delay := renewalDelay(ttl); delay <= 0 || delay > 8*time.Hour || ttl < 86400 && delay >= time.Duration(ttl)*time.Second {
			t.Fatal("TTL delay expired lease or overflowed", ttl, delay)
		}
	}
}
