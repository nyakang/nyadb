package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/testsupport/databendfixture"
	"github.com/nyakang/nyadb/internal/ui/sessionbrowser"
	"github.com/nyakang/nyadb/locales"
)

func TestDatabendNativePublicShellChooserGridWriteAndExplicitSQLRecovery(t *testing.T) {
	f := databendfixture.Read(t)
	wf := databendWorkbenchFixture{Endpoint: f.Endpoint, Username: f.Username, PasswordFile: f.PasswordFile, Archive: f.Archive, SHA256: f.SHA256, Version: f.Version, Directory: f.Directory, Database: f.Database}
	directory := filepath.Join(f.Directory, t.Name(), "store")
	store, err := driverstore.New(directory)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := store.Import(context.Background(), driverstore.Product{Key: "databend", Runtime: "java", MainClass: "com.dbx.agent.databend.DatabendAgent"}, f.Archive)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Remove(context.Background(), "databend", f.Version); err != nil {
			t.Error("public package still leased", err)
		}
	})
	s, _, profiles, _ := profileTestState(t)
	// Retain actual builtins; never substitute the Databend provider.
	dbruntime.RegisterBuiltins(s.runtime, nil)
	secret := f.Secret(t)
	endpoint, _ := url.Parse(f.Endpoint)
	uri := &url.URL{Scheme: "databend", Host: endpoint.Host, Path: "/" + f.Database, User: url.UserPassword(f.Username, secret), RawQuery: "ssl=false"}
	parsed, err := connections.ParseURI(uri.String())
	if err != nil {
		t.Fatal(err)
	}
	p := parsed.Profile
	p.Name = "Public Databend"
	p.DatabendOptions.DriverVersion, p.DatabendOptions.DriverSHA256, p.DatabendOptions.DriverDirectory = driver.Version, driver.SHA256, driver.Directory
	p, err = profiles.Create(context.Background(), p)
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
	chooser.Type("Databend")
	chooser.Frame()
	if err := chooser.Click(locales.Text("选择 ") + "Databend"); err != nil {
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
	credentialLabel := fmt.Sprintf(locales.Text("本次 %s 会话密码"), "Databend")
	taskPanelField(t, tt, credentialLabel, secret)
	if tt.HasText(secret) {
		t.Fatal("temporary credential exposed")
	}
	if err := tt.RightClick(p.Name); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.Text("打开连接")); err != nil {
		t.Fatal("public context-menu connection unavailable", err)
	}
	databendWorkbenchWait(t, tt, func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	if !s.runtime.IsConnected(p.ID) || s.sessions.StatusFor(p.ID) != "已连接" {
		t.Fatal("public session failed", s.sessions.Current().Error)
	}
	d := databendWorkbenchData(t, s, tt, services, p, "managed_values")
	cells := d.Page().Rows[0]
	if cells[0].Value.Text != "18446744073709551615" || cells[1].Value.Text != "9007199254740993.12345678901234567890" || !bytes.Equal(cells[2].Value.Bytes, []byte{0, 255, 128}) || cells[3].Value.Kind != contract.Null {
		t.Fatal("public grid lost typed values")
	}
	databendWorkbenchCapture(t, wf, tt, "public-grid")
	s.dataWorkspace.EditCell(services, d, 0, "absent", contract.Cell{Kind: contract.Text, Text: "public edited 猫"})
	databendWorkbenchPreview(t, s, tt, d)
	databendWorkbenchCapture(t, wf, tt, "public-preview", "取消", "确认并提交更改")
	databendWorkbenchClick(t, tt, "取消")
	if !d.HasDraft() {
		t.Fatal("canceled public preview lost draft")
	}
	databendWorkbenchConfirm(t, s, tt, d)
	rows := databendWorkbenchSQL(t, wf, `SELECT absent FROM "`+f.Database+`".managed_values`)
	if len(rows) != 1 || rows[0][0] != "public edited 猫" {
		t.Fatal("public write did not reach original server")
	}
	databendWorkbenchClick(t, tt, "原生顺序写入回执")
	databendWorkbenchClick(t, tt, "复制完整回执")
	receipt := tt.Clipboard()
	var envelope struct {
		Receipt         contract.DataSequentialWriteReceipt
		CommitConfirmed bool
	}
	if json.Unmarshal([]byte(receipt), &envelope) != nil || envelope.CommitConfirmed || len(envelope.Receipt.Statements) != 1 || envelope.Receipt.Statements[0].State != contract.DataWriteAcknowledged || envelope.Receipt.Statements[0].AffectedRows != 1 {
		t.Fatal("public write lacks original acknowledgement")
	}
	databendWorkbenchCapture(t, wf, tt, "public-receipt", "复制完整回执", "关闭")
	databendWorkbenchClick(t, tt, "关闭")
	s.openQuery()
	q := s.currentTab().query
	databendWorkbenchWait(t, tt, func() bool { return !q.Preparing() && !q.Loading() })
	q.Editor.Buffer.Set("SELECT count(*) FROM managed_values")
	tt.Frame()
	databendWorkbenchClick(t, tt, "运行当前或选中 SQL")
	databendWorkbenchWait(t, tt, func() bool { return !q.Loading() })
	if q.Result() == nil || q.Error() != "" {
		t.Fatal("public SQL failed", q.Error())
	}
	source := "INSERT INTO managed_writes VALUES (999)"
	q.Editor.Buffer.Set(source)
	generation := s.runtime.Generation(p.ID)
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if !q.NeedsRecovery() || q.Editor.Buffer.String() != source || d.Page() != nil {
		t.Fatal("public disconnect lost SQL or retained scoped data")
	}
	databendWorkbenchCapture(t, wf, tt, "public-disconnected")
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := vault.Set(p.ID, "", secret); err != nil {
		t.Fatal(err)
	}
	s.sessions.Connect(p, services.Invalidate)
	databendWorkbenchWait(t, tt, func() bool { return s.runtime.IsConnected(p.ID) && s.sessions.StatusFor(p.ID) == "已连接" })
	databendWorkbenchClick(t, tt, "建立新物理会话（不执行现有 SQL）")
	databendWorkbenchWait(t, tt, func() bool { return !q.NeedsRecovery() && !q.Loading() && !q.Preparing() })
	rows = databendWorkbenchSQL(t, wf, `SELECT count(*) FROM "`+f.Database+`".managed_writes WHERE id=999`)
	if q.Result() != nil || q.Editor.Buffer.String() != source || s.runtime.Generation(p.ID) <= generation || len(rows) != 1 || rows[0][0] != "0" {
		t.Fatal("public recovery replayed or lost original SQL")
	}
	databendWorkbenchCapture(t, wf, tt, "public-recovered")
	f.Journal(t, "public-shell.jsonl", map[string]any{"test": t.Name(), "public_provider_registered": true, "typed_grid": true, "preview_cancel_kept_draft": true, "receipt": json.RawMessage(receipt), "original_generation": generation, "replacement_generation": s.runtime.Generation(p.ID), "recovery_replayed": false, "sql_text_retained": true, "vault_recovery": true, "menu_connect": true, "processes": f.Starts.Load()})
}
