package schemaddl

import (
	"encoding/json"
	"strings"
	"testing"
)

func pgRecord(kind string, oid int64, sub int, raw string) PostgreSQLCatalogEntry {
	return PostgreSQLCatalogEntry{Kind: kind, OID: oid, SubID: sub, Data: json.RawMessage(raw)}
}
func pgCatalogFixture() PostgreSQLCatalog {
	return PostgreSQLCatalog{ServerVersion: 170000, Database: "user", Namespace: "demo", DatabaseOID: 12, NamespaceOID: 14, RelationOID: 16, Entries: []PostgreSQLCatalogEntry{
		pgRecord("relation", 16, 0, `{"relname":"table\"quoted","relkind":"r","relpersistence":"p","owner_name":"owner","access_method":"heap","reloptions":["fillfactor=75"],"relacl":["reader=r/owner"],"comment":"table comment"}`),
		pgRecord("column", 16, 2, `{"attnum":2,"attname":"generated","type_sql":"bigint","attgenerated":"s","default_sql":"(id * 2)","attstorage":"p","attstattarget":-1,"attislocal":true}`),
		pgRecord("column", 16, 1, `{"attnum":1,"attname":"id","type_sql":"bigint","attidentity":"a","attnotnull":true,"attstorage":"p","attstattarget":100,"attacl":["reader=r/owner"],"comment":"identity comment","identity_sequence":{"schema":"demo","name":"seq\"quoted","seqstart":9007199254740993,"seqincrement":-3,"seqmin":-9007199254740993,"seqmax":9007199254740999,"seqcache":13,"seqcycle":true}}`),
		pgRecord("constraint", 20, 0, `{"relation_oid":16,"name":"pkey","definition":"PRIMARY KEY (id)","contype":"p"}`),
		pgRecord("index", 30, 0, `{"relation_oid":16,"name":"pkey","definition":"CREATE UNIQUE INDEX pkey ON demo.t(id)","constraint_oid":20}`),
		pgRecord("index", 31, 0, `{"relation_oid":16,"name":"expr_idx","definition":"CREATE INDEX expr_idx ON demo.t ((id+1)) WHERE id>0","constraint_oid":0,"comment":"index comment"}`),
		pgRecord("trigger", 40, 0, `{"relation_oid":16,"name":"user_constraint","definition":"CREATE CONSTRAINT TRIGGER user_constraint AFTER UPDATE ON demo.t DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION demo.fn()","internal":false,"constraint_oid":41,"enabled":"R","comment":"trigger comment"}`),
		pgRecord("constraint", 41, 0, `{"relation_oid":16,"name":"user_constraint","definition":"TRIGGER DEFERRABLE INITIALLY DEFERRED","contype":"t"}`),
		pgRecord("trigger", 42, 0, `{"relation_oid":16,"name":"internal_fk","definition":"CREATE TRIGGER internal_fk AFTER UPDATE ON demo.t FOR EACH ROW EXECUTE FUNCTION pg_catalog.fn()","internal":true,"constraint_oid":21}`),
	}}
}

func TestPostgreSQLCatalogKeepsIdentitySettingsStableFieldsAndUserConstraintTrigger(t *testing.T) {
	p := pgCatalogFixture()
	table, err := p.Table()
	if err != nil {
		t.Fatal(err)
	}
	cols := table.Columns()
	if len(cols) != 2 || cols[0].ID != "column:1" || cols[1].ID != "column:2" {
		t.Fatalf("stable ordered fields: %+v", cols)
	}
	for _, part := range []string{`GENERATED ALWAYS AS IDENTITY`, `SEQUENCE NAME "demo"."seq""quoted"`, "START WITH 9007199254740993", "INCREMENT BY -3", "CACHE 13 CYCLE", "NOT NULL"} {
		if !strings.Contains(cols[0].SQL, part) {
			t.Fatalf("identity settings lost %s: %s", part, cols[0].SQL)
		}
	}
	if cols[0].Postgres.Comment != "identity comment" || cols[0].Postgres.ACL[0] != "reader=r/owner" || cols[0].Postgres.Statistics != 100 || !cols[1].Postgres.Local {
		t.Fatal("column metadata was omitted")
	}
	if cols[1].SQL != "bigint GENERATED ALWAYS AS ((id * 2)) STORED" {
		t.Fatal(cols[1].SQL)
	}
	if len(table.Definitions) != 2 || table.Definitions[0].Name != "expr_idx" || table.Definitions[1].Postgres.Enabled != "R" {
		t.Fatalf("constraint-owned index/internal trigger leaked, or user trigger missing: %+v", table.Definitions)
	}
	ddl, err := PostgreSQLStructuralDDL(table, "demo")
	if err != nil || !strings.Contains(ddl, `"table""quoted"`) || !strings.Contains(ddl, "WITH (fillfactor=75)") {
		t.Fatalf("canonical declaration %s %v", ddl, err)
	}
}

