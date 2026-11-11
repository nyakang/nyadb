package shell

import (
	"bytes"
	"context"
	"fmt"
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
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/ui/kvworkspace"
	"github.com/nyakang/nyadb/locales"
)

func TestConsulNativePublicShellChooserHTTPDocumentsMenusParkingDisconnectAndClose(t *testing.T) {
	if os.Getenv("NYADB_CONSUL_IT") != "1" {
		t.Skip("owned native Consul fixture required")
	}
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	u, err := url.Parse(os.Getenv("NYADB_CONSUL_ENDPOINT"))
	if err != nil || u.Hostname() == "" {
		t.Fatal("missing owned endpoint")
	}
	p, _ := connections.Defaults(connections.Consul)
	p.Name, p.Host = "public original Consul", u.Hostname()
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	p.Port = uint16(port)
	p.ConsulOptions.Auth, p.ConsulOptions.Datacenter, p.ConsulOptions.Prefix = os.Getenv("NYADB_CONSUL_AUTH"), "dc1", "native/shell/"
	if u.Scheme == "https" {
		p.TLSMode = "verify-full"
	}
	p.ConsulOptions.CAFile = os.Getenv("NYADB_CONSUL_CA")
	p.ConsulOptions.ClientCertFile = os.Getenv("NYADB_CONSUL_CERT")
	p.ConsulOptions.ClientKeyFile = os.Getenv("NYADB_CONSUL_KEY")
	secret := os.Getenv("NYADB_CONSUL_TOKEN")
	s, _, profiles, _ := profileTestState(t)
	p, err = profiles.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	t.Cleanup(s.CloseMySQLSessions)
	// Exercise the chooser through its native host and forward selection to editor.
	s.SetConnectionWindowHost(func() {}, func() {})
	s.openConnectionDialog()
	chooser := ui.NewTester(s.ConnectionWindowView, 900, 700)
	search, ok := chooser.Find(locales.Text("搜索连接类型"))
	if !ok {
		t.Fatal("chooser search missing")
	}
	chooser.ClickAt(search.X+search.W/2, search.Y+search.H/2)
	chooser.Type("Consul")
	chooser.Frame()
	if err = chooser.Click(locales.Text("选择 ") + "Consul"); err != nil {
		t.Fatal(err)
	}
	if err = chooser.Click(locales.Text("下一步")); err != nil {
		t.Fatal(err)
	}
	if s.connectionEditor.Draft().Kind != connections.Consul {
		t.Fatal("public chooser did not select Consul")
	}
	if err = chooser.Click(locales.Text("取消连接向导")); err != nil {
		t.Fatal(err)
	}
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	s.sessions.Current().Password = secret
	tt.Frame()
	if err = tt.Click(locales.Text("连接所选数据库")); err != nil {
		t.Fatal(err)
	}
	wait := func(done func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for !done() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(time.Millisecond)
		}
		if !done() {
			t.Fatal("original public workbench did not settle", s.sessions.Current().Error)
		}
		tt.Frame()
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	if s.sessions.StatusFor(p.ID) != "已连接" || !s.runtime.IsConnected(p.ID) {
		t.Fatal("public Consul session not published", s.sessions.Current().Error)
	}
	for _, action := range []string{"表", "视图", "函数", "查询", "用户", "备份", "新建查询"} {
		if s.toolbarActionAvailable(action) {
			t.Fatal("unsupported relational tool exposed", action)
		}
	}
	if err = tt.Click(locales.Text("新建 KV 请求")); err != nil {
		t.Fatal(err)
	}
	first := s.currentTab()
	if first.kv == nil || first.query != nil || first.data != nil || first.kv.Request().Method != contract.KVConsulHTTPRequestMethod {
		t.Fatal("public entry opened foreign workspace")
	}
	key := p.ConsulOptions.Prefix + t.Name()
	put, _ := consul.KeyPutRequest(key, []byte{0, 255}, nil, nil)
	request, _ := consul.DocumentRequest(put)
	first.kv.Editor.Buffer.Set(request.JSON)
	tt.Frame()
	for _, label := range []string{"运行 KV 请求", "我确认执行此原生写入请求", "确认执行 KV 写入"} {
		if err = tt.Click(locales.Text(label)); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	wait(func() bool { return !first.kv.Busy() })
	write, ok := first.kv.LastReceipt()
	if !ok || write.Result.HTTP == nil || !write.Result.HTTP.Acknowledged || !write.Result.HTTP.Succeeded {
		t.Fatal("public HTTP write receipt missing")
	}
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("second original HTTP document failed")
	}
	second := s.currentTab()
	wait(func() bool { return !second.kv.Busy() })
	read, _ := consul.KeyReadRequest(key)
	request, _ = consul.DocumentRequest(read)
	second.kv.Editor.Buffer.Set(request.JSON)
	s.kvWorkspace.Run(second.kv)
	s.selectTab(first.id)
	time.Sleep(100 * time.Millisecond)
	tt.Frame()
	if _, ok = second.kv.LastReceipt(); ok || !second.kv.Busy() {
		t.Fatal("parked original HTTP result consumed")
	}
	s.selectTab(second.id)
	wait(func() bool { return !second.kv.Busy() })
	receipt, _ := second.kv.LastReceipt()
	if receipt.Result.HTTP == nil || len(receipt.Result.HTTP.Entries) != 1 || !bytes.Equal(receipt.Result.HTTP.Entries[0].Value, []byte{0, 255}) {
		t.Fatal("parked original bytes lost")
	}
	menu := func(section, action string) {
		t.Helper()
		if err = tt.Click(locales.Text(section)); err != nil {
			t.Fatal(err)
		}
		if err = tt.ChooseMenuItem(locales.Text(action)); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	file := filepath.Join(t.TempDir(), "original.nyakv")
	s.SetKVFiles(kvworkspace.Files{SavePath: func(string) (string, error) { return file, nil }, OpenPath: func() (string, error) { return file, nil }, OpenReader: func(path string) (io.ReadCloser, error) { return os.Open(path) }, WriteStream: queries.WriteStream})
	authored := second.kv.Request()
	menu("文件", "保存请求")
	wait(func() bool { return !second.kv.FileBusy() })
	raw, err := os.ReadFile(file)
	decoded, decodeErr := kvworkspace.DecodeRequest(raw)
	if err != nil || decodeErr != nil || decoded != authored {
		t.Fatal("public menu changed HTTP request file")
	}
	menu("窗口", "新建 KV 请求")
	third := s.currentTab()
	if third.kv == nil || third.query != nil {
		t.Fatal("Window menu opened SQL")
	}
	menu("文件", "打开请求文本")
	wait(func() bool { return !third.kv.FileBusy() })
	if third.kv.Request() != authored {
		t.Fatal("File menu changed original HTTP draft")
	}
	if _, ok = third.kv.LastReceipt(); ok {
		t.Fatal("file open executed HTTP request")
	}
	s.selectTab(second.id)
	tt.Frame()
	generation := second.kv.Scope().Generation
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if !second.kv.NeedsRecovery() || second.kv.Request() != authored || s.runtime.IsConnected(p.ID) {
		t.Fatal("disconnect lost original document")
	}
	s.sessions.Current().Password = secret
	s.startMySQLSession(services)
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	if s.sessions.StatusFor(p.ID) != "已连接" {
		t.Fatal("reconnect failed")
	}
	s.kvWorkspace.Recover(second.kv)
	wait(func() bool { return !second.kv.Busy() })
	if second.kv.NeedsRecovery() || second.kv.Scope().Generation <= generation || second.kv.Request() != authored {
		t.Fatal("explicit recovery changed original draft")
	}
	// View menu exposes a native document action and omits relational object kinds.
	if err = tt.Click(locales.Text("查看")); err != nil {
		t.Fatal(err)
	}
	if err = tt.ChooseMenuItem(locales.Text("数据库对象"), locales.Text("新建 KV 请求")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if s.currentTab().kv == nil || s.currentTab().query != nil {
		t.Fatal("View menu opened foreign workspace")
	}
	s.selectTab(second.id)
	tt.Frame()
	for _, lang := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return lang })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			view := ui.NewTester(s.View, size[0], size[1])
			view.Frame()
			body, ok := view.Find(locales.Text("KV 原生回执"))
			if !ok || body.H < 24 || body.Y+body.H > float32(size[1])+1 {
				t.Fatal("original HTTP body clipped in workbench", lang, size, body)
			}
			if dir := os.Getenv("NYADB_CONSUL_PUBLIC_IMAGES"); dir != "" {
				if err = os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%dx%d-shell-native.png", lang, size[0], size[1])))
				if err != nil {
					t.Fatal(err)
				}
				err = png.Encode(f, view.Image())
				closeErr := f.Close()
				if err != nil || closeErr != nil {
					t.Fatal("native shell screenshot failed")
				}
			}
		}
	}
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	s.closeTabID(first.id)
	s.kvWorkspace.DecideLeave(ui.Services{}, true)
	if first.kv.Editor != nil || s.currentTab() != second || second.kv.Editor == nil {
		t.Fatal("inactive original close damaged active document")
	}
}
