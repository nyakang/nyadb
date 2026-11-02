package schemaddl

import (
	"encoding/json"
	"strings"
	"testing"
)

func pgRenameRefresh(t *testing.T, s *Snapshot) {
	t.Helper()
	var err error
	s.Table, err = s.Postgres.Table()
	if err != nil {
		t.Fatal(err)
	}
	s.Revision, err = s.Postgres.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
}

func pgRenameFixture(t *testing.T) Snapshot {
	t.Helper()
	base := pgMetadataFixture(t)
	base.Postgres.Entries[0] = pgRecord("relation", 16, 0, `{"relname":"quoted\"table","relkind":"r","relpersistence":"p","reltype":30,"comment":"before","future_property":9007199254740993}`)
	base.Postgres.Entries = append(base.Postgres.Entries,
		pgRecord("type", 30, 0, `{"typname":"quoted\"table","typtype":"c","typnamespace":14,"typrelid":16,"typarray":31,"future_property":9007199254740993}`),
		pgRecord("type", 31, 0, `{"typname":"_quoted\"table","typnamespace":14,"typelem":30,"future_property":9007199254740993}`),
		pgRecord("type", 32, 0, `{"typname":"_renamed","typnamespace":14,"typelem":0}`),
	)
	pgRenameRefresh(t, &base)
	return base
}

func pgRenamePatch(t *testing.T, s *Snapshot, kind string, oid int64, sub int, patch func(map[string]any)) {
	t.Helper()
	for i := range s.Postgres.Entries {
		e := &s.Postgres.Entries[i]
		if e.Kind != kind || e.OID != oid || e.SubID != sub {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(string(e.Data)))
		decoder.UseNumber()
		var r map[string]any
		if err := decoder.Decode(&r); err != nil {
			t.Fatal(err)
		}
		patch(r)
		var err error
		e.Data, err = json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatal("rename fixture record missing", kind, oid, sub)
}

func TestPostgreSQLRenamePreservesCompleteCatalogAndAllocatesArrayName(t *testing.T) {
	base := pgRenameFixture(t)
	draft := base.Table.Clone()
	draft.Name = "renamed"
	actual := base.Clone()
	pgRenamePatch(t, &actual, "relation", 16, 0, func(r map[string]any) { r["relname"] = "renamed" })
	pgRenamePatch(t, &actual, "type", 30, 0, func(r map[string]any) { r["typname"] = "renamed" })
	pgRenamePatch(t, &actual, "type", 31, 0, func(r map[string]any) { r["typname"] = "_renamed_1" })
	pgRenameRefresh(t, &actual)
	if err := VerifyPostgreSQLRename(base, draft, actual, "demo"); err != nil {
		t.Fatal(err)
	}
	if base.Table.Name != `quoted"table` || !strings.Contains(string(base.Postgres.Entries[0].Data), "9007199254740993") {
		t.Fatal("rename proof mutated or rounded the original")
	}
	for _, entry := range []struct {
		name  string
		kind  string
		oid   int64
		sub   int
		patch func(map[string]any)
	}{
		{"unknown exact value", "relation", 16, 0, func(r map[string]any) { r["future_property"] = json.Number("9007199254740992") }},
		{"external object", "unknown", 20, 0, func(r map[string]any) { r["untouched"] = []any{json.Number("1")} }},
		{"array identity", "type", 31, 0, func(r map[string]any) { r["typelem"] = json.Number("32") }},
		{"array name", "type", 31, 0, func(r map[string]any) { r["typname"] = "unexpected" }},
		{"permission loss", "relation", 16, 0, func(r map[string]any) { r["relacl"] = nil }},
	} {
		t.Run(entry.name, func(t *testing.T) {
			bad := actual.Clone()
			pgRenamePatch(t, &bad, entry.kind, entry.oid, entry.sub, entry.patch)
			pgRenameRefresh(t, &bad)
			if err := VerifyPostgreSQLRename(base, draft, bad, "demo"); err == nil {
				t.Fatal("rename granted permission for unrelated catalog change")
			}
		})
	}
}

func TestPostgreSQLColumnRenameRejectsUnrelatedAndIncompleteDerivedChanges(t *testing.T) {
	base := pgRenameFixture(t)
	draft := base.Table.Clone()
	draft.Clauses[0].Name = "renamed_column"
	actual := base.Clone()
	pgRenamePatch(t, &actual, "column", 16, 1, func(r map[string]any) { r["attname"] = "renamed_column" })
	pgRenameRefresh(t, &actual)
	if err := VerifyPostgreSQLRename(base, draft, actual, "demo"); err != nil {
		t.Fatal(err)
	}
	bad := actual.Clone()
	pgRenamePatch(t, &bad, "column", 16, 1, func(r map[string]any) { r["default_sql"] = "changed without a captured tree" })
	pgRenameRefresh(t, &bad)
	if err := VerifyPostgreSQLRename(base, draft, bad, "demo"); err == nil {
		t.Fatal("unbacked default presentation acquired change permission")
	}
	bad = actual.Clone()
	bad.Postgres.Entries = append(bad.Postgres.Entries, pgRecord("unknown", 900, 0, `{"new":true}`))
	pgRenameRefresh(t, &bad)
	if err := VerifyPostgreSQLRename(base, draft, bad, "demo"); err == nil {
		t.Fatal("rename unexpectedly added an object")
	}
	bad = actual.Clone()
	bad.Postgres.Database = "other"
	pgRenameRefresh(t, &bad)
	if err := VerifyPostgreSQLRename(base, draft, bad, "demo"); err == nil {
		t.Fatal("rename borrowed another database's identity")
	}
}

func TestPostgreSQLRenameArrayNameClipsAtUTF8Boundary(t *testing.T) {
	name, err := pgRenameUnusedArrayName(map[string]int64{}, strings.Repeat("表", 21))
	if err != nil || name != "_"+strings.Repeat("表", 20) {
		t.Fatal(name, err)
	}
}

func TestPostgreSQLRenameMovesOnlyAssociatedConflictingArray(t *testing.T) {
	base := pgRenameFixture(t)
	base.Postgres.Entries = append(base.Postgres.Entries,
		pgRecord("type", 42, 0, `{"typname":"other","typtype":"e","typnamespace":14,"typarray":43,"typisdefined":true}`),
		pgRecord("type", 43, 0, `{"typname":"_other","typtype":"b","typnamespace":14,"typelem":42,"typisdefined":true}`),
	)
	pgRenameRefresh(t, &base)
	draft := base.Table.Clone()
	draft.Name = "_other"
	actual := base.Clone()
	pgRenamePatch(t, &actual, "relation", 16, 0, func(r map[string]any) { r["relname"] = "_other" })
	pgRenamePatch(t, &actual, "type", 30, 0, func(r map[string]any) { r["typname"] = "_other" })
	pgRenamePatch(t, &actual, "type", 31, 0, func(r map[string]any) { r["typname"] = "__other_1" })
	pgRenamePatch(t, &actual, "type", 43, 0, func(r map[string]any) { r["typname"] = "__other" })
	pgRenameRefresh(t, &actual)
	if err := VerifyPostgreSQLRename(base, draft, actual, "demo"); err != nil {
		t.Fatal(err)
	}
	bad := actual.Clone()
	pgRenamePatch(t, &bad, "type", 42, 0, func(r map[string]any) { r["typname"] = "replaced" })
	pgRenameRefresh(t, &bad)
	if err := VerifyPostgreSQLRename(base, draft, bad, "demo"); err == nil {
		t.Fatal("moving an array changed its unrelated base type")
	}
}
