package shell

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func TestSQLiteAttachedNamespaceNavigatesAndEditsOriginalFile(t *testing.T) {
	s, _, profiles, _ := profileTestState(t)
	dir := t.TempDir()
	t.Cleanup(func() { s.CloseMySQLSessions() })
	main, aux := filepath.Join(dir, "main.sqlite"), filepath.Join(dir, "aux.sqlite")
	for path, text := range map[string]string{main: "main fixture", aux: "attached fixture"} {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec("CREATE TABLE same_name(id INTEGER PRIMARY KEY,label TEXT); INSERT INTO same_name VALUES(1,?)", text)
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	p, _ := connections.Defaults(connections.SQLite)
	p.Name, p.Database = "Attached fixture", main
	p.SQLiteOptions.Attachments = []connections.SQLiteAttachment{{Name: "aux", File: aux}}
	p, err := profiles.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	if err = tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	w := s.sessions.Current()
	wait := func(done func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !done() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(time.Millisecond)
		}
		if !done() {
			t.Fatalf("workspace timed out: %s %s", w.Status, w.Error)
		}
	}
	wait(func() bool { return w.Status != "连接中" })
	if w.Status != "已连接" || len(w.Databases) != 2 || w.Namespaces["aux"].Catalog != "main" {
		t.Fatalf("attached namespace missing: %+v", w)
	}
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, "aux"))
	wait(func() bool { return w.Pending == "" && w.Cache["aux"] != nil })
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: "aux", Object: "same_name", Category: "表"})
	tab := s.currentTab()
	if tab.data == nil || tab.data.Object().Schema != "aux" || tab.data.Object().ProfileID != p.ID {
		t.Fatal("navigator opened wrong namespace")
	}
	wait(func() bool { return !tab.data.Busy() })
	if tab.data.Page() == nil || tab.data.Page().Rows[0][1].Text != "attached fixture" {
		t.Fatalf("grid read main instead of aux: %s", tab.data.Error())
	}
	s.dataWorkspace.EditCell(services, tab.data, 0, "label", contract.Cell{Kind: contract.Text, Text: "saved attached"})
	tt.Frame()
	if err = tt.Click("保存数据更改"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !tab.data.Busy() })
	if tab.data.Page() == nil || tab.data.Page().Rows[0][1].Text != "saved attached" {
		t.Fatalf("attached write failed: %s", tab.data.Error())
	}
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	for path, want := range map[string]string{main: "main fixture", aux: "saved attached"} {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		var got string
		err = db.QueryRow("SELECT label FROM same_name").Scan(&got)
		db.Close()
		if err != nil || got != want {
			t.Fatalf("file identity changed: got %s want %s err %v", got, want, err)
		}
	}
}
