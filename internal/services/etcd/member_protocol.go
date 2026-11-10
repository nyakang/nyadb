package etcd

import (
	"context"
	"strings"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Official 3.5 maintenance handlers return these two definite rejections with
// legacy codes. Match only the original observed family, RPC and exact native
// code/message; keep the actual error and code unchanged in the receipt.
func (c *Client) definiteMemberRejection(request proto.Message, err error) bool {
	if _, ok := request.(*pb.DefragmentRequest); !ok || !strings.HasPrefix(c.identity.Version, "3.5.") {
		return false
	}
	s := status.Convert(err)
	return s.Code() == codes.Unknown && s.Message() == "auth: permission denied" || s.Code() == codes.Unavailable && s.Message() == "etcdserver: rpc not supported for learner"
}

// MemberList has a valid zero revision in official etcd 3.5/3.7. Defragment
// normally acknowledges with an empty protobuf and no header. Neither response
// may borrow a header or revision from an earlier Status observation.
func (c *Client) validateMemberResponse(request, reply proto.Message) error {
	h := responseHeader(reply)
	switch request.(type) {
	case *pb.DefragmentRequest:
		if _, ok := reply.(*pb.DefragmentResponse); !ok || h != nil && !c.matches(h) {
			return ErrProtocol
		}
		return nil
	case *pb.MemberListRequest:
		v, ok := reply.(*pb.MemberListResponse)
		if !ok || h == nil || h.ClusterId != c.identity.ClusterID || h.MemberId != c.identity.MemberID || h.Revision < 0 || h.RaftTerm == 0 || len(v.Members) == 0 || len(v.Members) > 4096 {
			return ErrProtocol
		}
		seen := make(map[uint64]bool, len(v.Members))
		for _, member := range v.Members {
			if member == nil || member.ID == 0 || seen[member.ID] {
				return ErrProtocol
			}
			seen[member.ID] = true
		}
		if !seen[c.identity.MemberID] {
			return ErrProtocol
		}
		return nil
	default:
		if !c.matches(h) {
			return ErrProtocol
		}
		return nil
	}
}

func (c *Client) observeDefragmentMember(ctx context.Context) (Response, error) {
	r := Response{Attempted: true}
	v, err := pb.NewMaintenanceClient(c.conn).Status(c.authContext(ctx), &pb.StatusRequest{})
	if err != nil {
		r.Code = status.Code(err)
		return r, err
	}
	if v == nil {
		return r, ErrProtocol
	}
	r.native = proto.Clone(v)
	r.Header = header(v.Header)
	if !c.matches(v.Header) || v.Version != c.identity.Version {
		return r, ErrProtocol
	}
	r.Completed = true
	return r, nil
}
