package etcd

import (
	"bytes"
	"context"
	"errors"
	"math"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	authpb "go.etcd.io/etcd/api/v3/authpb"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func authNativeClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("NYADB_ETCD_AUTH_IT") != "1" {
		t.Skip("requires owned original Auth fixture")
	}
	return nativeClient(t)
}

func authNativeName(t *testing.T) string {
	return " " + t.Name() + "-" + strconv.FormatInt(time.Now().UnixNano(), 36) + " "
}

func authNativeLease(t *testing.T, admin *Client) (*Session, contract.KVRequestLease) {
	t.Helper()
	p, secret := sessionProfile(t)
	if os.Getenv("NYADB_ETCD_MODE") == "anonymous" && admin.config.Auth == "basic" {
		p.EtcdOptions.Auth, p.Username, secret = "basic", "root", os.Getenv("NYADB_ETCD_SECRET")
	}
	base, err := OpenProfile(context.Background(), p, secret)
	if err != nil {
		t.Fatal("original Auth session handshake", err)
	}
	s := newSession(p, secret, base)
	l := reserveNativeKV(t, s, 1)
	t.Cleanup(func() { _ = s.Close() })
	return s, l
}

func TestEtcdNativeAuthOriginalFourteenRPCsExactNamesPermissionsAndRevision(t *testing.T) {
	c := authNativeClient(t)
	_, l := authNativeLease(t, c)
	before := nativeKVDo(t, l, &pb.AuthStatusRequest{})
	if before.Reply.Auth.Enabled != (os.Getenv("NYADB_ETCD_MODE") != "anonymous") {
		t.Fatal("AuthStatus changed actual enabled state")
	}
	user, role := authNativeName(t), authNativeName(t)+"role"
	key := bytes.Repeat([]byte{255}, 4096)
	permission := &authpb.Permission{PermType: authpb.READWRITE, Key: key, RangeEnd: []byte{0}}
	secret := " temporary original secret "
	added := nativeKVDo(t, l, &pb.AuthUserAddRequest{Name: user, Password: secret})
	if len(added.RequestOmittedFields) != 1 || strings.Contains(added.Request.JSON, secret) || strings.Contains(l.LastResult().Request.JSON, secret) {
		t.Fatal("retained original Auth request contains password")
	}
	nativeKVDo(t, l, &pb.AuthRoleAddRequest{Name: role})
	nativeKVDo(t, l, &pb.AuthRoleGrantPermissionRequest{Name: role, Perm: permission})
	nativeKVDo(t, l, &pb.AuthUserGrantRoleRequest{User: user, Role: role})
	users := nativeKVDo(t, l, &pb.AuthUserListRequest{})
	roles := nativeKVDo(t, l, &pb.AuthRoleListRequest{})
	if !slices.Contains(users.Reply.Auth.Users, user) || !slices.Contains(roles.Reply.Auth.Roles, role) {
		t.Fatal("native list lost exact untrimmed name")
	}
	gotUser := nativeKVDo(t, l, &pb.AuthUserGetRequest{Name: user})
	gotRole := nativeKVDo(t, l, &pb.AuthRoleGetRequest{Role: role})
	if !slices.Equal(gotUser.Reply.Auth.Roles, []string{role}) || len(gotRole.Reply.Auth.Permissions) != 1 || !bytes.Equal(gotRole.Reply.Auth.Permissions[0].Key, key) || !bytes.Equal(gotRole.Reply.Auth.Permissions[0].End, []byte{0}) || gotRole.Reply.Auth.Permissions[0].Access != "READWRITE" {
		t.Fatal("complete maximum binary key/unbounded permission changed")
	}
	nativeKVDo(t, l, &pb.AuthUserChangePasswordRequest{Name: user, Password: " replacement secret "})
	nativeKVDo(t, l, &pb.AuthUserRevokeRoleRequest{Name: user, Role: role})
	nativeKVDo(t, l, &pb.AuthRoleRevokePermissionRequest{Role: role, Key: key, RangeEnd: []byte{0}})
	if len(nativeKVDo(t, l, &pb.AuthUserGetRequest{Name: user}).Reply.Auth.Roles) != 0 || len(nativeKVDo(t, l, &pb.AuthRoleGetRequest{Role: role}).Reply.Auth.Permissions) != 0 {
		t.Fatal("native revocation did not use exact original identity")
	}
	nativeKVDo(t, l, &pb.AuthUserDeleteRequest{Name: user})
	nativeKVDo(t, l, &pb.AuthRoleDeleteRequest{Role: role})
	after := nativeKVDo(t, l, &pb.AuthStatusRequest{})
	if after.Reply.Auth.Revision <= before.Reply.Auth.Revision || after.Header.Revision != before.Header.Revision || after.Reply.Auth.Enabled != before.Reply.Auth.Enabled {
		t.Fatal("Auth revision was confused with KV revision or auth toggled")
	}
}

