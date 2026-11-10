package shell

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/kvworkspace"
	"github.com/nyakang/nyadb/locales"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func TestEtcdNativeShellPublicLeaseManagementOriginalFormsConfirmationAndCompleteArchives(t *testing.T) {
	if os.Getenv("NYADB_ETCD_LEASE_MANAGEMENT_IT") != "1" {
		t.Skip("requires owned public lease management fixture")
	}
	parsed, err := connections.ParseURI(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	s, tt, _, p, _ := openEtcdPublicGrid(t, parsed.Profile, parsed.Password)
	defer s.CloseMySQLSessions()
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("public native request document missing")
	}
	d := s.currentTab().kv
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	s.kvWorkspace.ChooseMethod(d, kvworkspace.LeaseBrowseMethod)
	s.kvWorkspace.Run(d)
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	original := d.Request()
	etcdPublicClick(t, tt, "管理租约")
	if err := tt.ChooseMenuItem(locales.Text("授予租约")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	captureEtcdPublic(t, s, "lease-management-public-grant-form")
	etcdPublicClick(t, tt, "预览授予请求")
	if _, ok := d.Confirmation(); !ok || d.Busy() {
		t.Fatal("public Grant preview dispatched")
	}
	captureEtcdPublic(t, s, "lease-management-public-grant-confirmation")
	var previewFile []byte
	s.SetKVFiles(kvworkspace.Files{SavePath: func(string) (string, error) { return "original-grant.nyakv", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var b bytes.Buffer
		err := write(&b)
		previewFile = bytes.Clone(b.Bytes())
		return err
	}})
	etcdPublicClick(t, tt, "保存租约预览请求")
	etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
	preview, err := kvworkspace.DecodeRequest(previewFile)
	if err != nil || preview.Method != "/etcdserverpb.Lease/LeaseGrant" || d.Request() != original || d.Busy() {
		t.Fatal("public native preview file changed browser/executed request", err)
	}
	etcdPublicClick(t, tt, "我确认执行此原生写入请求")
	etcdPublicClick(t, tt, "确认执行 KV 写入")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	grant, ok := d.LastReceipt()
	if !ok || !grant.Result.Completed || grant.Result.Reply.Lease.ID == 0 || grant.LeaseManagement == nil || grant.LeaseManagement.RequestedID != 0 || d.Request() != original {
		t.Fatal("public generated ID lost/editor changed", d.Error())
	}
	id := grant.Result.Reply.Lease.ID
	client := etcdPublicFixtureClient(t, parsed.Profile, parsed.Password)
	defer func() {
		plan, err := client.Prepare(&pb.LeaseRevokeRequest{ID: id})
		if err == nil {
			client.Execute(t.Context(), plan)
		}
	}()
	captureEtcdPublic(t, s, "lease-management-public-grant-receipt")
	prefix, _ := connections.EtcdPrefixBytes(parsed.Profile.EtcdOptions)
	key := append(bytes.Clone(prefix), 0, 255)
	etcdPublicNative(t, client, &pb.PutRequest{Key: key, Value: []byte{0, 255}, Lease: id})
	etcdPublicClick(t, tt, "读取已授予租约")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	captureEtcdPublic(t, s, "lease-management-public-detail")
	etcdPublicClick(t, tt, "管理租约")
	if err := tt.ChooseMenuItem(locales.Text("预览此租约持续续租")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if d.Busy() {
		t.Fatal("shortcut started renewal without authorization")
	}
	captureEtcdPublic(t, s, "lease-management-public-renewal-confirmation")
	etcdPublicClick(t, tt, "取消")
	s.kvWorkspace.ChooseMethod(d, kvworkspace.LeaseDetailMethod)
	etcdPublicClick(t, tt, "管理租约")
	if err := tt.ChooseMenuItem(locales.Text("预览撤销租约")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	captureEtcdPublic(t, s, "lease-management-public-revoke-form")
	etcdPublicClick(t, tt, "读取撤销关联键")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	if _, ok := d.Confirmation(); !ok {
		t.Fatal("public fresh original Revoke observation missing", d.Error())
	}
	captureEtcdPublic(t, s, "lease-management-public-revoke-confirmation")
	etcdPublicClick(t, tt, "复制完整撤销观察")
	if !bytes.Contains([]byte(tt.Clipboard()), []byte(`"requested_complete_keys": true`)) {
		t.Fatal("public clipboard missing original complete observation")
	}
	etcdPublicClick(t, tt, "我确认执行此原生写入请求")
	etcdPublicClick(t, tt, "确认执行 KV 写入")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	revoke, ok := d.LastReceipt()
	if !ok || !revoke.Result.Completed || revoke.LeaseManagement == nil || revoke.LeaseManagement.Observation == nil {
		t.Fatal("public Revoke receipt incomplete", d.Error())
	}
	native := &pb.LeaseTimeToLiveResponse{}
	if proto.Unmarshal(revoke.LeaseManagement.Observation.Result.Native, native) != nil || native.ID != id || len(native.Keys) != 1 || !bytes.Equal(native.Keys[0], key) {
		t.Fatal("public original native observation changed")
	}
	captureEtcdPublic(t, s, "lease-management-public-revoke-receipt")
	want, err := s.kvWorkspace.OperationRecord(d)
	if err != nil {
		t.Fatal(err)
	}
	var exported []byte
	s.SetKVFiles(kvworkspace.Files{SavePath: func(string) (string, error) { return "original-management.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var b bytes.Buffer
		err := write(&b)
		exported = bytes.Clone(b.Bytes())
		return err
	}})
	etcdPublicClick(t, tt, "导出操作记录")
	etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
	if !bytes.Equal(want, exported) {
		t.Fatal("public export lost original operation/observation")
	}
	captureEtcdPublic(t, s, "lease-management-public-exported")
}
