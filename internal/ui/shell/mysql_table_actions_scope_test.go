package shell

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
)

func TestTableChangeFailedTwoStageCopyInvalidatesCorrectDatabase(t *testing.T) {
	s, _ := structureTestBrowser(t)
	s.sessions.Current().Cache["analytics"] = []mysqlsvc.MySQLDBObject{{Name: "report", Kind: "表"}}
	s.mysqlTableChanged(ui.Services{}, objectworkspace.TableEvent{ProfileID: s.sessions.Current().ProfileID, Generation: s.sessions.Current().Generation, Database: "analytics", Table: "report", NewName: "copy", Action: mysqlsvc.MySQLTableCopyData, Error: errors.New("new table was created; INSERT failed")})
	if _, exists := s.sessions.Current().Cache["analytics"]; exists {
		t.Fatal("partial copy left stale destination cache")
	}
	if s.sessions.Current().Pending != "" {
		t.Fatal("partial copy refreshed unrelated database")
	}
	if _, exists := s.sessions.Current().Cache["app_db"]; !exists {
		t.Fatal("partial copy invalidated unrelated database")
	}
}
