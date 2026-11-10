package kvworkspace

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func requireAuthWorkspace(t *testing.T) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_PUBLIC_AUTH_IT") != "1" {
		t.Skip("requires owned public Auth acceptance")
	}
}
func authWorkspaceName(t *testing.T) string {
	return " auth " + t.Name() + " " + strconv.FormatInt(time.Now().UnixNano(), 36) + " "
}
func authWorkspacePrepare(t *testing.T, s *State, d *Document, action string, edit func(*authDraft)) {
	t.Helper()
	s.OpenAuthAction(d, action)
	if d.authDraft == nil {
		t.Fatal("Auth form unavailable", d.Error())
	}
	edit(d.authDraft)
	s.PrepareAuthAction(d)
	if d.confirmation == nil {
		t.Fatal("Auth immutable preview missing", d.Error())
	}
}
func authWorkspaceWrite(t *testing.T, s *State, d *Document, action string, edit func(*authDraft)) Receipt {
	t.Helper()
	authWorkspacePrepare(t, s, d, action, edit)
	s.Confirm(d, true)
	consume(t, s, d)
	r, ok := d.LastReceipt()
	if !ok || r.AuthManagement == nil {
		t.Fatal("complete Auth batch receipt missing", d.Error())
	}
	return r
}
func authWorkspaceOK(t *testing.T, r Receipt, steps int) {
	t.Helper()
	if r.Error != "" || !r.Result.Completed || r.Result.WriteOutcomeUnknown || len(r.AuthManagement.Steps) != steps {
		t.Fatal("Auth native batch failed", r.Error)
	}
	for _, step := range r.AuthManagement.Steps {
		if !step.Result.Completed || len(step.Result.Native) == 0 {
			t.Fatal("native step receipt lost")
		}
	}
}