func TestEtcdNativeAuthActualPasswordChangeAndNoPasswordCertificateIdentity(t *testing.T) {
	_ = authNativeClient(t)
	admin := accessNativeAdmin(t)
	_, l := authNativeLease(t, admin)
	user, role, key := authNativeName(t), authNativeName(t)+"role", nativeKey(t)
	initial, replacement := " original temporary password ", " replacement temporary password "
	nativeKVDo(t, l, &pb.AuthUserAddRequest{Name: user, Password: initial})
	nativeKVDo(t, l, &pb.AuthRoleAddRequest{Name: role})
	nativeKVDo(t, l, &pb.AuthRoleGrantPermissionRequest{Name: role, Perm: &authpb.Permission{Key: key, PermType: authpb.READ}})
	nativeKVDo(t, l, &pb.AuthUserGrantRoleRequest{User: user, Role: role})
	cfg, _ := nativeConfig(t)
	cfg.Auth, cfg.Username, cfg.ProbeKey = "basic", user, key
	old, err := Open(context.Background(), cfg, initial)
	if err != nil {
		t.Fatal("original password rejected", err)
	}
	defer old.Close()
	nativeDo(t, old, &pb.RangeRequest{Key: key})
	changed := nativeKVDo(t, l, &pb.AuthUserChangePasswordRequest{Name: user, Password: replacement})
	if !slices.Equal(changed.RequestOmittedFields, []string{"password"}) || strings.Contains(changed.Request.JSON, replacement) {
		t.Fatal("password change record was not explicitly redacted")
	}
	if bad, err := Open(context.Background(), cfg, initial); err == nil {
		bad.Close()
		t.Fatal("old password still creates a new token")
	}
	fresh, err := Open(context.Background(), cfg, replacement)
	if err != nil {
		t.Fatal("actual replacement password rejected", err)
	}
	defer fresh.Close()
	nativeDo(t, fresh, &pb.RangeRequest{Key: key})
	nativeKVDo(t, l, &pb.AuthUserDeleteRequest{Name: user})
	nativeKVDo(t, l, &pb.AuthRoleDeleteRequest{Role: role})
	// Passwordless users are a distinct native option; creation is covered in all
	// modes and actual CN authentication is covered by the mTLS fixture.
	certUser := "acl-cert"
	nativeKVDo(t, l, &pb.AuthUserAddRequest{Name: certUser, Options: &authpb.UserAddOptions{NoPassword: true}})
	nativeKVDo(t, l, &pb.AuthUserGrantRoleRequest{User: certUser, Role: "root"})
	if os.Getenv("NYADB_ETCD_MODE") == "mtls" {
		cfg.Auth, cfg.Username = "none", ""
		cfg.ClientCertFile, cfg.ClientKeyFile = os.Getenv("NYADB_ETCD_ACCESS_CERT"), os.Getenv("NYADB_ETCD_ACCESS_KEY")
		certificate, err := Open(context.Background(), cfg, "")
		if err != nil {
			t.Fatal("passwordless certificate identity rejected", err)
		}
		defer certificate.Close()
		if certificate.principal != certUser || !nativeDo(t, certificate, &pb.AuthStatusRequest{}).Native().(*pb.AuthStatusResponse).Enabled {
			t.Fatal("passwordless certificate followed another identity")
		}
	}
	nativeKVDo(t, l, &pb.AuthUserDeleteRequest{Name: certUser})
}

