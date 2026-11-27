package schemaddl

import "testing"

func TestDatabendClearRequiresOriginalAndPreservesDeleteSemantics(t *testing.T) {
	original := Snapshot{Engine: "databend", Exists: true, Revision: "private proof", Table: Table{Name: `clear_%猫"`}, Databend: &DatabendTableIdentity{Database: `db_%"`, TableID: "71"}}
	original.DDL = "CREATE TABLE " + Quote(original.Table.Name) + " (id BIGINT UNSIGNED NULL) ENGINE=FUSE"
	sql, err := PlanDatabendClear(original, original.Databend.Database, false)
	if err != nil || sql != "DELETE FROM "+Quote(original.Databend.Database)+"."+Quote(original.Table.Name) {
		t.Fatal(sql, err)
	}
	if _, err := PlanDatabendClear(original, original.Databend.Database, true); err == nil {
		t.Fatal("unsupported reset admitted")
	}
	for _, mutate := range []func(*Snapshot){func(s *Snapshot) { s.Databend.TableID = "0" }, func(s *Snapshot) { s.Revision = "" }, func(s *Snapshot) { s.Databend.Database = "another" }, func(s *Snapshot) { s.Exists = false }, func(s *Snapshot) { s.DDL += "; DELETE FROM another" }} {
		changed := original.Clone()
		mutate(&changed)
		if _, err := PlanDatabendClear(changed, original.Databend.Database, false); err == nil {
			t.Fatal("unqualified/unknown original clear admitted")
		}
	}
}
