package etcd

import (
	"fmt"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const nativeJSONLimit = 32 << 20

func decodeKVRequest(r contract.KVRequest) (proto.Message, contract.KVRequest, bool, error) {
	if len(r.JSON) > 16<<20 || !utf8.ValidString(r.JSON) {
		return nil, r, false, ErrBounds
	}
	var m proto.Message
	switch r.Method {
	case "/etcdserverpb.KV/Range":
		m = &pb.RangeRequest{}
	case "/etcdserverpb.KV/Put":
		m = &pb.PutRequest{}
	case "/etcdserverpb.KV/DeleteRange":
		m = &pb.DeleteRangeRequest{}
	case "/etcdserverpb.KV/Txn":
		m = &pb.TxnRequest{}
	case "/etcdserverpb.Maintenance/Status":
		m = &pb.StatusRequest{}
	case "/etcdserverpb.Lease/LeaseGrant":
		m = &pb.LeaseGrantRequest{}
	case "/etcdserverpb.Lease/LeaseRevoke":
		m = &pb.LeaseRevokeRequest{}
	case "/etcdserverpb.Lease/LeaseTimeToLive":
		m = &pb.LeaseTimeToLiveRequest{}
	case "/etcdserverpb.Lease/LeaseLeases":
		m = &pb.LeaseLeasesRequest{}
	case "/etcdserverpb.Lease/LeaseKeepAlive":
		m = &pb.LeaseKeepAliveRequest{}
	default:
		return nil, r, false, ErrInvalid
	}
	if err := (protojson.UnmarshalOptions{RecursionLimit: 16}).Unmarshal([]byte(r.JSON), m); err != nil {
		return nil, r, false, ErrInvalid
	}
	method, write, err := validateRequest(m)
	if err != nil || method != r.Method {
		return nil, r, false, ErrInvalid
	}
	if proto.Size(m) > requestLimit {
		return nil, r, false, ErrBounds
	}
	b, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(m)
	if err != nil {
		return nil, r, false, ErrInvalid
	}
	r.JSON = string(b)
	return m, r, write, nil
}
func neutralHeader(h *pb.ResponseHeader) contract.KVHeader {
	if h == nil {
		return contract.KVHeader{}
	}
	return contract.KVHeader{ClusterID: h.ClusterId, MemberID: h.MemberId, Revision: h.Revision, RaftTerm: h.RaftTerm}
}

// Bound JSON before serialization, including repeated default-valued messages.
// A native protobuf byte limit alone does not bound its expanded JSON size.
func jsonBound(m protoreflect.Message, remaining *int, depth int) bool {
	if depth > 16 {
		return false
	}
	spend := func(n int) bool { *remaining -= n; return *remaining >= 0 }
	if !spend(2) {
		return false
	}
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		if f.ContainingOneof() != nil && !m.Has(f) {
			continue
		}
		if !spend(len(f.Name()) + 5) {
			return false
		}
		if f.IsMap() {
			return false
		}
		value := m.Get(f)
		scalar := func(v protoreflect.Value) bool {
			switch f.Kind() {
			case protoreflect.MessageKind:
				if !v.Message().IsValid() {
					return spend(4)
				}
				return jsonBound(v.Message(), remaining, depth+1)
			case protoreflect.StringKind:
				return spend(6*len(v.String()) + 2)
			case protoreflect.BytesKind:
				return spend(4*((len(v.Bytes())+2)/3) + 2)
			case protoreflect.EnumKind:
				e := f.Enum().Values().ByNumber(v.Enum())
				if e != nil {
					return spend(len(e.Name()) + 2)
				}
				return spend(22)
			default:
				return spend(24)
			}
		}
		if f.IsList() {
			list := value.List()
			if !spend(list.Len() + 2) {
				return false
			}
			for j := 0; j < list.Len(); j++ {
				if !scalar(list.Get(j)) {
					return false
				}
			}
		} else if !scalar(value) {
			return false
		}
	}
	return true
}
func projectKVResult(request contract.KVRequest, r Response) contract.KVResult {
	out := contract.KVResult{Request: request, Attempted: r.Attempted, Completed: r.Completed, WriteOutcomeUnknown: r.WriteOutcomeUnknown, Code: r.Code.String(), Header: contract.KVHeader{ClusterID: r.Header.ClusterID, MemberID: r.Header.MemberID, Revision: r.Header.Revision, RaftTerm: r.Header.RaftTerm}}
	m := r.Native()
	if m == nil {
		return out
	}
	var err error
	out.Native, err = proto.Marshal(m)
	if err != nil {
		out.ProjectionError = "native protobuf capture failed"
		return out
	}
	budget := nativeJSONLimit
	if jsonBound(m.ProtoReflect(), &budget, 0) {
		b, e := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(m)
		if e == nil && len(b) <= nativeJSONLimit {
			out.JSON = string(b)
		} else {
			out.JSONCaptureError = "native JSON capture exceeds limits or is invalid"
		}
	} else {
		out.JSONCaptureError = "native JSON expansion exceeds 32 MiB; full protobuf retained"
	}
	rows := 10000
	reply, e := projectReply(m, &rows, 0)
	if e != nil {
		out.ProjectionError = "typed native reply exceeds projection limits or contains an unsupported response; full protobuf retained"
	} else {
		out.Reply = &reply
	}
	return out
}
func projectEntries(values []*mvccpb.KeyValue, budget *int) ([]contract.KVEntry, error) {
	*budget -= len(values)
	if *budget < 0 {
		return nil, ErrBounds
	}
	out := make([]contract.KVEntry, len(values))
	for i, v := range values {
		if v == nil {
			return nil, ErrProtocol
		}
		out[i] = contract.KVEntry{Key: append([]byte(nil), v.Key...), Value: append([]byte(nil), v.Value...), CreateRevision: v.CreateRevision, ModRevision: v.ModRevision, Version: v.Version, Lease: v.Lease}
	}
	return out, nil
}
func projectReply(m proto.Message, budget *int, depth int) (contract.KVReply, error) {
	r := contract.KVReply{Header: neutralHeader(responseHeader(m))}
	if depth > 16 {
		return r, ErrBounds
	}
	var err error
	switch v := m.(type) {
	case *pb.RangeResponse:
		r.Kind = "range"
		r.Count, r.More = v.Count, v.More
		r.Entries, err = projectEntries(v.Kvs, budget)
	case *pb.PutResponse:
		r.Kind = "put"
		if v.PrevKv != nil {
			r.Entries, err = projectEntries([]*mvccpb.KeyValue{v.PrevKv}, budget)
		}
	case *pb.DeleteRangeResponse:
		r.Kind = "delete"
		r.Deleted = v.Deleted
		r.Entries, err = projectEntries(v.PrevKvs, budget)
	case *pb.TxnResponse:
		r.Kind = "txn"
		r.Succeeded = v.Succeeded
		*budget -= len(v.Responses)
		if *budget < 0 {
			return r, ErrBounds
		}
		r.Operations = make([]contract.KVReply, len(v.Responses))
		for i, op := range v.Responses {
			if op == nil {
				return r, ErrProtocol
			}
			var inner proto.Message
			switch x := op.Response.(type) {
			case *pb.ResponseOp_ResponseRange:
				inner = x.ResponseRange
			case *pb.ResponseOp_ResponsePut:
				inner = x.ResponsePut
			case *pb.ResponseOp_ResponseDeleteRange:
				inner = x.ResponseDeleteRange
			case *pb.ResponseOp_ResponseTxn:
				inner = x.ResponseTxn
			default:
				return r, ErrProtocol
			}
			if inner == nil || !inner.ProtoReflect().IsValid() {
				return r, ErrProtocol
			}
			r.Operations[i], err = projectReply(inner, budget, depth+1)
			if err != nil {
				return r, err
			}
		}
	case *pb.LeaseGrantResponse:
		r.Kind = "lease_grant"
		r.Lease = &contract.KVLeaseReceipt{ID: v.ID, TTL: v.TTL, Error: v.Error}
	case *pb.LeaseKeepAliveResponse:
		r.Kind = "lease_keepalive"
		r.Lease = &contract.KVLeaseReceipt{ID: v.ID, TTL: v.TTL}
	case *pb.LeaseRevokeResponse:
		r.Kind = "lease_revoke"
	case *pb.LeaseTimeToLiveResponse:
		r.Kind = "lease_ttl"
		*budget -= len(v.Keys)
		if *budget < 0 {
			return r, ErrBounds
		}
		r.Lease = &contract.KVLeaseReceipt{ID: v.ID, TTL: v.TTL, GrantedTTL: v.GrantedTTL, Keys: make([][]byte, len(v.Keys))}
		for i, k := range v.Keys {
			r.Lease.Keys[i] = append([]byte(nil), k...)
		}
	case *pb.LeaseLeasesResponse:
		r.Kind = "lease_list"
		*budget -= len(v.Leases)
		if *budget < 0 {
			return r, ErrBounds
		}
		r.Lease = &contract.KVLeaseReceipt{IDs: make([]int64, len(v.Leases))}
		for i, l := range v.Leases {
			if l == nil {
				return r, ErrProtocol
			}
			r.Lease.IDs[i] = l.ID
		}
	case *pb.StatusResponse:
		r.Kind = "status"
		r.Status = &contract.KVStatus{Version: v.Version, DBSize: v.DbSize, DBSizeInUse: v.DbSizeInUse, Leader: v.Leader, RaftIndex: v.RaftIndex, RaftTerm: v.RaftTerm, RaftAppliedIndex: v.RaftAppliedIndex, Errors: append([]string(nil), v.Errors...), IsLearner: v.IsLearner}
	default:
		return r, fmt.Errorf("%w: unsupported native response", ErrProtocol)
	}
	return r, err
}
