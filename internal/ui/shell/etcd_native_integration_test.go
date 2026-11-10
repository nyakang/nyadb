package shell

import (
	"context"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/etcd"
	"github.com/nyakang/nyadb/locales"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestEtcdNativeShellPrivateKVDocumentsParkDisconnectRecoverAndOriginalClose(t *testing.T) {
	if os.Getenv("NYADB_ETCD_WORKSPACE_IT") != "1" {
		t.Skip("requires owned native etcd workspace fixture")
	}
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	u, err := url.Parse(os.Getenv("NYADB_ETCD_ENDPOINT"))
	if err != nil || u.Hostname() == "" {
		t.Fatal("missing original native endpoint")
	}
	p, _ := connections.Defaults(connections.Etcd)
	p.ID, p.Name, p.Host = 171, "原生 KV workspace", u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	p.Port = uint16(port)
	p.EtcdOptions.ConnectTimeoutSeconds, p.EtcdOptions.RequestTimeoutSeconds = 5, 5
	if u.Scheme == "https" {
		p.TLSMode = "verify-full"
	}
	p.EtcdOptions.CAFile = os.Getenv("NYADB_ETCD_CA")
	p.EtcdOptions.ClientCertFile = os.Getenv("NYADB_ETCD_CERT")
	p.EtcdOptions.ClientKeyFile = os.Getenv("NYADB_ETCD_KEY")
	secret := ""
	if os.Getenv("NYADB_ETCD_MODE") != "anonymous" {
		p.EtcdOptions.Auth, p.Username, secret = "basic", os.Getenv("NYADB_ETCD_USERNAME"), os.Getenv("NYADB_ETCD_SECRET")
	}
	s := newState(false)
	t.Cleanup(s.CloseMySQLSessions)
	s.profileMode = true
	s.navigation.SetProfiles([]connections.Profile{p}, nil, p.ID)
	s.runtime.Register(connections.Etcd, dbruntime.ProviderFunc(etcd.OpenSession))
	if err := s.runtime.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("injected original KV provider did not open")
	}
	first := s.currentTab()
	wait := func(tt *ui.Tester, done func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !done() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(time.Millisecond)
		}
		if !done() {
			t.Fatal("original native workbench did not settle")
		}
		tt.Frame()
	}
	tt := ui.NewTester(s.View, 1280, 800)
	wait(tt, func() bool { return !first.kv.Busy() })
	key := []byte("/shell/" + t.Name())
	raw, _ := protojson.Marshal(&pb.PutRequest{Key: key, Value: []byte{0, 255, 1}})
	s.kvWorkspace.SetMethod(first.kv, "/etcdserverpb.KV/Put")
	first.kv.Editor.Buffer.Set(string(raw))
	tt.Frame()
	for _, label := range []string{"运行 KV 请求", "我确认执行此原生写入请求", "确认执行 KV 写入"} {
		if err := tt.Click(locales.Text(label)); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	wait(tt, func() bool { return !first.kv.Busy() })
	if receipt, ok := first.kv.LastReceipt(); !ok || !receipt.Result.Completed {
		t.Fatal("native workbench lost Put receipt")
	}
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("second original KV document did not open")
	}
	second := s.currentTab()
	wait(tt, func() bool { return !second.kv.Busy() })
	s.selectTab(first.id)
	s.kvWorkspace.Run(first.kv) // Pure write preview; do not execute.
	s.selectTab(second.id)
	if first.identity() == second.identity() || first.profileID() != p.ID || second.profileID() != p.ID || s.inspectorVisible() {
		t.Fatal("private native tabs lost identity or inherited object inspector")
	}
	rangeJSON, _ := protojson.Marshal(&pb.RangeRequest{Key: key})
	s.kvWorkspace.SetMethod(second.kv, "/etcdserverpb.KV/Range")
	second.kv.Editor.Buffer.Set(string(rangeJSON))
	s.kvWorkspace.Run(second.kv)
	s.selectTab(first.id)
	time.Sleep(100 * time.Millisecond)
	tt.Frame()
	if _, ok := second.kv.LastReceipt(); ok || !second.kv.Busy() {
		t.Fatal("parked native receipt consumed by another document")
	}
	s.selectTab(second.id)
	wait(tt, func() bool { return !second.kv.Busy() })
	receipt, ok := second.kv.LastReceipt()
	if !ok || !receipt.Result.Completed || string(receipt.Result.Reply.Entries[0].Value) != string([]byte{0, 255, 1}) {
		t.Fatal("parked original result lost")
	}
	before := second.kv.Request()
	generation := second.kv.Scope().Generation
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if !second.kv.NeedsRecovery() || first.kv == nil || second.kv.Request() != before || !second.kv.Dirty() || s.runtime.IsConnected(p.ID) {
		t.Fatal("disconnect removed native text/document or kept original session")
	}
	if err := s.runtime.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	s.kvWorkspace.Recover(second.kv)
	wait(tt, func() bool { return !second.kv.Busy() })
	if second.kv.Scope().Generation <= generation || second.kv.Request() != before || second.kv.NeedsRecovery() {
		t.Fatal("explicit original-profile recovery changed authored input")
	}
	s.kvWorkspace.Run(second.kv)
	wait(tt, func() bool { return !second.kv.Busy() })
	receipt, _ = second.kv.LastReceipt()
	if receipt.Result.Reply.Entries[0].Version != 1 {
		t.Fatal("recovery replayed original Put")
	}
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			view := ui.NewTester(s.View, size[0], size[1])
			view.Frame()
			if dir := os.Getenv("NYADB_ETCD_WORKSPACE_IMAGES"); dir != "" {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				file, err := os.Create(filepath.Join(dir, language+"-"+strconv.Itoa(size[0])+"x"+strconv.Itoa(size[1])+"-shell-native.png"))
				if err != nil {
					t.Fatal(err)
				}
				err = png.Encode(file, view.Image())
				closeErr := file.Close()
				if err != nil || closeErr != nil {
					t.Fatal("original native workbench image")
				}
			}
		}
	}
	s.closeTab(1) // Original inactive dirty tab owns its leave decision.
	if first.kv.Editor == nil {
		t.Fatal("dirty inactive document closed without guard")
	}
	s.kvWorkspace.DecideLeave(ui.Services{}, true)
	if first.kv.Editor != nil || s.currentTab() != second || second.kv.Editor == nil {
		t.Fatal("inactive close damaged active original document")
	}
	if !s.RequestWorkbenchClose(func() {}) {
		t.Fatal("workbench exit ignored dirty native request")
	}
	s.kvWorkspace.DecideLeave(ui.Services{}, false)
	if second.kv.Editor == nil {
		t.Fatal("canceled exit disposed original document")
	}
	if _, ok := connections.Describe(connections.Etcd); ok {
		t.Fatal("private workbench published product")
	}
}
