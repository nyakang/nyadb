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

// Opt-in real MySQL 8.4 -> native MyGo Table rendering, isolated localhost only.
func TestMySQLRealDataNativeViewer(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_STRUCTURE_IT") != "1" {
		t.Skip("requires disposable mysql:8.4 on 127.0.0.1:34306")
	}
	s, _, service, _ := profileTestState(t)
	defer s.CloseMySQLSessions()
	p, err := service.Create(context.Background(), connections.Profile{
		Name: "Data reader fixture", Kind: connections.MySQL, Host: "127.0.0.1",
		Port: 34306, Username: "nyadb_it_reader", MySQLAdvanced: connections.DefaultMySQLAdvanced(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.selectedProfileID = p.ID
	s.loadProfiles()
	settings, err := connections.ResolveActiveSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.sessions.Manager.Connect(ctx, settings, mysqlIntegrationPassword(t)); err != nil {
		t.Fatal(err)
	}
	s.sessions.Current().ProfileID = p.ID
	s.sessions.Current().Status = "已连接"
	s.sessions.Current().Databases, err = s.sessions.Manager.Databases(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(s.sessions.Current().Databases, "nyadb_struct_a") {
		t.Fatal("fixture schema not visible")
	}
	s.navigation.Tree.Open.Add(navigation.ConnectionKey(p.ID))
	tt := ui.NewTester(s.View, 1280, 800)
	if err := doubleClickDatabase(tt, "数据库：nyadb_struct_a"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	if err := tt.Click("打开选中表数据"); err != nil {
		t.Fatal(err)
	}
	waitMySQLData(t, s, tt)
	if s.currentTab().data.Page() == nil || s.currentTab().data.Page().Table != "child" ||
		len(s.currentTab().data.Page().Columns) != 8 || len(s.currentTab().data.Page().Rows) != 3 ||
		!tt.HasText("中文备注") || !tt.HasText("1.25") {
		t.Fatalf("real rows were not rendered: %+v", s.currentTab().data.Page())
	}
	livePage := s.currentTab().data.Page()
	if len(livePage.ColumnMetadata) != 8 || livePage.ColumnMetadata[2].Type != "varchar(12)" {
		t.Fatalf("real server field metadata not loaded: %+v", livePage.ColumnMetadata)
	}
	if path := os.Getenv("NYADB_MYSQL_DATA_CAPTURE"); path != "" {
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
	if err := tt.Click("对象"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("打开选中表结构"); err != nil {
		t.Fatal(err)
	}
	waitMySQLStructure(t, s, tt)
	if len(s.tabs) != 3 || s.tabs[1].kind != tabMySQLData || s.currentTab().kind != tabStructure {
		t.Fatal("data/design tabs not independent")
	}
	s.disconnectMySQLSession()
	tt.Frame()
	if len(s.tabs) != 1 {
		t.Fatal("data tab/cache survived disconnect")
	}
}
