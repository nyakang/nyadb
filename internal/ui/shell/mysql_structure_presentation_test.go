package shell

import (
	"testing"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func intptr(n int64) *int64   { return &n }
func strptr(s string) *string { return &s }

// Navicat 18 observation O: the design grid has one row per secondary
// index and one row per foreign key, rather than one row per member column.
// Service metadata remains complete and retains PRIMARY.
func TestMySQLStructureDesignPresentation(t *testing.T) {
	indexes := []mysqlsvc.MySQLStructureIndex{
		{Name: "PRIMARY", Unique: true, Type: "BTREE", Columns: []mysqlsvc.MySQLStructureIndexColumn{{Name: "id", Position: 1}}},
		{Name: "uk_tenant_code", Unique: true, Type: "BTREE", Columns: []mysqlsvc.MySQLStructureIndexColumn{
			{Name: "tenant_id", Position: 1},
			{Name: "parent_code", Position: 2},
		}},
		{Name: "ix_note_prefix", Type: "BTREE", Columns: []mysqlsvc.MySQLStructureIndexColumn{
			{Name: "nullable_note", Position: 1, SubPart: intptr(12)},
		}},
	}
	shown := mysqlStructureDesignIndexes(indexes)
	if len(indexes) != 3 || len(shown) != 2 ||
		shown[0].Name != "uk_tenant_code" || shown[1].Name != "ix_note_prefix" {
		t.Fatalf("secondary index grouping: %+v", shown)
	}
	if got := mysqlStructureIndexColumnNames(shown[0].Columns); got != "tenant_id, parent_code" {
		t.Fatal(got)
	}
	if got := mysqlStructureIndexColumnNames(shown[1].Columns); got != "nullable_note(12)" {
		t.Fatal(got)
	}

	fk := []mysqlsvc.MySQLStructureForeignKeyColumn{
		{Name: "tenant_id", ReferencedName: "tenant_id", Position: 1},
		{Name: "parent_code", ReferencedName: "code", Position: 2},
	}
	if got := mysqlStructureFKColumnNames(fk, false); got != "tenant_id, parent_code" {
		t.Fatal(got)
	}
	if got := mysqlStructureFKColumnNames(fk, true); got != "tenant_id, code" {
		t.Fatal(got)
	}

	chars := mysqlsvc.MySQLStructureColumn{
		Name: "sku", Type: "varchar(64)", DataType: "varchar",
		CharacterMaxLength: intptr(64), Default: strptr(""),
	}
	if got := mysqlStructureColumnLength(chars); got != "64" {
		t.Fatal(got)
	}
	if got := mysqlStructureColumnDefault(chars); got != "\"\"" {
		t.Fatal(got)
	}
	decimal := mysqlsvc.MySQLStructureColumn{
		Name: "price", Type: "decimal(10,2)", DataType: "decimal",
		NumericPrecision: intptr(10), NumericScale: intptr(2),
	}
	if got := mysqlStructureColumnLength(decimal); got != "10" {
		t.Fatal(got)
	}
	if got := mysqlStructureColumnDecimals(decimal); got != "2" {
		t.Fatal(got)
	}
	if got := mysqlStructureColumnDefault(decimal); got != "" {
		t.Fatal(got)
	}
	if got := mysqlStructureColumnDefault(mysqlsvc.MySQLStructureColumn{Nullable: true}); got != "NULL" {
		t.Fatal(got)
	}
	if got := mysqlStructureColumnLength(mysqlsvc.MySQLStructureColumn{DataType: "bigint", NumericPrecision: intptr(20)}); got != "" {
		t.Fatalf("integer display length is not shown in observed design grid: %q", got)
	}
}
