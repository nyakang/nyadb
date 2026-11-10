package etcd

import (
	"bytes"
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	authpb "go.etcd.io/etcd/api/v3/authpb"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func authNativeRequests() []proto.Message {
	return []proto.Message{
		&pb.AuthStatusRequest{}, &pb.AuthUserListRequest{}, &pb.AuthUserGetRequest{Name: " exact user "},
		&pb.AuthUserAddRequest{Name: " exact user ", Password: " temporary secret "}, &pb.AuthUserDeleteRequest{Name: " exact user "}, &pb.AuthUserChangePasswordRequest{Name: " exact user ", Password: " replacement secret "},
		&pb.AuthUserGrantRoleRequest{User: " exact user ", Role: " exact role "}, &pb.AuthUserRevokeRoleRequest{Name: " exact user ", Role: " exact role "},
		&pb.AuthRoleListRequest{}, &pb.AuthRoleGetRequest{Role: " exact role "}, &pb.AuthRoleAddRequest{Name: " exact role "}, &pb.AuthRoleDeleteRequest{Role: " exact role "},
		&pb.AuthRoleGrantPermissionRequest{Name: " exact role ", Perm: &authpb.Permission{PermType: authpb.READWRITE, Key: []byte{0, 255}, RangeEnd: []byte{0}}}, &pb.AuthRoleRevokePermissionRequest{Role: " exact role ", Key: []byte{0, 255}, RangeEnd: []byte{0}},
	}
}

func TestAuthNativeRequestsStrictValidationAndCompleteImmutableCopies(t *testing.T) {
	c := &Client{}
	for _, request := range authNativeRequests() {
		method, write, err := validateRequest(request)
		if err != nil || !strings.HasPrefix(method, "/etcdserverpb.Auth/") {
			t.Fatal("supported native Auth rejected", err)
		}
		plan, err := c.Prepare(request)
		if err != nil || plan.Writes() != write || !proto.Equal(plan.Native(), request) {
			t.Fatal("Auth native snapshot changed", err)
		}
		proto.Reset(request)
		if proto.Equal(plan.Native(), request) && plan.Writes() {
			t.Fatal("caller reset changed original authorization")
		}
	}
	invalid := []proto.Message{
		&pb.AuthUserGetRequest{}, &pb.AuthUserAddRequest{Name: "user"}, &pb.AuthUserAddRequest{Name: "user", Password: "a", HashedPassword: "b"},
		&pb.AuthUserAddRequest{Name: "user", Password: "a", Options: &authpb.UserAddOptions{NoPassword: true}}, &pb.AuthUserDeleteRequest{}, &pb.AuthUserChangePasswordRequest{Name: "user"},
		&pb.AuthUserGrantRoleRequest{User: "user"}, &pb.AuthUserRevokeRoleRequest{Role: "role"}, &pb.AuthRoleGetRequest{}, &pb.AuthRoleAddRequest{}, &pb.AuthRoleDeleteRequest{},
		&pb.AuthRoleGrantPermissionRequest{Name: "role"}, &pb.AuthRoleGrantPermissionRequest{Name: "role", Perm: &authpb.Permission{Key: []byte("a"), PermType: 99}},
		&pb.AuthRoleRevokePermissionRequest{Role: "role", Key: []byte("a"), RangeEnd: []byte("a")}, &pb.AuthUserAddRequest{Name: string([]byte{255}), Password: "secret"},
		&pb.AuthRoleGrantPermissionRequest{Name: "role", Perm: &authpb.Permission{Key: bytes.Repeat([]byte{255}, 4097)}}, &pb.AuthRoleRevokePermissionRequest{Role: "role", Key: []byte("a"), RangeEnd: []byte("a")},
	}
	p := &authpb.Permission{Key: []byte("a")}
	p.ProtoReflect().SetUnknown([]byte{0xa0, 6, 1})
	invalid = append(invalid, &pb.AuthRoleGrantPermissionRequest{Name: "role", Perm: p})
	options := &authpb.UserAddOptions{NoPassword: true}
	options.ProtoReflect().SetUnknown([]byte{0xa0, 6, 1})
	invalid = append(invalid, &pb.AuthUserAddRequest{Name: "user", Options: options})
	for _, request := range invalid {
		if _, err := c.Prepare(request); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid native Auth accepted: %T", request)
		}
	}
	for _, request := range []proto.Message{&pb.AuthUserAddRequest{Name: "certificate", Options: &authpb.UserAddOptions{NoPassword: true}}, &pb.AuthUserAddRequest{Name: "hash-user", HashedPassword: "original opaque hash"}, &pb.AuthRoleGrantPermissionRequest{Name: "role", Perm: &authpb.Permission{Key: bytes.Repeat([]byte{255}, 4096), RangeEnd: []byte{0}, PermType: authpb.READ}}} {
		if _, err := c.Prepare(request); err != nil {
			t.Fatal("complete native secret/permission semantics rejected", err)
		}
	}
}

