package shell

import (
	"context"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func TestNavigatorSwitchActiveProfileConfiguration(t *testing.T) {
	s, tt, svc, _ := profileTestState(t)
	ctx := context.Background()
	profile, err := svc.Create(ctx, connections.Profile{
		Name: "Variants", Kind: connections.MySQL, Host: "localhost", Port: 3306,
		Variants: []connections.ConnectionVariant{
			{Name: "Staging", Host: "localhost", Port: 3307},
			{Name: "Lab", Host: "localhost", Port: 3308},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	tt.Frame()
	if err := tt.RightClick("Variants"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(tt.Menu(), "|"), "切换连接配置文件") {
		t.Fatalf("configuration submenu missing: %v", tt.Menu())
	}
	if err := tt.ChooseMenuItem("切换连接配置文件", "Staging"); err != nil {
		t.Fatalf("submenu cannot switch disconnected profile: %v", err)
	}
	stored, err := svc.Get(ctx, profile.ID)
	if err != nil || stored.ActiveVariant != "Staging" {
		t.Fatalf("variant was not persisted: %+v %v", stored, err)
	}
	s.sessions.Current().ProfileID = profile.ID
	s.sessions.Current().Status = "已连接"
	s.setProfileActiveVariant(profile.ID, "Lab")
	stored, err = svc.Get(ctx, profile.ID)
	if err != nil || stored.ActiveVariant != "Staging" {
		t.Fatalf("active session configuration changed: %+v %v", stored, err)
	}
	s.sessions.Current().ProfileID = 0
	s.sessions.Current().Status = "未连接"
	s.setProfileActiveVariant(profile.ID, "Nonexistent")
	stored, err = svc.Get(ctx, profile.ID)
	if err != nil || stored.ActiveVariant != "Staging" {
		t.Fatalf("invalid variant changed storage: %+v %v", stored, err)
	}
	s.setProfileActiveVariant(profile.ID, "")
	stored, err = svc.Get(ctx, profile.ID)
	if err != nil || stored.ActiveVariant != "" {
		t.Fatalf("default variant did not restore: %+v %v", stored, err)
	}
}

func TestNavigatorConnectionStatusIdentityAndRootInspector(t *testing.T) {
	s, tt, svc, _ := profileTestState(t)
	if !tt.HasText("我的连接") {
		t.Fatal("root navigation not visible")
	}
	p, err := svc.Create(context.Background(), connections.Profile{Name: "Connection", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	for _, state := range []string{"未连接", "连接中", "已连接", "连接失败"} {
		s.sessions.Current().ProfileID, s.sessions.Current().Status = p.ID, state
		tt.Frame()
		if _, ok := tt.Find("Connection · " + state); !ok {
			t.Fatalf("connection status %q missing accessible row label", state)
		}
	}
}

func TestNavigatorFailedMetadataRowCanRetry(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	db := navigation.DatabaseNode(s.sessions.Current().ProfileID, "app_db")
	s.navigation.Tree.Open.Add(navigation.ConnectionKey(s.sessions.Current().ProfileID))
	s.navigation.Tree.Open.Add(db.Key())
	s.sessions.MarkOpened(db.ProfileID, db.Database)
	s.sessions.Current().Errors["app_db"] = "read denied"
	called := make(chan struct{}, 1)
	s.sessions.ObjectLister = func(_ context.Context, _ int64, name string) ([]mysqlsvc.MySQLDBObject, error) {
		if name == "app_db" {
			called <- struct{}{}
		}
		return []mysqlsvc.MySQLDBObject{{Name: "orders", Kind: "表"}}, nil
	}
	tt.Frame()
	if !tt.HasText("读取失败，点击重试") {
		t.Fatalf("missing retry feedback: %v", tt.Texts())
	}
	if err := tt.Click("读取失败，点击重试"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	select {
	case <-called:
	default:
		t.Fatal("clicking the error node did not issue a metadata retry")
	}
	if s.sessions.Current().Errors["app_db"] != "" || len(s.sessions.Current().Cache["app_db"]) != 1 {
		t.Fatalf("retry did not clear error and load objects: %v %v", s.sessions.Current().Errors, s.sessions.Current().Cache)
	}
}

func TestNavigatorUnsupportedCategoryKeepsWorkbenchContext(t *testing.T) {
	s, _ := mysqlObjectTestState(t)
	node := navigation.Node{Kind: "category", ProfileID: s.sessions.Current().ProfileID, Database: "app_db", Category: "查询"}
	s.selectNavigationNode(ui.Services{}, node.Key())
	if s.sessions.Current().Kind != "查询" || s.sessions.Current().Row != -1 || len(s.sessions.SelectedObjects()) != 0 || !strings.Contains(s.status, "尚未") {
		t.Fatalf("unsupported folder must not pretend to filter objects: kind=%q row=%d status=%q", s.sessions.Current().Kind, s.sessions.Current().Row, s.status)
	}
}
