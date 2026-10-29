package shell

import (
	"testing"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestProgramDefinitionTabIsReadOnlyAndScopedToProfile(t *testing.T) {
	s := New()
	s.sessions.Current().ProfileID = 41
	s.sessions.Current().Status = "已连接"
	s.sessions.Current().Cache = map[string][]mysqlsvc.MySQLDBObject{
		"app": {{Name: "run_cleanup", Kind: "存储过程", Definition: "DELETE FROM logs"}},
	}
	s.openMySQLProgram("app", "run_cleanup", "存储过程")
	if len(s.tabs) != 2 || s.currentTab().kind != tabProgram || s.currentTab().programProfileID != 41 ||
		s.currentTab().program.Definition != "DELETE FROM logs" {
		t.Fatalf("program tab did not retain metadata and session identity: %+v", s.currentTab())
	}
	s.openMySQLProgram("app", "run_cleanup", "存储过程")
	if len(s.tabs) != 2 {
		t.Fatal("same program opened duplicate tabs")
	}
	s.clearMySQLTabsFor(41)
	if len(s.tabs) != 1 || s.currentTab().kind != tabObjects {
		t.Fatal("disconnect must close only the scoped program definition tabs")
	}
}
