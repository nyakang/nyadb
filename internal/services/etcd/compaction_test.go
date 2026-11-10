package etcd

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func TestCompactionPreparationKeepsExactRevisionPhysicalOptionAndNativeSnapshot(t *testing.T) {
	c := &Client{}
	for _, physical := range []bool{false, true} {
		request := &pb.CompactionRequest{Revision: math.MaxInt64, Physical: physical}
		plan, err := c.Prepare(request)
		if err != nil || !plan.Writes() || plan.method != "/etcdserverpb.KV/Compact" {
			t.Fatal("native Compact not a one-use write", err)
		}
		request.Revision, request.Physical = 1, !physical
		native := plan.Native().(*pb.CompactionRequest)
		if native.Revision != math.MaxInt64 || native.Physical != physical {
			t.Fatal("native original compaction mutated")
		}
		native.Revision = 2
		if plan.Native().(*pb.CompactionRequest).Revision != math.MaxInt64 {
			t.Fatal("compaction snapshot aliases caller")
		}
		input := contract.KVRequest{Method: plan.method, JSON: `{"revision":"9223372036854775807","physical":` + map[bool]string{false: "false", true: "true"}[physical] + `}`}
		parsed, canonical, write, err := decodeKVRequest(input)
		if err != nil || !write || !strings.Contains(canonical.JSON, `"9223372036854775807"`) || parsed.(*pb.CompactionRequest).Physical != physical {
			t.Fatal("neutral compaction narrowed revision or dropped physical option", err)
		}
	}
	for _, body := range []string{`{}`, `{"revision":"0"}`, `{"revision":"-1"}`, `{"revision":"9223372036854775808"}`, `{"revision":9007199254740993.1}`, `{"revision":"1","revision":"2"}`, `{"revision":"1","physical":1}`, `{"revision":"1","key":"YQ=="}`, `{"revision":"1","range_end":"AA=="}`, `{"revision":"1","physical":true,"unknown":true}`} {
		if _, _, _, err := decodeKVRequest(contract.KVRequest{Method: "/etcdserverpb.KV/Compact", JSON: body}); err == nil {
			t.Fatal("ambiguous or prefix-scoped Compact accepted")
		}
	}
	unknown := &pb.CompactionRequest{Revision: 1}
	unknown.ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
	if _, err := c.Prepare(unknown); err == nil {
		t.Fatal("unknown native compaction option accepted")
	}
	txn := &pb.TxnRequest{Success: []*pb.RequestOp{{Request: &pb.RequestOp_RequestRange{RequestRange: &pb.RangeRequest{Key: []byte("a")}}}}}
	if p, err := c.Prepare(txn); err != nil || p.Writes() {
		t.Fatal("adding compaction changed read-only transaction semantics")
	}
}

func TestCompactionReplyRetainsCompleteNativeHeaderAndUnknownFieldsWithoutInventedRevision(t *testing.T) {
	h := &pb.ResponseHeader{ClusterId: math.MaxUint64, MemberId: 9007199254740993, Revision: 9007199254740995, RaftTerm: math.MaxUint64}
	native := &pb.CompactionResponse{Header: h}
	native.ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
	request := contract.KVRequest{Method: "/etcdserverpb.KV/Compact", JSON: `{"revision":"9007199254740993","physical":true}`}
	r := projectKVResult(request, Response{Attempted: true, Completed: true, Header: header(h), native: native})
	captured := &pb.CompactionResponse{}
	if r.ProjectionError != "" || r.Reply == nil || r.Reply.Kind != "compaction" || r.Header.Revision != h.Revision || r.Request != request || proto.Unmarshal(r.Native, captured) != nil || !proto.Equal(captured, native) {
		t.Fatal("original native compaction receipt was projected incorrectly")
	}
	if !bytes.Equal(captured.ProtoReflect().GetUnknown(), []byte{0xa0, 6, 7}) || strings.Contains(r.JSON, "9007199254740993\"}") {
		t.Fatal("native extensions lost or requested revision manufactured as reply")
	}
	clone := r.Clone()
	clone.Native[0] ^= 1
	if bytes.Equal(clone.Native, r.Native) {
		t.Fatal("complete compaction native bytes alias caller")
	}
}
