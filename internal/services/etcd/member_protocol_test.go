package etcd

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type memberProtocolRelay struct {
	upstream          *Client
	mode              string
	ready             atomic.Bool
	calls             atomic.Int32
	ranges            atomic.Int32
	directory         atomic.Pointer[pb.MemberListResponse]
	statusObservation atomic.Pointer[pb.StatusResponse]
	entered, done     chan struct{}
	request           *pb.DefragmentRequest
	receipt           *pb.DefragmentResponse
}

func (f *memberProtocolRelay) serve(_ any, stream grpc.ServerStream) error {
	method, _ := grpc.MethodFromServerStream(stream)
	var req, reply proto.Message
	switch method {
	case "/etcdserverpb.Maintenance/Status":
		req, reply = &pb.StatusRequest{}, &pb.StatusResponse{Header: &pb.ResponseHeader{ClusterId: 7, MemberId: 9, Revision: 11, RaftTerm: 2}, Version: "3.7.3", Leader: 9}
	case "/etcdserverpb.KV/Range":
		f.ranges.Add(1)
		req, reply = &pb.RangeRequest{}, &pb.RangeResponse{Header: &pb.ResponseHeader{ClusterId: 7, MemberId: 9, Revision: 11, RaftTerm: 2}}
	case "/etcdserverpb.Maintenance/Defragment":
		req, reply = &pb.DefragmentRequest{}, &pb.DefragmentResponse{}
	case "/etcdserverpb.Cluster/MemberList":
		req, reply = &pb.MemberListRequest{}, &pb.MemberListResponse{Header: &pb.ResponseHeader{ClusterId: 7, MemberId: 9, RaftTerm: 2}, Members: []*pb.Member{{ID: 9, Name: "source", ClientURLs: []string{"http://declared:2379"}}}}
	default:
		if f.upstream == nil {
			return status.Error(codes.Unimplemented, "not a member fixture route")
		}
		return (&nativeFault{upstream: f.upstream}).serve(nil, stream)
	}
	if err := stream.RecvMsg(req); err != nil {
		return err
	}
	if f.upstream != nil {
		if err := f.upstream.conn.Invoke(f.upstream.authContext(stream.Context()), method, req, reply); err != nil {
			return err
		}
	}
	if method == "/etcdserverpb.Cluster/MemberList" {
		if directory := f.directory.Load(); directory != nil {
			reply = proto.Clone(directory)
		}
	}
	if f.ready.Load() && method == "/etcdserverpb.Maintenance/Status" {
		if observed := f.statusObservation.Load(); observed != nil {
			reply = proto.Clone(observed)
		}
		s := reply.(*pb.StatusResponse)
		switch f.mode {
		case "before-foreign":
			s.Header.ClusterId++
		case "before-member":
			s.Header.MemberId++
		case "before-version":
			s.Version = "3.5.35"
		case "before-denied":
			return status.Error(codes.PermissionDenied, "original status denied")
		case "learner":
			s.IsLearner = true
		}
	}
	if method == "/etcdserverpb.Maintenance/Defragment" {
		if f.calls.Add(1) != 1 {
			return status.Error(codes.Internal, "original member maintenance replayed")
		}
		f.request = proto.Clone(req).(*pb.DefragmentRequest)
		f.receipt = proto.Clone(reply).(*pb.DefragmentResponse)
		close(f.entered)
		defer close(f.done)
		switch f.mode {
		case "lost":
			return status.Error(codes.Unavailable, "actual processed member receipt withheld")
		case "cancel", "close":
			<-stream.Context().Done()
			return stream.Context().Err()
		case "foreign":
			reply.(*pb.DefragmentResponse).Header = &pb.ResponseHeader{ClusterId: 99, MemberId: 9, Revision: 11, RaftTerm: 2}
		case "zero-header":
			reply.(*pb.DefragmentResponse).Header = &pb.ResponseHeader{}
		case "extensions":
			reply.ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
		}
	}
	return stream.SendMsg(reply)
}

func memberRelayClient(t *testing.T, upstream *Client, cfg Config, secret, mode string) (*Client, *memberProtocolRelay) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &memberProtocolRelay{upstream: upstream, mode: mode, entered: make(chan struct{}), done: make(chan struct{})}
	server := grpc.NewServer(grpc.UnknownServiceHandler(f.serve))
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	cfg.Endpoint = "http://" + listener.Addr().String()
	cfg.CAFile, cfg.ClientCertFile, cfg.ClientKeyFile, cfg.ServerName = "", "", "", ""
	c, err := Open(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	f.ready.Store(true)
	return c, f
}

