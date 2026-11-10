package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/kvworkspace"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func requireEtcdPublicRenewal(t *testing.T) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_RENEWAL_WORKSPACE_IT") != "1" {
		t.Skip("requires owned public continuous renewal acceptance")
	}
}
func TestEtcdNativeShellPublicRenewalOriginalConfirmationControlsAndCompleteArchives(t *testing.T) {
	requireEtcdPublicRenewal(t)
	parsed, err := connections.ParseURI(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	prefix, _ := connections.EtcdPrefixBytes(parsed.Profile.EtcdOptions)
	key := append(bytes.Clone(prefix), 0, 255)
	client := etcdPublicFixtureClient(t, parsed.Profile, parsed.Password)
	etcdPublicNative(t, client, &pb.PutRequest{Key: key, Value: []byte("original-renewal-key")})
	s, tt, _, p, _ := openEtcdPublicGrid(t, parsed.Profile, parsed.Password)
	defer s.CloseMySQLSessions()
	grant := etcdPublicNative(t, client, &pb.LeaseGrantRequest{TTL: 30, ID: 9007199254740993}).(*pb.LeaseGrantResponse)
	etcdPublicNative(t, client, &pb.PutRequest{Key: key, Value: []byte("original-renewal-key"), Lease: grant.ID})
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("public request document missing")
	}
	d := s.currentTab().kv
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	available := s.kvWorkspace.MethodsFor(d)
	found := false
	for _, method := range available {
		found = found || method.Path == kvworkspace.RenewalMethod
	}
	if !found {
		t.Fatal("builtin runtime did not publish renewal method")
	}
	s.kvWorkspace.ChooseMethod(d, kvworkspace.RenewalMethod)
	d.Editor.Buffer.Set(`{"ID":"9007199254740993"}`)
	authored := d.Request()
	captureEtcdPublic(t, s, "renewal-public-request")
	etcdPublicClick(t, tt, "开始持续续租")
	if d.Busy() {
		t.Fatal("public start bypassed explicit continuous confirmation")
	}
	captureEtcdPublic(t, s, "renewal-public-confirmation")
	etcdPublicClick(t, tt, "确认开始持续续租")
	if d.Busy() {
		t.Fatal("unchecked authorization dispatched")
	}
	etcdPublicClick(t, tt, "我授权持续续租此原租约")
	etcdPublicClick(t, tt, "确认开始持续续租")
	etcdPublicWait(t, tt, func() bool { r, ok := d.LastRenewalReceipt(); return ok && len(r.Batch.Receipts) > 0 })
	captureEtcdPublic(t, s, "renewal-public-active")
	etcdPublicClick(t, tt, "停止持续续租")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	r, ok := d.LastRenewalReceipt()
	if !ok || r.Authored != authored || r.Batch.Request.LeaseID != grant.ID || !r.Batch.Ended || r.Batch.Terminal.WriteOutcomeUnknown || r.Scope.Generation != d.Scope().Generation {
		t.Fatal("public original renewal receipt changed", d.Error())
	}
	for _, receipt := range r.Batch.Receipts {
		native := &pb.LeaseKeepAliveResponse{}
		if proto.Unmarshal(receipt.Native, native) != nil || native.ID != grant.ID || native.TTL != receipt.TTL {
			t.Fatal("public full original receipt changed")
		}
	}
	captureEtcdPublic(t, s, "renewal-public-stopped")
	etcdPublicClick(t, tt, "复制完整续租记录")
	etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
	original := []byte(tt.Clipboard())
	var record map[string]json.RawMessage
	if json.Unmarshal(original, &record) != nil || !bytes.Contains(original, []byte(`"original_lease_id": "9007199254740993"`)) {
		t.Fatal("public copy lost native precision")
	}
	var exported []byte
	s.SetKVFiles(kvworkspace.Files{SavePath: func(string) (string, error) { return "public-renewal.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var b bytes.Buffer
		err := write(&b)
		exported = bytes.Clone(b.Bytes())
		return err
	}})
	etcdPublicClick(t, tt, "导出续租记录")
	etcdPublicWait(t, tt, func() bool { return !d.FileBusy() })
	if !bytes.Equal(exported, original) {
		t.Fatal("public export changed complete receipt")
	}
	captureEtcdPublic(t, s, "renewal-public-exported")
	etcdPublicClick(t, tt, "清除续租回执")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	if _, ok = d.LastRenewalReceipt(); ok {
		t.Fatal("public explicit disposal retained receipt")
	}
	captureEtcdPublic(t, s, "renewal-public-cleared")
	etcdPublicNative(t, client, &pb.LeaseRevokeRequest{ID: grant.ID})
}
func TestEtcdNativeShellPublicRenewalParkedPublicationRecoveryNeverRestarts(t *testing.T) {
	requireEtcdPublicRenewal(t)
	parsed, err := connections.ParseURI(etcdPublicURI(t))
	if err != nil {
		t.Fatal(err)
	}
	prefix, _ := connections.EtcdPrefixBytes(parsed.Profile.EtcdOptions)
	key := append(bytes.Clone(prefix), 1)
	client := etcdPublicFixtureClient(t, parsed.Profile, parsed.Password)
	etcdPublicNative(t, client, &pb.PutRequest{Key: key, Value: []byte("parked-renewal-key")})
	s, tt, _, p, grid := openEtcdPublicGrid(t, parsed.Profile, parsed.Password)
	defer s.CloseMySQLSessions()
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("public original requests missing")
	}
	tab := s.currentTab()
	d := tab.kv
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	grant := etcdPublicNative(t, client, &pb.LeaseGrantRequest{TTL: 3}).(*pb.LeaseGrantResponse)
	etcdPublicNative(t, client, &pb.PutRequest{Key: key, Value: []byte("parked-renewal-key"), Lease: grant.ID})
	s.kvWorkspace.ChooseMethod(d, kvworkspace.RenewalMethod)
	d.Editor.Buffer.Set(`{"ID":"` + strconv.FormatInt(grant.ID, 10) + `"}`)
	s.kvWorkspace.Run(d)
	s.kvWorkspace.ConfirmRenewal(d, true)
	etcdPublicWait(t, tt, func() bool { r, ok := d.LastRenewalReceipt(); return ok && len(r.Batch.Receipts) > 0 })
	original, _ := d.LastRenewalReceipt()
	authored := d.Request()
	for _, candidate := range s.tabs {
		if candidate.data == grid {
			s.selectTab(candidate.id)
		}
	}
	time.Sleep(1400 * time.Millisecond)
	tt.Frame()
	parked, _ := d.LastRenewalReceipt()
	if len(parked.Batch.Receipts) != len(original.Batch.Receipts) || !d.Busy() {
		t.Fatal("parked receipts entered active grid")
	}
	if err = s.runtime.Connect(context.Background(), p, parsed.Password); err != nil {
		t.Fatal(err)
	}
	s.selectTab(tab.id)
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	retained, _ := d.LastRenewalReceipt()
	if len(retained.Batch.Receipts) < 2 || !d.NeedsRecovery() || retained.Scope.Generation != original.Scope.Generation || d.Request() != authored || !retained.Batch.Ended {
		t.Fatal("publication replacement lost original parked receipts", d.Error())
	}
	captureEtcdPublic(t, s, "renewal-public-recovery-required")
	etcdPublicClick(t, tt, "建立新会话（不执行现有请求）")
	etcdPublicWait(t, tt, func() bool { return !d.Busy() })
	recovered, _ := d.LastRenewalReceipt()
	if d.NeedsRecovery() || recovered.Scope.Generation != original.Scope.Generation || d.Scope().Generation <= original.Scope.Generation || len(recovered.Batch.Receipts) != len(retained.Batch.Receipts) || d.Request() != authored {
		t.Fatal("explicit recovery replayed or changed original receipt", d.Error())
	}
	captureEtcdPublic(t, s, "renewal-public-recovered-retained")
	deadline := time.Now().Add(8 * time.Second)
	expired := false
	for time.Now().Before(deadline) {
		rangeResult := etcdPublicNative(t, client, &pb.RangeRequest{Key: key}).(*pb.RangeResponse)
		if len(rangeResult.Kvs) == 0 {
			expired = true
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if !expired {
		t.Fatal("public recovery resumed renewal")
	}
}
