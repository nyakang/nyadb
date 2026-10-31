package mysql

import (
	"github.com/nyakang/nyadb/internal/db/contract"
	"strings"
	"testing"
)

func TestBackupLiteralsPreserveNullEmptyBinaryAndExactNumbers(t *testing.T) {
	for _, c := range []struct {
		cell contract.Cell
		want string
	}{{contract.Cell{Kind: contract.Null}, "NULL"}, {contract.Cell{Kind: contract.Text, Text: "NULL"}, "CONVERT(X'4e554c4c' USING utf8mb4)"}, {contract.Cell{Kind: contract.Text}, "CONVERT(X'' USING utf8mb4)"}, {contract.Cell{Kind: contract.Binary}, "X''"}, {contract.Cell{Kind: contract.Binary, Bytes: []byte{0, 255}}, "X'00ff'"}, {contract.Cell{Kind: contract.Decimal, Text: "9007199254740993.0001"}, "9007199254740993.0001"}} {
		got, e := dumpLiteral(c.cell)
		if e != nil || got != c.want {
			t.Fatalf("%+v => %q %v", c.cell, got, e)
		}
	}
	if _, e := dumpLiteral(contract.Cell{Kind: contract.Integer, Text: "1; DROP TABLE x"}); e == nil {
		t.Fatal("unsafe numeric literal accepted")
	}
}
func TestBackupRebindAndViewDependencyOrder(t *testing.T) {
	source := "CREATE VIEW `source`.`child` AS SELECT 'source.table' AS value FROM `source`.`parent`"
	got, e := RebindBackupDDL(source, "source", "target")
	if e != nil || !strings.Contains(got, "'source.table'") || !strings.Contains(got, "`target`.`parent`") {
		t.Fatal(got, e)
	}
	steps, e := orderRestoreViews([]RestoreStep{{Kind: "TABLE", Name: "base"}, {Kind: "VIEW", Name: "child", SQL: "CREATE VIEW child AS SELECT * FROM parent"}, {Kind: "VIEW", Name: "parent", SQL: "CREATE VIEW parent AS SELECT * FROM base"}})
	if e != nil || len(steps) != 3 || steps[1].Name != "parent" {
		t.Fatal(steps, e)
	}
	if _, e = orderRestoreViews([]RestoreStep{{Kind: "VIEW", Name: "a", SQL: "CREATE VIEW a AS SELECT * FROM b"}, {Kind: "VIEW", Name: "b", SQL: "CREATE VIEW b AS SELECT * FROM a"}}); e == nil {
		t.Fatal("view cycle accepted")
	}
}
func TestStreamImporterAcceptsLargeRecordWithoutRaisingEditorLimit(t *testing.T) {
	columns := []MySQLStructureColumn{{Name: "body", DataType: "longtext", Type: "longtext", Nullable: true}}
	values := map[string]MySQLDataValue{"body": {Text: strings.Repeat("x", MySQLDataEditMaxBytes+1)}}
	if _, _, _, e := mysqlEditAssignments(columns, values, true); e == nil {
		t.Fatal("editor limit changed")
	}
	if e := mysqlDataImportValidateRow(columns, MySQLDataEdit{Kind: "insert", Values: values}, true); e != nil {
		t.Fatal(e)
	}
}
