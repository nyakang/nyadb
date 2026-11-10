package etcd

import (
	"bytes"
	"context"
	"errors"
	"math"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestAlarmStrictOneRequestSelectionAndImmutableUnsignedIdentity(t *testing.T) {
	c := &Client{}
	for _, r := range []*pb.AlarmRequest{{}, {Alarm: pb.AlarmType_NOSPACE}, {Alarm: pb.AlarmType_CORRUPT}, {Action: pb.AlarmRequest_DEACTIVATE, MemberID: math.MaxUint64, Alarm: pb.AlarmType_NOSPACE}} {
		p, err := c.Prepare(r)
		if err != nil || p.Writes() != (r.Action == pb.AlarmRequest_DEACTIVATE) {
			t.Fatal("valid native alarm request rejected", err)
		}
		expected := proto.Clone(r)
		r.MemberID = 1
		snapshot := p.Native().(*pb.AlarmRequest)
		if !proto.Equal(snapshot, expected) {
			t.Fatal("alarm plan aliases caller")
		}
		snapshot.Alarm = pb.AlarmType_NONE
		if !proto.Equal(p.Native(), expected) {
			t.Fatal("inspection aliases plan")
		}
	}
	for _, r := range []*pb.AlarmRequest{{MemberID: 1}, {Alarm: pb.AlarmType(99)}, {Alarm: pb.AlarmType(-1)}, {Action: pb.AlarmRequest_ACTIVATE, MemberID: 1, Alarm: pb.AlarmType_NOSPACE}, {Action: pb.AlarmRequest_DEACTIVATE}, {Action: pb.AlarmRequest_DEACTIVATE, MemberID: 1}, {Action: pb.AlarmRequest_DEACTIVATE, Alarm: pb.AlarmType_NOSPACE}, {Action: pb.AlarmRequest_AlarmAction(99)}} {
		if _, err := c.Prepare(r); !errors.Is(err, ErrInvalid) {
			t.Fatal("unsupported alarm semantics accepted", r, err)
		}
	}
	r := &pb.AlarmRequest{}
	r.ProtoReflect().SetUnknown([]byte{0xa0, 6, 1})
	if _, err := c.Prepare(r); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown request semantics accepted")
	}
	input := contract.KVRequest{Method: "/etcdserverpb.Maintenance/Alarm", JSON: `{"action":"DEACTIVATE","memberID":"18446744073709551615","alarm":"NOSPACE"}`}
	m, canonical, write, err := decodeKVRequest(input)
	if err != nil || !write || m.(*pb.AlarmRequest).MemberID != math.MaxUint64 || !bytes.Contains([]byte(canonical.JSON), []byte(`"18446744073709551615"`)) {
		t.Fatal("exact unsigned target changed", err)
	}
	for _, raw := range []string{`{"memberID":"1"}`, `{"memberID":"-1"}`, `{"action":"ACTIVATE","memberID":"1","alarm":"NOSPACE"}`, `{"action":"DEACTIVATE","memberID":"18446744073709551616","alarm":"NOSPACE"}`, `{"unknown":true}`} {
		input.JSON = raw
		if _, _, _, err := decodeKVRequest(input); err == nil {
			t.Fatal("invalid request JSON accepted", raw)
		}
	}
}