func TestEtcdNativeAuthWorkspaceBilingualOriginalFormsAndCompletePermissions(t *testing.T) {
	requireAuthWorkspace(t)
	p, secret := workspaceProfile(t)
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			s, d, _ := workspaceNativeState(t, p, secret)
			s.ChooseMethod(d, AuthManagementMethod)
			original := d.Request()
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			workspaceCapture(t, tt, language, size, "auth-native-open-pure")
			workspaceClick(t, tt, "管理用户与角色")
			if err := tt.ChooseMenuItem(authActionLabel(authPath("RoleAdd"))); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			name := authWorkspaceName(t)
			workspaceClick(t, tt, "原始名称")
			tt.Type(name)
			workspaceClick(t, tt, "创建后单独授予初始权限")
			d.authDraft.access, d.authDraft.rangeKind, d.authDraft.encoding = "READWRITE", "unbounded", "base64"
			key := bytes.Repeat([]byte{255}, 4096)
			d.authDraft.key = base64.StdEncoding.EncodeToString(key)
			tt.Frame()
			workspaceCapture(t, tt, language, size, "auth-native-role-permission-form")
			workspaceClick(t, tt, "预览 Auth 写入步骤")
			if d.confirmation == nil || len(d.confirmation.auth.plans) != 2 {
				t.Fatal("initial permission is not separate", d.Error())
			}
			workspaceCapture(t, tt, language, size, "auth-native-role-step-confirmation")
			workspaceClick(t, tt, "我确认顺序执行这些原生写入步骤")
			workspaceClick(t, tt, "确认执行 Auth 写入")
			consume(t, s, d)
			authWorkspaceOK(t, *d.last, 2)
			workspaceCapture(t, tt, language, size, "auth-native-role-step-receipts")
			workspaceClick(t, tt, "角色目录")
			workspaceClick(t, tt, "刷新权限目录")
			consume(t, s, d)
			s.SelectAuthName(d, name)
			workspaceClick(t, tt, "读取名称详情")
			consume(t, s, d)
			detail := d.authState().roleDetail
			if detail == nil || len(detail.Result.Reply.Auth.Permissions) != 1 || !bytes.Equal(detail.Result.Reply.Auth.Permissions[0].Key, key) || !bytes.Equal(detail.Result.Reply.Auth.Permissions[0].End, []byte{0}) {
				t.Fatal("native permission range truncated", d.Error())
			}
			d.authState().detailRow = 0
			tt.Frame()
			workspaceCapture(t, tt, language, size, "auth-native-complete-permission-detail")
			workspaceClick(t, tt, "复制完整权限")
			var permission contract.KVAuthPermission
			if json.Unmarshal([]byte(tt.Clipboard()), &permission) != nil || !bytes.Equal(permission.Key, key) {
				t.Fatal("complete native clipboard lost binary range")
			}
			workspaceClick(t, tt, "管理用户与角色")
			if err := tt.ChooseMenuItem(authActionLabel(authPath("UserAdd"))); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			user := name + "user"
			workspaceClick(t, tt, "原始名称")
			tt.Type(user)
			workspaceClick(t, tt, "临时密码")
			tt.Type("original temporary secret")
			d.authDraft.initialRoles = name
			tt.Frame()
			workspaceCapture(t, tt, language, size, "auth-native-user-secret-form")
			workspaceClick(t, tt, "预览 Auth 写入步骤")
			workspaceClick(t, tt, "复制完整 Auth 预览")
			if strings.Contains(tt.Clipboard(), "original temporary secret") || !strings.Contains(tt.Clipboard(), "request_omitted_fields") {
				t.Fatal("native public preview leaked password")
			}
			workspaceCapture(t, tt, language, size, "auth-native-user-step-confirmation")
			workspaceClick(t, tt, "我确认顺序执行这些原生写入步骤")
			workspaceClick(t, tt, "确认执行 Auth 写入")
			consume(t, s, d)
			authWorkspaceOK(t, *d.last, 2)
			workspaceClick(t, tt, "复制完整 JSON")
			if strings.Contains(tt.Clipboard(), "original temporary secret") || !strings.Contains(tt.Clipboard(), "original_step_receipts") {
				t.Fatal("native full batch clipboard changed")
			}
			workspaceCapture(t, tt, language, size, "auth-native-user-step-receipts")
			workspaceClick(t, tt, "用户目录")
			workspaceClick(t, tt, "刷新权限目录")
			consume(t, s, d)
			s.SelectAuthName(d, user)
			workspaceClick(t, tt, "读取名称详情")
			consume(t, s, d)
			if !slices.Contains(d.authState().userDetail.Result.Reply.Auth.Roles, name) {
				t.Fatal("native initial user role missing")
			}
			workspaceClick(t, tt, "读取认证状态")
			consume(t, s, d)
			workspaceCapture(t, tt, language, size, "auth-native-user-detail-auth-status")
			authWorkspaceOK(t, authWorkspaceWrite(t, s, d, "UserDelete", func(f *authDraft) { f.name = user }), 1)
			authWorkspaceOK(t, authWorkspaceWrite(t, s, d, "RoleDelete", func(f *authDraft) { f.name = name }), 1)
			s.SetAuthCategory(d, "users")
			if view, err := authRequestView(d.Request()); err != nil || view != "users" || d.Request().Method != original.Method {
				t.Fatal("forms replaced pure browser editor")
			}
			s.CloseAll()
		}
	}
}

func TestEtcdNativeAuthWorkspaceDefinitePartialStepsAndNativeNameReuse(t *testing.T) {
	requireAuthWorkspace(t)
	p, secret := workspaceProfile(t)
	s, d, _ := workspaceNativeState(t, p, secret)
	s.ChooseMethod(d, AuthManagementMethod)
	user, role := authWorkspaceName(t), authWorkspaceName(t)+"role"
	authWorkspaceOK(t, authWorkspaceWrite(t, s, d, "RoleAdd", func(f *authDraft) { f.name = role }), 1)
	r := authWorkspaceWrite(t, s, d, "UserAdd", func(f *authDraft) {
		f.name = user
		f.password = "partial original secret"
		f.initialRoles = role + "\nmissing-original-role\nroot"
	})
	if r.Error == "" || r.Result.WriteOutcomeUnknown || len(r.AuthManagement.Requests) != 4 || len(r.AuthManagement.Steps) != 3 || !r.AuthManagement.Steps[1].Result.Completed || r.AuthManagement.Steps[2].Result.Completed {
		t.Fatal("definite partial batch replayed/lost steps")
	}
	s.ReadAuth(d, "UserGet", user)
	consume(t, s, d)
	if !slices.Equal(d.last.Result.Reply.Auth.Roles, []string{role}) {
		t.Fatal("later initial role ran after definite failure")
	}
	// Capture a by-name removal, independently replace that same name, then
	// execute the native removal. The UI must not invent incarnation protection.
	authWorkspacePrepare(t, s, d, "UserDelete", func(f *authDraft) { f.name = user })
	admin := s.Open(2, p.ID, DefaultRequest())
	workspaceExecute(t, s, admin, workspaceRequest(t, "Auth/UserDelete", &pb.AuthUserDeleteRequest{Name: user}))
	workspaceExecute(t, s, admin, workspaceRequest(t, "Auth/UserAdd", &pb.AuthUserAddRequest{Name: user, Password: "replacement-secret"}))
	s.Confirm(d, true)
	consume(t, s, d)
	authWorkspaceOK(t, *d.last, 1)
	s.ReadAuth(d, "UserGet", user)
	consume(t, s, d)
	if d.last.Result.Completed || d.last.Result.WriteOutcomeUnknown {
		t.Fatal("by-name removal did not target reused native name")
	}
	authWorkspaceOK(t, authWorkspaceWrite(t, s, d, "RoleDelete", func(f *authDraft) { f.name = role }), 1)
}

