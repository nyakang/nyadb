package shell

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/testsupport/zookeeperfixture"
	"github.com/nyakang/nyadb/internal/ui/kvworkspace"
	"github.com/nyakang/nyadb/internal/ui/sessionbrowser"
	"github.com/nyakang/nyadb/locales"
)

func TestZooKeeperNativePublicShellChooserNavigationDocumentsFilesAndRecovery(t *testing.T) {
	f := zookeeperfixture.Read(t)
	_, driver := f.Install(t)
	s, _, profiles, _ := profileTestState(t)
	t.Cleanup(s.CloseMySQLSessions)
	p := f.Profile(t, driver, 0)
	p.ZooKeeperOptions.Auth, p.Username = "digest", "fixture-user"
	p, err := profiles.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	observe := f.Observe(driver.Directory, driver.Version)
	s.sessions.SetEngineRuntime(sessionbrowser.EngineRuntime{Open: func(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
		return s.runtime.ConnectSession(driverstore.WithRuntimeObserver(ctx, observe), p, secret)
	}, Connect: s.runtime.Connect, Session: s.runtime.Session, Disconnect: s.runtime.Disconnect, Close: s.runtime.Close, IsConnected: s.runtime.IsConnected})
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	s.SetConnectionWindowHost(func() {}, func() {})
	s.openConnectionDialog()
	chooser := ui.NewTester(s.ConnectionWindowView, 920, 780)
	if err := chooser.Click(locales.Text("搜索连接类型")); err != nil {
		t.Fatal(err)
	}
	chooser.Type("ZooKeeper")
	chooser.Frame()
	if err := chooser.Click(locales.Text("选择 ") + "ZooKeeper"); err != nil {
		t.Fatal(err)
	}
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{920, 780}, {720, 520}} {
			view := ui.NewTester(s.ConnectionWindowView, size[0], size[1])
			view.Frame()
			if _, ok := view.Find(locales.Text("下一步")); !ok {
				t.Fatal("public chooser selection lost")
			}
			f.Screenshot(t, view, fmt.Sprintf("%s-%dx%d-public-chooser", language, size[0], size[1]))
		}
	}
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	chooser.Frame()
	if err := chooser.Click(locales.Text("下一步")); err != nil {
		t.Fatal(err)
	}
	if s.connectionEditor.Draft().Kind != p.Kind {
		t.Fatal("public chooser forwarded foreign product")
	}
	if err := chooser.Click(locales.Text("取消连接向导")); err != nil {
		t.Fatal(err)
	}
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	input := func(label, value string) {
		t.Helper()
		if err := tt.Click(locales.Text(label)); err != nil {
			t.Fatal(err)
		}
		tt.Command("selectAll")
		tt.Type(value)
		tt.Frame()
	}
	secret := "original-session-fixture-secret"
	input(fmt.Sprintf(locales.Text("本次 %s 会话密码"), "ZooKeeper"), secret)
	if tt.HasText(secret) {
		t.Fatal("temporary native session secret was displayed")
	}
	if err := tt.Click(locales.Text("连接所选数据库")); err != nil {
		t.Fatal(err)
	}
	wait := func(pending func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for pending() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(2 * time.Millisecond)
		}
		tt.Frame()
		if pending() {
			t.Fatal("public shell job did not settle", s.sessions.Current().Error)
		}
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) == "连接中" })
	if s.sessions.StatusFor(p.ID) != "已连接" || s.sessions.Current().Error != "" || !s.runtime.IsConnected(p.ID) || len(s.sessions.DatabasesFor(p.ID)) != 0 {
		t.Fatal("native connection invented relational metadata", s.sessions.Current().Error)
	}
	for _, action := range []string{"表", "视图", "函数", "查询", "用户", "备份", "新建查询"} {
		if s.toolbarActionAvailable(action) {
			t.Fatal("relational action published for ZooKeeper", action)
		}
	}
	if err := tt.Click(locales.Text("新建 KV 请求")); err != nil {
		t.Fatal(err)
	}
	first := s.currentTab()
	if first == nil || first.kv == nil || first.query != nil || first.data != nil {
		t.Fatal("native entry opened foreign workbench")
	}
	wait(func() bool { return first.kv.Busy() })
	if first.kv.Request().Method != "kv_list_prefix" || first.kv.Scope().Namespace.ProfileID != p.ID {
		t.Fatal("native entry opened foreign workbench")
	}
	key := "/nyadb_public_shell_" + rand.Text()
	value := []byte{0, 255, 128}
	body, _ := json.Marshal(map[string]any{"key": key, "value": map[string]string{"encoding": "base64", "data": base64.StdEncoding.EncodeToString(value)}, "writeMode": "create", "createMode": "persistent"})
	s.kvWorkspace.SetMethod(first.kv, "kv_put")
	first.kv.Editor.Buffer.Set(string(body))
	tt.Frame()
	if err := tt.Click(locales.Text("运行 KV 请求")); err != nil {
		t.Fatal(err)
	}
	capture := func(stage string) {
		t.Helper()
		for _, language := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return language })
			for _, size := range [][2]int{{1280, 800}, {720, 520}} {
				view := ui.NewTester(s.View, size[0], size[1])
				view.Frame()
				f.Screenshot(t, view, fmt.Sprintf("%s-%dx%d-public-shell-%s", language, size[0], size[1], stage))
			}
		}
		i18n.SetLocaleFunc(func() string { return "zh-Hans" })
		tt.Frame()
	}
	capture("confirmation")
	for _, label := range []string{"我确认执行此原生写入请求", "确认执行 KV 写入"} {
		if err := tt.Click(locales.Text(label)); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	wait(func() bool { return first.kv.Busy() })
	write, ok := first.kv.LastReceipt()
	if !ok || !write.Result.Completed || write.Result.ZNode == nil || !write.Result.ZNode.Acknowledged || write.Result.ZNode.Reply.CreatedKey != key {
		t.Fatal("public native write lost original receipt")
	}
	capture("receipt")
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("second original request document unavailable")
	}
	second := s.currentTab()
	wait(func() bool { return second.kv.Busy() })
	s.kvWorkspace.SetMethod(second.kv, "kv_get")
	body, _ = json.Marshal(map[string]string{"key": key})
	second.kv.Editor.Buffer.Set(string(body))
	s.kvWorkspace.Run(second.kv)
	s.selectTab(first.id)
	// A real server read can finish while parked; admission awaits original activation.
	time.Sleep(100 * time.Millisecond)
	tt.Frame()
	if _, ok := second.kv.LastReceipt(); ok || !second.kv.Busy() {
		t.Fatal("parked public completion entered another document")
	}
	s.selectTab(second.id)
	wait(func() bool { return second.kv.Busy() })
	read, ok := second.kv.LastReceipt()
	if !ok || read.Result.ZNode == nil || read.Result.ZNode.Reply.Node == nil || !bytes.Equal(read.Result.ZNode.Reply.Node.Value, value) {
		t.Fatal("original parked native bytes lost")
	}
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
	file := filepath.Join(t.TempDir(), "original.nyakv")
	var exportPath string
	s.SetKVFiles(kvworkspace.Files{SavePath: func(name string) (string, error) {
		if name == "response.jsonrpc.json" {
			exportPath = filepath.Join(filepath.Dir(file), name)
			return exportPath, nil
		}
		return file, nil
	}, OpenPath: func() (string, error) { return file, nil }, OpenReader: func(path string) (io.ReadCloser, error) { return os.Open(path) }, WriteStream: queries.WriteStream})
	authored := second.kv.Request()
	menu("文件", "保存请求")
	wait(func() bool { return second.kv.FileBusy() })
	raw, err := os.ReadFile(file)
	decoded, decodeErr := kvworkspace.DecodeRequest(raw)
	if err != nil || decodeErr != nil || decoded != authored {
		t.Fatal("public file menu changed original request")
	}
	s.kvWorkspace.ExportReceipt(second.kv, true)
	wait(func() bool { return second.kv.FileBusy() })
	raw, err = os.ReadFile(exportPath)
	if err != nil || !bytes.Equal(raw, read.Result.ZNode.Native) {
		t.Fatal("public original JSON-RPC export was changed", err)
	}
	menu("窗口", "新建 KV 请求")
	third := s.currentTab()
	wait(func() bool { return third.kv.Busy() })
	menu("文件", "打开请求文本")
	wait(func() bool { return third.kv.FileBusy() })
	if third.kv.Request() != authored {
		t.Fatal("public file open lost original method or text")
	}
	if _, ok := third.kv.LastReceipt(); ok {
		t.Fatal("file open executed the original request")
	}
	s.selectTab(second.id)
	tt.Frame()
	generation := second.kv.Scope().Generation
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if !second.kv.NeedsRecovery() || second.kv.Request() != authored || s.runtime.IsConnected(p.ID) {
		t.Fatal("disconnect discarded original request")
	}
	if err := tt.Click("对象"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	input(fmt.Sprintf(locales.Text("本次 %s 会话密码"), "ZooKeeper"), secret)
	if err := tt.Click(locales.Text("连接所选数据库")); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) == "连接中" })
	if s.sessions.StatusFor(p.ID) != "已连接" {
		t.Fatal("original public profile did not reconnect", s.sessions.Current().Error)
	}
	s.selectTab(second.id)
	tt.Frame()
	before := f.Starts.Load()
	if err := tt.Click(locales.Text("建立新会话（不执行现有请求）")); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return second.kv.Busy() })
	if second.kv.NeedsRecovery() || second.kv.Scope().Generation <= generation || second.kv.Request() != authored || f.Starts.Load() != before {
		t.Fatal("explicit public recovery connected/replayed or changed original draft", second.kv.Error())
	}
	recovered, ok := second.kv.LastReceipt()
	if !ok || !bytes.Equal(recovered.Result.ZNode.Native, read.Result.ZNode.Native) {
		t.Fatal("public recovery lost original native receipt")
	}
	capture("recovery")
	activeID := second.id
	s.closeTabID(first.id)
	s.kvWorkspace.DecideLeave(services, true)
	if first.kv.Editor != nil || s.currentTab().id != activeID || second.kv.Editor == nil {
		t.Fatal("inactive close changed active document")
	}
	// Deletion uses the original profile identity and clears its saved credential.
	if err := vault.Set(p.ID, "", secret); err != nil {
		t.Fatal(err)
	}
	s.deleteProfileID(p.ID)
	if _, found, _ := vault.Get(p.ID, ""); found {
		t.Fatal("deleted native profile retained saved credential")
	}
	s.CloseMySQLSessions()
	for _, tab := range []*tab{first, second, third} {
		if tab.kv.Editor != nil {
			t.Fatal("workbench close retained native editor cache")
		}
	}
}
