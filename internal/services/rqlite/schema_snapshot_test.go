package rqlite

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func schemaUnitJSON(t *testing.T, catalog schemaddl.SQLiteCatalog) string {
	t.Helper()
	rows := make([][]any, 0, len(catalog.Entries))
	for _, e := range catalog.Entries {
		null := 0
		if e.SQLNull {
			null = 1
		}
		rows = append(rows, []any{hex.EncodeToString([]byte(e.Type)), hex.EncodeToString([]byte(e.Name)), hex.EncodeToString([]byte(e.Table)), e.Root, hex.EncodeToString([]byte(e.SQL)), null})
	}
	raw, err := json.Marshal(map[string]any{"results": []any{
		map[string]any{"columns": []string{"schema_version"}, "types": []string{"integer"}, "values": [][]any{{catalog.Version}}},
		map[string]any{"columns": []string{"type", "name", "table", "root", "sql", "null"}, "types": []string{"text", "text", "text", "integer", "text", "integer"}, "values": rows},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func schemaUnitOriginal() schemaddl.SQLiteCatalog {
	return schemaddl.SQLiteCatalog{Version: 12, Entries: []schemaddl.SQLiteCatalogEntry{
		{Type: "index", Name: "sqlite_autoindex_original_1", Table: "original", Root: 3, SQLNull: true},
		{Type: "table", Name: "original", Table: "original", Root: 2, SQL: "CREATE TABLE original(id INTEGER PRIMARY KEY,v TEXT DEFAULT 'X''61''' UNIQUE)"},
	}}
}

func schemaUnitTarget(s *Session, name string) contract.SchemaDesignTarget {
	return contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: s.ProfileID(), Catalog: "main", Kind: "table", Name: name}, Generation: s.Generation()}
}

func TestSchemaSnapshotReadOnlyCompleteCatalogAndOriginalSeal(t *testing.T) {
	catalog := schemaUnitOriginal()
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/db/query" || !r.URL.Query().Has("transaction") || r.URL.Query().Get("retries") != "0" {
			t.Error("schema snapshot changed original atomic read semantics")
		}
		var statements []string
		if err := json.NewDecoder(r.Body).Decode(&statements); err != nil || len(statements) != 2 || statements[1] != schemaCatalogSQL {
			t.Error("schema snapshot was partial or split", err)
		}
		fmt.Fprint(w, schemaUnitJSON(t, catalog))
	}, func(p *connections.Profile) { p.RQLiteOptions.ReadOnly = true })
	ops := s.SchemaDesign()
	if ops.Snapshot == nil || ops.Apply != nil || ops.Drop != nil || ops.Copy != nil || ops.Rename != nil || s.Capabilities().DesignTables || s.Capabilities().Objects {
		t.Fatal("readable schema catalog published mutation methods")
	}
	original, err := ops.Snapshot(context.Background(), schemaUnitTarget(s, "original"))
	if err != nil || !original.Schema.Exists || original.Schema.DDL != catalog.Entries[1].SQL || len(original.Schema.Table.Columns()) != 2 || len(original.Schema.Table.Definitions) != 0 || original.Schema.SQLite == nil || !original.Schema.SQLite.Entries[0].SQLNull || len(original.Schema.Revision) != 64 {
		t.Fatal("complete original native schema or implicit index was lost", err, original)
	}
	guard, err := s.schemaSnapshotGuard(original)
	if err != nil || !strings.Contains(guard, schemaTextLiteral(catalog.Entries[1].SQL)) || !strings.Contains(guard, "temp.sqlite_schema") || strings.Contains(guard, "foreign_keys=") {
		t.Fatal("original guard lost complete bytes or mutated connection state", err)
	}
	for _, edit := range []func(*contract.SchemaDesignSnapshot){
		func(p *contract.SchemaDesignSnapshot) { p.Schema.SQLite.Version++ },
		func(p *contract.SchemaDesignSnapshot) { p.Schema.SQLite.Entries[0].Root++ },
		func(p *contract.SchemaDesignSnapshot) { p.Schema.SQLite.Entries[0].SQLNull = false },
		func(p *contract.SchemaDesignSnapshot) { p.Schema.Table.Clauses[0].SQL = "TEXT" },
		func(p *contract.SchemaDesignSnapshot) { p.Target.Object.Name = "other" },
		func(p *contract.SchemaDesignSnapshot) { p.Schema.DDL = "changed" },
		func(p *contract.SchemaDesignSnapshot) { p.Schema.Exists = false },
	} {
		changed := original.Clone()
		edit(&changed)
		if _, err := s.schemaSnapshotGuard(changed); !errors.Is(err, schemaddl.ErrConflict) {
			t.Fatal("changed original catalog became an executable guard", err)
		}
	}
	original.Schema.SQLite.Entries[1].SQL = "changed"
	if catalog.Entries[1].SQL == "changed" {
		t.Fatal("native snapshot shares caller-owned catalog buffers")
	}
}

