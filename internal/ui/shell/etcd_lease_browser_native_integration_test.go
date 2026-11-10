package shell

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"os"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/kvworkspace"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func requirePublicLeaseBrowser(t *testing.T) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_LEASE_BROWSER_WORKSPACE_IT") != "1" {
		t.Skip("requires owned public lease browser fixture")
	}
}
func TestEtcdNativeShellPublicLeaseBrowserOriginalListSelectionDetailAndCompleteArchives(t *testing.T) {
	requirePublicLeaseBrowser(t)
	parsed, err := connections.ParseURI(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	prefix, _ := connections.EtcdPrefixBytes(parsed.Profile.EtcdOptions)
	key := append(bytes.Clone(prefix), 0, 255)
	client := etcdPublicFixtureClient(t, parsed.Profile, parsed.Password)
	etcdPublicNative(t, client, &pb.LeaseGrantRequest{ID: 9007199254741077, TTL: 120})
	etcdPublicNative(t, client, &pb.LeaseGrantRequest{ID: 9007199254741078, TTL: 120})
	defer etcdPublicNative(t, client, &pb.LeaseRevokeRequest{ID: 9007199254741077})
	defer etcdPublicNative(t, client, &pb.LeaseRevokeRequest{ID: 9007199254741078})
	etcdPublicNative(t, client, &pb.PutRequest{Key: key, Value: []byte{0, 255}, Lease: 9007199254741077})
	s, tt, _, p, _ := openEtcdPublicGrid(t, parsed.Profile, parsed.Password)
	defer s.CloseMySQLSessions()
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("public KV document missing")
	}
	d := s.currentTab().kv
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	found := false
	for _, method := range s.kvWorkspace.MethodsFor(d) {
		found = found || method.Path == kvworkspace.LeaseBrowseMethod
	}
	if !found {
		t.Fatal("builtin original lease missing browser facet")
	}
	s.kvWorkspace.ChooseMethod(d, kvworkspace.LeaseBrowseMethod)
	d.Editor.Buffer.Set(`{"limit":1,"after":"9007199254741076"}`)
	captureEtcdPublic(t, s, "lease-browser-public-request")
	etcdPublicClick(t, tt, "读取租约观察")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	page, ok := d.LeasePageReceipt()
	if !ok || len(page.Page.Rows) != 1 || page.Page.Rows[0].ID != 9007199254741077 || !page.Page.More {
		t.Fatal("public native ID window", d.Error())
	}
	captureEtcdPublic(t, s, "lease-browser-public-list")
	s.kvWorkspace.SelectLease(d, 9007199254741077)
	tt.Frame()
	etcdPublicClick(t, tt, "读取所选租约详情")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	detail, ok := d.LeaseDetailReceipt()
	if !ok || detail.Detail.Request.ID != 9007199254741077 {
		t.Fatal("public original detail unavailable", d.Error())
	}
	native := &pb.LeaseTimeToLiveResponse{}
	if proto.Unmarshal(detail.Detail.Result.Native, native) != nil || native.ID != 9007199254741077 || len(native.Keys) != 1 || !bytes.Equal(native.Keys[0], key) {
		t.Fatal("public full native detail changed")
	}
	captureEtcdPublic(t, s, "lease-browser-public-detail")
	etcdPublicClick(t, tt, "复制完整租约记录")
	etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
	original := []byte(tt.Clipboard())
	if !bytes.Contains(original, []byte(base64.StdEncoding.EncodeToString(key))) {
		t.Fatal("public complete keys missing from archive")
	}
	var exported []byte
	s.SetKVFiles(kvworkspace.Files{SavePath: func(string) (string, error) { return "original-browser.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var b bytes.Buffer
		err := write(&b)
		exported = bytes.Clone(b.Bytes())
		return err
	}})
	etcdPublicClick(t, tt, "导出租约记录")
	etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
	if !bytes.Equal(original, exported) {
		t.Fatal("public export changed original snapshot")
	}
	captureEtcdPublic(t, s, "lease-browser-public-exported")
	etcdPublicClick(t, tt, "租约列表")
	etcdPublicClick(t, tt, "下一个 ID 窗口")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	next, ok := d.LeasePageReceipt()
	if !ok || next.Page.Request.After == nil || *next.Page.Request.After != 9007199254741077 || len(next.Page.Rows) != 1 || next.Page.Rows[0].ID != 9007199254741078 {
		t.Fatal("public client boundary rounded")
	}
	captureEtcdPublic(t, s, "lease-browser-public-next-window")
	etcdPublicClick(t, tt, "清除浏览回执")
	if _, ok = d.LeasePageReceipt(); ok {
		t.Fatal("public clear retained cache")
	}
	captureEtcdPublic(t, s, "lease-browser-public-cleared")
}
func TestEtcdNativeShellPublicLeaseBrowserParkedPublicationRecoveryRetainsOriginalPage(t *testing.T) {
	requirePublicLeaseBrowser(t)
	parsed, err := connections.ParseURI(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	prefix, _ := connections.EtcdPrefixBytes(parsed.Profile.EtcdOptions)
	client := etcdPublicFixtureClient(t, parsed.Profile, parsed.Password)
	key := append(bytes.Clone(prefix), 1)
	etcdPublicNative(t, client, &pb.PutRequest{Key: key, Value: []byte("parked-browser")})
	s, tt, _, p, grid := openEtcdPublicGrid(t, parsed.Profile, parsed.Password)
	defer s.CloseMySQLSessions()
	etcdPublicNative(t, client, &pb.LeaseGrantRequest{ID: 9007199254741077, TTL: 120})
	defer etcdPublicNative(t, client, &pb.LeaseRevokeRequest{ID: 9007199254741077})
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("request tab missing")
	}
	tab := s.currentTab()
	d := tab.kv
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	s.kvWorkspace.ChooseMethod(d, kvworkspace.LeaseBrowseMethod)
	d.Editor.Buffer.Set(`{"limit":200}`)
	s.kvWorkspace.Run(d)
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	original, ok := d.LeasePageReceipt()
	if !ok {
		t.Fatal("original page missing", d.Error())
	}
	s.kvWorkspace.Run(d)
	for _, candidate := range s.tabs {
		if candidate.data == grid {
			s.selectTab(candidate.id)
		}
	}
	time.Sleep(80 * time.Millisecond)
	tt.Frame()
	if !d.Busy() {
		t.Fatal("parked completion consumed by grid")
	}
	if err = s.runtime.Connect(context.Background(), p, parsed.Password); err != nil {
		t.Fatal(err)
	}
	s.selectTab(tab.id)
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	last, _ := d.LastLeaseBrowserReceipt()
	cached, _ := d.LeasePageReceipt()
	if !d.NeedsRecovery() || last.Scope.Generation != original.Scope.Generation || cached.Scope.Generation != original.Scope.Generation || !bytes.Equal(cached.Page.List.Native, original.Page.List.Native) || last.Error == "" {
		t.Fatal("late page replaced original cache", d.Error())
	}
	captureEtcdPublic(t, s, "lease-browser-public-recovery-required")
	authored := d.Request()
	etcdPublicClick(t, tt, "建立新会话（不执行现有请求）")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	recovered, _ := d.LeasePageReceipt()
	if d.NeedsRecovery() || d.Scope().Generation <= original.Scope.Generation || recovered.Scope.Generation != original.Scope.Generation || d.Request() != authored {
		t.Fatal("recovery reread or changed original page")
	}
	captureEtcdPublic(t, s, "lease-browser-public-recovered-retained")
}
