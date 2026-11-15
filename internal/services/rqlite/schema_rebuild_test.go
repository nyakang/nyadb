package rqlite

import (
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestSchemaRebuildFallbackRetainsEngineAndDeclarationBounds(t *testing.T) {
	catalog := schemaUnitOriginal()
	table, err := schemaddl.ParseSQLite(catalog.Entries[1].SQL)
	if err != nil {
		t.Fatal(err)
	}
	original := contract.SchemaDesignSnapshot{Schema: schemaddl.Snapshot{Engine: "sqlite", Revision: "original-complete-catalog", DDL: catalog.Entries[1].SQL, Exists: true, Table: table, SQLite: &catalog}}
	original.Target.Object.Name = table.Name
	desired := table.Clone()
	desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:check", Kind: "constraint", SQL: "CHECK(id>0)"})
	for _, mutate := range []func(*contract.SchemaDesignSnapshot, *schemaddl.Table){
		func(s *contract.SchemaDesignSnapshot, _ *schemaddl.Table) { s.Schema.Engine = "opengauss" },
		func(s *contract.SchemaDesignSnapshot, _ *schemaddl.Table) {
			s.Schema.OpenGauss = &schemaddl.OpenGaussCatalog{}
		},
		func(_ *contract.SchemaDesignSnapshot, d *schemaddl.Table) {
			d.Postgres = &schemaddl.PostgreSQLTableProperties{}
		},
		func(_ *contract.SchemaDesignSnapshot, d *schemaddl.Table) {
			d.Clauses[0].OpenGauss = &schemaddl.OpenGaussColumnProperties{}
		},
		func(_ *contract.SchemaDesignSnapshot, d *schemaddl.Table) {
			d.Definitions = []schemaddl.Definition{{Kind: "index", Name: "foreign", SQL: "CREATE INDEX foreign ON original(v)", Postgres: &schemaddl.PostgreSQLDefinitionProperties{}}}
		},
	} {
		s, d := original.Clone(), desired.Clone()
		mutate(&s, &d)
		if plan, err := planSchemaDocument(s, d, false, "1", "0123456789abcdef"); err == nil || len(plan.SQL) != 0 {
			t.Fatal("reconstruction fallback discarded foreign engine semantics", err)
		}
	}
	for _, addition := range []struct{ sql, mode string }{
		{"TEXT DEFAULT 'literal'", "native_columns"},
		{"TEXT NOT NULL DEFAULT 'filled'", "rebuild"},
		{"TEXT DEFAULT CURRENT_TIME", "rebuild"},
	} {
		d := table.Clone()
		d.Clauses = append(d.Clauses, schemaddl.Clause{ID: "new:added", Kind: "column", Name: "added", SQL: addition.sql})
		plan, err := planSchemaDocument(original, d, false, "1", "0123456789abcdef")
		if err != nil || plan.Mode != addition.mode {
			t.Fatal("original native ADD path or reconstruction-only declaration was lost", addition.sql, plan.Mode, err)
		}
	}
}