func TestMemberProtocolAcceptsNativeZeroRevisionAndEmptyAckWithoutInventingHeaders(t *testing.T) {
	c := &Client{identity: Identity{Header: Header{ClusterID: 7, MemberID: 9}, Version: "3.7.3"}}
	r := &pb.MemberListResponse{Header: &pb.ResponseHeader{ClusterId: 7, MemberId: 9, RaftTerm: 2}, Members: []*pb.Member{{ID: 9}, {ID: 18446744073709551615, IsLearner: true}}}
	if err := c.validateMemberResponse(&pb.MemberListRequest{}, r); err != nil {
		t.Fatal("native zero-revision directory rejected", err)
	}
	for _, mutate := range []func(*pb.MemberListResponse){
		func(v *pb.MemberListResponse) { v.Header = nil },
		func(v *pb.MemberListResponse) { v.Header.ClusterId++ },
		func(v *pb.MemberListResponse) { v.Header.MemberId++ },
		func(v *pb.MemberListResponse) { v.Header.Revision = -1 },
		func(v *pb.MemberListResponse) { v.Header.RaftTerm = 0 },
		func(v *pb.MemberListResponse) { v.Members[0] = nil },
		func(v *pb.MemberListResponse) { v.Members[0].ID = 0 },
		func(v *pb.MemberListResponse) { v.Members[1].ID = 9 },
		func(v *pb.MemberListResponse) { v.Members = v.Members[1:] },
	} {
		v := proto.Clone(r).(*pb.MemberListResponse)
		mutate(v)
		if !errors.Is(c.validateMemberResponse(&pb.MemberListRequest{}, v), ErrProtocol) {
			t.Fatal("invalid member observation accepted", v)
		}
	}
	if err := c.validateMemberResponse(&pb.DefragmentRequest{}, &pb.DefragmentResponse{}); err != nil {
		t.Fatal("valid empty native acknowledgement rejected", err)
	}
	for _, m := range []proto.Message{&pb.MemberListRequest{}, &pb.DefragmentRequest{}} {
		m.ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
		if _, _, err := validateRequest(m); !errors.Is(err, ErrInvalid) {
			t.Fatal("unsupported maintenance request extensions accepted", err)
		}
	}
}

func TestMemberProjectionBoundsRepeatedStringsWithoutChangingNativeOutcome(t *testing.T) {
	for _, native := range []proto.Message{
		&pb.StatusResponse{Header: &pb.ResponseHeader{ClusterId: 7, MemberId: 9, Revision: 1, RaftTerm: 1}, Version: "3.7.3", Errors: make([]string, 10001)},
		&pb.MemberListResponse{Header: &pb.ResponseHeader{ClusterId: 7, MemberId: 9, RaftTerm: 1}, Members: []*pb.Member{{ID: 9, ClientURLs: make([]string, 10001)}}},
	} {
		r := projectKVResult(contract.KVRequest{}, Response{Attempted: true, Completed: true, native: native})
		if !r.Completed || r.WriteOutcomeUnknown || !r.NativeCaptured || r.ProjectionError == "" || r.Reply != nil || r.JSONCaptureError != "" || len(r.Native) == 0 {
			t.Fatal("projection cache bound changed complete native outcome", r)
		}
		decoded := native.ProtoReflect().New().Interface()
		if err := proto.Unmarshal(r.Native, decoded); err != nil || !proto.Equal(decoded, native) {
			t.Fatal("full bounded native evidence lost", err)
		}
	}
}