func TestAlarmCompleteReplyOriginalHeaderUnknownEnumAndExtensions(t *testing.T) {
	c := &Client{identity: Identity{Header: Header{ClusterID: 7, MemberID: 9}, Version: "3.7.3"}}
	h := &pb.ResponseHeader{ClusterId: 7, MemberId: 9, Revision: 11, RaftTerm: 2}
	original := &pb.AlarmResponse{Header: h, Alarms: []*pb.AlarmMember{{MemberID: math.MaxUint64, Alarm: pb.AlarmType_NOSPACE}, {MemberID: 1, Alarm: pb.AlarmType(99)}}}
	original.ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
	original.Alarms[0].ProtoReflect().SetUnknown([]byte{0xa0, 6, 8})
	h.ProtoReflect().SetUnknown([]byte{0xa0, 6, 9})
	if err := c.validateAlarmResponse(&pb.AlarmRequest{}, original); err != nil {
		t.Fatal("complete unknown read semantics rejected", err)
	}
	r := projectKVResult(contract.KVRequest{Method: "/etcdserverpb.Maintenance/Alarm", JSON: "{}"}, Response{Attempted: true, Completed: true, native: original, Header: header(h)})
	if !r.NativeCaptured || !r.Completed || r.JSON == "" || r.JSONCaptureError != "" || r.ProjectionError != "" || r.Reply == nil || r.Reply.Kind != "alarm" || len(r.Reply.Alarms) != 2 || r.Reply.Alarms[0].MemberID != math.MaxUint64 || r.Reply.Alarms[1].Type != 99 {
		t.Fatal("complete alarm evidence lost")
	}
	decoded := &pb.AlarmResponse{}
	if proto.Unmarshal(r.Native, decoded) != nil || !proto.Equal(original, decoded) {
		t.Fatal("complete native extensions lost")
	}
	copy := r.Clone()
	copy.Reply.Alarms[0].NativeExtensions[0] = 0
	copy.Reply.Alarms[0].MemberID = 0
	if r.Reply.Alarms[0].MemberID != math.MaxUint64 || r.Reply.Alarms[0].NativeExtensions[0] != 0xa0 {
		t.Fatal("alarm copy aliases evidence")
	}
	for _, mutate := range []func(*pb.AlarmResponse){func(v *pb.AlarmResponse) { v.Header = nil }, func(v *pb.AlarmResponse) { v.Header.ClusterId++ }, func(v *pb.AlarmResponse) { v.Header.MemberId++ }, func(v *pb.AlarmResponse) { v.Header.Revision = 0 }, func(v *pb.AlarmResponse) { v.Header.RaftTerm = 0 }, func(v *pb.AlarmResponse) { v.Alarms[0] = nil }, func(v *pb.AlarmResponse) { v.Alarms[0].MemberID = 0 }, func(v *pb.AlarmResponse) { v.Alarms[0].Alarm = pb.AlarmType_NONE }, func(v *pb.AlarmResponse) { v.Alarms[1] = proto.Clone(v.Alarms[0]).(*pb.AlarmMember) }} {
		v := proto.Clone(original).(*pb.AlarmResponse)
		mutate(v)
		if !errors.Is(c.validateAlarmResponse(&pb.AlarmRequest{}, v), ErrProtocol) {
			t.Fatal("invalid original reply accepted")
		}
	}
	exact := &pb.AlarmRequest{Action: pb.AlarmRequest_DEACTIVATE, MemberID: math.MaxUint64, Alarm: pb.AlarmType_NOSPACE}
	if !errors.Is(c.validateAlarmResponse(exact, original), ErrProtocol) {
		t.Fatal("another alarm included in exact ACK")
	}
	for _, alarms := range [][]*pb.AlarmMember{nil, {original.Alarms[0]}} {
		if err := c.validateAlarmResponse(exact, &pb.AlarmResponse{Header: h, Alarms: alarms}); err != nil {
			t.Fatal("empty or removed native ACK rejected", err)
		}
	}
	if !errors.Is(c.validateAlarmResponse(&pb.AlarmRequest{Alarm: pb.AlarmType_NOSPACE}, original), ErrProtocol) {
		t.Fatal("native type filter mismatch accepted")
	}
}

func TestAlarmProjectionBudgetKeepsAcknowledgedNativeReply(t *testing.T) {
	native := &pb.AlarmResponse{Header: &pb.ResponseHeader{ClusterId: 7, MemberId: 9, Revision: 11, RaftTerm: 2}}
	for i := 0; i < 10001; i++ {
		native.Alarms = append(native.Alarms, &pb.AlarmMember{MemberID: uint64(i + 1), Alarm: pb.AlarmType_NOSPACE})
	}
	r := projectKVResult(contract.KVRequest{}, Response{Attempted: true, Completed: true, native: native})
	if !r.Completed || r.WriteOutcomeUnknown || !r.NativeCaptured || r.ProjectionError == "" || r.Reply != nil || r.JSONCaptureError != "" {
		t.Fatal("projection bound changed native acknowledgement")
	}
	v := &pb.AlarmResponse{}
	if proto.Unmarshal(r.Native, v) != nil || !proto.Equal(v, native) {
		t.Fatal("complete bounded native evidence lost")
	}
}

func TestAlarmDoesNotBorrowLegacyMemberDenialClassificationAndPreCancelDoesNotSend(t *testing.T) {
	c := &Client{identity: Identity{Version: "3.5.35"}}
	if c.definiteMemberRejection(&pb.AlarmRequest{}, status.Error(codes.Unknown, "auth: permission denied")) {
		t.Fatal("unobserved legacy member rule applied to Alarm")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c = &Client{}
	p, err := c.Prepare(&pb.AlarmRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Execute(ctx, p); !errors.Is(err, context.Canceled) || p.used.Load() {
		t.Fatal("pre-cancel consumed or sent alarm")
	}
}
