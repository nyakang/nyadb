package shell

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/testsupport/databendfixture"
	"github.com/nyakang/nyadb/locales"
)

func TestDatabendNativePublicDatabaseCreatePreviewReceiptAndRecovery(t *testing.T) {
	f := databendfixture.Read(t)
	wf := databendWorkbenchFixture{Endpoint: f.Endpoint, Username: f.Username, PasswordFile: f.PasswordFile, Archive: f.Archive, SHA256: f.SHA256, Version: f.Version, Directory: f.Directory, Database: f.Database}
	s, tt, p, host := databendSchemaPublicState(t, f)
	services := *host
	query := func(sql string) [][]string { return databendWorkbenchSQL(t, wf, sql) }
	name := `public_create_%猫"database61`
	quoted := schemaddl.Quote(name)
	absent := func() bool {
		return len(query("SELECT name FROM system.databases WHERE name='"+name+"' AND dropped_on IS NULL")) == 0
	}
	other := databendWorkbenchData(t, s, tt, services, p, "managed_values")
	activeID := s.currentTab().id
	w := s.objectWorkspace
	if !w.CanCreateDatabase(p.ID) {
		t.Fatal("native database creation not published")
	}
	open := func() {
		if err := tt.RightClick(p.Name); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem(locales.Text("新建数据库")); err != nil {
			t.Fatal("public create menu", err)
		}
		tt.Frame()
		if !w.DatabaseCreateDialogOpen() {
			t.Fatal("public database create modal missing")
		}
		setTestInput(t, tt, "数据库名称", name)
		databendWorkbenchClick(t, tt, "重新读取数据库")
		databendWorkbenchWait(t, tt, func() bool { return !w.DatabaseCreatePending() })
		if w.DatabaseCreateError() != "" {
			t.Fatal("native create preview", w.DatabaseCreateError())
		}
	}
	open()
	databendWorkbenchCapture(t, wf, tt, "database-create-preview", "数据库名称", "确认创建数据库", "关闭")
	databendWorkbenchClick(t, tt, "关闭")
	if w.DatabaseCreateDialogOpen() || !absent() {
		t.Fatal("preview close created database")
	}
	open()
	gen := s.runtime.Generation(p.ID)
	databendWorkbenchClick(t, tt, "确认创建数据库")
	originalTab := s.activeTab
	s.activeTab = 0
	s.selectedProfileID = p.ID + 10000
	s.sessions.Activate(p.ID + 10000)
	deadline := time.Now().Add(60 * time.Second)
	for absent() {
		tt.Frame()
		if time.Now().After(deadline) {
			t.Fatal("creation did not reach original server")
		}
	}
	tt.Frame()
	if !w.DatabaseCreatePending() || w.DatabaseCreateOriginalOutcome() != nil {
		t.Fatal("parked create receipt consumed by foreign profile")
	}
	s.activeTab = originalTab
	s.selectedProfileID = p.ID
	s.sessions.Activate(p.ID)
	databendWorkbenchWait(t, tt, func() bool { return !w.DatabaseCreatePending() })
	out := w.DatabaseCreateOriginalOutcome()
	if out == nil || !out.Verified || out.State != "committed" || out.Error != nil || out.Receipt == nil || out.Actual == nil || out.Receipt.Original.Target.Generation != gen || out.Actual.Target != out.Receipt.Original.Target || out.Receipt.SQL != "CREATE DATABASE "+quoted {
		t.Fatal("public original create acknowledgement", out, w.DatabaseCreateError())
	}
	databendWorkbenchWait(t, tt, func() bool { return slices.Contains(s.sessions.Current().Databases, name) })
	if s.currentTab().id != activeID || s.currentTab().data != other || other.Page() == nil || len(other.Page().Rows) != 1 {
		t.Fatal("create changed unrelated original data tab")
	}
	query("CREATE TABLE " + quoted + ".probe (id UInt64)")
	query("INSERT INTO " + quoted + ".probe VALUES(18446744073709551615)")
	rows := query("SELECT to_string(id) FROM " + quoted + ".probe")
	if !reflect.DeepEqual(rows, [][]string{{"18446744073709551615"}}) {
		t.Fatal("new database unusable", rows)
	}
	files := make(chan string, 1)
	s.SetEngineFiles(func(string) (string, error) { return "database-receipt.json", nil }, func(_ string, write func(io.Writer) error) error {
		var b bytes.Buffer
		err := write(&b)
		files <- b.String()
		return err
	})
	databendWorkbenchCapture(t, wf, tt, "database-create-receipt", "复制完整回执", "导出完整回执", "关闭")
	databendWorkbenchClick(t, tt, "复制完整回执")
	var copied struct {
		Kind    string
		Receipt contract.DatabaseCreateReceipt
	}
	if json.Unmarshal([]byte(tt.Clipboard()), &copied) != nil || copied.Kind != "database-create-original-receipt" || !reflect.DeepEqual(copied.Receipt, *out.Receipt) {
		t.Fatal("public clipboard receipt incomplete")
	}
	databendWorkbenchClick(t, tt, "导出完整回执")
	var saved string
	select {
	case saved = <-files:
	case <-time.After(10 * time.Second):
		t.Fatal("receipt export did not complete")
	}
	if saved != tt.Clipboard() {
		t.Fatal("receipt file differs from complete clipboard")
	}
	// Explicit original-profile reconnection retains the prior receipt and only
	// permits observation. Even absence after DROP cannot replay this creation.
	query("DROP DATABASE " + quoted)
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if !reflect.DeepEqual(out, w.DatabaseCreateOriginalOutcome()) {
		t.Fatal("disconnect changed original receipt")
	}
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := vault.Set(p.ID, "", f.Secret(t)); err != nil {
		t.Fatal(err)
	}
	databendWorkbenchCapture(t, wf, tt, "database-create-disconnected", "恢复原连接", "连接所选数据库", "复制完整回执")
	databendWorkbenchClick(t, tt, "连接所选数据库")
	databendWorkbenchWait(t, tt, func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	if !s.runtime.IsConnected(p.ID) || s.runtime.Generation(p.ID) == gen {
		t.Fatal("explicit recovery failed", s.sessions.Current().Error)
	}
	databendWorkbenchClick(t, tt, "重新读取数据库")
	databendWorkbenchWait(t, tt, func() bool { return !w.DatabaseCreatePending() })
	databendWorkbenchClick(t, tt, "确认创建数据库")
	if !absent() || w.DatabaseCreatePending() || !reflect.DeepEqual(out, w.DatabaseCreateOriginalOutcome()) {
		t.Fatal("recovery replayed or rewrote original create")
	}
	databendWorkbenchCapture(t, wf, tt, "database-create-recovered", "复制完整回执", "关闭")
	databendWorkbenchClick(t, tt, "关闭")
	if w.DatabaseCreateDialogOpen() || w.DatabaseCreateOriginalOutcome() != nil {
		t.Fatal("closed create document retained state")
	}
	f.Journal(t, "public-database-create.jsonl", map[string]any{"test": t.Name(), "receipt": copied.Receipt, "public_menu": true, "preview_cancelled": true, "parked_retained": true, "namespace_refreshed": true, "unrelated_tab_preserved": true, "complete_export": true, "recovery_no_replay": true, "public_vault_recovery": true, "independent_rows": rows, "processes": f.Starts.Load()})
}