func TestAuthJSONRecordsOmitSecretFieldsWithoutChangingNativePreparation(t *testing.T) {
	l := &kvLease{s: &Session{}}
	for _, request := range []proto.Message{&pb.AuthUserAddRequest{Name: " exact user ", Password: " temporary original password "}, &pb.AuthUserChangePasswordRequest{Name: " exact user ", HashedPassword: " original password hash "}} {
		method, _, _ := validateRequest(request)
		raw, _ := protojson.Marshal(request)
		native, snapshot, write, err := decodeKVRequest(contract.KVRequest{Method: method, JSON: string(raw)})
		if err != nil || !write || !proto.Equal(native, request) {
			t.Fatal("native Auth decoding changed secret", err)
		}
		record := recordedAuthRequest(snapshot, native)
		if len(record.OmittedFields) != 1 || strings.Contains(record.Request.JSON, "password") || strings.Contains(record.Request.JSON, "Password") || !strings.Contains(record.Request.JSON, " exact user ") {
			t.Fatal("record contains a secret or changed the original name")
		}
		plan := &kvPreparation{owner: l, request: snapshot, native: native, write: true, record: record}
		if plan.Snapshot() != snapshot || !proto.Equal(plan.native, request) {
			t.Fatal("record redaction changed native authorization")
		}
		cloned := plan.RecordedRequest()
		cloned.OmittedFields[0] = "changed"
		if plan.RecordedRequest().OmittedFields[0] == "changed" {
			t.Fatal("omission metadata aliases preparation")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		result, err := l.Execute(ctx, plan)
		if err == nil || result.Attempted || result.Request != record.Request || len(result.RequestOmittedFields) != 1 {
			t.Fatal("pre-dispatch cancellation leaked/changed recorded input")
		}
	}
	for _, text := range []string{`{"name":"u","password":"a","password":"b"}`, `{"name":"u","password":null}`, `{"name":"u","password":"a","unknown":true}`, `{"name":"u","hashedPassword":"a","hashedPassword":"b"}`} {
		if _, _, _, err := decodeKVRequest(contract.KVRequest{Method: "/etcdserverpb.Auth/UserAdd", JSON: text}); err == nil {
			t.Fatal("ambiguous/unsupported Auth JSON accepted")
		}
	}
}

func TestAuthReceiptsPreserveCompleteBinaryPermissionsExtensionsAndAuthRevision(t *testing.T) {
	h := &pb.ResponseHeader{ClusterId: math.MaxUint64, MemberId: 3, Revision: 9007199254740993, RaftTerm: 1}
	permission := &authpb.Permission{PermType: authpb.READWRITE, Key: bytes.Repeat([]byte{255}, 4096), RangeEnd: []byte{0}}
	permission.ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
	response := &pb.AuthRoleGetResponse{Header: h, Perm: []*authpb.Permission{permission}}
	if err := validateAuthReply(&pb.AuthRoleGetRequest{Role: "role"}, response); err != nil {
		t.Fatal("valid original binary permission/extensions rejected", err)
	}
	result := projectKVResult(contract.KVRequest{}, Response{Attempted: true, Completed: true, native: response, Header: header(h)})
	if result.Reply == nil || result.Reply.Kind != "auth_role_get" || result.ProjectionError != "" || !bytes.Equal(result.Reply.Auth.Permissions[0].NativeExtensions, []byte{0xa0, 6, 7}) {
		t.Fatal("full original Auth permission projection missing")
	}
	native := &pb.AuthRoleGetResponse{}
	if proto.Unmarshal(result.Native, native) != nil || !proto.Equal(native, response) {
		t.Fatal("complete original native Auth reply changed")
	}
	copy := result.Clone()
	copy.Reply.Auth.Permissions[0].Key[0] = 0
	copy.Reply.Auth.Permissions[0].NativeExtensions[0] = 0
	if result.Reply.Auth.Permissions[0].Key[0] != 255 || result.Reply.Auth.Permissions[0].NativeExtensions[0] != 0xa0 {
		t.Fatal("original permission alias")
	}
	status := projectKVResult(contract.KVRequest{}, Response{Attempted: true, Completed: true, native: &pb.AuthStatusResponse{Header: h, AuthRevision: math.MaxUint64, Enabled: true}})
	if status.Reply.Auth.Revision != math.MaxUint64 || !status.Reply.Auth.Enabled || status.Reply.Header.Revision != h.Revision {
		t.Fatal("auth revision was truncated or replaced KV header")
	}
}

func TestAuthMalformedRepliesAndProjectionBoundsRemainSeparate(t *testing.T) {
	for _, response := range []proto.Message{&pb.AuthUserListResponse{Users: []string{"u", "u"}}, &pb.AuthUserListResponse{Users: []string{""}}, &pb.AuthRoleListResponse{Roles: []string{"r", "r"}}} {
		request := proto.Message(&pb.AuthUserListRequest{})
		if _, ok := response.(*pb.AuthRoleListResponse); ok {
			request = &pb.AuthRoleListRequest{}
		}
		if !errors.Is(validateAuthReply(request, response), ErrProtocol) {
			t.Fatal("malformed original Auth list admitted")
		}
	}
	for _, permission := range []*authpb.Permission{nil, {Key: []byte("a"), PermType: 99}, {Key: []byte("a"), RangeEnd: []byte("a")}} {
		if !errors.Is(validateAuthReply(&pb.AuthRoleGetRequest{Role: "r"}, &pb.AuthRoleGetResponse{Perm: []*authpb.Permission{permission}}), ErrProtocol) {
			t.Fatal("malformed native permission admitted")
		}
	}
	r := &pb.AuthUserListResponse{Users: make([]string, 10001)}
	for i := range r.Users {
		r.Users[i] = "name"
	}
	result := projectKVResult(contract.KVRequest{}, Response{Attempted: true, Completed: true, native: r})
	if !result.Completed || result.WriteOutcomeUnknown || result.ProjectionError == "" || len(result.Native) == 0 {
		t.Fatal("projection budget discarded native evidence or changed outcome")
	}
}