func TestEtcdNativeAuthActualRestrictedIdentityAndDefiniteDenials(t *testing.T) {
	_ = authNativeClient(t)
	admin := accessNativeAdmin(t)
	user, role, key := authNativeName(t), authNativeName(t)+"role", nativeKey(t)
	accessNativeUser(t, admin, user, role, []*authpb.Permission{{PermType: authpb.READ, Key: key}})
	cfg, _ := nativeConfig(t)
	cfg.Auth, cfg.Username, cfg.ProbeKey = "basic", user, key
	limited, err := Open(context.Background(), cfg, os.Getenv("NYADB_ETCD_SECRET"))
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()
	nativeDo(t, limited, &pb.AuthUserGetRequest{Name: user})
	nativeDo(t, limited, &pb.AuthRoleGetRequest{Role: role})
	for _, request := range []proto.Message{&pb.AuthUserListRequest{}, &pb.AuthRoleListRequest{}, &pb.AuthUserGetRequest{Name: "root"}, &pb.AuthRoleAddRequest{Name: authNativeName(t)}, &pb.AuthUserChangePasswordRequest{Name: "root", Password: "rejected"}, &pb.AuthRoleGrantPermissionRequest{Name: role, Perm: &authpb.Permission{Key: key, PermType: authpb.READWRITE}}} {
		plan, err := limited.Prepare(request)
		if err != nil {
			t.Fatal(err)
		}
		r, err := limited.Execute(context.Background(), plan)
		if status.Code(err) != codes.PermissionDenied || !r.Attempted || r.Completed || r.WriteOutcomeUnknown || !limited.Alive() {
			t.Fatal("actual native admin denial narrowed, retired, or bypassed", err)
		}
	}
	nativeDo(t, limited, &pb.RangeRequest{Key: key})
	_, l := authNativeLease(t, admin)
	for _, request := range []proto.Message{&pb.AuthUserDeleteRequest{Name: authNativeName(t)}, &pb.AuthRoleDeleteRequest{Role: authNativeName(t)}} {
		plan, _ := l.Prepare(nativeKVRequest(t, request))
		r, err := l.Execute(context.Background(), plan)
		if err == nil || r.Completed || r.WriteOutcomeUnknown || l.CheckKVLease() != nil {
			t.Fatal("definite absent identity rejection became unknown")
		}
	}
	nativeKVDo(t, l, &pb.AuthUserDeleteRequest{Name: user})
	nativeKVDo(t, l, &pb.AuthRoleDeleteRequest{Role: role})
}

type authFault struct {
	upstream      *Client
	method, mode  string
	calls         atomic.Int32
	entered, done chan struct{}
	receipt       proto.Message
}

func (f *authFault) serve(_ any, stream grpc.ServerStream) error {
	method, _ := grpc.MethodFromServerStream(stream)
	request := authRequest(method)
	if request == nil {
		return (&nativeFault{upstream: f.upstream}).serve(nil, stream)
	}
	if err := stream.RecvMsg(request); err != nil {
		return err
	}
	response := authResponse(request)
	if err := f.upstream.conn.Invoke(f.upstream.authContext(stream.Context()), method, request, response); err != nil {
		return err
	}
	if method == f.method {
		f.calls.Add(1)
		f.receipt = proto.Clone(response)
		close(f.entered)
		switch f.mode {
		case "lost":
			close(f.done)
			return status.Error(codes.Unavailable, "original processed Auth receipt discarded")
		case "cancel", "close":
			<-stream.Context().Done()
			close(f.done)
			return stream.Context().Err()
		case "foreign":
			responseHeader(response).ClusterId++
		case "duplicate":
			response.(*pb.AuthUserListResponse).Users = []string{"duplicate", "duplicate"}
		case "extensions":
			response.ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
		case "auth-revision":
			response.(*pb.AuthStatusResponse).AuthRevision = math.MaxUint64
		}
		close(f.done)
	}
	return stream.SendMsg(response)
}

