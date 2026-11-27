package schemaddl

import (
	"strings"
	"testing"
)

func TestDatabendRenamePreservesOpaqueDefinitionAndQualifiedSource(t *testing.T) {
	original := Snapshot{Engine: "databend", Exists: true, Revision: "private proof", Table: Table{Name: `odd_%猫"table`}, Databend: &DatabendTableIdentity{Database: `db_%"猫`, TableID: "18446744073709551615"}}
	original.DDL = "CREATE TABLE " + Quote(original.Table.Name) + " (\n  v VARCHAR NULL DEFAULT 'a; DROP TABLE x'\n) ENGINE=FUSE"
	destination := `new_%;"猫`
	sql, err := PlanDatabendRename(original, original.Databend.Database, destination)
	if err != nil || sql != "ALTER TABLE "+Quote(original.Databend.Database)+"."+Quote(original.Table.Name)+" RENAME TO "+Quote(destination) {
		t.Fatal(sql, err)
	}
	body, err := DatabendTableBody(original.DDL, original.Table.Name)
	if err != nil || !strings.Contains(body, "DEFAULT 'a; DROP TABLE x'") {
		t.Fatal("opaque declaration changed", body, err)
	}
	copy := original.Clone()
	copy.Databend.TableID = "5"
	if original.Databend.TableID != "18446744073709551615" {
		t.Fatal("clone shared native identity")
	}
	for _, mutate := range []func(*Snapshot){func(s *Snapshot) { s.Databend = nil }, func(s *Snapshot) { s.Databend.TableID = "01" }, func(s *Snapshot) { s.Databend.Database = "foreign" }, func(s *Snapshot) { s.DDL = strings.Replace(s.DDL, "ENGINE=FUSE", "ENGINE=VIEW", 1) }, func(s *Snapshot) { s.DDL += "; SELECT 1" }, func(s *Snapshot) { s.Revision = "" }, func(s *Snapshot) { s.Exists = false }} {
		altered := original.Clone()
		mutate(&altered)
		if _, err := PlanDatabendRename(altered, original.Databend.Database, destination); err == nil {
			t.Fatal("unsupported/foreign original accepted", altered)
		}
	}
	for _, dest := range []string{"", original.Table.Name, "a\nb", "\x00", string([]byte{0xff}), strings.Repeat("x", 1025)} {
		if _, err := PlanDatabendRename(original, original.Databend.Database, dest); err == nil {
			t.Fatal("invalid destination accepted")
		}
	}
}
