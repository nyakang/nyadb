package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/kvworkspace"
	"github.com/nyakang/nyadb/locales"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func requirePublicAuth(t *testing.T) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_PUBLIC_AUTH_IT") != "1" {
		t.Skip("requires owned public Auth fixture")
	}
}
func TestEtcdNativeShellPublicAuthOriginalFormsConfirmationAndCompleteArchives(t *testing.T) {
	requirePublicAuth(t)
	parsed, err := connections.ParseURI(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	s, tt, _, p, _ := openEtcdPublicGrid(t, parsed.Profile, parsed.Password)
	defer s.CloseMySQLSessions()
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("public native request tab unavailable")
	}
	d := s.currentTab().kv
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	s.kvWorkspace.ChooseMethod(d, kvworkspace.AuthManagementMethod)
	original := d.Request()
	if !d.AuthManagementAvailable() {
		t.Fatal("original Auth facet not published")
	}
	captureEtcdPublic(t, s, "auth-public-open-pure")
	role := " original role " + strconv.FormatInt(time.Now().UnixNano(), 36) + " "
	user := role + "user"
	etcdPublicClick(t, tt, "管理用户与角色")
	if err = tt.ChooseMenuItem(locales.Text("创建角色")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	etcdPublicClick(t, tt, "原始名称")
	tt.Type(role)
	etcdPublicClick(t, tt, "创建后单独授予初始权限")
	tt.Frame()
	etcdPublicClick(t, tt, "权限键")
	tt.Type("/original-role-key/")
	captureEtcdPublic(t, s, "auth-public-role-permission-form")
	etcdPublicClick(t, tt, "预览 Auth 写入步骤")
	if _, ok := d.Confirmation(); !ok || d.Busy() {
		t.Fatal("public role preview executed", d.Error())
	}
	captureEtcdPublic(t, s, "auth-public-role-step-confirmation")
	etcdPublicClick(t, tt, "我确认顺序执行这些原生写入步骤")
	etcdPublicClick(t, tt, "确认执行 Auth 写入")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	r, ok := d.LastReceipt()
	if !ok || r.Error != "" || len(r.AuthManagement.Steps) != 2 || !r.Result.Completed {
		t.Fatal("public initial permission receipt missing", d.Error())
	}
	captureEtcdPublic(t, s, "auth-public-role-step-receipts")
	etcdPublicClick(t, tt, "管理用户与角色")
	if err = tt.ChooseMenuItem(locales.Text("创建用户")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	etcdPublicClick(t, tt, "原始名称")
	tt.Type(user)
	etcdPublicClick(t, tt, "临时密码")
	tt.Type("public original temporary secret")
	etcdPublicClick(t, tt, "初始角色")
	tt.Type(role)
	captureEtcdPublic(t, s, "auth-public-user-secret-form")
	etcdPublicClick(t, tt, "预览 Auth 写入步骤")
	etcdPublicClick(t, tt, "复制完整 Auth 预览")
	preview := tt.Clipboard()
	if strings.Contains(preview, "public original temporary secret") || !strings.Contains(preview, "request_omitted_fields") || !strings.Contains(preview, role) {
		t.Fatal("public preview leaked password or lost planned grant")
	}
	captureEtcdPublic(t, s, "auth-public-user-step-confirmation")
	var previewFile []byte
	s.SetKVFiles(kvworkspace.Files{SavePath: func(string) (string, error) { return "original-auth.nyakv-auth-preview.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var b bytes.Buffer
		err := write(&b)
		previewFile = bytes.Clone(b.Bytes())
		return err
	}})
	etcdPublicClick(t, tt, "保存 Auth 预览记录")
	etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
	if string(previewFile) != preview || d.Request() != original || d.Busy() {
		t.Fatal("redacted preview export changed/executed original document")
	}
	if _, err := kvworkspace.DecodeRequest(previewFile); err == nil {
		t.Fatal("redacted preview accepted as runnable request")
	}
	etcdPublicClick(t, tt, "我确认顺序执行这些原生写入步骤")
	etcdPublicClick(t, tt, "确认执行 Auth 写入")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	r, ok = d.LastReceipt()
	if !ok || r.AuthManagement == nil || len(r.AuthManagement.Steps) != 2 || !r.Result.Completed || r.Error != "" {
		t.Fatal("public user/grant original receipts missing", d.Error())
	}
	native := &pb.AuthUserAddResponse{}
	if proto.Unmarshal(r.AuthManagement.Steps[0].Result.Native, native) != nil || native.Header == nil {
		t.Fatal("public full original native first step lost")
	}
	captureEtcdPublic(t, s, "auth-public-user-step-receipts")
	etcdPublicClick(t, tt, "刷新权限目录")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	etcdPublicClick(t, tt, "详情名称")
	tt.Command("selectAll")
	tt.Type(user)
	etcdPublicClick(t, tt, "读取名称详情")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	detail, _ := d.LastReceipt()
	if detail.Result.Reply.Auth == nil || len(detail.Result.Reply.Auth.Roles) != 1 || detail.Result.Reply.Auth.Roles[0] != role {
		t.Fatal("public name detail used another user")
	}
	etcdPublicClick(t, tt, "读取认证状态")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	captureEtcdPublic(t, s, "auth-public-user-detail-status")
	// Remove these owned identities through the fixture client before a fresh
	// explicit management write verifies the public operation export.
	client := etcdPublicFixtureClient(t, parsed.Profile, parsed.Password)
	etcdPublicNative(t, client, &pb.AuthUserDeleteRequest{Name: user})
	etcdPublicNative(t, client, &pb.AuthRoleDeleteRequest{Role: role})
	// Re-run a fresh explicit single write so the actual export is a public
	// original management receipt, not a manually substituted cached object.
	etcdPublicClick(t, tt, "管理用户与角色")
	if err = tt.ChooseMenuItem(locales.Text("创建角色")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	etcdPublicClick(t, tt, "原始名称")
	tt.Type(role)
	etcdPublicClick(t, tt, "预览 Auth 写入步骤")
	etcdPublicClick(t, tt, "我确认顺序执行这些原生写入步骤")
	etcdPublicClick(t, tt, "确认执行 Auth 写入")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	want, err := s.kvWorkspace.OperationRecord(d)
	if err != nil {
		t.Fatal(err)
	}
	var exported []byte
	s.SetKVFiles(kvworkspace.Files{SavePath: func(string) (string, error) { return "original-auth-receipt.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var b bytes.Buffer
		err := write(&b)
		exported = bytes.Clone(b.Bytes())
		return err
	}})
	etcdPublicClick(t, tt, "导出操作记录")
	etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
	if !bytes.Equal(want, exported) || d.Request() != original {
		t.Fatal("public operation export changed original complete record")
	}
	etcdPublicClick(t, tt, "复制完整 JSON")
	if tt.Clipboard() != string(want) {
		t.Fatal("public clipboard differs from complete management record")
	}
	captureEtcdPublic(t, s, "auth-public-exported-complete")
	etcdPublicNative(t, client, &pb.AuthRoleDeleteRequest{Role: role})
	// The only runnable file is the pure view, with no temporary secret.
	pure, err := kvworkspace.EncodeRequest(d.Request())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if json.Unmarshal(pure, &fields) != nil || bytes.Contains(pure, []byte("secret")) {
		t.Fatal("pure public view envelope malformed")
	}
}

func TestEtcdNativeShellPublicAuthParkedPublicationRecoveryNeverReplays(t *testing.T) {
	requirePublicAuth(t)
	parsed, err := connections.ParseURI(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	s, tt, _, p, grid := openEtcdPublicGrid(t, parsed.Profile, parsed.Password)
	defer s.CloseMySQLSessions()
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("public native document missing")
	}
	tab := s.currentTab()
	d := tab.kv
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	s.kvWorkspace.ChooseMethod(d, kvworkspace.AuthManagementMethod)
	s.kvWorkspace.Run(d)
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	before, _ := d.LastReceipt()
	original := d.Request()
	s.kvWorkspace.Run(d)
	for _, candidate := range s.tabs {
		if candidate.data == grid {
			s.selectTab(candidate.id)
		}
	}
	time.Sleep(80 * time.Millisecond)
	tt.Frame()
	if !d.Busy() {
		t.Fatal("parked Auth read consumed by grid")
	}
	if err = s.runtime.Connect(context.Background(), p, parsed.Password); err != nil {
		t.Fatal(err)
	}
	s.selectTab(tab.id)
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	late, _ := d.LastReceipt()
	if !d.NeedsRecovery() || late.Scope.Generation != before.Scope.Generation || d.Scope().Generation != before.Scope.Generation {
		t.Fatal("late Auth observation used replacement generation")
	}
	captureEtcdPublic(t, s, "auth-public-recovery-required")
	etcdPublicClick(t, tt, "建立新会话（不执行现有请求）")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	after, _ := d.LastReceipt()
	if d.NeedsRecovery() || d.Scope().Generation <= before.Scope.Generation || d.Request() != original || !bytes.Equal(after.Result.Native, late.Result.Native) || after.Scope.Generation != late.Scope.Generation {
		t.Fatal("recovery replayed or erased original Auth read")
	}
	captureEtcdPublic(t, s, "auth-public-recovered-retained")
}
