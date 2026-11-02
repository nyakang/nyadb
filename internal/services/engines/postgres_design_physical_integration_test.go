package engines

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func pgPhysicalRecords(t *testing.T, catalog *schemaddl.PostgreSQLCatalog) map[int64]map[string]json.RawMessage {
	t.Helper()
	out := map[int64]map[string]json.RawMessage{}
	for _, entry := range catalog.Entries {
		if entry.Kind != "relation" {
			continue
		}
		var value map[string]json.RawMessage
		if err := json.Unmarshal(entry.Data, &value); err != nil {
			t.Fatal(err)
		}
		out[entry.OID] = value
	}
	return out
}

func pgPhysicalColumn(t *testing.T, catalog *schemaddl.PostgreSQLCatalog, name string) map[string]json.RawMessage {
	t.Helper()
	for _, entry := range catalog.Entries {
		if entry.Kind != "column" || entry.OID != catalog.RelationOID {
			continue
		}
		var value map[string]json.RawMessage
		if err := json.Unmarshal(entry.Data, &value); err != nil {
			t.Fatal(err)
		}
		var column string
		if err := json.Unmarshal(value["attname"], &column); err != nil {
			t.Fatal(err)
		}
		if column == name {
			return value
		}
	}
	t.Fatal("fixture physical column is missing")
	return nil
}

func TestPostgreSQL17DesignPhysicalNoRewriteFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_physical_metadata;
CREATE TABLE nyadb_physical_metadata.t(id integer,label text DEFAULT 'before');
INSERT INTO nyadb_physical_metadata.t VALUES(1,repeat('fixture ',1000));
ALTER TABLE nyadb_physical_metadata.t ADD COLUMN fast numeric(40,20) DEFAULT 1.00000000000000000001;`)
	base := pgDesignRead(t, s, "nyadb_physical_metadata", "t")
	before := pgPhysicalRecords(t, base.Schema.Postgres)[base.Schema.Postgres.RelationOID]
	cache := pgPhysicalColumn(t, base.Schema.Postgres, "fast")
	if string(cache["atthasmissing"]) != "true" {
		t.Fatal("fixture did not create a fast default")
	}
	draft := base.Schema.Table.Clone()
	pgNativeEditField(t, &draft, "label", func(f *schemaddl.PostgreSQLField) { f.Default = `'after'::text` })
	draft.Clauses = append(draft.Clauses, schemaddl.Clause{ID: "new:plain", Kind: "column", Name: "plain", SQL: "integer"})
	actual := pgNativeFixtureApplyWithOriginalContext(t, s, base, draft, false)
	after := pgPhysicalRecords(t, actual.Schema.Postgres)[actual.Schema.Postgres.RelationOID]
	for _, field := range []string{"relfilenode", "reltoastrelid"} {
		if string(before[field]) != string(after[field]) {
			t.Fatal("metadata/default edit unexpectedly rewrote fixture storage")
		}
	}
	current := pgPhysicalColumn(t, actual.Schema.Postgres, "fast")
	for _, field := range []string{"atthasmissing", "attmissingval"} {
		if string(cache[field]) != string(current[field]) {
			t.Fatal("metadata edit changed a retained missing-value cache")
		}
	}
	if string(pgPhysicalColumn(t, actual.Schema.Postgres, "plain")["atthasmissing"]) != "false" {
		t.Fatal("nullable field without default acquired a cache")
	}
	// Explicit removal has its separately derived native dropped-slot proof.
	draft = actual.Schema.Table.Clone()
	draft.Clauses = slices.DeleteFunc(draft.Clauses, func(c schemaddl.Clause) bool { return c.Kind == "column" && c.Name == "fast" })
	actual = pgNativeFixtureApplyWithOriginalContext(t, s, actual, draft, true)
	if len(actual.Schema.Table.Columns()) != 3 {
		t.Fatal("explicit removal changed other columns")
	}
}

func TestPostgreSQL17DesignPhysicalFreshToastFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_physical_fresh;
CREATE TABLE nyadb_physical_fresh.t(id integer);
INSERT INTO nyadb_physical_fresh.t VALUES(1),(2);`)
	base := pgDesignRead(t, s, "nyadb_physical_fresh", "t")
	before := pgPhysicalRecords(t, base.Schema.Postgres)[base.Schema.Postgres.RelationOID]
	if string(before["reltoastrelid"]) != `"0"` {
		t.Fatal("fixture already owns TOAST storage")
	}
	draft := base.Schema.Table.Clone()
	draft.Clauses = append(draft.Clauses,
		schemaddl.Clause{ID: "new:label", Kind: "column", Name: "label", SQL: "text"},
		schemaddl.Clause{ID: "new:exact", Kind: "column", Name: "exact", SQL: "numeric(40,20) DEFAULT 1.00000000000000000001"},
	)
	actual := pgNativeFixtureApplyWithOriginalContext(t, s, base, draft, false)
	after := pgPhysicalRecords(t, actual.Schema.Postgres)[actual.Schema.Postgres.RelationOID]
	if string(after["reltoastrelid"]) == `"0"` || string(after["relfilenode"]) != string(before["relfilenode"]) {
		t.Fatal("new varlena fields did not create owned TOAST without heap rewrite")
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM nyadb_physical_fresh.t WHERE label IS NULL AND exact=1.00000000000000000001`).Scan(&count); err != nil || count != 2 {
		t.Fatal("new fields did not retain NULL and precise fast-default semantics", err)
	}
}

func TestPostgreSQL17DesignPhysicalPersistenceFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_physical_persistence;
CREATE TABLE nyadb_physical_persistence.t(id integer,label text);
INSERT INTO nyadb_physical_persistence.t VALUES(1,repeat('fixture ',1000));`)
	base := pgDesignRead(t, s, "nyadb_physical_persistence", "t")
	for _, persistence := range []string{"u", "p"} {
		draft := base.Schema.Table.Clone()
		draft.Postgres.Persistence = persistence
		base = pgNativeFixtureApplyWithOriginalContext(t, s, base, draft, false)
		if base.Schema.Table.Postgres.Persistence != persistence {
			t.Fatal("fixture did not apply requested heap persistence")
		}
		var correct bool
		if err := s.db.QueryRow(`SELECT bool_and(storage.relpersistence=heap.relpersistence)
FROM pg_class heap JOIN pg_class storage ON storage.oid=heap.reltoastrelid OR storage.oid IN(SELECT indexrelid FROM pg_index WHERE indrelid=heap.reltoastrelid)
WHERE heap.oid='nyadb_physical_persistence.t'::regclass`).Scan(&correct); err != nil || !correct {
			t.Fatal("TOAST storage did not retain its requested heap persistence", err)
		}
	}
}

