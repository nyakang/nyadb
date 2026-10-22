package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func mysqlInspectorSampleTab() *tab {
	literalNull := "NULL"
	length := int64(80)
	precision, scale := int64(10), int64(2)
	charset, collation := "utf8mb4", "utf8mb4_0900_ai_ci"
	return &tab{
		dataDatabase: "test_schema", dataTable: "entries", dataSelectedRow: -1,
		dataPage: &connections.MySQLDataPage{
			Columns: []string{"id", "label", "price"},
			ColumnKinds: []string{"bigint", "varchar", "decimal"},
			ColumnMetadata: []connections.MySQLStructureColumn{
				{Name: "id", DataType: "bigint", Type: "bigint unsigned", Key: "PRI", Extra: "auto_increment"},
				{Name: "label", DataType: "varchar", Type: "varchar(80)", Nullable: true,
					Default: &literalNull, CharacterMaxLength: &length, CharacterSet: &charset,
					Collation: &collation, Comment: "reviewed"},
				{Name: "price", DataType: "decimal", Type: "decimal(10,2)", NumericPrecision: &precision,
					NumericScale: &scale, Nullable: true},
			},
			Rows: [][]connections.MySQLDataCell{{{Text: "1"}, {Text: "sample"}, {Text: "12.50"}}},
		},
	}
}

func TestMySQLInspectorUsesExactFieldMetadataAndLifecycle(t *testing.T) {
	tab := mysqlInspectorSampleTab()
	field, found, complete := mysqlSelectedDataField(tab)
	if !found || !complete || field.Name != "id" || field.Type != "bigint unsigned" {
		t.Fatalf("default field: %+v %v %v", field, found, complete)
	}
	s := &State{}
	tt := ui.NewTester(func(c *ui.Context) { s.mysqlDataInspector(c, tab) }, 330, 780)
	tt.Frame()
	if !tt.HasText("bigint unsigned") || !tt.HasText("主键") || !tt.HasText("auto_increment") {
		t.Fatalf("initial field attributes missing: %v", tt.Texts())
	}
	tab.dataState.Cells = ui.ColumnRange{Cursor: "label", Anchor: "label"}
	tt.Frame()
	field, found, complete = mysqlSelectedDataField(tab)
	if !found || !complete || field.Name != "label" || field.Default == nil || *field.Default != "NULL" {
		t.Fatalf("selected field metadata: %+v %v %v", field, found, complete)
	}
	if !tt.HasText("varchar(80)") || !tt.HasText("\"NULL\"") || !tt.HasText("utf8mb4") {
		t.Fatalf("literal NULL or charset missing: %v", tt.Texts())
	}
	tab.dataSelectedRow = 0
	tt.Frame()
	if name, cell, ok := mysqlSelectedDataCell(tab); !ok || name != "label" || cell.Text != "sample" {
		t.Fatal("cell selection and field metadata diverged")
	}
	tab.dataState.Cells = ui.ColumnRange{Cursor: "price", Anchor: "price"}
	tt.Frame()
	if !tt.HasText("decimal(10,2)") {
		t.Fatal("cell changed but metadata did not")
	}
	tab.dataHiddenColumns = map[string]bool{"price": true}
	if _, found, _ := mysqlSelectedDataField(tab); found {
		t.Fatal("hidden field leaked into inspector")
	}
	tab.dataPage = nil
	tab.dataLoading = false
	tab.dataErr = "disconnected"
	tt.Frame()
	if !tt.HasText("字段元数据不可用，请重试读取数据") || tt.HasText("decimal(10,2)") {
		t.Fatal("stale metadata survived page failure")
	}
}

func TestMySQLInspectorFallbackHasNoFabricatedAttributes(t *testing.T) {
	tab := &tab{dataPage: &connections.MySQLDataPage{
		Columns: []string{"id"}, ColumnKinds: []string{"bigint"},
	}}
	field, found, complete := mysqlSelectedDataField(tab)
	if !found || complete || field.Type != "bigint" || field.Key != "" {
		t.Fatalf("legacy fields must not fake metadata: %+v", field)
	}
	tt := ui.NewTester(func(c *ui.Context) { (&State{}).mysqlDataInspector(c, tab) }, 330, 700)
	tt.Frame()
	if !tt.HasText("仅有基础字段类型，完整元数据尚未加载") || tt.HasText("主键") {
		t.Fatal("missing metadata rendered as confirmed attributes")
	}
}
