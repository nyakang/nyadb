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
	s.tabs = append(s.tabs, &tab{kind: tabMySQLData, data: newDataDocument(s, p.ID, "app", "invoices")})
	s.activeTab = 1
	id, db, table, ok := s.activeTabObject()
	if !ok || id != p.ID || db != "app" || table != "invoices" {
		t.Fatalf("bad active tab: %d %q %q %t", id, db, table, ok)
	}
	s.navigateProfileID = id
	s.navigateDatabase = db
	s.navigateTable = table
	s.objectWorkspace.SelectRow(s.sessions.Current().ProfileID, -1)
	s.resolveNavigatorObject()
	if s.objectWorkspace.SelectedRow(s.sessions.Current().ProfileID) != 1 || s.navigateDatabase != "" {
		t.Fatalf("failed to resolve target table: row=%d nav=%q", s.objectWorkspace.SelectedRow(s.sessions.Current().ProfileID), s.navigateDatabase)
	}
	// A stale result from a different session must never select a row.
	s.objectWorkspace.SelectRow(s.sessions.Current().ProfileID, -1)
	s.navigateProfileID = p.ID + 1
	s.navigateDatabase = "app"
	s.navigateTable = "customers"
	s.resolveNavigatorObject()
	if s.objectWorkspace.SelectedRow(s.sessions.Current().ProfileID) != -1 {
		t.Fatal("stale target selected across sessions")
	}
}
