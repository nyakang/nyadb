package shell

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func waitMultiProfileSession(t *testing.T, s *State, tt *ui.Tester) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for s.mysqlSessionStatus == "连接中" && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond * 5)
	}
	if s.mysqlSessionStatus != "已连接" {
		t.Fatalf("mock session did not connect: state=%s error=%s", s.mysqlSessionStatus, s.mysqlSessionError)
	}
}

func TestMySQLMultiProfileBrowserSessionsDoNotDisconnectOrMixCaches(t *testing.T) {
	s, tt, svc, _ := profileTestState(t)
	ctx := context.Background()
	a, err := svc.Create(ctx, connections.Profile{Name: "Server A", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.Create(ctx, connections.Profile{Name: "Server B", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.mysqlSessionOpener = func(context.Context, connections.ActiveSettings, string) error { return nil }
	s.mysqlDatabaseLister = func(_ context.Context, id int64) ([]string, error) {
		if id == a.ID {
			return []string{"shared", "only_a"}, nil
		}
		return []string{"shared", "only_b"}, nil
	}
	s.selectProfileByName(a.Name)
	tt.Frame()
	if err := tt.Click("连接所选 MySQL"); err != nil {
		t.Fatal(err)
	}
	waitMultiProfileSession(t, s, tt)
	s.mysqlObjectCache["shared"] = []mysqlsvc.MySQLDBObject{{Name: "a_orders", Kind: "表"}}
	s.markMySQLDatabaseOpened(a.ID, "shared")
	s.mysqlSelectedDatabase = "shared"
	s.objectRow = 0

	s.selectProfileByName(b.Name)
	if s.mysqlSessionProfileID != 0 || s.mysqlSessionStateFor(a.ID) != "已连接" {
		t.Fatalf("switch to unopened B disconnected A: active=%d statuses A=%s B=%s",
			s.mysqlSessionProfileID, s.mysqlSessionStateFor(a.ID), s.mysqlSessionStateFor(b.ID))
	}
	tt.Frame()
	if err := tt.Click("连接所选 MySQL"); err != nil {
		t.Fatal(err)
	}
	waitMultiProfileSession(t, s, tt)
	s.mysqlObjectCache["shared"] = []mysqlsvc.MySQLDBObject{{Name: "b_orders", Kind: "表"}}
	if s.mysqlDatabaseOpenedFor(b.ID, "shared") || !s.mysqlDatabaseOpenedFor(a.ID, "shared") ||
		s.navigationChildren(databaseNode(b.ID, "shared").key()) != nil {
		t.Fatal("same-named database opening state leaked across profiles")
	}
	s.mysqlSelectedDatabase = "shared"
	s.objectRow = 0

	if s.mysqlSessionStateFor(a.ID) != "已连接" || s.mysqlSessionStateFor(b.ID) != "已连接" {
		t.Fatal("connecting B lost A or vice versa")
	}
	s.selectProfileByName(a.Name)
	if !s.mysqlDatabaseOpenedFor(a.ID, "shared") || s.mysqlDatabaseOpenedFor(b.ID, "shared") {
		t.Fatal("restoring a workspace lost its independent database opening state")
	}
	if s.mysqlSessionProfileID != a.ID || s.mysqlSelectedDatabase != "shared" ||
		len(s.mysqlObjectCache["shared"]) != 1 || s.mysqlObjectCache["shared"][0].Name != "a_orders" {
		t.Fatalf("A browser state was replaced by B: active=%d cache=%+v", s.mysqlSessionProfileID, s.mysqlObjectCache)
	}
	if got := s.navigationChildren(profileNode(b.ID)); len(got) != 2 {
		t.Fatalf("parked B lost its database tree: %v", got)
	}
	s.selectProfileByName(b.Name)
	if s.mysqlSessionProfileID != b.ID || s.mysqlObjectCache["shared"][0].Name != "b_orders" {
		t.Fatalf("B browser state was replaced by A: active=%d cache=%+v", s.mysqlSessionProfileID, s.mysqlObjectCache)
	}
	// Clicking an error in an expanded parked connection must retry that
	// connection, even when its database has the same name as the active one.
	parked := s.parkedMySQLWorkspace(a.ID)
	delete(parked.objectCache, "shared")
	parked.objectErrors["shared"] = "permission denied"
	s.explorerTree.Open.Add(profileNode(a.ID))
	s.explorerTree.Open.Add(databaseNode(a.ID, "shared").key())
	requests := make(chan int64, 1)
	s.mysqlObjectLister = func(_ context.Context, id int64, _ string) ([]mysqlsvc.MySQLDBObject, error) {
		requests <- id
		return []mysqlsvc.MySQLDBObject{{Name: "a_retry", Kind: "表"}}, nil
	}
	tt.Frame()
	if err := tt.Click("读取失败，点击重试"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	if requested := <-requests; requested != a.ID || s.mysqlSessionProfileID != a.ID ||
		s.mysqlObjectCacheFor(b.ID)["shared"][0].Name != "b_orders" {
		t.Fatal("parked retry used or modified the wrong profile")
	}
	s.selectProfileByName(b.Name)
	// Explicitly closing parked A must not close B.
	s.disconnectMySQLProfile(a.ID)
	if s.mysqlSessionStateFor(a.ID) != "未连接" || s.mysqlSessionStateFor(b.ID) != "已连接" {
		t.Fatalf("disconnecting parked A damaged B: A=%s B=%s", s.mysqlSessionStateFor(a.ID), s.mysqlSessionStateFor(b.ID))
	}
	s.closeAllMySQLConnections()
	if s.mysqlSessionStateFor(b.ID) != "未连接" || len(s.mysqlWorkspaces) != 0 {
		t.Fatal("close-all leaked parked or active session")
	}
}

func TestMySQLMultiProfileTabsAndMetadataRefreshAreScoped(t *testing.T) {
	s, _, svc, _ := profileTestState(t)
	ctx := context.Background()
	a, err := svc.Create(ctx, connections.Profile{Name: "A", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.Create(ctx, connections.Profile{Name: "B", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.mysqlSessionProfileID, s.mysqlSessionStatus, s.selectedProfileID = a.ID, "已连接", a.ID
	s.mysqlSessionDatabases = []string{"shared", "only_a"}
	s.mysqlObjectCache = map[string][]mysqlsvc.MySQLDBObject{"shared": {{Name: "one", Kind: "表"}}}
	s.tabs = append(s.tabs, &tab{kind: tabMySQLData, title: "A data", dataProfileID: a.ID, dataDatabase: "shared", dataTable: "one"})
	s.tabs = append(s.tabs, &tab{kind: tabStructure, title: "B structure", structureProfileID: b.ID, structureDatabase: "shared", structureTable: "two"})
	s.activateMySQLProfile(b.ID)
	s.mysqlSessionProfileID, s.mysqlSessionStatus, s.selectedProfileID = b.ID, "已连接", b.ID
	s.mysqlSessionDatabases = []string{"shared", "only_b"}
	s.mysqlObjectCache["shared"] = []mysqlsvc.MySQLDBObject{{Name: "two", Kind: "表"}}
	s.pruneMySQLStructureTabs(map[string]bool{"only_b": true})
	if len(s.tabs) != 2 || s.tabs[1].dataProfileID != a.ID {
		t.Fatalf("B refresh incorrectly deleted A tabs: %+v", s.tabs)
	}
	s.activateMySQLProfile(a.ID)
	if s.mysqlObjectCache["shared"][0].Name != "one" {
		t.Fatal("A metadata cache changed with B refresh")
	}
	s.clearMySQLTabsFor(a.ID)
	for _, tab := range s.tabs {
		if tab.kind == tabMySQLData && tab.dataProfileID == a.ID {
			t.Fatal("closing A retained its data tab")
		}
	}
}

func TestMySQLMultiProfilePendingResultsSurviveNavigation(t *testing.T) {
	s, tt, svc, _ := profileTestState(t)
	ctx := context.Background()
	a, err := svc.Create(ctx, connections.Profile{Name: "Slow A", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.Create(ctx, connections.Profile{Name: "Other B", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	release := make(chan struct{})
	s.mysqlSessionOpener = func(ctx context.Context, settings connections.ActiveSettings, _ string) error {
		if settings.ProfileID == a.ID {
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	s.mysqlDatabaseLister = func(_ context.Context, _ int64) ([]string, error) { return []string{"shared"}, nil }
	s.selectProfileByName(a.Name)
	tt.Frame()
	if err := tt.Click("连接所选 MySQL"); err != nil {
		t.Fatal(err)
	}
	if s.mysqlSessionStateFor(a.ID) != "连接中" {
		t.Fatal("A should be connecting")
	}
	s.selectProfileByName(b.Name)
	tt.Frame()
	if err := tt.Click("连接所选 MySQL"); err != nil {
		t.Fatal(err)
	}
	waitMultiProfileSession(t, s, tt)
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for s.mysqlSessionStateFor(a.ID) == "连接中" && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond * 5)
	}
	if s.mysqlSessionStateFor(a.ID) != "已连接" || s.mysqlSessionProfileID != b.ID {
		t.Fatalf("parked handshake was not processed independently: A=%s current=%d", s.mysqlSessionStateFor(a.ID), s.mysqlSessionProfileID)
	}
	s.selectProfileByName(a.Name)
	waitMultiProfileSession(t, s, tt)
	if s.mysqlSessionStateFor(b.ID) != "已连接" {
		t.Fatal("A completion discarded B's connection")
	}
	s.closeAllMySQLConnections()
}

func TestMySQLMultiProfileOpeningOldTabReactivatesItsSession(t *testing.T) {
	s, tt, svc, _ := profileTestState(t)
	ctx := context.Background()
	a, err := svc.Create(ctx, connections.Profile{Name: "Tab A", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.Create(ctx, connections.Profile{Name: "Tab B", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(a.Name)
	s.mysqlSessionProfileID, s.mysqlSessionStatus = a.ID, "已连接"
	s.mysqlSessionDatabases = []string{"app_a"}
	s.tabs = append(s.tabs, &tab{kind: tabStructure, title: "app_a.orders", structureProfileID: a.ID, structureDatabase: "app_a", structureTable: "orders"})
	s.selectProfileByName(b.Name)
	if s.mysqlSessionStateFor(a.ID) != "已连接" || s.activeTab != 0 {
		t.Fatal("selecting B failed to park A and return to Objects tab")
	}
	s.mysqlSessionProfileID, s.mysqlSessionStatus = b.ID, "已连接"
	s.mysqlSessionDatabases = []string{"app_b"}
	s.activeTab = 1 // clicking the previously opened A structure tab
	tt.Frame()
	if s.mysqlSessionProfileID != a.ID || s.mysqlSessionStateFor(b.ID) != "已连接" || s.selectedProfileID != a.ID {
		t.Fatalf("tab activation failed to restore A: active=%d selected=%d statusB=%s",
			s.mysqlSessionProfileID, s.selectedProfileID, s.mysqlSessionStateFor(b.ID))
	}
	s.closeAllMySQLConnections()
}
