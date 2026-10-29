package dataworkspace

import (
	"context"
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestMySQLRealKeysetAndOnDemandFieldNativeViewer(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_STRUCTURE_IT") != "1" {
		t.Skip("requires isolated mysql8.4")
	}
	manager := mysqlsvc.NewMySQLSessionManager()
	t.Cleanup(func() { _ = manager.Close() })
	settings := connections.ActiveSettings{ProfileID: 12, Host: "127.0.0.1", Port: 34306, Username: "nyadb_it_reader", MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := manager.Connect(ctx, settings, mysqlIntegrationPassword(t)); err != nil {
		t.Fatal(err)
	}
	s := New(Dependencies{Session: func() Session { return Session{ProfileID: 12, Connected: true} }, Runtime: func() Runtime { return MySQLRuntime(manager) }, Inspector: InspectorDependencies{Session: func() Session { return Session{ProfileID: 12, Connected: true} }}})
	doc := s.Open(1, 12, "nyadb_perf", "blob_entries")
	tt := ui.NewTester(func(c *ui.Context) {
		s.Consume(c.Services())
		theme.Apply(c)
		ui.Row(c).Fill().Children(func() {
			ui.Box(c).Grow(1).MinWidth(0).Children(func() { s.View(c, doc) })
			ui.Box(c).Width(250).Children(func() { s.InspectorView(c, doc) })
		})
	}, 1280, 800)
	s.Load(ui.Services{}, doc, false)
	waitMySQLData(t, s, doc, tt)
	tab := doc
	if tab.page == nil || tab.page.Table != "blob_entries" || !tab.page.KeysetAvailable ||
		len(tab.page.RowRefs) != 2 || tab.page.RowRefs[0] == nil {
		t.Fatalf("no keyed rows: %+v", tab.page)
	}
	tab.selectedRow = 0
	tab.grid.Cells = ui.ColumnRange{Cursor: "payload", Anchor: "payload"}
	s.ShowField(tab)
	tt.Frame()
	if err := tt.Click("读取所选字段下一段"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for tab.panel.ChunkLoading() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	tt.Frame()
	if tab.panel.ChunkError() != "" || tab.panel.ChunkNext() != 1024 || tab.panel.ChunkTotal() != 1048576 ||
		len(tab.panel.ChunkText()) != 2050 {
		t.Fatalf("chunk retrieval: next=%d total=%d len=%d err=%q", tab.panel.ChunkNext(), tab.panel.ChunkTotal(), len(tab.panel.ChunkText()), tab.panel.ChunkError())
	}
	if err := tt.Click("读取所选字段下一段"); err != nil {
		t.Fatal(err)
	}
	for tab.panel.ChunkLoading() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	tt.Frame()
	if tab.panel.ChunkNext() != 2048 {
		t.Fatalf("chunk wasn't advanced: %d", tab.panel.ChunkNext())
	}
	if path := os.Getenv("NYADB_MYSQL_KEYSET_CAPTURE"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, tt.Image()); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	tab.limit = 1
	if err := tt.Click("刷新表数据：blob_entries"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, doc, tt)
	if !tab.page.KeysetAvailable || tab.page.NextCursor == nil || tab.offset != 0 {
		t.Fatal("initial page isn't seekable")
	}
	if err := tt.Click("下一页数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, doc, tt)
	if tab.offset != 1 || !tab.useKeyset || tab.after == nil ||
		tab.page.Rows[0][0].Text != "2" {
		t.Fatalf("next seek page: %+v", tab.page)
	}
	if err := tt.Click("上一页数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, doc, tt)
	if tab.offset != 0 || tab.page.Rows[0][0].Text != "1" {
		t.Fatalf("seek history backward navigation: %+v", tab.page)
	}
	s.Close(doc)
	tt.Frame()
	if len(s.documents) != 0 || doc.page != nil {
		t.Fatal("closed data document retained cache")
	}
}