func TestPostgreSQLCatalogFingerprintPreservesLargeNumbersAndNormalizesRecordOrder(t *testing.T) {
	p := pgCatalogFixture()
	p.Entries = append(p.Entries, pgRecord("unknown", 55, 0, `{"big":9007199254740993,"new_property":{"b":true,"a":7}}`))
	first, err := p.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	q := p.Clone()
	q.Entries[len(q.Entries)-1].Data = json.RawMessage(`{ "new_property": {"a":7,"b":true}, "big":9007199254740993 }`)
	for i, j := 0, len(q.Entries)-1; i < j; i, j = i+1, j-1 {
		q.Entries[i], q.Entries[j] = q.Entries[j], q.Entries[i]
	}
	second, err := q.Fingerprint()
	if err != nil || first != second {
		t.Fatal("row order or JSON formatting changed revision", err)
	}
	q.Entries[0].Data = json.RawMessage(`{"big":9007199254740992,"new_property":{"b":true,"a":7}}`)
	third, err := q.Fingerprint()
	if err != nil || third == first {
		t.Fatal("catalog numbers were rounded", err)
	}
}

func TestPostgreSQLCatalogCloneIsolatesRecordsAndEditableProperties(t *testing.T) {
	p := pgCatalogFixture()
	table, err := p.Table()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := Snapshot{Engine: "postgresql", Table: table, Postgres: &p}
	x := snapshot.Clone()
	x.Postgres.Entries[0].Data[0] = '!'
	x.Table.Postgres.Parameters[0] = "changed"
	x.Table.Postgres.ACL[0] = "changed"
	x.Table.Clauses[0].Postgres.ACL[0] = "changed"
	x.Table.Definitions[1].Postgres.Enabled = "D"
	if snapshot.Postgres.Entries[0].Data[0] != '{' || snapshot.Table.Postgres.Parameters[0] != "fillfactor=75" || snapshot.Table.Postgres.ACL[0] != "reader=r/owner" || snapshot.Table.Clauses[0].Postgres.ACL[0] != "reader=r/owner" || snapshot.Table.Definitions[1].Postgres.Enabled != "R" {
		t.Fatal("worker clone shared mutable original metadata")
	}
}

func TestPostgreSQLCatalogStatisticsDefaultDoesNotDisableAnalysis(t *testing.T) {
	for _, sample := range []struct {
		raw  string
		want int
	}{{"null", -1}, {"-1", -1}, {"0", 0}, {"73", 73}} {
		p := pgCatalogFixture()
		for i, entry := range p.Entries {
			if entry.Kind != "column" || entry.SubID != 2 {
				continue
			}
			var data map[string]json.RawMessage
			if err := json.Unmarshal(entry.Data, &data); err != nil {
				t.Fatal(err)
			}
			data["attstattarget"] = json.RawMessage(sample.raw)
			p.Entries[i].Data, _ = json.Marshal(data)
		}
		table, err := p.Table()
		if err != nil || table.Columns()[1].Postgres.Statistics != sample.want {
			t.Fatal("statistics default/zero confused", sample.raw, err)
		}
	}
}

func TestPostgreSQLCatalogRejectsIncompleteRecordsAndTruncatedIdentifiers(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{} {}`, `{} trailing`, string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}), strings.Repeat("x", 1<<20+1)} {
		p := pgCatalogFixture()
		p.Entries[0].Data = json.RawMessage(raw)
		if _, err := p.Fingerprint(); err == nil {
			t.Fatalf("invalid catalog accepted %q", raw[:min(40, len(raw))])
		}
	}
	p := pgCatalogFixture()
	p.Entries = append(p.Entries, p.Entries[0])
	if _, err := p.Fingerprint(); err == nil {
		t.Fatal("duplicate stable catalog identity accepted")
	}
	for _, name := range []string{strings.Repeat("a", 64), strings.Repeat("字", 22), "\x00"} {
		if PostgreSQLIdentifier(name) == nil {
			t.Fatal("truncated PostgreSQL identifier accepted")
		}
	}
	if PostgreSQLIdentifier(strings.Repeat("字", 21)) != nil {
		t.Fatal("valid 63-byte identifier refused")
	}
}

func TestPostgreSQLPartitionDeclarationUsesCapturedParentAndBound(t *testing.T) {
	p := pgCatalogFixture()
	p.Entries[0].Data = json.RawMessage(`{"relname":"leaf","relkind":"r","relpersistence":"p","relispartition":true,"parents":["\"outside\".\"parent\""],"partition_bound":"FOR VALUES FROM (1) TO (10)","access_method":"heap"}`)
	table, err := p.Table()
	if err != nil {
		t.Fatal(err)
	}
	ddl, err := PostgreSQLStructuralDDL(table, "demo")
	if err != nil || !strings.Contains(ddl, `PARTITION OF "outside"."parent" FOR VALUES FROM (1) TO (10)`) {
		t.Fatalf("partition became an ordinary standalone table: %s %v", ddl, err)
	}
}

func TestSQLiteCannotAcceptPostgreSQLPropertyDrafts(t *testing.T) {
	base, err := ParseSQLite("CREATE TABLE t(id INTEGER)")
	if err != nil {
		t.Fatal(err)
	}
	for _, attach := range []func(*Table){func(t *Table) { t.Postgres = &PostgreSQLTableProperties{} }, func(t *Table) { t.Clauses[0].Postgres = &PostgreSQLColumnProperties{} }, func(t *Table) {
		t.Definitions = []Definition{{Kind: "index", Name: "i", SQL: "CREATE INDEX i ON t(id)", Postgres: &PostgreSQLDefinitionProperties{}}}
	}} {
		draft := base.Clone()
		attach(&draft)
		if _, err := CreateSQLite(draft, "main", "t"); err == nil {
			t.Fatal("SQLite ignored foreign engine properties")
		}
	}
}