// This proxy forwards every original native RPC to the owned official server.
// Faults occur only after the server has actually processed the selected RPC.
type authWorkspaceFault struct {
	base                   *workspaceFault
	method, mode           string
	calls                  atomic.Int32
	armed                  atomic.Bool
	entered, done, release chan struct{}
	receipt                proto.Message
}

func authWorkspaceMessages(method string) (proto.Message, proto.Message) {
	switch method {
	case authPath("AuthStatus"):
		return &pb.AuthStatusRequest{}, &pb.AuthStatusResponse{}
	case "/etcdserverpb.Auth/Authenticate":
		return &pb.AuthenticateRequest{}, &pb.AuthenticateResponse{}
	case authPath("UserList"):
		return &pb.AuthUserListRequest{}, &pb.AuthUserListResponse{}
	case authPath("UserGet"):
		return &pb.AuthUserGetRequest{}, &pb.AuthUserGetResponse{}
	case authPath("UserAdd"):
		return &pb.AuthUserAddRequest{}, &pb.AuthUserAddResponse{}
	case authPath("UserDelete"):
		return &pb.AuthUserDeleteRequest{}, &pb.AuthUserDeleteResponse{}
	case authPath("UserChangePassword"):
		return &pb.AuthUserChangePasswordRequest{}, &pb.AuthUserChangePasswordResponse{}
	case authPath("UserGrantRole"):
		return &pb.AuthUserGrantRoleRequest{}, &pb.AuthUserGrantRoleResponse{}
	case authPath("UserRevokeRole"):
		return &pb.AuthUserRevokeRoleRequest{}, &pb.AuthUserRevokeRoleResponse{}
	case authPath("RoleList"):
		return &pb.AuthRoleListRequest{}, &pb.AuthRoleListResponse{}
	case authPath("RoleGet"):
		return &pb.AuthRoleGetRequest{}, &pb.AuthRoleGetResponse{}
	case authPath("RoleAdd"):
		return &pb.AuthRoleAddRequest{}, &pb.AuthRoleAddResponse{}
	case authPath("RoleDelete"):
		return &pb.AuthRoleDeleteRequest{}, &pb.AuthRoleDeleteResponse{}
	case authPath("RoleGrantPermission"):
		return &pb.AuthRoleGrantPermissionRequest{}, &pb.AuthRoleGrantPermissionResponse{}
	case authPath("RoleRevokePermission"):
		return &pb.AuthRoleRevokePermissionRequest{}, &pb.AuthRoleRevokePermissionResponse{}
	case "/etcdserverpb.Maintenance/Status":
		return &pb.StatusRequest{}, &pb.StatusResponse{}
	case "/etcdserverpb.KV/Range":
		return &pb.RangeRequest{}, &pb.RangeResponse{}
	}
	return nil, nil
}
func (f *authWorkspaceFault) serve(_ any, stream grpc.ServerStream) error {
	method, _ := grpc.MethodFromServerStream(stream)
	request, response := authWorkspaceMessages(method)
	if request == nil {
		return status.Error(codes.Unimplemented, "fixture RPC unsupported")
	}
	if err := stream.RecvMsg(request); err != nil {
		return err
	}
	if err := f.base.upstream.Invoke(f.base.context(stream.Context()), method, request, response); err != nil {
		return err
	}
	if f.armed.Load() && method == f.method && f.calls.Add(1) == 1 {
		f.receipt = proto.Clone(response)
		close(f.entered)
		defer close(f.done)
		switch f.mode {
		case "lost":
			return status.Error(codes.Unavailable, "actual processed Auth step receipt withheld")
		case "cancel", "close", "replace", "disconnect":
			<-stream.Context().Done()
			return stream.Context().Err()
		case "park":
			select {
			case <-f.release:
			case <-stream.Context().Done():
				return stream.Context().Err()
			}
		case "foreign":
			header := response.ProtoReflect().Descriptor().Fields().ByName("header")
			h := response.ProtoReflect().Mutable(header).Message().Interface().(*pb.ResponseHeader)
			h.ClusterId++
		case "extensions":
			response.ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
		case "auth-revision":
			response.(*pb.AuthStatusResponse).AuthRevision = math.MaxUint64
		}
	}
	return stream.SendMsg(response)
}
func authWorkspaceProxy(t *testing.T, p connections.Profile, method, mode string) (connections.Profile, *authWorkspaceFault) {
	t.Helper()
	_, base := workspaceProxy(t, p, nil, "unused")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &authWorkspaceFault{base: base, method: method, mode: mode, entered: make(chan struct{}), done: make(chan struct{}), release: make(chan struct{})}
	server := grpc.NewServer(grpc.UnknownServiceHandler(f.serve))
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	p.Host = "127.0.0.1"
	p.Port = uint16(listener.Addr().(*net.TCPAddr).Port)
	p.TLSMode = "disable"
	p.EtcdOptions.CAFile, p.EtcdOptions.ClientCertFile, p.EtcdOptions.ClientKeyFile = "", "", ""
	return p, f
}

