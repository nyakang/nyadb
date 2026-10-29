package shell

import (
	"errors"
	"strings"
	"testing"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestTableChangeOldGenerationCannotTouchCurrentBrowser(t *testing.T) {
	s, tt := structureTestBrowser(t)
	original := len(s.mysqlObjectCache["app_db"])
	results := make(chan mysqlTableChangeResult, 1)
	s.mysqlTablePending = true
	s.mysqlTableDialog = true
	s.mysqlTableResults = results
	results <- mysqlTableChangeResult{
		profileID: s.mysqlSessionProfileID, generation: s.mysqlSessionGeneration + 1,
		database: "app_db", table: "orders", action: mysqlsvc.MySQLTableDrop,
	}
	tt.Frame()
	if len(s.mysqlObjectCache["app_db"]) != original || s.mysqlTableDialog ||
		!strings.Contains(s.status, "原会话已失效") {
		t.Fatalf("stale result changed browser: status=%q cache=%+v", s.status, s.mysqlObjectCache)
	}
}

func TestTableChangeFailedTwoStageCopyInvalidatesCorrectDatabase(t *testing.T) {
	s, tt := structureTestBrowser(t)
	s.mysqlObjectCache["analytics"] = []mysqlsvc.MySQLDBObject{{Name: "report", Kind: "表"}}
	ch := make(chan mysqlTableChangeResult, 1)
	s.mysqlTableResults = ch
	s.mysqlTablePending = true
	ch <- mysqlTableChangeResult{
		profileID: s.mysqlSessionProfileID, generation: s.mysqlSessionGeneration,
		database: "analytics", table: "report", newName: "copy",
		action: mysqlsvc.MySQLTableCopyData, err: errors.New("new table was created; INSERT failed"),
	}
	tt.Frame()
	if _, exists := s.mysqlObjectCache["analytics"]; exists {
		t.Fatal("partial copy left the database cache stale")
	}
	if s.mysqlObjectPending != "" || !strings.Contains(s.mysqlTableError, "INSERT failed") {
		t.Fatalf("unexpected cross-schema refresh/error: pending=%q err=%q", s.mysqlObjectPending, s.mysqlTableError)
	}
	if _, exists := s.mysqlObjectCache["app_db"]; !exists {
		t.Fatal("partial copy invalidated the active unrelated schema")
	}
}
