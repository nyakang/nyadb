package shell

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestDictionaryRealSQLiteNativeEntriesExportOriginalAttachedNamespace(t *testing.T) {
	s, _, profiles, _ := profileTestState(t)
	dir := t.TempDir()
	main, aux := filepath.Join(dir, "main.sqlite"), filepath.Join(dir, "aux.sqlite")
	for _, path := range []string{main, aux} {
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	options := connections.DefaultSQLiteOptions()
	options.Attachments = []connections.SQLiteAttachment{{Name: "aux", File: aux}}
	p, err := profiles.Create(context.Background(), connections.Profile{Name: "Dictionary SQLite fixture", Kind: connections.SQLite, Database: main, SQLiteOptions: options})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	t.Cleanup(s.CloseMySQLSessions)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	tt.Frame()
	s.startMySQLSession(services)
	until := time.Now().Add(3 * time.Second)
	for s.sessions.StatusFor(p.ID) == "连接中" && time.Now().Before(until) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	session, err := s.engineSession(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := session.Reserve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = lease.Execute(context.Background(), `CREATE TABLE main.records(main_only TEXT);
CREATE TABLE aux.parent(id INTEGER PRIMARY KEY);
CREATE TABLE aux.records(id INTEGER PRIMARY KEY,aux_only TEXT DEFAULT '',parent_id INTEGER REFERENCES parent(id));
CREATE INDEX aux.idx_aux ON records(aux_only);
CREATE VIEW aux.records_view AS SELECT aux_only FROM records;
INSERT INTO aux.records VALUES(1,'row_value_must_not_be_exported',NULL);`)
	lease.Close()
	if err != nil {
		t.Fatal(err)
	}
	s.openMySQLDatabase(services, "aux", false)
	until = time.Now().Add(3 * time.Second)
	for (s.sessions.Current().Pending != "" || len(s.sessions.Current().Cache["aux"]) != 3) && time.Now().Before(until) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if !s.objectWorkspace.CanDictionary(p.ID, "aux", "", "") {
		t.Fatal("read-only native dictionary unavailable")
	}
	path := filepath.Join(dir, "dictionary.md")
	s.SetDictionaryFilePicker(func(string) (string, error) { return path, nil })
	if err = tt.Click("工具"); err != nil {
		t.Fatal(err)
	}
	if err = tt.ChooseMenuItem("数据字典…"); err != nil {
		t.Fatal(err)
	}
	if err = tt.Click("读取字典"); err != nil {
		t.Fatal(err)
	}
	until = time.Now().Add(3 * time.Second)
	for !tt.HasText("字典包含 3 个对象；导出完整已读字典，搜索仅过滤预览。") && time.Now().Before(until) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if !tt.HasText("字典包含 3 个对象；导出完整已读字典，搜索仅过滤预览。") {
		t.Fatal("real metadata did not arrive")
	}
	if err = tt.Click("导出字典…"); err != nil {
		t.Fatal(err)
	}
	var data []byte
	until = time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		tt.Frame()
		data, err = os.ReadFile(path)
		if err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil || !strings.Contains(string(data), "aux&#95;only") || !strings.Contains(string(data), "idx&#95;aux") || !strings.Contains(string(data), "records&#95;view") || strings.Contains(string(data), "main_only") || strings.Contains(string(data), "row_value_must_not_be_exported") {
		t.Fatal("incorrect or row-reading dictionary export", err, string(data))
	}
	if err = tt.Click("关闭"); err != nil {
		t.Fatal(err)
	}
	// Same feature is reachable from the original table's native context menu.
	if err = tt.RightClick("records"); err != nil {
		t.Fatal(err)
	}
	if err = tt.ChooseMenuItem("数据字典…"); err != nil {
		t.Fatal(err)
	}
	if err = tt.Click("读取字典"); err != nil {
		t.Fatal(err)
	}
	until = time.Now().Add(3 * time.Second)
	for !tt.HasText("aux_only") && time.Now().Before(until) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if !tt.HasText("aux_only") || !tt.HasText("字典包含 1 个对象；导出完整已读字典，搜索仅过滤预览。") {
		t.Fatal("table menu read another scope")
	}
}
