package sqltext

import (
	"reflect"
	"strings"
	"testing"
)

func TestSQLServerBatchesPreserveVariablesBodiesAndQuotedGO(t *testing.T) {
	first := "DECLARE @n int=1; SELECT N'GO;\\', [odd]]name]; --GO\n/* outer /* GO */ nested */\nSELECT @n;"
	second := "CREATE PROCEDURE dbo.p AS BEGIN SELECT N'line\nGO\ntext'; END;"
	source := first + "\nGO -- split\n" + second + "\nGO\n"
	parts, err := SplitSQLServer(source)
	if err != nil || !reflect.DeepEqual(parts, []string{first, second}) {
		t.Fatal(parts, err)
	}
	parts, err = SplitSQLServer("SELECT 1\nGO /* multi\n line */ SELECT 2")
	if err != nil || !reflect.DeepEqual(parts, []string{"SELECT 1", "SELECT 2"}) {
		t.Fatal("GO multiline comment", parts, err)
	}
	if SQLServerStatementAt(source, len(first)+16) != second {
		t.Fatal("caret lost owning batch")
	}
	formatted, err := FormatSQLServer("select N'go;\\', [select]]from] --select\nGO\nselect @from;")
	if err != nil || !strings.Contains(formatted, "N'go;\\', [select]]from] --select\nGO\nSELECT @from;") {
		t.Fatal(formatted, err)
	}
	for _, bad := range []string{"SELECT 'open", "SELECT [open", "/* open", "SELECT 1\nGO 2", "SELECT 1\nGO 0"} {
		if _, err := SplitSQLServer(bad); err == nil {
			t.Fatal("invalid syntax accepted", bad)
		}
	}
}
func TestSQLServerExplainRejectsBatchReplayAndModeChanges(t *testing.T) {
	for _, source := range []string{"SELECT N';GO', [x] FROM dbo.t;", "WITH q AS (SELECT 1 AS x) SELECT x FROM q"} {
		if err := SingleSQLServerStatement(source); err != nil {
			t.Fatal(source, err)
		}
	}
	for _, source := range []string{"SELECT 1; DELETE dbo.t", "SELECT 1\nGO\nDELETE dbo.t", "SET SHOWPLAN_XML OFF", "EXEC dbo.p", "SELECT 1 SET SHOWPLAN_XML OFF"} {
		if SingleSQLServerStatement(source) == nil {
			t.Fatal("unsafe explain accepted", source)
		}
	}
}
func TestSQLServerCompletionQuotesBracketsAndOffersTSQL(t *testing.T) {
	cat := CompletionCatalog{Dialect: "tsql", Namespace: "dbo", Objects: []CompletionObject{{Namespace: "dbo", Name: "odd]table", Columns: []string{"from]value"}}}}
	result := CompleteSQL("SELECT * FROM dbo.", len("SELECT * FROM dbo."), cat)
	found := false
	for _, item := range result.Items {
		if item.Name == "odd]table" {
			found = true
			if item.Insert != "[odd]]table]" {
				t.Fatal(item)
			}
		}
	}
	if !found {
		t.Fatal("missing schema table", result)
	}
	result = CompleteSQL("FET", 3, cat)
	found = false
	for _, item := range result.Items {
		if item.Name == "FETCH" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing FETCH")
	}
}
