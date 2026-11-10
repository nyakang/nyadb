package kvworkspace

import (
	"bytes"
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/etcd"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

func TestEtcdNativeAuthWorkspaceActualPasswordRestrictedAndCertificateIdentities(t *testing.T) {
	requireAuthWorkspace(t)
	p, secret := workspaceProfile(t)
	_, base := workspaceProxy(t, p, nil, "unused")
	auth := pb.NewAuthClient(base.upstream)
	ctx := context.Background()
	if os.Getenv("NYADB_ETCD_MODE") == "anonymous" {
		secret = os.Getenv("NYADB_ETCD_SECRET")
		if _, err := auth.UserAdd(ctx, &pb.AuthUserAddRequest{Name: "root", Password: secret}); err != nil {
			t.Fatal(err)
		}
		if _, err := auth.UserGrantRole(ctx, &pb.AuthUserGrantRoleRequest{User: "root", Role: "root"}); err != nil {
			t.Fatal(err)
		}
		if _, err := auth.AuthEnable(ctx, &pb.AuthEnableRequest{}); err != nil {
			t.Fatal(err)
		}
		p.EtcdOptions.Auth, p.Username = "basic", "root"
	}
	root, err := auth.Authenticate(ctx, &pb.AuthenticateRequest{Name: "root", Password: secret})
	if err != nil {
		t.Fatal(err)
	}
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("token", root.Token))
	if os.Getenv("NYADB_ETCD_MODE") == "anonymous" {
		t.Cleanup(func() {
			if _, err := auth.AuthDisable(ctx, &pb.AuthDisableRequest{}); err != nil {
				t.Error(err)
			}
			if _, err := auth.UserDelete(context.Background(), &pb.AuthUserDeleteRequest{Name: "root"}); err != nil {
				t.Error(err)
			}
		})
	}
	s, d, _ := workspaceNativeState(t, p, secret)
	s.ChooseMethod(d, AuthManagementMethod)
	user, role := authWorkspaceName(t), authWorkspaceName(t)+"role"
	old, newSecret := "original temporary password", "replacement temporary password"
	authWorkspaceOK(t, authWorkspaceWrite(t, s, d, "RoleAdd", func(f *authDraft) {
		f.name = role
		f.initialPermission = true
		f.access = "READWRITE"
		f.rangeKind = "prefix"
		f.key = "/public-auth/"
	}), 2)
	authWorkspaceOK(t, authWorkspaceWrite(t, s, d, "UserAdd", func(f *authDraft) { f.name = user; f.password = old; f.initialRoles = role }), 2)
	limited := p
	limited.Username = user
	limited.EtcdOptions.Auth = "basic"
	limited.EtcdOptions.Prefix = "/public-auth/"
	ls, ld, _ := workspaceNativeState(t, limited, old)
	ls.ChooseMethod(ld, AuthManagementMethod)
	for _, name := range []string{"UserList", "RoleList"} {
		ls.ReadAuth(ld, name, "")
		consume(t, ls, ld)
		if ld.last.Result.Completed || ld.last.Result.WriteOutcomeUnknown || ld.last.Result.Code != codes.PermissionDenied.String() {
			t.Fatal("restricted Auth directory did not use actual token", ld.Error())
		}
	}
	denied := authWorkspaceWrite(t, ls, ld, "UserAdd", func(f *authDraft) { f.name = "denied original user"; f.password = "unexecuted temporary password" })
	if denied.Result.Completed || denied.Result.WriteOutcomeUnknown || denied.Result.Code != codes.PermissionDenied.String() {
		t.Fatal("restricted Auth write not a definite denial")
	}
	changed := authWorkspaceWrite(t, s, d, "UserChangePassword", func(f *authDraft) { f.name = user; f.password = newSecret })
	authWorkspaceOK(t, changed, 1)
	if !slices.Equal(changed.Result.RequestOmittedFields, []string{"password"}) || strings.Contains(changed.AuthManagement.Requests[0].Request.JSON, newSecret) {
		t.Fatal("public password change exposed original secret")
	}
	if bad, err := etcd.OpenProfile(context.Background(), limited, old); err == nil {
		bad.Close()
		t.Fatal("old password created another identity after public change")
	}
	fresh, err := etcd.OpenProfile(context.Background(), limited, newSecret)
	if err != nil {
		t.Fatal("public replacement password rejected", err)
	}
	defer fresh.Close()
	// Acknowledged grant/revoke changes must reach actual original executors.
	authWorkspaceOK(t, authWorkspaceWrite(t, s, d, "UserRevokeRole", func(f *authDraft) { f.name = user; f.role = role }), 1)
	s.ReadAuth(d, "UserGet", user)
	consume(t, s, d)
	if len(d.last.Result.Reply.Auth.Roles) != 0 {
		t.Fatal("native public role revocation missing")
	}
	authWorkspaceOK(t, authWorkspaceWrite(t, s, d, "UserGrantRole", func(f *authDraft) { f.name = user; f.role = role }), 1)
	authWorkspaceOK(t, authWorkspaceWrite(t, s, d, "RoleRevokePermission", func(f *authDraft) { f.name = role; f.rangeKind = "prefix"; f.key = "/public-auth/" }), 1)
	s.ReadAuth(d, "RoleGet", role)
	consume(t, s, d)
	if len(d.last.Result.Reply.Auth.Permissions) != 0 {
		t.Fatal("native permission revocation missing")
	}
	authWorkspaceOK(t, authWorkspaceWrite(t, s, d, "UserDelete", func(f *authDraft) { f.name = user }), 1)
	authWorkspaceOK(t, authWorkspaceWrite(t, s, d, "RoleDelete", func(f *authDraft) { f.name = role }), 1)
	authWorkspaceOK(t, authWorkspaceWrite(t, s, d, "UserAdd", func(f *authDraft) { f.name = "acl-cert"; f.noPassword = true; f.initialRoles = "root" }), 2)
	if os.Getenv("NYADB_ETCD_MODE") == "mtls" {
		cert := p
		cert.EtcdOptions.Auth, cert.Username = "certificate", ""
		cert.EtcdOptions.ClientCertFile, cert.EtcdOptions.ClientKeyFile = os.Getenv("NYADB_ETCD_ACCESS_CERT"), os.Getenv("NYADB_ETCD_ACCESS_KEY")
		cs, cd, _ := workspaceNativeState(t, cert, "")
		cs.ChooseMethod(cd, AuthManagementMethod)
		cs.ReadAuth(cd, "UserGet", "acl-cert")
		consume(t, cs, cd)
		if cd.last.Result.Reply.Auth == nil || !slices.Equal(cd.last.Result.Reply.Auth.Roles, []string{"root"}) {
			t.Fatal("public certificate followed another name")
		}
		cs.ReadAuth(cd, "AuthStatus", "")
		consume(t, cs, cd)
		if !cd.last.Result.Reply.Auth.Enabled || len(cd.last.Result.Native) == 0 || bytes.Equal(cd.Scope().Prefix, []byte("acl-cert")) {
			t.Fatal("public certificate Auth state lost actual identity")
		}
	}
	authWorkspaceOK(t, authWorkspaceWrite(t, s, d, "UserDelete", func(f *authDraft) { f.name = "acl-cert" }), 1)
}
