package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestActiveTabNavigationRestoresPriorFiltersOnRequest(t *testing.T) {
	s, tt := mysqlObjectTestState(t)
	id := s.sessions.Current().ProfileID
	s.sessions.Current().Cache["app_db"] = []mysqlsvc.MySQLDBObject{{Name: "orders", Kind: "表"}}
	s.tabs = append(s.tabs, &tab{kind: tabMySQLData, title: "orders", data: newDataDocument(s, id, "app_db", "orders")})
	s.activeTab = len(s.tabs) - 1
	s.navigation.Filter.Search = "hidden"
	s.navigation.Filter.Engine = connections.SQLite
	s.navigation.Filter.StarredOnly = true
	s.navigateToActiveTab(ui.Services{})
	if !s.navigation.CanRestoreFilter() || s.navigation.Filter.Search != "" || s.navigation.Filter.Engine != "" || s.navigation.Filter.StarredOnly {
		t.Fatalf("active navigation did not snapshot temporary filter: %+v", s.navigation.CanRestoreFilter())
	}
	tt.Frame()
	if err := tt.Click("恢复之前的筛选"); err != nil {
		t.Fatal(err)
	}
	if s.navigation.CanRestoreFilter() || s.navigation.Filter.Search != "hidden" || s.navigation.Filter.Engine != connections.SQLite || !s.navigation.Filter.StarredOnly {
		t.Fatalf("filter was not restored: search=%q type=%s starred=%v", s.navigation.Filter.Search, s.navigation.Filter.Engine, s.navigation.Filter.StarredOnly)
	}
	s.navigateToActiveTab(ui.Services{})
	if !s.navigation.CanRestoreFilter() {
		t.Fatal("second active navigation failed to capture filters")
	}
	s.navigation.Filter.Search = "new manual search"
	s.navigation.ChangeFilter()
	if s.navigation.CanRestoreFilter() {
		t.Fatal("manual search must supersede a temporary filter restore")
	}
}
