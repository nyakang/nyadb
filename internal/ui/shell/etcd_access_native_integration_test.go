package shell

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/csv"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	authpb "go.etcd.io/etcd/api/v3/authpb"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// This channel provisions only the unique owned fixture. Public grid requests
// use the restricted URI, sessionbrowser, Manager and original data document.
func etcdAccessFixtureAuth(t *testing.T) (pb.AuthClient, context.Context) {
	t.Helper()
	u, err := url.Parse(os.Getenv("NYADB_ETCD_ENDPOINT"))
	if err != nil {
		t.Fatal(err)
	}
	var transport credentials.TransportCredentials = insecure.NewCredentials()
	if os.Getenv("NYADB_ETCD_CA") != "" {
		ca, err := os.ReadFile(os.Getenv("NYADB_ETCD_CA"))
		if err != nil {
			t.Fatal(err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(ca) {
			t.Fatal("owned CA")
		}
		tc := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		if os.Getenv("NYADB_ETCD_CERT") != "" {
			cert, err := tls.LoadX509KeyPair(os.Getenv("NYADB_ETCD_CERT"), os.Getenv("NYADB_ETCD_KEY"))
			if err != nil {
				t.Fatal(err)
			}
			tc.Certificates = []tls.Certificate{cert}
		}
		transport = credentials.NewTLS(tc)
	}
	conn, err := grpc.NewClient("passthrough:///"+u.Host, grpc.WithTransportCredentials(transport), grpc.WithDisableRetry(), grpc.WithDisableServiceConfig(), grpc.WithNoProxy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	auth := pb.NewAuthClient(conn)
	secret := os.Getenv("NYADB_ETCD_SECRET")
	if os.Getenv("NYADB_ETCD_MODE") == "anonymous" {
		if _, err := auth.UserAdd(ctx, &pb.AuthUserAddRequest{Name: "root", Password: secret}); err != nil {
			t.Fatal(err)
		}
		if _, err := auth.UserGrantRole(ctx, &pb.AuthUserGrantRoleRequest{User: "root", Role: "root"}); err != nil {
			t.Fatal(err)
		}
		if _, err := auth.AuthEnable(ctx, &pb.AuthEnableRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	token, err := auth.Authenticate(ctx, &pb.AuthenticateRequest{Name: "root", Password: secret})
	if err != nil {
		t.Fatal(err)
	}
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("token", token.Token))
	if os.Getenv("NYADB_ETCD_MODE") == "anonymous" {
		t.Cleanup(func() {
			if _, err := auth.AuthDisable(ctx, &pb.AuthDisableRequest{}); err != nil {
				t.Error("restore owned anonymous fixture", err)
			}
			if _, err := auth.UserDelete(ctx, &pb.AuthUserDeleteRequest{Name: "root"}); err != nil {
				t.Error("remove owned anonymous fixture root", err)
			}
		})
	}
	return auth, ctx
}

func TestEtcdNativeShellPublicAccessReadOnlyScopeCountExportSaveAndExplicitReopen(t *testing.T) {
	if os.Getenv("NYADB_ETCD_ACCESS_IT") != "1" {
		t.Skip("requires owned public access fixture")
	}
	parsed, err := connections.ParseURI(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	auth, ctx := etcdAccessFixtureAuth(t)
	adminProfile := parsed.Profile
	adminProfile.Username, adminProfile.EtcdOptions.Auth = "root", "basic"
	admin := etcdPublicFixtureClient(t, adminProfile, os.Getenv("NYADB_ETCD_SECRET"))
	prefix, _ := connections.EtcdPrefixBytes(parsed.Profile.EtcdOptions)
	key := func(s string) []byte { return append(bytes.Clone(prefix), s...) }
	for _, suffix := range []string{"a/readonly", "b/forbidden", "c/writable", "d/forbidden"} {
		etcdPublicNative(t, admin, &pb.PutRequest{Key: key(suffix), Value: []byte(suffix)})
	}
	user := " public " + strconv.FormatInt(time.Now().UnixNano(), 36) + " 用户 "
	role := "public-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := auth.UserAdd(ctx, &pb.AuthUserAddRequest{Name: user, Password: os.Getenv("NYADB_ETCD_SECRET")}); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.RoleAdd(ctx, &pb.AuthRoleAddRequest{Name: role}); err != nil {
		t.Fatal(err)
	}
	for _, permission := range []*authpb.Permission{{PermType: authpb.READ, Key: key("a/"), RangeEnd: key("b/")}, {PermType: authpb.READWRITE, Key: key("c/"), RangeEnd: key("d/")}} {
		if _, err := auth.RoleGrantPermission(ctx, &pb.AuthRoleGrantPermissionRequest{Name: role, Perm: permission}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := auth.UserGrantRole(ctx, &pb.AuthUserGrantRoleRequest{User: user, Role: role}); err != nil {
		t.Fatal(err)
	}
	restrictedURI, err := url.Parse(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	restrictedURI.User = url.UserPassword(user, os.Getenv("NYADB_ETCD_SECRET"))
	query := restrictedURI.Query()
	query.Set("auth", "basic")
	// Retain the exact original prefix rather than creating another fixture.
	query.Set("prefix", parsed.Profile.EtcdOptions.Prefix)
	query.Set("prefix_encoding", parsed.Profile.EtcdOptions.PrefixEncoding)
	restrictedURI.RawQuery = query.Encode()
	restricted, err := connections.ParseURI(restrictedURI.String())
	if err != nil || restricted.Profile.Username != user || restricted.Password != os.Getenv("NYADB_ETCD_SECRET") {
		t.Fatal("restricted generic URI lost the actual identity", err)
	}
	p := restricted.Profile
	s, tt, services, p, d := openEtcdPublicGrid(t, p, os.Getenv("NYADB_ETCD_SECRET"))
	s.inspectorOn = true
	tt.Frame()
	if len(d.Page().Rows) != 2 || !d.Page().ReadOnlyRows[0] || d.Page().ReadOnlyRows[1] || !bytes.Equal(d.Page().RowRefs[0].Values[0], key("a/readonly")) || !bytes.Equal(d.Page().RowRefs[1].Values[0], key("c/writable")) {
		t.Fatal("public restricted grid lost holes or per-row grants")
	}
	s.dataWorkspace.EditCell(*services, d, 0, "value", contract.Cell{Kind: contract.Binary, Bytes: []byte("denied")})
	if d.HasDraft() {
		t.Fatal("public read-only row admitted a draft")
	}
	etcdPublicClick(t, tt, d.Page().Rows[0][1].Text)
	etcdPublicClick(t, tt, "删除记录")
	if d.HasDraft() || len(d.Page().Rows) != 2 {
		t.Fatal("public read-only selection enabled deletion")
	}
	etcdPublicClick(t, tt, "获取整表行数")
	etcdPublicWait(t, tt, func() bool { return d.Inspector() != nil && !d.Inspector().CountLoading() })
	if d.Inspector().Count() == nil || *d.Inspector().Count() != 2 {
		t.Fatal("public count included unreadable keys", d.Inspector().CountError())
	}
	if !tt.HasText("仅显示当前前缀内可读取的键；行数统计和导出使用相同授权范围。") {
		t.Fatal("authorized subset is not explained in the inspector")
	}
	capture := func(phase string) {
		t.Helper()
		original := os.Getenv("NYADB_ETCD_PUBLIC_IMAGES")
		t.Setenv("NYADB_ETCD_PUBLIC_IMAGES", os.Getenv("NYADB_ETCD_ACCESS_IMAGES"))
		captureEtcdPublic(t, s, "access-"+phase)
		t.Setenv("NYADB_ETCD_PUBLIC_IMAGES", original)
	}
	capture("mixed")
	// Use the mature export wizard and the same owning fixed-revision lease.
	etcdPublicClick(t, tt, "导出")
	etcdPublicClick(t, tt, "下一步")
	etcdPublicClick(t, tt, "当前页")
	etcdPublicClick(t, tt, "完整筛选结果")
	etcdPublicClick(t, tt, "下一步")
	etcdPublicClick(t, tt, "下一步")
	output := filepath.Join(t.TempDir(), "authorized.csv")
	etcdPublicClick(t, tt, "目标文件路径")
	tt.Type(output)
	etcdPublicClick(t, tt, "导出")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	f, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(f).ReadAll()
	_ = f.Close()
	if err != nil || len(rows) != 3 || rows[1][0] != d.Page().Rows[0][0].Text || rows[2][0] != d.Page().Rows[1][0].Text {
		t.Fatal("public complete export changed authorized scope", err)
	}
	etcdPublicClick(t, tt, "关闭")
	s.dataWorkspace.EditCell(*services, d, 1, "value", contract.Cell{Kind: contract.Binary, Bytes: []byte("saved-once")})
	etcdPublicPreview(t, tt)
	etcdPublicClick(t, tt, "确认并提交更改")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	if d.HasDraft() || !s.runtime.IsConnected(p.ID) {
		t.Fatal("public writable subset did not settle")
	}
	receipt := etcdPublicReceipt(t, tt)
	if !receipt.Completed || receipt.Reply == nil || !receipt.Reply.Succeeded || receipt.WriteOutcomeUnknown {
		t.Fatal("public authorized write lost its complete receipt")
	}
	actual := etcdPublicNative(t, admin, &pb.RangeRequest{Key: key("c/writable")}).(*pb.RangeResponse)
	if len(actual.Kvs) != 1 || actual.Kvs[0].Version != 2 || string(actual.Kvs[0].Value) != "saved-once" {
		t.Fatal("public authorized write did not execute exactly once")
	}
	capture("saved")
	if _, err := auth.RoleRevokePermission(ctx, &pb.AuthRoleRevokePermissionRequest{Role: role, Key: key("c/"), RangeEnd: key("d/")}); err != nil {
		t.Fatal(err)
	}
	s.dataWorkspace.Refresh(*services, d)
	etcdPublicWait(t, tt, func() bool { return !d.Loading() && !d.Busy() })
	if d.Page() == nil || len(d.Page().Rows) != 2 || !s.dataWorkspace.CanReopen(d) {
		t.Fatal("revoked original range replaced cache or retried implicitly", d.Error())
	}
	capture("revoked")
	etcdPublicClick(t, tt, "重新打开数据")
	etcdPublicWait(t, tt, func() bool { return s.currentTab().data != d && !s.currentTab().data.Loading() })
	d = s.currentTab().data
	if d.Error() != "" || len(d.Page().Rows) != 1 || d.Page().Editable == nil || *d.Page().Editable || !d.Page().ReadOnlyRows[0] {
		t.Fatal("explicit public reopen lost remaining read-only grant", d.Error())
	}
	capture("reopened")
	// Narrow readonly and empty prefix profiles still open through the same
	// public navigation and shared grid, without probing a forbidden key.
	for _, suffix := range []string{"a/", "missing/"} {
		narrow := parsed.Profile
		narrow.Username, narrow.EtcdOptions.Auth = user, "basic"
		// Binary profile prefixes are encoded by the connection model.
		narrow.EtcdOptions.PrefixEncoding = "base64"
		narrow.EtcdOptions.Prefix = base64.StdEncoding.EncodeToString(key(suffix))
		s, tt, services, _, d = openEtcdPublicGrid(t, narrow, os.Getenv("NYADB_ETCD_SECRET"))
		s.inspectorOn = true
		tt.Frame()
		want, phase := 1, "readonly"
		if suffix == "missing/" {
			want, phase = 0, "empty"
		}
		if d.Error() != "" || len(d.Page().Rows) != want || d.Page().Editable == nil || *d.Page().Editable {
			t.Fatal("public prefix intersection", d.Error())
		}
		s.dataWorkspace.EditCell(*services, d, 0, "value", contract.Cell{Kind: contract.Binary, Bytes: []byte("denied")})
		etcdPublicClick(t, tt, "添加记录")
		if d.HasDraft() || len(d.Page().Rows) != want {
			t.Fatal("readonly or empty grid admitted insert/edit")
		}
		capture(phase)
	}
}
