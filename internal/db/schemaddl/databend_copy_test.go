package schemaddl

import (
	"strings"
	"testing"
)

func TestDatabendCopyPreservesLikeAndRejectsUnknownTransformations(t *testing.T) {
	s := Snapshot{Engine: "databend", Exists: true, Revision: "proof", Table: Table{Name: `source_%猫"`}, Databend: &DatabendTableIdentity{Database: `db_%"`, TableID: "71", Columns: []DatabendColumn{{Name: `column_%"`, Type: "BIGINT UNSIGNED"}}}}
	s.DDL = "CREATE TABLE " + Quote(s.Table.Name) + " (\n  " + Quote(s.Databend.Columns[0].Name) + " BIGINT UNSIGNED NOT NULL\n) ENGINE=FUSE"
	source := Quote(s.Databend.Database) + "." + Quote(s.Table.Name)
	expected := "CREATE TABLE " + Quote(s.Databend.Database) + ".\"target\" LIKE " + source
	for _, data := range []bool{false, true} {
		sql, err := PlanDatabendCopy(s, s.Databend.Database, "target", data, false, false)
		want := expected
		if data {
			want += " AS SELECT " + Quote(s.Databend.Columns[0].Name) + " FROM " + source
		}
		if err != nil || sql != want {
			t.Fatal(sql, err)
		}
	}
	for _, mutate := range []func(*Snapshot){func(s *Snapshot) { s.Databend.Columns = nil }, func(s *Snapshot) { s.Databend.Columns[0].Generated = true }, func(s *Snapshot) { s.Databend.Columns[0].Name = "bad\nname" }, func(s *Snapshot) { s.Databend.Columns = append(s.Databend.Columns, s.Databend.Columns[0]) }, func(s *Snapshot) { s.DDL = strings.ReplaceAll(s.DDL, "ENGINE=FUSE", "ENGINE=MEMORY") }} {
		copy := s.Clone()
		mutate(&copy)
		if _, err := PlanDatabendCopy(copy, s.Databend.Database, "target", true, false, false); err == nil {
			t.Fatal("unknown copy structure admitted")
		}
	}
	for _, flags := range [][2]bool{{true, false}, {false, true}, {true, true}} {
		if _, err := PlanDatabendCopy(s, s.Databend.Database, "target", true, flags[0], flags[1]); err == nil {
			t.Fatal("foreign engine copy flags admitted")
		}
	}
	copy := s.Clone()
	copy.Databend.Columns[0].Name = "mutated"
	if s.Databend.Columns[0].Name == "mutated" {
		t.Fatal("copy proof shares mutable columns")
	}
}
