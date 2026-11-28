package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/testsupport/prestosqlfixture"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/sessionbrowser"
	"github.com/nyakang/nyadb/locales"
)

func TestPrestoSQLNativePublicShellGridWriteExportAndExplicitRecovery(t *testing.T) {
	f := prestosqlfixture.Read(t)
	ctx := context.Background()
	directory := filepath.Join(f.Directory, t.Name(), "store")
	store, err := driverstore.New(directory)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := store.Import(ctx, driverstore.Product{Key: "prestosql", Runtime: "java", MainClass: "app.dbx.jdbc.DbxJdbcPlugin"}, f.Archive)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.RemovePinned(ctx, driver.Key, driver.Version, driver.SHA256); err != nil {
			t.Error("public package remained leased", err)
		}
	})
	s, _, profiles, _ := profileTestState(t)
	dbruntime.RegisterBuiltins(s.runtime, nil)
	parsed, err := connections.ParseURI(f.URI(t))
	if err != nil {
		t.Fatal(err)
	}
	p := parsed.Profile
	p.Name = "Public PrestoSQL"
	p.PrestoSQLOptions.DriverVersion, p.PrestoSQLOptions.DriverSHA256, p.PrestoSQLOptions.DriverDirectory = driver.Version, driver.SHA256, driver.Directory
	p, err = profiles.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	observe := f.Observe(directory)
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
	chooser.Type("PrestoSQL")
	chooser.Frame()
	if err := chooser.Click(locales.Text("选择 ") + "PrestoSQL"); err != nil {
		t.Fatal(err)
	}
	f.Screenshot(t, chooser, "shell-public-chooser")
	if err := chooser.Click(locales.Text("下一步")); err != nil {
		t.Fatal(err)
	}
	if s.connectionEditor.Draft().Kind != p.Kind {
		t.Fatal("chooser lost product identity")
	}
	if err := chooser.Click(locales.Text("取消连接向导")); err != nil {
		t.Fatal(err)
	}
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	wait := func(done func() bool) {
		t.Helper()
		deadline := time.Now().Add(150 * time.Second)
		for !done() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(5 * time.Millisecond)
		}
		tt.Frame()
		if !done() {
			t.Fatal("public workbench did not settle")
		}
	}
	click := func(label string) {
		t.Helper()
		tt.Frame()
		if err := tt.Click(locales.Text(label)); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	secrets := f.Credentials(t)
	if secrets.Password != "" {
		taskPanelField(t, tt, "PrestoSQL 会话密码", secrets.Password)
		taskPanelField(t, tt, "PrestoSQL 会话信任库密码", secrets.TrustStorePassword)
	}
	if secrets.Password != "" && tt.HasText(secrets.Password) {
		t.Fatal("session credential displayed")
	}
	if err := tt.RightClick(p.Name); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.Text("打开连接")); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	if !s.runtime.IsConnected(p.ID) {
		t.Fatal("public session failed", s.sessions.Current().Error)
	}
	session, err := s.runtime.Session(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := contract.Require[contract.QueryProvider](session)
	if err != nil {
		t.Fatal(err)
	}
	inspect, err := provider.ReserveQuery(67001)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(inspect.Release)
	label := `"memory"."default"`
	run := func(sql string) contract.SQLResult {
		t.Helper()
		r, e := inspect.Execute(ctx, label, sql)
		if e != nil {
			t.Fatal("original fixture SQL", e)
		}
		return r
	}
	run(`CREATE TABLE memory.default.shell_public67 AS SELECT BIGINT '9223372036854775807' AS id,CAST('original 猫' AS VARCHAR) AS note,X'00ff80' AS payload`)
	s.openMySQLDatabase(services, label, true)
	wait(func() bool { return s.sessions.Current().Pending == "" && s.sessions.Current().Cache[label] != nil })
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: label, Object: "shell_public67", Category: "表"})
	tab := s.currentTab()
	if tab == nil || tab.data == nil {
		t.Fatal("qualified shared data document missing")
	}
	d := tab.data
	wait(func() bool { return !d.Busy() })
	if d.Page() == nil || d.Error() != "" || d.Object().Catalog != "memory" || d.Object().Schema != "default" || d.Page().Rows[0][0].Value.Text != "9223372036854775807" || !bytes.Equal(d.Page().Rows[0][2].Value.Bytes, []byte{0, 255, 128}) {
		t.Fatal("shared typed page", d.Error())
	}
	f.Screenshot(t, tt, "shell-shared-grid")
	click("添加记录")
	tt.Command("cancel")
	tt.Frame()
	s.dataWorkspace.EditCell(services, d, 1, "id", contract.Cell{Kind: contract.Integer, Text: "2"})
	s.dataWorkspace.EditCell(services, d, 1, "note", contract.Cell{Kind: contract.Text, Text: "inserted 猫"})
	s.dataWorkspace.EditCell(services, d, 1, "payload", contract.Cell{Kind: contract.Null})
	tt.Frame()
	click("保存数据更改")
	wait(func() bool { return tt.HasText(locales.Text("确认 PrestoSQL 数据新增")) || !d.Busy() })
	if !tt.HasText(locales.Text("确认 PrestoSQL 数据新增")) {
		f.Screenshot(t, tt, "shell-missing-confirmation")
		t.Fatal("native insert confirmation missing", d.Error(), d.HasDraft(), len(d.Page().Rows))
	}
	f.Screenshot(t, tt, "shell-insert-preview")
	click("取消")
	if !d.HasDraft() || run("SELECT count(*) FROM memory.default.shell_public67").Sets[0].Rows[0][0].Text != "1" {
		t.Fatal("preview cancel wrote or discarded draft")
	}
	click("保存数据更改")
	wait(func() bool { return tt.HasText(locales.Text("确认 PrestoSQL 数据新增")) || !d.Busy() })
	click("确认并提交更改")
	wait(func() bool { return !d.Busy() })
	if d.HasDraft() || d.Error() != "" || run("SELECT count(*) FROM memory.default.shell_public67").Sets[0].Rows[0][0].Text != "2" {
		t.Fatal("public insert acknowledgement", d.Error())
	}
	click("原生顺序写入回执")
	click("复制完整回执")
	receipt := tt.Clipboard()
	if !json.Valid([]byte(receipt)) {
		t.Fatal("complete receipt missing")
	}
	f.Screenshot(t, tt, "shell-insert-receipt")
	click("关闭")
	var exported bytes.Buffer
	s.SetEngineFiles(func(name string) (string, error) { return name, nil }, func(_ string, write func(io.Writer) error) error { return write(&exported) })
	s.dataWorkspace.ExportJSONL(services, d)
	wait(func() bool { return !d.Busy() })
	if d.Error() != "" || bytes.Count(exported.Bytes(), []byte{'\n'}) != 2 {
		t.Fatal("public JSONL export", d.Error())
	}
	s.openQuery()
	q := s.currentTab().query
	wait(func() bool { return !q.Preparing() && !q.Loading() })
	q.Editor.Buffer.Set("SELECT count(*) FROM shell_public67")
	tt.Frame()
	click("运行当前或选中 SQL")
	wait(func() bool { return !q.Loading() })
	if q.Result() == nil || q.Error() != "" {
		t.Fatal("public SQL", q.Error())
	}
	text := "INSERT INTO shell_public67 (id) VALUES (999)"
	q.Editor.Buffer.Set(text)
	generation := s.runtime.Generation(p.ID)
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if !q.NeedsRecovery() || q.Editor.Buffer.String() != text || d.Page() != nil {
		t.Fatal("disconnect lost text or retained old data")
	}
	f.Screenshot(t, tt, "shell-disconnected")
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := vault.Set(p.ID, "", f.Secret(t)); err != nil {
		t.Fatal(err)
	}
	s.sessions.Connect(p, services.Invalidate)
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	if !s.runtime.IsConnected(p.ID) {
		t.Fatal("vault recovery failed", s.sessions.Current().Error)
	}
	click("建立新物理会话（不执行现有 SQL）")
	wait(func() bool { return !q.NeedsRecovery() && !q.Loading() && !q.Preparing() })
	if q.Result() != nil || q.Editor.Buffer.String() != text || s.runtime.Generation(p.ID) <= generation {
		t.Fatal("explicit recovery altered SQL")
	}
	session, err = s.runtime.Session(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	provider, err = contract.Require[contract.QueryProvider](session)
	if err != nil {
		t.Fatal(err)
	}
	inspect, err = provider.ReserveQuery(67002)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(inspect.Release)
	if run("SELECT count(*) FROM memory.default.shell_public67 WHERE id=999").Sets[0].Rows[0][0].Text != "0" {
		t.Fatal("recovery replayed SQL")
	}
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		tt.SetLocale(language)
		f.Screenshot(t, tt, fmt.Sprintf("%s-shell-recovered", language))
	}
	run("DROP TABLE memory.default.shell_public67")
	f.Journal(t, "public-shell.jsonl", map[string]any{"test": t.Name(), "typed_grid": true, "insert_preview_canceled_without_write": true, "receipt": json.RawMessage(receipt), "export_rows": 2, "original_generation": generation, "replacement_generation": s.runtime.Generation(p.ID), "recovery_replayed": false, "temporary_credentials": secrets.Password != "", "vault_recovery": true, "starts": f.Starts.Load()})
}