func TestEtcdNativeAuthWorkspaceActualProcessedPartialBatchCancelCloseParkAndRecovery(t *testing.T) {
	requireAuthWorkspace(t)
	for _, mode := range []string{"lost", "cancel", "close", "park", "replace", "disconnect", "foreign"} {
		t.Run(mode, func(t *testing.T) {
			p, secret := workspaceProfile(t)
			admin, ad, _ := workspaceNativeState(t, p, secret)
			user, role := authWorkspaceName(t), authWorkspaceName(t)+"role"
			authWorkspaceOK(t, authWorkspaceWrite(t, admin, ad, "RoleAdd", func(f *authDraft) { f.name = role }), 1)
			proxy, fault := authWorkspaceProxy(t, p, authPath("UserGrantRole"), mode)
			s, d, m := workspaceNativeState(t, proxy, secret)
			// Establish this document's physical client before arming reply faults.
			s.Preconnect(d)
			consume(t, s, d)
			fault.armed.Store(true)
			s.ChooseMethod(d, AuthManagementMethod)
			original := d.Request()
			scope := d.Scope()
			active := true
			s.deps.Active = func(candidate *Document) bool { return active && candidate == d }
			authWorkspacePrepare(t, s, d, "UserAdd", func(f *authDraft) {
				f.name = user
				f.password = "original transient partial secret"
				f.initialRoles = role + "\nroot"
			})
			s.Confirm(d, true)
			finished := d.job.finished
			await(t, fault.entered)
			// The first grant really happened before its reply was withheld.
			admin.ReadAuth(ad, "UserGet", user)
			consume(t, admin, ad)
			if !slices.Equal(ad.last.Result.Reply.Auth.Roles, []string{role}) {
				t.Fatal("fault preceded actual server processing")
			}
			switch mode {
			case "cancel":
				s.RequestCancel(d)
				if !d.Busy() {
					t.Fatal("cancel settled before original executor acknowledgement")
				}
			case "close":
				s.Close(d)
			case "park":
				active = false
				close(fault.release)
				await(t, finished)
				s.Consume()
				if !d.Busy() || d.last != nil {
					t.Fatal("parked batch entered active state")
				}
				active = true
			case "replace":
				if err := m.Connect(context.Background(), proxy, secret); err != nil {
					t.Fatal(err)
				}
			case "disconnect":
				s.DisconnectProfile(p.ID)
			}
			await(t, finished)
			await(t, fault.done)
			if mode == "close" {
				if s.owned(d) {
					t.Fatal("closed original document retained")
				}
			} else {
				consume(t, s, d)
				r, ok := d.LastReceipt()
				want := 2
				if mode == "park" {
					want = 3
				}
				if !ok || len(r.AuthManagement.Steps) != want || r.Scope.Generation != scope.Generation || !r.AuthManagement.Steps[0].Result.Completed || d.Request() != original {
					t.Fatal("original batch evidence lost", d.Error())
				}
				if mode != "park" {
					unknown, ok := d.UnknownReceipt()
					if !ok || len(unknown.AuthManagement.Steps) != 2 {
						t.Fatal("processed partial grant did not create original unknown barrier")
					}
					if mode == "foreign" && (len(unknown.Result.Native) == 0 || unknown.Result.Header.ClusterID == scope.Header.ClusterID) {
						t.Fatal("foreign native receipt discarded")
					}
					s.Recover(d)
					consume(t, s, d)
					if d.NeedsRecovery() || d.Request() != original || d.unknown == nil {
						t.Fatal("pure Auth recovery failed", d.Error())
					}
					s.ReadAuth(d, "UserGet", user)
					consume(t, s, d)
					s.OpenAuthAction(d, "UserDelete")
					if d.authDraft != nil || d.unknown == nil {
						t.Fatal("later read resolved original unknown write")
					}
				}
			}
			expectedCalls := int32(1)
			if mode == "park" {
				expectedCalls = 2
			}
			if fault.calls.Load() != expectedCalls {
				t.Fatal("actual original grant replayed")
			}
			admin.ReadAuth(ad, "UserGet", user)
			consume(t, admin, ad)
			expected := []string{role}
			if mode == "park" {
				expected = append(expected, "root")
				slices.Sort(expected)
			}
			if !slices.Equal(ad.last.Result.Reply.Auth.Roles, expected) {
				t.Fatal("later batch step ran after uncertain grant")
			}
			authWorkspaceOK(t, authWorkspaceWrite(t, admin, ad, "UserDelete", func(f *authDraft) { f.name = user }), 1)
			authWorkspaceOK(t, authWorkspaceWrite(t, admin, ad, "RoleDelete", func(f *authDraft) { f.name = role }), 1)
		})
	}
}

