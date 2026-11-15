package schemaddl

import (
	"reflect"
	"testing"
)

func TestSQLiteRebuildDependenciesPreserveGrammarAndQuotedIdentity(t *testing.T) {
	table, err := ParseSQLite(`CREATE TABLE t("references" TEXT DEFAULT 'REFERENCES wrong(id)',v INTEGER REFERENCES "Parent.Table"(id),CONSTRAINT "foreign" FOREIGN KEY(v) REFERENCES [Parent Other](id),CHECK(length('REFERENCES missing')>0))`)
	if err != nil {
		t.Fatal(err)
	}
	parents, err := SQLiteReferencedTables(table)
	if err != nil || !reflect.DeepEqual(parents, []string{"Parent.Table", "Parent Other"}) {
		t.Fatal("reconstruction guessed dependencies from literals or lost qualified text", parents, err)
	}
	for _, c := range []struct {
		sql    string
		native bool
	}{
		{"TEXT", true}, {"BLOB DEFAULT X'00FF'", true}, {"INTEGER DEFAULT -42", true}, {"REAL DEFAULT 1.25", true},
		{"TEXT DEFAULT 'CURRENT_TIME'", true}, {"TEXT DEFAULT CURRENT_TIME", false}, {"INTEGER DEFAULT (1+2)", false},
		{"TEXT NOT NULL DEFAULT 'filled'", false}, {"INTEGER UNIQUE", false}, {"TEXT AS ('generated') STORED", false},
	} {
		if got := SQLiteNativeAddition(Clause{Kind: "column", Name: "v", SQL: c.sql}); got != c.native {
			t.Fatal("native ADD eligibility lost complete declaration semantics", c.sql, got)
		}
	}
	columns, err := SQLiteTriggerUpdateColumns(`CREATE TRIGGER "UPDATE OF fake" INSTEAD OF UPDATE OF "real.name",[other] ON v WHEN new."real.name"='ON UPDATE OF fake' BEGIN SELECT 1; END`)
	if err != nil || !reflect.DeepEqual(columns, []string{"real.name", "other"}) {
		t.Fatal("view trigger header was flattened", columns, err)
	}
}

func TestSQLiteRebuildCatalogAdmitsOnlyDesiredObjectsAndKeepsOriginalUnselectedRoots(t *testing.T) {
	original := nativeSQLiteOriginal(t)
	desired := original.Table.Clone()
	desired.Clauses[0], desired.Clauses[1] = desired.Clauses[1], desired.Clauses[0]
	sql, err := CreateSQLite(desired, "main", desired.Name)
	if err != nil {
		t.Fatal(err)
	}
	actual := original.Clone()
	actual.Table, err = ParseSQLite(sql)
	if err != nil {
		t.Fatal(err)
	}
	actual.SQLite.Entries[0].SQL = sql
	actual.SQLite.Entries[0].Root = 77
	if err := VerifySQLiteRebuildCatalog(original, desired, actual); err != nil {
		t.Fatal("complete selected reconstruction was rejected", err)
	}
	for _, edit := range []func(*Snapshot){
		func(s *Snapshot) { s.SQLite.Entries[1].Root++ },
		func(s *Snapshot) { s.SQLite.Entries[1].SQL = "CREATE TABLE unselected(v TEXT)" },
		func(s *Snapshot) { s.SQLite.Entries = s.SQLite.Entries[:1] },
		func(s *Snapshot) { s.Table.Clauses[0].SQL = "TEXT DEFAULT 'lost'" },
	} {
		changed := actual.Clone()
		edit(&changed)
		if VerifySQLiteRebuildCatalog(original, desired, changed) == nil {
			t.Fatal("lossy or foreign reconstruction catalog was accepted")
		}
	}
}
