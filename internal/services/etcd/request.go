package etcd

import (
	"bytes"
	"sync/atomic"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

const (
	requestLimit  = 8 << 20
	responseLimit = 16 << 20
)

// Plan captures a complete native request and a shared one-use flag. Copies of
// a Plan cannot replay it. Native() returns a separate deep copy for inspection.
type Plan struct {
	owner        *Client
	request      proto.Message
	method       string
	write        bool
	used         *atomic.Bool
	followerOnly bool
}

func (p Plan) Native() proto.Message {
	if p.request == nil {
		return nil
	}
	return proto.Clone(p.request)
}
func (p Plan) Writes() bool { return p.write }

func (c *Client) Prepare(request proto.Message) (Plan, error) {
	if !c.Alive() {
		return Plan{}, ErrRetired
	}
	if request == nil || !request.ProtoReflect().IsValid() {
		return Plan{}, ErrInvalid
	}
	// Validate before walking/copying protobuf graphs: nested transactions are
	// unsupported, and an in-process caller can otherwise construct a cycle.
	method, write, err := validateRequest(request)
	if err != nil {
		return Plan{}, err
	}
	if proto.Size(request) > requestLimit {
		return Plan{}, ErrBounds
	}
	r := proto.Clone(request)
	return Plan{owner: c, request: r, method: method, write: write, used: &atomic.Bool{}}, nil
}

func validKey(b []byte) bool { return len(b) > 0 && len(b) <= 4096 }
func validEnd(key, end []byte) bool {
	return len(end) == 0 || len(end) == 1 && end[0] == 0 || len(end) <= 4096 && bytes.Compare(end, key) > 0
}

func validateRequest(r proto.Message) (string, bool, error) {
	if len(r.ProtoReflect().GetUnknown()) > 0 {
		return "", false, ErrInvalid
	}
	switch v := r.(type) {
	case *pb.RangeRequest:
		if !validKey(v.Key) || !validEnd(v.Key, v.RangeEnd) || v.Revision < 0 || v.Limit < 0 || v.Limit > 10000 || len(v.RangeEnd) > 0 && v.Limit == 0 && !v.CountOnly || v.SortOrder < pb.RangeRequest_NONE || v.SortOrder > pb.RangeRequest_DESCEND || v.SortTarget < pb.RangeRequest_KEY || v.SortTarget > pb.RangeRequest_MOD || v.MinModRevision < 0 || v.MaxModRevision < 0 || v.MinCreateRevision < 0 || v.MaxCreateRevision < 0 {
			return "", false, ErrInvalid
		}
		return "/etcdserverpb.KV/Range", false, nil
	case *pb.CompactionRequest:
		if v.Revision <= 0 {
			return "", false, ErrInvalid
		}
		return "/etcdserverpb.KV/Compact", true, nil
	case *pb.PutRequest:
		if !validKey(v.Key) || len(v.Value) > 1<<20 || v.IgnoreValue && len(v.Value) > 0 || v.IgnoreLease && v.Lease != 0 {
			return "", false, ErrInvalid
		}
		return "/etcdserverpb.KV/Put", true, nil
	case *pb.DeleteRangeRequest:
		if !validKey(v.Key) || !validEnd(v.Key, v.RangeEnd) {
			return "", false, ErrInvalid
		}
		return "/etcdserverpb.KV/DeleteRange", true, nil
	case *pb.TxnRequest:
		if len(v.Compare)+len(v.Success)+len(v.Failure) > 128 || len(v.Success)+len(v.Failure) == 0 {
			return "", false, ErrInvalid
		}
		for _, cmp := range v.Compare {
			if !validCompare(cmp) {
				return "", false, ErrInvalid
			}
		}
		write := false
		for _, ops := range [][]*pb.RequestOp{v.Success, v.Failure} {
			for _, op := range ops {
				if op == nil || len(op.ProtoReflect().GetUnknown()) > 0 {
					return "", false, ErrInvalid
				}
				var inner proto.Message
				switch x := op.Request.(type) {
				case *pb.RequestOp_RequestRange:
					if x == nil {
						return "", false, ErrInvalid
					}
					inner = x.RequestRange
				case *pb.RequestOp_RequestPut:
					if x == nil {
						return "", false, ErrInvalid
					}
					inner = x.RequestPut
				case *pb.RequestOp_RequestDeleteRange:
					if x == nil {
						return "", false, ErrInvalid
					}
					inner = x.RequestDeleteRange
				default:
					return "", false, ErrInvalid
				}
				if inner == nil || !inner.ProtoReflect().IsValid() {
					return "", false, ErrInvalid
				}
				_, w, err := validateRequest(inner)
				if err != nil {
					return "", false, err
				}
				write = write || w
			}
		}
		return "/etcdserverpb.KV/Txn", write, nil
	case *pb.LeaseGrantRequest:
		if v.TTL <= 0 || v.ID < 0 {
			return "", false, ErrInvalid
		}
		return "/etcdserverpb.Lease/LeaseGrant", true, nil
	case *pb.LeaseRevokeRequest:
		if v.ID == 0 {
			return "", false, ErrInvalid
		}
		return "/etcdserverpb.Lease/LeaseRevoke", true, nil
	case *pb.LeaseTimeToLiveRequest:
		if v.ID == 0 {
			return "", false, ErrInvalid
		}
		return "/etcdserverpb.Lease/LeaseTimeToLive", false, nil
	case *pb.LeaseLeasesRequest:
		return "/etcdserverpb.Lease/LeaseLeases", false, nil
	case *pb.LeaseKeepAliveRequest:
		if v.ID == 0 {
			return "", false, ErrInvalid
		}
		return "/etcdserverpb.Lease/LeaseKeepAlive", true, nil
	case *pb.StatusRequest:
		return "/etcdserverpb.Maintenance/Status", false, nil
	case *pb.MemberListRequest:
		return "/etcdserverpb.Cluster/MemberList", false, nil
	case *pb.DefragmentRequest:
		return "/etcdserverpb.Maintenance/Defragment", true, nil
	default:
		return validateAuthRequest(r)
	}
}

func validCompare(v *pb.Compare) bool {
	if v == nil || len(v.ProtoReflect().GetUnknown()) > 0 || !validKey(v.Key) || !validEnd(v.Key, v.RangeEnd) || v.Result < pb.Compare_EQUAL || v.Result > pb.Compare_NOT_EQUAL {
		return false
	}
	switch x := v.TargetUnion.(type) {
	case *pb.Compare_Version:
		return x != nil && v.Target == pb.Compare_VERSION && x.Version >= 0
	case *pb.Compare_CreateRevision:
		return x != nil && v.Target == pb.Compare_CREATE && x.CreateRevision >= 0
	case *pb.Compare_ModRevision:
		return x != nil && v.Target == pb.Compare_MOD && x.ModRevision >= 0
	case *pb.Compare_Value:
		return x != nil && v.Target == pb.Compare_VALUE && len(x.Value) <= 1<<20
	case *pb.Compare_Lease:
		return x != nil && v.Target == pb.Compare_LEASE
	}
	return false
}

// PrefixRange and AfterKey preserve arbitrary binary keys, including ff and
// embedded zeroes. Callers choose a fixed revision explicitly for MVCC paging.
func PrefixRange(prefix []byte, limit int64, revision int64) *pb.RangeRequest {
	start := append([]byte(nil), prefix...)
	end := []byte{0}
	if len(start) == 0 {
		start = []byte{0}
	} else {
		for i := len(prefix) - 1; i >= 0; i-- {
			if prefix[i] != 255 {
				end = append([]byte(nil), prefix[:i+1]...)
				end[i]++
				break
			}
		}
	}
	return &pb.RangeRequest{Key: start, RangeEnd: end, Limit: limit, Revision: revision, SortOrder: pb.RangeRequest_ASCEND, SortTarget: pb.RangeRequest_KEY}
}

func AfterKey(key []byte) []byte { out := make([]byte, len(key)+1); copy(out, key); return out }