func TestMemberOpenRequiresExplicitOriginalIdentityWithoutKVProbeOrEndpointFallback(t *testing.T) {
	original, relay := memberRelayClient(t, nil, Config{}, "", "ok")
	cfg := original.config
	expected := original.Identity()
	before := relay.ranges.Load()
	member, err := OpenMember(context.Background(), cfg, "", expected)
	if err != nil || relay.ranges.Load() != before {
		t.Fatal("member handshake required an unrelated KV read", err)
	}
	defer member.Close()
	wrong := expected
	wrong.MemberID++
	if c, err := OpenMember(context.Background(), cfg, "", wrong); !errors.Is(err, ErrProtocol) {
		if c != nil {
			c.Close()
		}
		t.Fatal("member identity failure selected a different target", err)
	}
	if !original.Alive() || !member.Alive() || relay.ranges.Load() != before || relay.calls.Load() != 0 {
		t.Fatal("rejected member changed an original transport or dispatched work")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c, err := OpenMember(ctx, cfg, "", expected); !errors.Is(err, context.Canceled) || c != nil {
		t.Fatal("canceled member acquisition opened a transport", err)
	}
}

func TestMemberLegacyRejectionKeepsOtherUnknownFailuresAmbiguous(t *testing.T) {
	c := &Client{identity: Identity{Version: "3.5.35"}}
	for _, tc := range []struct {
		request  proto.Message
		code     codes.Code
		message  string
		definite bool
	}{
		{&pb.DefragmentRequest{}, codes.Unknown, "auth: permission denied", true},
		{&pb.DefragmentRequest{}, codes.Unavailable, "etcdserver: rpc not supported for learner", true},
		{&pb.DefragmentRequest{}, codes.Unavailable, "auth: permission denied", false},
		{&pb.DefragmentRequest{}, codes.Unknown, "auth: permission denied; receipt lost", false},
		{&pb.DefragmentRequest{}, codes.Unknown, "arbitrary execution failure", false},
		{&pb.PutRequest{}, codes.Unknown, "auth: permission denied", false},
	} {
		if got := c.definiteMemberRejection(tc.request, status.Error(tc.code, tc.message)); got != tc.definite {
			t.Fatal("unrelated native failure reclassified", tc.code, tc.message)
		}
	}
	c.identity.Version = "3.7.3"
	if c.definiteMemberRejection(&pb.DefragmentRequest{}, status.Error(codes.Unknown, "auth: permission denied")) {
		t.Fatal("legacy exception applied to another observed version")
	}
}

func TestMemberObservedHandshakeKeepsActualRejectedIdentityAndNativeEmptyCapture(t *testing.T) {
	original, f := memberRelayClient(t, nil, Config{}, "", "ok")
	expected := original.Identity()
	expected.MemberID++
	c, receipt, err := OpenMemberObserved(context.Background(), original.config, "", expected)
	if !errors.Is(err, ErrProtocol) || c != nil || !receipt.Attempted || receipt.Completed || receipt.WriteOutcomeUnknown || receipt.Code != codes.OK || receipt.Native() == nil || receipt.Header.MemberID != original.Identity().MemberID {
		t.Fatal("failed candidate observation was replaced by expected identity", err, receipt)
	}
	actual := receipt.Native().(*pb.StatusResponse)
	actual.Header.MemberId = expected.MemberID
	if receipt.Native().(*pb.StatusResponse).Header.MemberId != original.Identity().MemberID || !original.Alive() || f.calls.Load() != 0 {
		t.Fatal("inspection mutated original observation/transport")
	}
	r := projectKVResult(contract.KVRequest{Method: "/etcdserverpb.Maintenance/Defragment", JSON: "{}"}, Response{Attempted: true, Completed: true, native: &pb.DefragmentResponse{}})
	if !r.NativeCaptured || len(r.Native) != 0 || r.JSON == "" || r.Reply == nil || r.Reply.Kind != "defragment" || r.Header != (contract.KVHeader{}) || r.ProjectionError != "" || r.WriteOutcomeUnknown {
		t.Fatal("empty native ACK was mistaken for missing capture", r)
	}
}

func TestDefragmentRequiresFreshOriginalMemberBeforeDispatchAndRetainsIndependentObservation(t *testing.T) {
	for _, mode := range []string{"ok", "extensions", "before-foreign", "before-member", "before-version", "before-denied", "foreign", "zero-header"} {
		t.Run(mode, func(t *testing.T) {
			c, f := memberRelayClient(t, nil, Config{RequestTimeout: time.Second}, "", mode)
			input := &pb.DefragmentRequest{}
			p, err := c.Prepare(input)
			if err != nil || !p.Writes() || f.calls.Load() != 0 {
				t.Fatal("preparation dispatched maintenance", err)
			}
			input.ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
			r, err := c.Execute(context.Background(), p)
			b, exists := r.Before()
			if !exists || !b.Attempted || b.WriteOutcomeUnknown || b.Native() == nil && mode != "before-denied" {
				t.Fatal("independent original Status missing", b, err)
			}
			beforeFailure := mode == "before-foreign" || mode == "before-member" || mode == "before-version" || mode == "before-denied"
			if beforeFailure {
				if err == nil || r.Attempted || r.Completed || r.WriteOutcomeUnknown || f.calls.Load() != 0 || b.Completed {
					t.Fatal("failed identity observation dispatched a write", err, r)
				}
				return
			}
			if f.calls.Load() != 1 || !r.Attempted || !b.Completed || b.Header.ClusterID != c.Identity().ClusterID {
				t.Fatal("original maintenance/Status execution changed", err)
			}
			if mode == "foreign" || mode == "zero-header" {
				if err == nil || !r.WriteOutcomeUnknown || r.Completed || c.Alive() || r.Native() == nil {
					t.Fatal("invalid present header confirmed", err, r)
				}
				return
			}
			if err != nil || !r.Completed || r.WriteOutcomeUnknown || r.Header != (Header{}) || r.Native().(*pb.DefragmentResponse).Header != nil {
				t.Fatal("empty success borrowed a Status header", err, r)
			}
			if mode == "extensions" && !bytes.Equal(r.Native().ProtoReflect().GetUnknown(), []byte{0xa0, 6, 7}) {
				t.Fatal("native response extension discarded")
			}
			b.native.(*pb.StatusResponse).Header.ClusterId++
			again, _ := c.LastResponse().Before()
			if again.Header.ClusterID != 7 || again.Native().(*pb.StatusResponse).Header.ClusterId != 7 {
				t.Fatal("inspection mutated retained observation")
			}
			if _, err := c.Execute(context.Background(), p); !errors.Is(err, ErrUsed) || f.calls.Load() != 1 {
				t.Fatal("native maintenance preparation replayed", err)
			}
		})
	}
}
