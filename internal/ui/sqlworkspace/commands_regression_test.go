package sqlworkspace

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestSQLFormatIsUndoableAndExplainNeedsConfirmation(t *testing.T) {
	s := New(Dependencies{Connected: func(int64) bool { return true }})
	t.Cleanup(s.CloseAll)
	text := "select 'from; NULL', (select 1) from t where id=1"
	d := s.Open(2, text, 1, "db", false)
	calls := make(chan string, 1)
	s.Executor = func(_ context.Context, _ int64, _ string, sql string) (mysqlsvc.MySQLSQLResult, error) {
		calls <- sql
		return mysqlsvc.MySQLSQLResult{}, nil
	}
	tt := ui.NewTester(func(c *ui.Context) { s.Consume(); s.View(c, d); s.Modals(c) }, 1280, 800)
	tt.Frame()
	if e := tt.Click("格式化 SQL"); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(d.Editor.Buffer.String(), "\nFROM") || !strings.Contains(d.Editor.Buffer.String(), "'from; NULL'") {
		t.Fatal("format damaged literals or did not lay out clauses")
	}
	if !d.Editor.Buffer.Undo() || d.Editor.Buffer.String() != text {
		t.Fatal("format is not one undoable operation")
	}
	d.Editor.Select(0, 0)
	s.Explain(d)
	tt.Frame()
	select {
	case <-calls:
		t.Fatal("EXPLAIN executed without confirmation")
	default:
	}
	if s.persistence.explainSQL != "EXPLAIN "+text || strings.Contains(s.persistence.explainSQL, "ANALYZE") {
		t.Fatal("unexpected explain statement")
	}
	if e := tt.Click("执行 EXPLAIN"); e != nil {
		t.Fatal(e)
	}
	consumeUntil(t, s, func() bool { return !d.loading })
	if sql := <-calls; sql != "EXPLAIN "+text {
		t.Fatal(sql)
	}
}

func TestExplainRejectsMultipleSelectedStatements(t *testing.T) {
	s := New(Dependencies{Connected: func(int64) bool { return true }})
	t.Cleanup(s.CloseAll)
	d := s.Open(2, "SELECT 1; INSERT INTO t VALUES (1)", 1, "db", false)
	d.Editor.Select(0, d.Editor.Buffer.Len())
	s.Explain(d)
	if s.persistence.explainOpen || s.persistence.explainDoc != nil || d.err == "" || d.loading {
		t.Fatal("multi-statement selection opened or executed an EXPLAIN preview")
	}
}

func TestExplainUsesPortableCaretAndPreservesDollarLiteral(t *testing.T) {
	s := New(Dependencies{
		Connected:        func(int64) bool { return true },
		StatementAtCaret: func(_ int64, source string, cursor int) string { return sqltext.PortableStatementAt(source, cursor) },
		ValidateExplain:  func(_ int64, source string) error { return sqltext.SinglePortableStatement(source) },
	})
	t.Cleanup(s.CloseAll)
	d := s.Open(2, "SELECT $q$a;b$q$; DELETE FROM t", 1, "db", false)
	d.Editor.Select(10, 10)
	s.Explain(d)
	if !s.persistence.explainOpen || s.persistence.explainSQL != "EXPLAIN SELECT $q$a;b$q$" || d.loading {
		t.Fatalf("wrong portable preview %q, error %q", s.persistence.explainSQL, d.err)
	}
	d.Editor.Select(0, d.Editor.Buffer.Len())
	s.Explain(d)
	if s.persistence.explainOpen || s.persistence.explainSQL != "" {
		t.Fatal("a previous preview survived rejected input")
	}
}

func TestSQLResultSelectionAndJSONPreserveValueKinds(t *testing.T) {
	d := &Document{result: &contract.SQLResult{Sets: []contract.ResultSet{{Columns: []contract.Column{{Name: "text"}, {Name: "binary"}}, Rows: [][]contract.Cell{{{Kind: contract.Null}, {Kind: contract.Binary}}, {{Kind: contract.Text, Text: "NULL"}, {Kind: contract.Binary, Bytes: []byte{0, 255}}}}}}}}
	d.selection.Add(1)
	d.grid.Cells.Anchor = "sql_1"
	d.grid.Cells.Cursor = "sql_1"
	selected := resultData(d, true)
	if len(selected.Columns) != 1 || selected.Columns[0].Name != "binary" || len(selected.Rows) != 1 || selected.Rows[0][0].Display() != "0x00ff" {
		t.Fatalf("wrong selection %+v", selected)
	}
	b, e := EncodeResult(selectedResult(d), "JSON")
	if e != nil {
		t.Fatal(e)
	}
	var result struct{ Rows [][]contract.Cell }
	if e = json.Unmarshal(b, &result); e != nil {
		t.Fatal(e)
	}
	if result.Rows[0][0].Kind != contract.Null || result.Rows[1][0].Kind != contract.Text || result.Rows[0][1].Kind != contract.Binary {
		t.Fatal("JSON conflated values")
	}
}

func TestSQLDelimitedResultNullMarkerDoesNotCollideWithText(t *testing.T) {
	set := contract.ResultSet{Columns: []contract.Column{{Name: "v"}}, Rows: [][]contract.Cell{{{Kind: contract.Null}}, {{Kind: contract.Text, Text: "NULL"}}, {{Kind: contract.Text, Text: `\N`}}, {{Kind: contract.Text}}, {{Kind: contract.Binary}}}}
	b, e := EncodeResult(set, "TSV")
	if e != nil {
		t.Fatal(e)
	}
	if string(b) != "v\n\\N\nNULL\n\\\\N\n\n0x\n" {
		t.Fatalf("value identities merged: %q", b)
	}
}
