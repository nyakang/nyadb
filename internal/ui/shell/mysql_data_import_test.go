package shell

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/dataworkspace"
)

func TestMySQLImportedTableCompletionValidatesGenerationAndKeepsSourceDocument(t *testing.T) {
	s, tt := structureTestBrowser(t)
	s.dataWorkspace.Loader = func(_ context.Context, _ int64, database, table string, _ int64, limit int) (*mysqlsvc.MySQLDataPage, error) {
		return &mysqlsvc.MySQLDataPage{Database: database, Table: table, Limit: limit, Columns: []string{"id"}}, nil
	}
	s.openMySQLData(ui.Services{}, "app_db", "orders")
	waitMySQLData(t, s, tt)
	source := s.currentTab().data
	w := s.sessions.Current()
	event := dataworkspace.TableImportEvent{ProfileID: w.ProfileID, Generation: w.Generation, Database: "app_db", Table: "imported", Created: true, Completed: true}
	deps := s.dataWorkspaceDependencies()
	before := len(s.tabs)
	for _, invalid := range []dataworkspace.TableImportEvent{
		{ProfileID: event.ProfileID + 1, Generation: event.Generation, Database: event.Database, Table: event.Table, Created: true},
		{ProfileID: event.ProfileID, Generation: event.Generation + 1, Database: event.Database, Table: event.Table, Created: true},
		{ProfileID: event.ProfileID, Generation: event.Generation, Database: event.Database, Table: event.Table, Created: true, Uncertain: true},
		{ProfileID: event.ProfileID, Generation: event.Generation, Database: event.Database, Table: event.Table},
	} {
		deps.OpenImportedTable(ui.Services{}, invalid)
		if len(s.tabs) != before || s.currentTab().data != source {
			t.Fatal("invalid completion opened a table", invalid)
		}
	}
	request := w.ObjectRequest()
	stale := event
	stale.Generation++
	deps.TableImported(ui.Services{}, stale)
	if w.ObjectRequest() != request || w.Cache[event.Database] == nil {
		t.Fatal("stale completion invalidated current metadata")
	}
	s.sessions.ObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) {
		return []mysqlsvc.MySQLDBObject{{Name: "orders", Kind: "表", Engine: "InnoDB"}, {Name: "imported", Kind: "表", Engine: "InnoDB"}}, nil
	}
	deps.TableImported(ui.Services{}, event)
	waitMySQLObjects(t, s, tt)
	if w.ObjectRequest() <= request || len(w.Cache[event.Database]) != 2 || source.Table() != "orders" {
		t.Fatal("new table did not refresh qualified metadata")
	}
	deps.OpenImportedTable(ui.Services{}, event)
	waitMySQLData(t, s, tt)
	if len(s.tabs) != before+1 || s.currentTab().data.Table() != "imported" || s.tabs[before-1].data != source || source.Table() != "orders" {
		t.Fatal("new target replaced source document")
	}
	deps.OpenImportedTable(ui.Services{}, event)
	if len(s.tabs) != before+1 {
		t.Fatal("opening imported table duplicated its document")
	}
}
