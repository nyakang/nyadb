package shell

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestNavigationResolvesActiveTabOnlyInMatchingSessionAndSnapshot(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	p, err := service.Create(context.Background(), connections.Profile{Name: "Local", Kind: connections.MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.sessions.Current().ProfileID = p.ID
	s.sessions.Current().Status = "已连接"
	s.sessions.Current().Database = "app"
	s.sessions.Current().Cache = map[string][]mysqlsvc.MySQLDBObject{
		"app": {{Name: "customers", Kind: "表"}, {Name: "invoices", Kind: "表"}},
	}
	s.tabs = append(s.tabs, &tab{kind: tabMySQLData, dataProfileID: p.ID, dataDatabase: "app", dataTable: "invoices"})
	s.activeTab = 1
	id, db, table, ok := s.activeTabObject()
	if !ok || id != p.ID || db != "app" || table != "invoices" {
		t.Fatalf("bad active tab: %d %q %q %t", id, db, table, ok)
	}
	s.sessions.Current().NavigateProfileID = id
	s.sessions.Current().NavigateDatabase = db
	s.sessions.Current().NavigateTable = table
	s.sessions.Current().Row = -1
	s.resolveNavigatorObject()
	if s.sessions.Current().Row != 1 || s.sessions.Current().NavigateDatabase != "" {
		t.Fatalf("failed to resolve target table: row=%d nav=%q", s.sessions.Current().Row, s.sessions.Current().NavigateDatabase)
	}
	// A stale result from a different session must never select a row.
	s.sessions.Current().Row = -1
	s.sessions.Current().NavigateProfileID = p.ID + 1
	s.sessions.Current().NavigateDatabase = "app"
	s.sessions.Current().NavigateTable = "customers"
	s.resolveNavigatorObject()
	if s.sessions.Current().Row != -1 {
		t.Fatal("stale target selected across sessions")
	}
}
