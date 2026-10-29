package shell

import (
	"context"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestNavigatorCategoriesAndMetadataProgress(t *testing.T) {
	s, _ := mysqlObjectTestState(t)
	db := databaseNode(s.mysqlSessionProfileID, "app_db")
	delete(s.mysqlObjectCache, "app_db")
	delete(s.mysqlObjectErrors, "app_db")

	checkStatus := func(want string) {
		t.Helper()
		children := s.navigationChildren(db.key())
		if len(children) != 1 {
			t.Fatalf("expected one status row, got %v", children)
		}
		n := parseNavigationNode(children[0])
		if n.Kind != "status" || n.Object != want || n.Database != "app_db" {
			t.Fatalf("unexpected loading node: %+v", n)
		}
	}
	checkStatus("waiting")
	s.mysqlObjectPending = "app_db"
	checkStatus("loading")
	s.mysqlObjectPending = ""
	s.mysqlObjectErrors["app_db"] = "permission denied"
	checkStatus("error")

	s.mysqlObjectCache["app_db"] = []mysqlsvc.MySQLDBObject{
		{Name: "customers", Kind: "表"},
		{Name: "customers", Kind: "视图"},
		{Name: "get_customer", Kind: "函数"},
		{Name: "refresh_customer", Kind: "存储过程"},
		{Name: "nightly_cleanup", Kind: "事件"},
	}
	children := s.navigationChildren(db.key())
	if len(children) != len(mysqlNavigationCategories) {
		t.Fatalf("incomplete database categories: got %d", len(children))
	}
	for i, key := range children {
		n := parseNavigationNode(key)
		if n.Kind != "category" || n.Category != mysqlNavigationCategories[i] {
			t.Fatalf("incorrect category order: %+v", n)
		}
		items := s.navigationChildren(key)
		if supportedMySQLCategory(n.Category) {
			if len(items) != 1 || parseNavigationNode(items[0]).Category != n.Category ||
				parseNavigationNode(items[0]).Kind != "object" {
				t.Fatalf("supported category %q lost real objects: %v", n.Category, items)
			}
		} else if len(items) != 0 {
			t.Fatalf("unsupported category %q fabricated objects: %v", n.Category, items)
		}
	}
	unsupported := parseNavigationNode(children[5])
	s.selectNavigationNode(ui.Services{}, unsupported.key())
	if s.mysqlObjectKind != "查询" || s.objectRow != -1 || len(s.mysqlSelectedObjects()) != 0 || !strings.Contains(s.status, "尚未") {
		t.Fatalf("unsupported folder must not pretend to filter objects: kind=%q row=%d status=%q",
			s.mysqlObjectKind, s.objectRow, s.status)
	}
}

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
	s.mysqlSessionProfileID = profile.ID
	s.mysqlSessionStatus = "已连接"
	s.setProfileActiveVariant(profile.ID, "Lab")
	stored, err = svc.Get(ctx, profile.ID)
	if err != nil || stored.ActiveVariant != "Staging" {
		t.Fatalf("active session configuration changed: %+v %v", stored, err)
	}
	s.mysqlSessionProfileID = 0
	s.mysqlSessionStatus = "未连接"
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
		s.mysqlSessionProfileID, s.mysqlSessionStatus = p.ID, state
		tt.Frame()
		if _, ok := tt.Find("Connection · " + state); !ok {
			t.Fatalf("connection status %q missing accessible row label", state)
		}
	}
}

func TestNavigatorFailedMetadataRowCanRetry(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	db := databaseNode(s.mysqlSessionProfileID, "app_db")
	s.explorerTree.Open.Add(profileNode(s.mysqlSessionProfileID))
	s.explorerTree.Open.Add(db.key())
	s.mysqlObjectErrors["app_db"] = "read denied"
	called := make(chan struct{}, 1)
	s.mysqlObjectLister = func(_ context.Context, _ int64, name string) ([]mysqlsvc.MySQLDBObject, error) {
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
	if s.mysqlObjectErrors["app_db"] != "" || len(s.mysqlObjectCache["app_db"]) != 1 {
		t.Fatalf("retry did not clear error and load objects: %v %v", s.mysqlObjectErrors, s.mysqlObjectCache)
	}
}