func authProxy(t *testing.T, upstream *Client, method, mode string) (*Client, *authFault) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &authFault{upstream: upstream, method: method, mode: mode, entered: make(chan struct{}), done: make(chan struct{})}
	server := grpc.NewServer(grpc.UnknownServiceHandler(f.serve))
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	cfg, secret := nativeConfig(t)
	cfg.Endpoint = "http://" + listener.Addr().String()
	cfg.CAFile, cfg.ClientCertFile, cfg.ClientKeyFile, cfg.ServerName = "", "", "", ""
	c, err := Open(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal("Auth original proxy handshake", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, f
}

func authFaultWrite(t *testing.T, admin *Client, kind string) proto.Message {
	t.Helper()
	user, role, key := authNativeName(t), authNativeName(t)+"role", nativeKey(t)
	if kind == "UserAdd" {
		return &pb.AuthUserAddRequest{Name: user, Password: " original transient secret "}
	}
	if kind == "RoleAdd" {
		return &pb.AuthRoleAddRequest{Name: role}
	}
	nativeDo(t, admin, &pb.AuthUserAddRequest{Name: user, Password: " original transient secret "})
	nativeDo(t, admin, &pb.AuthRoleAddRequest{Name: role})
	switch kind {
	case "UserDelete":
		return &pb.AuthUserDeleteRequest{Name: user}
	case "UserChangePassword":
		return &pb.AuthUserChangePasswordRequest{Name: user, Password: " replacement transient secret "}
	case "UserGrantRole":
		return &pb.AuthUserGrantRoleRequest{User: user, Role: role}
	case "UserRevokeRole":
		nativeDo(t, admin, &pb.AuthUserGrantRoleRequest{User: user, Role: role})
		return &pb.AuthUserRevokeRoleRequest{Name: user, Role: role}
	case "RoleDelete":
		return &pb.AuthRoleDeleteRequest{Role: role}
	case "RoleGrantPermission":
		return &pb.AuthRoleGrantPermissionRequest{Name: role, Perm: &authpb.Permission{Key: key, PermType: authpb.READWRITE}}
	case "RoleRevokePermission":
		nativeDo(t, admin, &pb.AuthRoleGrantPermissionRequest{Name: role, Perm: &authpb.Permission{Key: key, PermType: authpb.READWRITE}})
		return &pb.AuthRoleRevokePermissionRequest{Role: role, Key: key}
	}
	t.Fatal("unknown Auth write")
	return nil
}

func TestEtcdNativeAuthActualProcessedWritesLostCancelCloseAndForeignReceiptNeverReplay(t *testing.T) {
	admin := authNativeClient(t)
	for _, kind := range []string{"UserAdd", "UserDelete", "UserChangePassword", "UserGrantRole", "UserRevokeRole", "RoleAdd", "RoleDelete", "RoleGrantPermission", "RoleRevokePermission"} {
		for _, mode := range []string{"lost", "cancel", "close", "foreign"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				request := authFaultWrite(t, admin, kind)
				method, _, _ := validateRequest(request)
				c, f := authProxy(t, admin, method, mode)
				p, secret := sessionProfile(t)
				s := newSession(p, secret, nativeClient(t))
				defer s.Close()
				var opens atomic.Int32
				s.opener = func(ctx context.Context, p connections.Profile, secret string) (*Client, error) {
					if opens.Add(1) == 1 {
						return c, nil
					}
					return OpenProfile(ctx, p, secret)
				}
				l := reserveNativeKV(t, s, 1)
				plan, err := l.Prepare(nativeKVRequest(t, request))
				if err != nil || opens.Load() != 0 {
					t.Fatal("Auth preparation performed IO", err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type result struct {
					r   contract.KVResult
					err error
				}
				out := make(chan result, 1)
				go func() { r, err := l.Execute(ctx, plan); out <- result{r, err} }()
				waitSignal(t, f.entered)
				if mode == "cancel" {
					cancel()
				} else if mode == "close" {
					if err := l.Close(); err != nil {
						t.Fatal(err)
					}
				}
				settled := <-out
				waitSignal(t, f.done)
				if settled.err == nil || !settled.r.Attempted || settled.r.Completed || !settled.r.WriteOutcomeUnknown || f.calls.Load() != 1 || f.receipt == nil || !admin.matches(responseHeader(f.receipt)) {
					t.Fatal("processed original Auth write replayed or uncertainty erased", settled.err)
				}
				if kind == "UserAdd" || kind == "UserChangePassword" {
					if len(settled.r.RequestOmittedFields) != 1 || strings.Contains(settled.r.Request.JSON, "transient secret") || strings.Contains(l.LastResult().Request.JSON, "transient secret") {
						t.Fatal("failed original Auth write leaked recorded password")
					}
				}
				if mode == "foreign" && (len(settled.r.Native) == 0 || settled.r.Header.ClusterID == admin.identity.ClusterID) {
					t.Fatal("unexpected complete original server reply was discarded")
				}
				recovered, err := s.RecoverKV(l)
				if err != nil || opens.Load() != 1 {
					t.Fatal("recovery opened/replayed Auth", err)
				}
				defer recovered.Lease.Close()
				if mode != "close" && (!recovered.Last.WriteOutcomeUnknown || strings.Contains(recovered.Last.Request.JSON, "transient secret")) {
					t.Fatal("recovery lost original redacted uncertainty")
				}
				if _, err := recovered.Lease.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
					t.Fatal("recovery admitted original one-use Auth plan")
				}
				before := nativeDo(t, admin, &pb.AuthStatusRequest{}).Native().(*pb.AuthStatusResponse).AuthRevision
				nativeKVDo(t, recovered.Lease, &pb.AuthStatusRequest{})
				after := nativeDo(t, admin, &pb.AuthStatusRequest{}).Native().(*pb.AuthStatusResponse).AuthRevision
				if before != after || f.calls.Load() != 1 || opens.Load() != 2 {
					t.Fatal("explicit recovery read replayed original Auth mutation")
				}
			})
		}
	}
}