func TestEtcdNativeAuthWorkspaceOriginalReadParkingRevisionAndNativeExtensions(t *testing.T) {
	requireAuthWorkspace(t)
	for _, mode := range []string{"park", "cancel", "replace", "extensions", "auth-revision"} {
		t.Run(mode, func(t *testing.T) {
			p, secret := workspaceProfile(t)
			method := authPath("UserList")
			if mode == "auth-revision" {
				method = authPath("AuthStatus")
			}
			proxy, fault := authWorkspaceProxy(t, p, method, mode)
			s, d, m := workspaceNativeState(t, proxy, secret)
			// Establish this document's physical client before arming reply faults.
			s.Preconnect(d)
			consume(t, s, d)
			fault.armed.Store(true)
			s.ChooseMethod(d, AuthManagementMethod)
			active := true
			s.deps.Active = func(candidate *Document) bool { return active && candidate == d }
			scope := d.Scope()
			name := "UserList"
			if mode == "auth-revision" {
				name = "AuthStatus"
			}
			s.ReadAuth(d, name, "")
			finished := d.job.finished
			await(t, fault.entered)
			switch mode {
			case "park":
				active = false
				close(fault.release)
				await(t, finished)
				s.Consume()
				if !d.Busy() || d.authState().users != nil {
					t.Fatal("parked read consumed")
				}
				active = true
			case "cancel":
				s.RequestCancel(d)
			case "replace":
				if err := m.Connect(context.Background(), proxy, secret); err != nil {
					t.Fatal(err)
				}
			}
			await(t, finished)
			consume(t, s, d)
			await(t, fault.done)
			if d.last == nil || d.last.Scope.Generation != scope.Generation {
				t.Fatal("original read receipt moved generation")
			}
			if mode == "cancel" || mode == "replace" {
				if d.authState().users != nil {
					t.Fatal("retired read replaced live names")
				}
				s.Recover(d)
				consume(t, s, d)
				if fault.calls.Load() != 1 {
					t.Fatal("recovery reread directory")
				}
			} else if mode == "auth-revision" {
				if d.authState().status == nil {
					t.Fatalf("Auth status cache unavailable: error=%q attempted=%t completed=%t code=%s", d.Error(), d.last.Result.Attempted, d.last.Result.Completed, d.last.Result.Code)
				}
				if d.authState().status.Result.Reply.Auth.Revision != math.MaxUint64 || d.Scope().Generation != scope.Generation || d.last.Result.Header.Revision == int64(math.MaxInt64) {
					t.Fatal("Auth revision narrowed or became public/KV revision")
				}
			} else {
				native := &pb.AuthUserListResponse{}
				if proto.Unmarshal(d.last.Result.Native, native) != nil {
					t.Fatal("native directory receipt absent")
				}
				if mode == "extensions" && !bytes.Equal(native.ProtoReflect().GetUnknown(), []byte{0xa0, 6, 7}) {
					t.Fatal("native extension lost")
				}
			}
		})
	}
}
