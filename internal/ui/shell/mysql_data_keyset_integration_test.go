package shell

import (
	"context"
	"image/png"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func TestMySQLRealKeysetAndOnDemandFieldNativeViewer(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_STRUCTURE_IT") != "1" {
		t.Skip("requires isolated mysql8.4")
	}
	s, _, service, _ := profileTestState(t)
	defer s.CloseMySQLSessions()
	p, err := service.Create(context.Background(), connections.Profile{
		Name: "Keyset real fixture", Kind: connections.MySQL, Host: "127.0.0.1", Port: 34306,
		Username: "nyadb_it_reader", MySQLAdvanced: connections.DefaultMySQLAdvanced()})
	if err != nil {
		t.Fatal(err)
	}
	s.selectedProfileID = p.ID
	s.loadProfiles()
	settings, err := connections.ResolveActiveSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err = s.sessions.Manager.Connect(ctx, settings, mysqlIntegrationPassword(t)); err != nil {
		t.Fatal(err)
	}
	s.sessions.Current().ProfileID = p.ID
	s.sessions.Current().Status = "已连接"
	s.sessions.Current().Databases, err = s.sessions.Manager.Databases(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(s.sessions.Current().Databases, "nyadb_perf") {
		t.Fatal("performance fixture not visible")
	}
	s.navigation.Tree.Open.Add(navigation.ConnectionKey(p.ID))
	tt := ui.NewTester(s.View, 1280, 800)
	if err = doubleClickDatabase(tt, "数据库：nyadb_perf"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	if err = tt.Click("打开选中表数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	tab := s.currentTab()
	if tab.dataPage == nil || tab.dataPage.Table != "blob_entries" || !tab.dataPage.KeysetAvailable ||
		len(tab.dataPage.RowRefs) != 2 || tab.dataPage.RowRefs[0] == nil {
		t.Fatalf("no keyed rows: %+v", tab.dataPage)
	}
	tab.dataSelectedRow = 0
	tab.dataState.Cells = ui.ColumnRange{Cursor: "payload", Anchor: "payload"}
	tt.Frame()
	if err = tt.Click("读取所选字段下一段"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for tab.dataPanel.ChunkLoading() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	tt.Frame()
	if tab.dataPanel.ChunkError() != "" || tab.dataPanel.ChunkNext() != 1024 || tab.dataPanel.ChunkTotal() != 1048576 ||
		len(tab.dataPanel.ChunkText()) != 2050 {
		t.Fatalf("chunk retrieval: next=%d total=%d len=%d err=%q", tab.dataPanel.ChunkNext(), tab.dataPanel.ChunkTotal(), len(tab.dataPanel.ChunkText()), tab.dataPanel.ChunkError())
	}
	if err = tt.Click("读取所选字段下一段"); err != nil {
		t.Fatal(err)
	}
	for tab.dataPanel.ChunkLoading() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	tt.Frame()
	if tab.dataPanel.ChunkNext() != 2048 {
		t.Fatalf("chunk wasn't advanced: %d", tab.dataPanel.ChunkNext())
	}
	if path := os.Getenv("NYADB_MYSQL_KEYSET_CAPTURE"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = png.Encode(f, tt.Image()); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	tab.dataLimit = 1
	if err = tt.Click("刷新表数据：blob_entries"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if !tab.dataPage.KeysetAvailable || tab.dataPage.NextCursor == nil || tab.dataOffset != 0 {
		t.Fatal("initial page isn't seekable")
	}
	if err = tt.Click("下一页数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if tab.dataOffset != 1 || !tab.dataUseKeyset || tab.dataAfter == nil ||
		tab.dataPage.Rows[0][0].Text != "2" {
		t.Fatalf("next seek page: %+v", tab.dataPage)
	}
	if err = tt.Click("上一页数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if tab.dataOffset != 0 || tab.dataPage.Rows[0][0].Text != "1" {
		t.Fatalf("seek history backward navigation: %+v", tab.dataPage)
	}
	s.disconnectMySQLSession()
	tt.Frame()
	if len(s.tabs) != 1 {
		t.Fatal("disconnected data tab retained")
	}
}