func TestEtcdNativeAuthActualReadFaultsAndCompleteNativeExtensions(t *testing.T) {
	admin := authNativeClient(t)
	for _, mode := range []string{"lost", "cancel", "close", "foreign", "duplicate", "extensions", "auth-revision"} {
		t.Run(mode, func(t *testing.T) {
			method := "/etcdserverpb.Auth/UserList"
			if mode == "auth-revision" {
				method = "/etcdserverpb.Auth/AuthStatus"
			}
			c, f := authProxy(t, admin, method, mode)
			plan, _ := c.Prepare(authRequest(method))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				r   Response
				err error
			}
			out := make(chan result, 1)
			go func() { r, err := c.Execute(ctx, plan); out <- result{r, err} }()
			waitSignal(t, f.entered)
			if mode == "cancel" {
				cancel()
			} else if mode == "close" {
				_ = c.Close()
			}
			settled := <-out
			waitSignal(t, f.done)
			if settled.r.WriteOutcomeUnknown || f.calls.Load() != 1 {
				t.Fatal("Auth read became a write or replayed")
			}
			if mode == "extensions" || mode == "auth-revision" {
				if settled.err != nil || !settled.r.Completed {
					t.Fatal("complete valid Auth evidence rejected", settled.err)
				}
				projected := projectKVResult(contract.KVRequest{Method: method}, settled.r)
				if mode == "extensions" && !bytes.Equal(settled.r.Native().ProtoReflect().GetUnknown(), []byte{0xa0, 6, 7}) {
					t.Fatal("native Auth extensions removed")
				}
				if mode == "auth-revision" && projected.Reply.Auth.Revision != math.MaxUint64 {
					t.Fatal("authRevision narrowed")
				}
			} else if settled.err == nil || settled.r.Completed {
				t.Fatal("invalid/lost original Auth read admitted")
			}
		})
	}
}

func TestEtcdNativeAuthOriginalAccessCacheInvalidatedAfterAcknowledgedWrite(t *testing.T) {
	c := authNativeClient(t)
	if _, err := c.permissions(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	cached := c.access != nil
	c.mu.Unlock()
	if !cached {
		t.Fatal("original access cache not populated")
	}
	name := authNativeName(t)
	nativeDo(t, c, &pb.AuthRoleAddRequest{Name: name})
	c.mu.Lock()
	cached = c.access != nil
	c.mu.Unlock()
	if cached {
		t.Fatal("acknowledged Auth write left old permission cache")
	}
	if _, err := c.permissions(context.Background()); err != nil {
		t.Fatal("fresh permission observation failed", err)
	}
	nativeDo(t, c, &pb.AuthRoleDeleteRequest{Role: name})
}