func TestPostgreSQL17DesignPhysicalRewriteFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_physical;
CREATE TABLE nyadb_physical.t(id integer PRIMARY KEY,label text);
INSERT INTO nyadb_physical.t VALUES(1,repeat('owned fixture ',1000));
ALTER TABLE nyadb_physical.t ADD COLUMN fast numeric(40,20) DEFAULT 1.00000000000000000001;`)
	base := pgDesignRead(t, s, "nyadb_physical", "t")
	before := pgPhysicalRecords(t, base.Schema.Postgres)
	draft := base.Schema.Table.Clone()
	pgNativeEditField(t, &draft, "id", func(f *schemaddl.PostgreSQLField) { f.Type = "bigint" })
	actual := pgNativeFixtureApplyWithOriginalContext(t, s, base, draft, false)
	after := pgPhysicalRecords(t, actual.Schema.Postgres)
	if actual.Schema.Postgres.RelationOID != base.Schema.Postgres.RelationOID {
		t.Fatal("native rewrite changed table identity")
	}
	for oid, a := range before {
		b := after[oid]
		if b == nil {
			t.Logf("native rewrite removed relation OID %d", oid)
			continue
		}
		for _, field := range []string{"relfilenode", "reltoastrelid"} {
			if string(a[field]) != string(b[field]) {
				t.Logf("native rewrite relation OID %d: %s %s -> %s", oid, field, a[field], b[field])
			}
		}
	}
	for oid := range after {
		if before[oid] == nil {
			t.Logf("native rewrite added relation OID %d", oid)
		}
	}
	var exact string
	if err := s.db.QueryRow(`SELECT fast::text FROM nyadb_physical.t`).Scan(&exact); err != nil || exact != "1.00000000000000000001" {
		t.Fatal("rewrite changed precise missing value", err)
	}
}
