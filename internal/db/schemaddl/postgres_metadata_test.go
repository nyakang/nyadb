package schemaddl

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func pgMetadataFixture(t *testing.T) Snapshot {
	t.Helper()
	p := PostgreSQLCatalog{ServerVersion: 170000, Database: "user", Namespace: "demo", DatabaseOID: 12, NamespaceOID: 14, RelationOID: 16, Entries: []PostgreSQLCatalogEntry{
		pgRecord("relation", 16, 0, `{"relname":"quoted\"table","relkind":"r","relpersistence":"p","comment":"before","future_property":9007199254740993}`),
		pgRecord("column", 16, 1, `{"attnum":1,"attname":"text\"column","type_sql":"text","attstorage":"x","attcompression":"","attstattarget":-1,"attislocal":true,"comment":"column before","future_property":9007199254740993}`),
		pgRecord("unknown", 20, 0, `{"untouched":[9007199254740993]}`),
	}}
	table, err := p.Table()
	if err != nil {
		t.Fatal(err)
	}
	revision, err := p.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return Snapshot{Engine: "postgresql", Exists: true, Table: table, Postgres: &p, Revision: revision}
}

func TestPostgreSQLMetadataPlanQuotesCommentsAndOnlyEmitsNativeAttributeChanges(t *testing.T) {
	base := pgMetadataFixture(t)
	draft := base.Table.Clone()
	draft.Postgres.Comment = "table ' comment\\line;\nsecond"
	p := draft.Clauses[0].Postgres
	p.Comment, p.Storage, p.Compression, p.Statistics = "", "e", "p", 73
	plan, err := PlanPostgreSQLMetadata(base, draft, "demo")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"COMMENT ON TABLE \"demo\".\"quoted\"\"table\" IS 'table '' comment\\line;\nsecond'",
		`COMMENT ON COLUMN "demo"."quoted""table"."text""column" IS NULL`,
		`ALTER TABLE ONLY "demo"."quoted""table" ALTER COLUMN "text""column" SET STORAGE EXTERNAL`,
		`ALTER TABLE ONLY "demo"."quoted""table" ALTER COLUMN "text""column" SET COMPRESSION pglz`,
		`ALTER TABLE ONLY "demo"."quoted""table" ALTER COLUMN "text""column" SET STATISTICS 73`,
	}
	if plan.Mode != "metadata" || Signature(plan.SQL) != Signature(want) {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if base.Table.Postgres.Comment != "before" || base.Table.Clauses[0].Postgres.Storage != "x" {
		t.Fatal("planner mutated original")
	}
	noop, err := PlanPostgreSQLMetadata(base, base.Table, "demo")
	if err != nil || noop.Mode != "noop" || len(noop.SQL) != 0 {
		t.Fatal(noop, err)
	}
}

func TestPostgreSQLMetadataRejectsUnimplementedAndUntrustedDraftChanges(t *testing.T) {
	base := pgMetadataFixture(t)
	mutations := []func(*Table){
		func(x *Table) { x.Name = "renamed" },
		func(x *Table) { x.Options = "WITH (fillfactor=50)" },
		func(x *Table) { x.Postgres.Owner = "other" },
		func(x *Table) { x.Postgres.ACL = []string{"other=r/owner"} },
		func(x *Table) { x.Clauses[0].ID = "column:2" },
		func(x *Table) { x.Clauses[0].Name = "renamed" },
		func(x *Table) { x.Clauses[0].SQL = "bigint" },
		func(x *Table) { x.Clauses[0].Kind = "constraint" },
		func(x *Table) { x.Clauses[0].Postgres.ACL = []string{"other=r/owner"} },
		func(x *Table) { x.Clauses[0].Postgres.Local = false },
		func(x *Table) { x.Clauses[0].Postgres.Statistics = 10001 },
		func(x *Table) { x.Clauses[0].Postgres.Statistics = -2 },
		func(x *Table) { x.Clauses[0].Postgres.Compression = "p; DROP TABLE t" },
		func(x *Table) { x.Clauses[0].Postgres.Storage = "EXTENDED" },
		func(x *Table) { x.Clauses = nil },
		func(x *Table) { x.Definitions = []Definition{{Kind: "trigger", Name: "t", SQL: "anything"}} },
		func(x *Table) { x.Postgres.Comment = "bad\x00comment" },
		func(x *Table) { x.Postgres.Comment = string([]byte{0xff}) },
		func(x *Table) { x.Clauses[0].Postgres.Comment = strings.Repeat("x", (1<<20)+1) },
	}
	for i, mutate := range mutations {
		draft := base.Table.Clone()
		mutate(&draft)
		if plan, err := PlanPostgreSQLMetadata(base, draft, "demo"); err == nil || len(plan.SQL) != 0 {
			t.Fatalf("mutation %d acquired partial write permission: %+v %v", i, plan, err)
		}
	}
	for _, mutate := range []func(*Snapshot){
		func(x *Snapshot) { x.Revision = "stale" },
		func(x *Snapshot) { x.Table.Clauses[0].SQL = "spoofed" },
	} {
		x := base.Clone()
		mutate(&x)
		if _, err := PlanPostgreSQLMetadata(x, x.Table, "demo"); !errors.Is(err, ErrConflict) {
			t.Fatal("untrusted baseline accepted", err)
		}
	}
	for _, mutate := range []func(*Snapshot){
		func(x *Snapshot) { x.Engine = "sqlite" },
		func(x *Snapshot) { x.Exists = false },
		func(x *Snapshot) { x.Postgres.Namespace = "other" },
	} {
		x := base.Clone()
		mutate(&x)
		if _, err := PlanPostgreSQLMetadata(x, x.Table, "demo"); !errors.Is(err, ErrUnsupported) {
			t.Fatal("foreign scope accepted", err)
		}
	}
}

