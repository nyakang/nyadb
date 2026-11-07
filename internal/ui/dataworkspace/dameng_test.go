package dataworkspace

import (
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestDamengDocumentRequiresQualifiedTypedSQLAndRetainsNativeImportModes(t *testing.T) {
	s := New(Dependencies{})
	d := s.OpenObject(1, contract.ObjectRef{ProfileID: 7, Catalog: "DAMENG", Schema: "Native", Kind: "table", Name: "Records"}, contract.Capabilities{Query: true, Export: true, Import: true})
	d.importDialect = "dameng"
	if _, err := mysqlDataSQLSelection(d, false); !errors.Is(err, contract.ErrDataPage) {
		t.Fatal("unloaded native document generated SQL", err)
	}
	d.page = &contract.DataPage{Object: d.object, Database: d.object.Schema, Table: d.object.Name, Columns: []string{"ID", "T"}, ColumnMetadata: []contract.StructureColumn{{Name: "ID", DataType: "BIGINT", ValueKind: contract.Integer, Key: "PRI"}, {Name: "T", DataType: "VARCHAR", ValueKind: contract.Text}}, Rows: [][]contract.DataCell{{{Value: &contract.Cell{Kind: contract.Integer, Text: "9007199254740993"}}, {Value: &contract.Cell{Kind: contract.Text, Text: "中文 '"}}}}}
	d.selectedRow = 0
	statement, err := mysqlDataSQLSelection(d, false)
	if err != nil || !strings.Contains(statement, `"Native"."Records"`) || strings.Contains(statement, "_utf8mb4") || !strings.Contains(statement, "中文 ''") {
		t.Fatal("native SQL copy lost identity/type", err, statement)
	}
	d.page.Rows[0][1].Truncated = true
	if _, err = mysqlDataSQLSelection(d, false); !errors.Is(err, contract.ErrDataEditLimit) {
		t.Fatal("truncated preview emitted as complete SQL", err)
	}
	d.edit.Drafts = map[string]*mysqlRowDraft{}
	d.page.Rows[0][1].Truncated = false
	d.page.Rows[0][1].Value = nil
	if _, err = mysqlDataSQLSelection(d, false); !errors.Is(err, contract.ErrDataPage) {
		t.Fatal("untyped preview emitted as native SQL", err)
	}
	if choices := mysqlDataImportTargetChoices(d); len(choices) != 2 {
		t.Fatal("native existing/new-table import not offered", choices)
	}
}
