package dataworkspace

import (
	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"testing"
)

func TestImportFromEmptyDatabaseHasNoFabricatedPageAndReviewsNewTarget(t *testing.T) {
	session := Session{ProfileID: 8, Generation: 4, Connected: true}
	s := New(Dependencies{Session: func() Session { return session }})
	defer s.CloseAll()
	d := s.OpenImport(2, 8, "empty_db")
	if d == nil || d.page != nil || d.table != "" || d.transfer.Target != "new" {
		t.Fatal("empty database import fabricated table metadata")
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, 900, 650)
	if !tt.HasText("导入向导") {
		t.Fatal("pageless import wizard did not render")
	}
	d.transfer.Input = &mysqlDataImportInput{Fields: []string{"id", "text"}, Rows: [][]mysqlsvc.MySQLDataValue{{{Text: "1"}, {Text: "NULL"}}}}
	d.transfer.NewColumns = mysqlDataInferImportColumns(d.transfer.Input)
	d.transfer.NewName = "actual_new"
	d.transfer.Confirmation = "actual_new"
	d.transfer.Mapping = []string{"id", "text"}
	columns, edits, e := mysqlDataImportRequest(d)
	if e != nil || len(columns) != 2 || edits[0].Values["text"].Text != "NULL" {
		t.Fatal(columns, edits, e)
	}
	s.Close(d)
	if len(s.documents) != 0 {
		t.Fatal("import-only document leaked")
	}
}
func TestHostDataActionDoesNotOpenWizardAfterGenerationChange(t *testing.T) {
	s, d, session := transferTestDocument()
	defer s.CloseAll()
	d.transfer.Open = false
	d.loading = true
	s.RequestAction(ui.Services{}, d, "export")
	if d.hostAction != "export" {
		t.Fatal("pending export owner lost")
	}
	session.Generation++
	d.loading = false
	s.consumeHostActions()
	if d.transfer.Open || d.hostAction != "" {
		t.Fatal("stale host action opened wizard")
	}
}