func pgMetadataActual(t *testing.T, base Snapshot, edit func(map[string]any, PostgreSQLCatalogEntry)) Snapshot {
	t.Helper()
	x := base.Clone()
	for i, e := range x.Postgres.Entries {
		decoder := json.NewDecoder(strings.NewReader(string(e.Data)))
		decoder.UseNumber()
		var raw map[string]any
		if err := decoder.Decode(&raw); err != nil {
			t.Fatal(err)
		}
		edit(raw, e)
		data, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		x.Postgres.Entries[i].Data = data
	}
	var err error
	x.Table, err = x.Postgres.Table()
	if err != nil {
		t.Fatal(err)
	}
	x.Revision, err = x.Postgres.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func TestPostgreSQLMetadataVerificationPreservesUnknownExactCatalogProperties(t *testing.T) {
	base := pgMetadataFixture(t)
	draft := base.Table.Clone()
	draft.Postgres.Comment = "after"
	draft.Clauses[0].Postgres.Statistics = 73
	actual := pgMetadataActual(t, base, func(raw map[string]any, e PostgreSQLCatalogEntry) {
		if e.Kind == "relation" {
			raw["comment"] = "after"
		}
		if e.Kind == "column" {
			raw["attstattarget"] = 73
		}
	})
	if err := VerifyPostgreSQLMetadata(base, draft, actual, "demo"); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(map[string]any, PostgreSQLCatalogEntry){
		func(raw map[string]any, e PostgreSQLCatalogEntry) {
			if e.Kind == "unknown" {
				raw["untouched"] = []json.Number{"9007199254740992"}
			}
		},
		func(raw map[string]any, e PostgreSQLCatalogEntry) {
			if e.Kind == "column" {
				raw["future_property"] = json.Number("9007199254740992")
			}
		},
		func(raw map[string]any, e PostgreSQLCatalogEntry) {
			if e.Kind == "relation" {
				raw["future_property"] = json.Number("9007199254740992")
			}
		},
	} {
		changed := pgMetadataActual(t, actual, edit)
		if err := VerifyPostgreSQLMetadata(base, draft, changed, "demo"); err == nil {
			t.Fatal("unrequested metadata mutation passed")
		}
	}
	if err := VerifyPostgreSQLMetadata(base, base.Table, base, "demo"); err != nil {
		t.Fatal("noop verification failed", err)
	}
}

func TestSchemaSnapshotClonePreservesEmptyCatalogArraysAndRevision(t *testing.T) {
	base := pgMetadataFixture(t)
	base.Table.Definitions = []Definition{}
	base.Table.Postgres.Parameters, base.Table.Postgres.ACL, base.Table.Postgres.Parents = []string{}, []string{}, []string{}
	base.Table.Clauses[0].Postgres.Parameters, base.Table.Clauses[0].Postgres.ForeignOptions, base.Table.Clauses[0].Postgres.ACL = []string{}, []string{}, []string{}
	if Signature(base) != Signature(base.Clone()) {
		t.Fatal("immutable request copy changed its original schema signature")
	}
}

func TestPostgreSQLMetadataResetStatisticsUsesVersionedCatalogDefault(t *testing.T) {
	for _, version := range []int{160000, 170000} {
		base := pgMetadataFixture(t)
		base.Postgres.ServerVersion = version
		base = pgMetadataActual(t, base, func(raw map[string]any, entry PostgreSQLCatalogEntry) {
			if entry.Kind == "column" {
				raw["attstattarget"] = 73
			}
		})
		desired := base.Table.Clone()
		desired.Clauses[0].Postgres.Statistics = -1
		plan, err := PlanPostgreSQLMetadata(base, desired, "demo")
		if err != nil || len(plan.SQL) != 1 || !strings.HasSuffix(plan.SQL[0], "SET STATISTICS -1") {
			t.Fatal("default reset not planned", plan, err)
		}
		actual := pgMetadataActual(t, base, func(raw map[string]any, entry PostgreSQLCatalogEntry) {
			if entry.Kind == "column" {
				if version >= 170000 {
					raw["attstattarget"] = nil
				} else {
					raw["attstattarget"] = -1
				}
			}
		})
		if err := VerifyPostgreSQLMetadata(base, desired, actual, "demo"); err != nil {
			t.Fatal("native default rejected", version, err)
		}
		wrong := pgMetadataActual(t, actual, func(raw map[string]any, entry PostgreSQLCatalogEntry) {
			if entry.Kind == "column" {
				raw["attstattarget"] = 0
			}
		})
		if err := VerifyPostgreSQLMetadata(base, desired, wrong, "demo"); err == nil {
			t.Fatal("zero acquired default reset permission")
		}
	}
}
