package rqlite

import (
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestSchemaRebuildCapturesCompleteInboundClosureAndKeepsUnrelatedPhysicalRows(t *testing.T) {
	ddls := []string{
		`CREATE TABLE "Grand Child"(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES "Child Ω"(id))`,
		`CREATE TABLE parent(id INTEGER PRIMARY KEY)`,
		`CREATE TABLE unrelated(rowid TEXT,oid TEXT,_rowid_ TEXT,v BLOB DEFAULT 'REFERENCES Target(id)',parent INTEGER REFERENCES absent(id))`,
		`CREATE TABLE "Child Ω"(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES tArGeT(id),cycle INTEGER REFERENCES "Grand Child"(id))`,
		`CREATE TABLE Target(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES parent(id))`,
	}
	catalog := schemaddl.SQLiteCatalog{}
	var selected schemaddl.Table
	for _, ddl := range ddls {
		table, err := schemaddl.ParseSQLite(ddl)
		if err != nil {
			t.Fatal(err)
		}
		catalog.Entries = append(catalog.Entries, schemaddl.SQLiteCatalogEntry{Type: "table", Name: table.Name, Table: table.Name, SQL: ddl})
		if table.Name == "Target" {
			selected = table
		}
	}
	catalog.Entries = append(catalog.Entries, schemaddl.SQLiteCatalogEntry{Type: "table", Name: "sqlite_sequence", Table: "sqlite_sequence", SQL: "CREATE TABLE sqlite_sequence(name,seq)"})
	original := contract.SchemaDesignSnapshot{Schema: schemaddl.Snapshot{Engine: "sqlite", Exists: true, Table: selected, SQLite: &catalog}}
	original.Target.Object.Name = selected.Name
	p, err := buildSchemaRebuildProgram(original, selected, "original_scope")
	if err != nil {
		t.Fatal("unrelated inaccessible rowid blocked selected reconstruction", err)
	}
	var names []string
	for _, table := range p.tables {
		names = append(names, table.table.Name)
	}
	if !reflect.DeepEqual(names, []string{"Grand Child", "Child Ω", "Target", "sqlite_sequence"}) {
		t.Fatal("transitive/cyclic inbound closure lost a child or captured an outbound/unrelated parent", names)
	}
	catalog.Entries[2].SQL = `CREATE TABLE unrelated(rowid TEXT,oid TEXT,_rowid_ TEXT,parent INTEGER REFERENCES Target(id))`
	if _, err := buildSchemaRebuildProgram(original, selected, "original_scope"); err == nil {
		t.Fatal("affected inaccessible physical rowid was silently discarded")
	}
}

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
