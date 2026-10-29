package shell

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func waitMultiProfileSession(t *testing.T, s *State, tt *ui.Tester) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for s.sessions.Current().Status == "连接中" && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond * 5)
	}
	if s.sessions.Current().Status != "已连接" {
		t.Fatalf("mock session did not connect: state=%s error=%s", s.sessions.Current().Status, s.sessions.Current().Error)
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
	s.sessions.Opener = func(context.Context, connections.ActiveSettings, string) error { return nil }
	s.sessions.DatabaseLister = func(_ context.Context, id int64) ([]string, error) {
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
	s.sessions.Current().Cache["shared"] = []mysqlsvc.MySQLDBObject{{Name: "a_orders", Kind: "表"}}
	s.sessions.MarkOpened(a.ID, "shared")
	s.sessions.Current().Database = "shared"
	s.sessions.Current().Row = 0

	s.selectProfileByName(b.Name)
	if s.sessions.Current().ProfileID != 0 || s.sessions.StatusFor(a.ID) != "已连接" {
		t.Fatalf("switch to unopened B disconnected A: active=%d statuses A=%s B=%s",
			s.sessions.Current().ProfileID, s.sessions.StatusFor(a.ID), s.sessions.StatusFor(b.ID))
	}
	tt.Frame()
	if err := tt.Click("连接所选 MySQL"); err != nil {
		t.Fatal(err)
	}
	waitMultiProfileSession(t, s, tt)
	s.sessions.Current().Cache["shared"] = []mysqlsvc.MySQLDBObject{{Name: "b_orders", Kind: "表"}}
	if s.sessions.DatabaseOpened(b.ID, "shared") || !s.sessions.DatabaseOpened(a.ID, "shared") ||
		s.navigation.Children(navigation.DatabaseNode(b.ID, "shared").Key()) != nil {
		t.Fatal("same-named database opening state leaked across profiles")
	}
	s.sessions.Current().Database = "shared"
	s.sessions.Current().Row = 0

	if s.sessions.StatusFor(a.ID) != "已连接" || s.sessions.StatusFor(b.ID) != "已连接" {
		t.Fatal("connecting B lost A or vice versa")
	}
	s.selectProfileByName(a.Name)
	if !s.sessions.DatabaseOpened(a.ID, "shared") || s.sessions.DatabaseOpened(b.ID, "shared") {
		t.Fatal("restoring a workspace lost its independent database opening state")
	}
	if s.sessions.Current().ProfileID != a.ID || s.sessions.Current().Database != "shared" ||
		len(s.sessions.Current().Cache["shared"]) != 1 || s.sessions.Current().Cache["shared"][0].Name != "a_orders" {
		t.Fatalf("A browser state was replaced by B: active=%d cache=%+v", s.sessions.Current().ProfileID, s.sessions.Current().Cache)
	}
	if got := s.navigation.Children(navigation.ConnectionKey(b.ID)); len(got) != 2 {
		t.Fatalf("parked B lost its database tree: %v", got)
	}
	s.selectProfileByName(b.Name)
	if s.sessions.Current().ProfileID != b.ID || s.sessions.Current().Cache["shared"][0].Name != "b_orders" {
		t.Fatalf("B browser state was replaced by A: active=%d cache=%+v", s.sessions.Current().ProfileID, s.sessions.Current().Cache)
	}
	// Clicking an error in an expanded parked connection must retry that
	// connection, even when its database has the same name as the active one.
	parked := s.sessions.Parked(a.ID)
	delete(parked.Cache, "shared")
	parked.Errors["shared"] = "permission denied"
	s.navigation.Tree.Open.Add(navigation.ConnectionKey(a.ID))
	s.navigation.Tree.Open.Add(navigation.DatabaseNode(a.ID, "shared").Key())
	requests := make(chan int64, 1)
	s.sessions.ObjectLister = func(_ context.Context, id int64, _ string) ([]mysqlsvc.MySQLDBObject, error) {
		requests <- id
		return []mysqlsvc.MySQLDBObject{{Name: "a_retry", Kind: "表"}}, nil
	}
	tt.Frame()
	if err := tt.Click("读取失败，点击重试"); err != nil {
		t.Fatal(err)
	}
	waitMySQLObjects(t, s, tt)
	if requested := <-requests; requested != a.ID || s.sessions.Current().ProfileID != a.ID ||
		s.sessions.CacheFor(b.ID)["shared"][0].Name != "b_orders" {
		t.Fatal("parked retry used or modified the wrong profile")
	}
	s.selectProfileByName(b.Name)
	// Explicitly closing parked A must not close B.
	s.disconnectMySQLProfile(a.ID)
	if s.sessions.StatusFor(a.ID) != "未连接" || s.sessions.StatusFor(b.ID) != "已连接" {
		t.Fatalf("disconnecting parked A damaged B: A=%s B=%s", s.sessions.StatusFor(a.ID), s.sessions.StatusFor(b.ID))
	}
	s.closeAllMySQLConnections()
	if s.sessions.StatusFor(b.ID) != "未连接" || s.sessions.HasSessions() {
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
	s.sessions.Current().ProfileID, s.sessions.Current().Status, s.selectedProfileID = a.ID, "已连接", a.ID
	s.sessions.Current().Databases = []string{"shared", "only_a"}
	s.sessions.Current().Cache = map[string][]mysqlsvc.MySQLDBObject{"shared": {{Name: "one", Kind: "表"}}}
	s.tabs = append(s.tabs, &tab{kind: tabMySQLData, title: "A data", dataProfileID: a.ID, dataDatabase: "shared", dataTable: "one"})
	s.tabs = append(s.tabs, &tab{kind: tabStructure, title: "B structure", structureProfileID: b.ID, structureDatabase: "shared", structureTable: "two"})
	s.sessions.Activate(b.ID)
	s.sessions.Current().ProfileID, s.sessions.Current().Status, s.selectedProfileID = b.ID, "已连接", b.ID
	s.sessions.Current().Databases = []string{"shared", "only_b"}
	s.sessions.Current().Cache["shared"] = []mysqlsvc.MySQLDBObject{{Name: "two", Kind: "表"}}
	s.pruneMySQLStructureTabs(map[string]bool{"only_b": true})
	if len(s.tabs) != 2 || s.tabs[1].dataProfileID != a.ID {
		t.Fatalf("B refresh incorrectly deleted A tabs: %+v", s.tabs)
	}
	s.sessions.Activate(a.ID)
	if s.sessions.Current().Cache["shared"][0].Name != "one" {
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
	s.sessions.Opener = func(ctx context.Context, settings connections.ActiveSettings, _ string) error {
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
	s.sessions.DatabaseLister = func(_ context.Context, _ int64) ([]string, error) { return []string{"shared"}, nil }
	s.selectProfileByName(a.Name)
	tt.Frame()
	if err := tt.Click("连接所选 MySQL"); err != nil {
		t.Fatal(err)
	}
	if s.sessions.StatusFor(a.ID) != "连接中" {
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
	for s.sessions.StatusFor(a.ID) == "连接中" && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond * 5)
	}
	if s.sessions.StatusFor(a.ID) != "已连接" || s.sessions.Current().ProfileID != b.ID {
		t.Fatalf("parked handshake was not processed independently: A=%s current=%d", s.sessions.StatusFor(a.ID), s.sessions.Current().ProfileID)
	}
	s.selectProfileByName(a.Name)
	waitMultiProfileSession(t, s, tt)
	if s.sessions.StatusFor(b.ID) != "已连接" {
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
	s.sessions.Current().ProfileID, s.sessions.Current().Status = a.ID, "已连接"
	s.sessions.Current().Databases = []string{"app_a"}
	s.tabs = append(s.tabs, &tab{kind: tabStructure, title: "app_a.orders", structureProfileID: a.ID, structureDatabase: "app_a", structureTable: "orders"})
	s.selectProfileByName(b.Name)
	if s.sessions.StatusFor(a.ID) != "已连接" || s.activeTab != 0 {
		t.Fatal("selecting B failed to park A and return to Objects tab")
	}
	s.sessions.Current().ProfileID, s.sessions.Current().Status = b.ID, "已连接"
	s.sessions.Current().Databases = []string{"app_b"}
	s.activeTab = 1 // clicking the previously opened A structure tab
	tt.Frame()
	if s.sessions.Current().ProfileID != a.ID || s.sessions.StatusFor(b.ID) != "已连接" || s.selectedProfileID != a.ID {
		t.Fatalf("tab activation failed to restore A: active=%d selected=%d statusB=%s",
			s.sessions.Current().ProfileID, s.selectedProfileID, s.sessions.StatusFor(b.ID))
	}
	s.closeAllMySQLConnections()
}
