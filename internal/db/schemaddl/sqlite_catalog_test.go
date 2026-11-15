package schemaddl

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSQLiteCatalogSnapshotCloneKeepsCompleteOriginalRecords(t *testing.T) {
	s := Snapshot{Engine: "sqlite", Revision: "captured", SQLite: &SQLiteCatalog{Version: -1, Entries: []SQLiteCatalogEntry{
		{Type: "index", Name: "sqlite_autoindex_original_1", Table: "original", Root: 3, SQLNull: true},
		{Type: "table", Name: "original", Table: "original", Root: 2, SQL: "CREATE TABLE original(v TEXT UNIQUE)"},
	}}}
	if err := s.SQLite.Validate(); err != nil {
		t.Fatal(err)
	}
	copy := s.Clone()
	if Signature(copy) != Signature(s) {
		t.Fatal("clone changed original schema revision payload")
	}
	copy.SQLite.Entries[0].SQLNull = false
	copy.SQLite.Entries[1].SQL = "changed"
	if !s.SQLite.Entries[0].SQLNull || s.SQLite.Entries[1].SQL == "changed" {
		t.Fatal("snapshot clone shared original catalog records")
	}
	for _, entries := range [][]SQLiteCatalogEntry{nil, {}} {
		s.SQLite.Entries = entries
		copy := s.Clone()
		if Signature(s) != Signature(copy) || (entries == nil) != (copy.SQLite.Entries == nil) {
			t.Fatal("clone changed an empty original catalog")
		}
	}
	s.SQLite = nil
	raw, err := json.Marshal(s)
	if err != nil || strings.Contains(string(raw), "SQLite") || s.Clone().SQLite != nil {
		t.Fatal("absent SQLite catalog changed legacy snapshot serialization", err, string(raw))
	}
}

func TestSQLiteCatalogRejectsPartialAndAmbiguousNativeRecords(t *testing.T) {
	base := &SQLiteCatalog{Version: 1, Entries: []SQLiteCatalogEntry{{Type: "table", Name: "original", Table: "original", Root: 2, SQL: "CREATE TABLE original(id INTEGER)"}}}
	for _, test := range []struct {
		name string
		edit func(*SQLiteCatalog)
	}{
		{"version_overflow", func(p *SQLiteCatalog) { p.Version = 1 << 31 }},
		{"root_overflow", func(p *SQLiteCatalog) { p.Entries[0].Root = 1<<32 - 1 }},
		{"negative_root", func(p *SQLiteCatalog) { p.Entries[0].Root = -1 }},
		{"missing_name", func(p *SQLiteCatalog) { p.Entries[0].Name = "" }},
		{"null_sql_with_text", func(p *SQLiteCatalog) { p.Entries[0].SQLNull = true }},
		{"invalid_utf8", func(p *SQLiteCatalog) { p.Entries[0].SQL = "\xff" }},
		{"embedded_nul", func(p *SQLiteCatalog) { p.Entries[0].Name = "original\x00hidden" }},
		{"duplicate", func(p *SQLiteCatalog) { p.Entries = append(p.Entries, p.Entries[0]) }},
		{"unordered", func(p *SQLiteCatalog) {
			p.Entries = append(p.Entries, SQLiteCatalogEntry{Type: "index", Name: "ix", Table: "original"})
		}},
		{"unknown_type", func(p *SQLiteCatalog) { p.Entries[0].Type = "sequence" }},
		{"incomplete_text", func(p *SQLiteCatalog) { p.Entries[0].SQL = strings.Repeat("x", 1<<20+1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := base.Clone()
			test.edit(p)
			if p.Validate() == nil {
				t.Fatal("incomplete native catalog became a schema baseline")
			}
		})
	}
}
