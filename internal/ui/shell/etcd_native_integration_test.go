package shell

import (
	"context"
	"image/png"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/ui/kvworkspace"
	"github.com/nyakang/nyadb/locales"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestEtcdNativeShellPublicKVDocumentsParkDisconnectRecoverAndOriginalClose(t *testing.T) {
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
	s, _, profiles, _ := profileTestState(t)
	p.ID = 0
	p, err = profiles.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	t.Cleanup(s.CloseMySQLSessions)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	s.sessions.Current().Password = secret
	tt.Frame()
	if err := tt.Click(locales.Text("连接所选数据库")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for s.sessions.StatusFor(p.ID) == "连接中" && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if s.sessions.StatusFor(p.ID) != "已连接" || !s.runtime.IsConnected(p.ID) {
		t.Fatal("public sessionbrowser did not publish matching connected state", s.sessions.Current().Error)
	}
	tt.Frame()
	if err := tt.Click(locales.Text("新建 KV 请求")); err != nil {
		t.Fatal(err)
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
		t.Fatal("native tabs lost identity or inherited object inspector")
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
	// File/Window menu commands retain native request envelopes and never
	// execute a request merely because its file was opened.
	requestPath := filepath.Join(t.TempDir(), "original.nyakv")
	copyPath := filepath.Join(t.TempDir(), "copy.nyakv")
	pickedPath := requestPath
	s.SetKVFiles(kvworkspace.Files{OpenPath: func() (string, error) { return requestPath, nil }, SavePath: func(string) (string, error) { return pickedPath, nil }, OpenReader: func(path string) (io.ReadCloser, error) { return os.Open(path) }, WriteStream: queries.WriteStream})
	menu := func(section, action string) {
		t.Helper()
		if err := tt.Click(locales.Text(section)); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem(locales.Text(action)); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	capturedFile := second.kv.Request()
	menu("文件", "保存请求")
	wait(tt, func() bool { return !second.kv.FileBusy() })
	rawFile, err := os.ReadFile(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := kvworkspace.DecodeRequest(rawFile)
	if err != nil || decoded != capturedFile {
		t.Fatal("native menu saved a SQL file or changed original request", err)
	}
	second.kv.Editor.Buffer.Set(second.kv.Editor.Buffer.String() + "\n")
	menu("窗口", "新建 KV 请求")
	third := s.currentTab()
	if third.kv == nil || third.query != nil || third == second {
		t.Fatal("native window menu opened SQL")
	}
	wait(tt, func() bool { return !third.kv.Busy() })
	menu("文件", "打开请求文本")
	wait(tt, func() bool { return !third.kv.FileBusy() })
	if third.kv.Request() != capturedFile {
		t.Fatal("native file menu changed envelope")
	}
	if _, ok := third.kv.LastReceipt(); ok {
		t.Fatal("opening native request file executed it")
	}
	pickedPath = copyPath
	menu("文件", "另存为")
	wait(tt, func() bool { return !third.kv.FileBusy() })
	copyRaw, err := os.ReadFile(copyPath)
	if err != nil || string(copyRaw) != string(rawFile) {
		t.Fatal("native menu save-as changed captured file", err)
	}
	s.selectTab(second.id)
	tt.Frame()
	before := second.kv.Request()
	generation := second.kv.Scope().Generation
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if !second.kv.NeedsRecovery() || first.kv == nil || second.kv.Request() != before || !second.kv.Dirty() || s.runtime.IsConnected(p.ID) || s.sessions.StatusFor(p.ID) == "已连接" {
		t.Fatal("disconnect removed native text/document or kept original session")
	}
	s.sessions.Current().Password = secret
	s.startMySQLSession(services)
	wait(tt, func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	if s.sessions.StatusFor(p.ID) != "已连接" || !s.runtime.IsConnected(p.ID) {
		t.Fatal("public recovery handshake did not synchronize connected state")
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
	if descriptor, ok := connections.Describe(connections.Etcd); !ok || !descriptor.Capabilities.KeyValues || descriptor.Capabilities.Query {
		t.Fatal("native KV descriptor capability boundary changed")
	}
}
