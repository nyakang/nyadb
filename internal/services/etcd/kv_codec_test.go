package etcd

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"google.golang.org/protobuf/proto"
)

func TestKVPreparationRejectsAmbiguityAndPreservesNativeExactValues(t *testing.T) {
	r := contract.KVRequest{Method: "/etcdserverpb.KV/Put", JSON: `{"key":"AP8=","value":"AP8BAg==","lease":"9007199254740993","prevKv":true}`}
	m, canonical, write, err := decodeKVRequest(r)
	if err != nil || !write {
		t.Fatal(err)
	}
	put := m.(*pb.PutRequest)
	if !bytes.Equal(put.Key, []byte{0, 255}) || put.Lease != 9007199254740993 || !put.PrevKv || !strings.Contains(canonical.JSON, `"9007199254740993"`) {
		t.Fatal("native identity lost")
	}
	for _, body := range []string{`{"key":"YQ==","key":"Yg=="}`, `{"key":"YQ==","prev_kv":true,"prevKv":false}`, `{"key":"YQ==","unknown":1}`, `{"key":null}`, `{"key":"YQ==","lease":"9223372036854775808"}`, `{"key":"YQ==","lease":9007199254740993.1}`, `{"key":"%%%"}`, `{"key":"YQ=="} {}`, `[]`} {
		if _, _, _, err := decodeKVRequest(contract.KVRequest{Method: r.Method, JSON: body}); err == nil {
			t.Fatal("ambiguous native request accepted")
		}
	}
	for _, method := range []string{"Put", "/etcdserverpb.Auth/AuthDisable", "/etcdserverpb.KV/Compact", "/etcdserverpb.Maintenance/Defragment"} {
		if _, _, _, err := decodeKVRequest(contract.KVRequest{Method: method, JSON: `{}`}); err == nil {
			t.Fatal("unsupported method accepted")
		}
	}
	txn := contract.KVRequest{Method: "/etcdserverpb.KV/Txn", JSON: `{"success":[{"request_txn":{"success":[{"request_put":{"key":"YQ=="}}]}}]}`}
	if _, _, _, err := decodeKVRequest(txn); err == nil {
		t.Fatal("nested transactions accepted")
	}
}
func TestKVReplyExactSnapshotAndExpandedCaptureBounds(t *testing.T) {
	header := &pb.ResponseHeader{ClusterId: math.MaxUint64, MemberId: 9007199254740993, Revision: 9007199254740993, RaftTerm: math.MaxUint64}
	native := &pb.TxnResponse{Header: header, Succeeded: false, Responses: []*pb.ResponseOp{{Response: &pb.ResponseOp_ResponseRange{ResponseRange: &pb.RangeResponse{Header: header, Count: 1, Kvs: []*mvccpb.KeyValue{{Key: []byte{0, 255}, Value: []byte{255, 0}, CreateRevision: 9007199254740993, ModRevision: 9007199254740995, Version: 4, Lease: 9007199254740993}}}}}}}
	out := projectKVResult(contract.KVRequest{Method: "/etcdserverpb.KV/Txn"}, Response{Attempted: true, Completed: true, Header: Header{ClusterID: header.ClusterId}, native: native})
	if !out.Completed || out.WriteOutcomeUnknown || out.JSONCaptureError != "" || out.ProjectionError != "" || out.Reply.Succeeded || out.Reply.Operations[0].Entries[0].Lease != 9007199254740993 {
		t.Fatal("receipt lost")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(out.JSON), &fields) != nil || !strings.Contains(string(fields["header"]), `"18446744073709551615"`) {
		t.Fatal("JSON integer narrowed")
	}
	clone := out.Clone()
	clone.Native[0] ^= 1
	clone.Reply.Operations[0].Entries[0].Key[0] = 1
	if out.Reply.Operations[0].Entries[0].Key[0] != 0 || bytes.Equal(clone.Native, out.Native) {
		t.Fatal("snapshot alias")
	}
	decoded := &pb.TxnResponse{}
	if proto.Unmarshal(out.Native, decoded) != nil || !proto.Equal(native, decoded) {
		t.Fatal("complete native snapshot changed")
	}
	large := &pb.StatusResponse{Header: header, Version: "3.7.3", Errors: []string{strings.Repeat("\x00", 6<<20)}}
	full := projectKVResult(contract.KVRequest{}, Response{Attempted: true, Completed: true, native: large})
	if full.JSON != "" || full.JSONCaptureError == "" || !full.Completed || full.WriteOutcomeUnknown {
		t.Fatal("expansion changed acknowledged completion")
	}
	decodedStatus := &pb.StatusResponse{}
	if proto.Unmarshal(full.Native, decodedStatus) != nil || !proto.Equal(large, decodedStatus) {
		t.Fatal("overbound JSON discarded complete native reply")
	}
	leases := &pb.LeaseLeasesResponse{Header: header, Leases: make([]*pb.LeaseStatus, 10001)}
	for i := range leases.Leases {
		leases.Leases[i] = &pb.LeaseStatus{ID: int64(i) + 1}
	}
	bounded := projectKVResult(contract.KVRequest{}, Response{Attempted: true, Completed: true, native: leases})
	if bounded.Reply != nil || bounded.ProjectionError == "" || bounded.JSON == "" || !bounded.Completed || len(bounded.Native) == 0 {
		t.Fatal("projection bound erased native evidence")
	}
}