func TestSchemaSnapshotScopeMissingIdentityAndASCIICaseRules(t *testing.T) {
	catalog := schemaUnitOriginal()
	var calls int
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, schemaUnitJSON(t, catalog)) }, nil)
	for _, edit := range []func(*contract.SchemaDesignTarget){
		func(p *contract.SchemaDesignTarget) { p.Generation++ },
		func(p *contract.SchemaDesignTarget) { p.Object.ProfileID++ },
		func(p *contract.SchemaDesignTarget) { p.Object.Catalog = "foreign" },
		func(p *contract.SchemaDesignTarget) { p.Object.Schema = "foreign" },
		func(p *contract.SchemaDesignTarget) { p.Object.Kind = "view" },
		func(p *contract.SchemaDesignTarget) { p.Object.Name = "sqlite_sequence" },
	} {
		target := schemaUnitTarget(s, "original")
		edit(&target)
		if _, err := s.schemaSnapshot(context.Background(), target); err == nil {
			t.Fatal("foreign or internal schema scope admitted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid schema scope dispatched")
	}
	if _, err := s.schemaSnapshot(context.Background(), schemaUnitTarget(s, "ORIGINAL")); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("case-mismatched existing table became a new target", err)
	}
	for _, name := range []string{"new_table", "å"} {
		catalog = schemaddl.SQLiteCatalog{Entries: []schemaddl.SQLiteCatalogEntry{{Type: "table", Name: "Å", Table: "Å", Root: 2, SQL: `CREATE TABLE "Å"(id INTEGER)`}}}
		x, err := s.schemaSnapshot(context.Background(), schemaUnitTarget(s, name))
		if err != nil || x.Schema.Exists || x.Schema.Table.Name != name || x.Schema.SQLite == nil || len(x.Schema.SQLite.Entries) != 1 || x.Schema.Revision == "" {
			t.Fatal("missing or distinct non-ASCII identity lost complete scope", err, x)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.schemaSnapshot(context.Background(), schemaUnitTarget(s, "original")); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed original schema provider remained usable", err)
	}
}

func TestSchemaSnapshotRejectsIncompleteNativeCatalog(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*schemaddl.SQLiteCatalog)
	}{
		{"duplicate_entry", func(c *schemaddl.SQLiteCatalog) { c.Entries = append(c.Entries, c.Entries[1]) }},
		{"invalid_utf8", func(c *schemaddl.SQLiteCatalog) { c.Entries[1].SQL = "\xff" }},
		{"truncated_definition", func(c *schemaddl.SQLiteCatalog) { c.Entries[1].SQL = "CREATE TABLE original(" }},
		{"inconsistent_null", func(c *schemaddl.SQLiteCatalog) { c.Entries[1].SQLNull = true }},
		{"unparseable_virtual_table", func(c *schemaddl.SQLiteCatalog) { c.Entries[1].SQL = "CREATE VIRTUAL TABLE original USING fts5(v)" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := schemaUnitOriginal()
			test.edit(&catalog)
			s := mockSession(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, schemaUnitJSON(t, catalog)) }, nil)
			out, err := s.schemaSnapshot(context.Background(), schemaUnitTarget(s, "original"))
			if err == nil || out.Schema.Revision != "" || out.Schema.Exists || out.Schema.SQLite != nil {
				t.Fatal("incomplete native schema became an original baseline", err, out)
			}
		})
	}
}
