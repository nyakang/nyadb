package engines

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func postgresDesignFixture(t *testing.T) *Session {
	t.Helper()
	if os.Getenv("NYADB_POSTGRES_IT") != "1" {
		t.Skip("requires explicitly enabled disposable PostgreSQL fixture")
	}
	port, err := strconv.ParseUint(os.Getenv("NYADB_POSTGRES_PORT"), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager()
	t.Cleanup(func() { _ = m.Close() })
	err = m.Connect(context.Background(), connections.Profile{ID: 91, Kind: connections.PostgreSQL, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_it", Database: "nyadb_it", TLSMode: "disable"}, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.Session(91)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func pgDesignRead(t *testing.T, s *Session, schema, table string) contract.SchemaDesignSnapshot {
	t.Helper()
	op := s.SchemaDesign()
	if op.Snapshot == nil || op.Apply == nil || op.CanApply == nil {
		t.Fatal("PostgreSQL execution binding is incomplete")
	}
	snap, err := op.Snapshot(context.Background(), contract.SchemaDesignTarget{Object: s.Namespace(schema).Object("table", table), Generation: s.Generation()})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}
func pgCatalogHas(p *schemaddl.PostgreSQLCatalog, kind, field string, value any) bool {
	for _, e := range p.Entries {
		if e.Kind != kind {
			continue
		}
		var v map[string]any
		if json.Unmarshal(e.Data, &v) == nil && v[field] == value {
			return true
		}
	}
	return false
}

func TestPostgreSQL17DesignCatalogFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_design; CREATE SCHEMA nyadb_design_external;
CREATE ROLE nyadb_design_reader;
CREATE TYPE nyadb_design_external.mood AS ENUM ('first','second');
CREATE TYPE nyadb_design_external.composite AS (value text);
CREATE FUNCTION nyadb_design_external.mood_integer(nyadb_design_external.mood) RETURNS integer LANGUAGE SQL IMMUTABLE AS $$SELECT 1$$;
CREATE CAST (nyadb_design_external.mood AS integer) WITH FUNCTION nyadb_design_external.mood_integer(nyadb_design_external.mood) AS ASSIGNMENT;
CREATE FUNCTION nyadb_design_external.valid_amount(numeric) RETURNS boolean LANGUAGE SQL IMMUTABLE AS $$SELECT $1>0$$;
CREATE DOMAIN nyadb_design_external.positive AS numeric(40,20) CHECK (nyadb_design_external.valid_amount(VALUE));
CREATE TABLE nyadb_design_external.parent(id bigint PRIMARY KEY);
INSERT INTO nyadb_design_external.parent VALUES (1);
CREATE UNLOGGED TABLE nyadb_design."Full Table" (
 id bigint GENERATED ALWAYS AS IDENTITY (START WITH 101 INCREMENT BY 3 CACHE 13),
 parent_id bigint REFERENCES nyadb_design_external.parent(id) DEFERRABLE INITIALLY DEFERRED,
 amount nyadb_design_external.positive,
 exact numeric(40,20), optional text DEFAULT 'back\slash;value' COLLATE "C",
 payload bytea, mood nyadb_design_external.mood, composite_value nyadb_design_external.composite,
 generated bigint GENERATED ALWAYS AS (parent_id*2) STORED,
 dropped integer,
 CONSTRAINT full_pkey PRIMARY KEY (id), CONSTRAINT exact_positive CHECK (exact>0)
) WITH (fillfactor=75);
ALTER TABLE nyadb_design."Full Table" DROP COLUMN dropped;
ALTER TABLE nyadb_design."Full Table" ALTER COLUMN optional SET STORAGE EXTERNAL;
ALTER TABLE nyadb_design."Full Table" ALTER COLUMN optional SET COMPRESSION pglz;
ALTER TABLE nyadb_design."Full Table" ALTER COLUMN exact SET STATISTICS 73;
ALTER TABLE nyadb_design."Full Table" ADD CONSTRAINT pending_check CHECK (parent_id>0) NOT VALID;
CREATE INDEX expression_idx ON nyadb_design."Full Table" ((lower(optional))) INCLUDE (payload) WHERE optional IS NOT NULL;
COMMENT ON TABLE nyadb_design."Full Table" IS 'table comment';
COMMENT ON COLUMN nyadb_design."Full Table".optional IS 'column comment';
COMMENT ON INDEX nyadb_design.expression_idx IS 'index comment';
GRANT SELECT ON nyadb_design."Full Table" TO nyadb_design_reader;
GRANT UPDATE(optional) ON nyadb_design."Full Table" TO nyadb_design_reader;
ALTER TABLE nyadb_design."Full Table" ENABLE ROW LEVEL SECURITY;
ALTER TABLE nyadb_design."Full Table" FORCE ROW LEVEL SECURITY;
CREATE POLICY visible_rows ON nyadb_design."Full Table" TO nyadb_design_reader USING (parent_id>0) WITH CHECK (parent_id>0);
ALTER TABLE nyadb_design."Full Table" REPLICA IDENTITY FULL;
CREATE VIEW nyadb_design_external.dependent_view AS SELECT optional FROM nyadb_design."Full Table";
CREATE UNLOGGED TABLE nyadb_design_external.incoming_fk(parent_id bigint REFERENCES nyadb_design."Full Table"(id));
CREATE FUNCTION nyadb_design_external.on_change() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RETURN NEW; END$$;
CREATE TRIGGER on_change BEFORE UPDATE ON nyadb_design."Full Table" FOR EACH ROW EXECUTE FUNCTION nyadb_design_external.on_change();
ALTER TABLE nyadb_design."Full Table" DISABLE TRIGGER on_change;
COMMENT ON TRIGGER on_change ON nyadb_design."Full Table" IS 'trigger comment';
CREATE CONSTRAINT TRIGGER deferred_change AFTER UPDATE ON nyadb_design."Full Table" DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nyadb_design_external.on_change();
INSERT INTO nyadb_design."Full Table"(parent_id,amount,exact,optional,payload,mood,composite_value) VALUES(1,1.00000000000000000001,12345678901234567890.12345678901234567890,NULL,decode('00ff','hex'),'first',ROW('text'));
CREATE TABLE nyadb_design.partition_parent(id integer,label text) PARTITION BY RANGE (id);
CREATE TABLE nyadb_design_external.partition_leaf PARTITION OF nyadb_design.partition_parent FOR VALUES FROM (0) TO (10);
CREATE TABLE nyadb_design.inherit_parent(id integer DEFAULT 1);
CREATE TABLE nyadb_design_external.inherit_child(extra text) INHERITS(nyadb_design.inherit_parent);
CREATE OPERATOR FAMILY nyadb_design_external.int_family USING btree;
CREATE OPERATOR CLASS nyadb_design_external.int_ops FOR TYPE integer USING btree FAMILY nyadb_design_external.int_family AS OPERATOR 1 < (integer,integer), OPERATOR 2 <= (integer,integer), OPERATOR 3 = (integer,integer), OPERATOR 4 >= (integer,integer), OPERATOR 5 > (integer,integer), FUNCTION 1 btint4cmp(integer,integer);
CREATE INDEX opclass_index ON nyadb_design.inherit_parent (id nyadb_design_external.int_ops);
CREATE PUBLICATION nyadb_design_publication FOR TABLE nyadb_design.partition_parent;
ALTER DEFAULT PRIVILEGES IN SCHEMA nyadb_design GRANT SELECT ON TABLES TO nyadb_design_reader;
CREATE AGGREGATE nyadb_design.total(bigint) (SFUNC=int8pl,STYPE=bigint,INITCOND='0');`)
	var version int
	if err := s.db.QueryRow("SHOW server_version_num").Scan(&version); err != nil {
		t.Fatal(err)
	}
	publication := "CREATE PUBLICATION nyadb_design_schema_publication FOR TABLES IN SCHEMA nyadb_design"
	if version < 150000 {
		publication = `CREATE PUBLICATION nyadb_design_schema_publication FOR TABLE nyadb_design.inherit_parent`
	}
	run(t, s, publication)
	snapshot := pgDesignRead(t, s, "nyadb_design", "Full Table")
	p := snapshot.Schema.Postgres
	if !snapshot.Schema.Exists || p == nil || p.RelationOID == 0 || snapshot.Schema.Revision == "" || len(snapshot.Schema.Table.Columns()) != 9 {
		t.Fatalf("incomplete catalog: exists=%v table=%+v", snapshot.Schema.Exists, snapshot.Schema.Table)
	}
	props := snapshot.Schema.Table.Postgres
	if props == nil || props.Persistence != "u" || props.Comment != "table comment" || !props.RowSecurity || !props.ForceRowSecurity || props.ReplicaIdentity != "f" || len(props.ACL) == 0 {
		t.Fatal("table properties missing", props)
	}
	for _, check := range []struct {
		kind, field string
		value       any
	}{
		{"column", "attisdropped", true}, {"constraint", "convalidated", false},
		{"relation", "relname", "dependent_view"}, {"relation", "relname", "incoming_fk"},
		{"relation", "relname", "partition_leaf"}, {"relation", "relname", "inherit_child"},
		{"routine", "proname", "on_change"}, {"routine", "proname", "valid_amount"},
		{"routine", "proname", "mood_integer"},
		{"access_method", "amname", "heap"},
		{"operator_class", "opcname", "int_ops"},
		{"operator_family", "opfname", "int_family"},
		{"type", "typname", "mood"}, {"type", "typname", "positive"}, {"type", "typname", "composite"},
		{"enum", "enumlabel", "second"}, {"policy", "polname", "visible_rows"},
		{"publication", "pubname", "nyadb_design_publication"},
		{"publication", "pubname", "nyadb_design_schema_publication"},
	} {
		if !pgCatalogHas(p, check.kind, check.field, check.value) {
			t.Fatalf("catalog omitted %s.%s=%v", check.kind, check.field, check.value)
		}
	}
	if !pgCatalogHas(p, "column", "attname", "value") {
		t.Fatal("external composite type attributes missing")
	}
	if !pgCatalogHas(p, "aggregate", "agginitval", "0") {
		t.Fatal("aggregate attempted function decompilation or omitted full settings")
	}
	if !pgCatalogHas(p, "default_acl", "defaclobjtype", "r") {
		t.Fatal("namespace default ACL missing")
	}
	if len(snapshot.Schema.Table.Definitions) != 3 {
		t.Fatalf("constraint indexes/internal triggers were exposed or user constraint trigger omitted: %+v", snapshot.Schema.Table.Definitions)
	}
	if !strings.Contains(snapshot.Schema.DDL, "GENERATED ALWAYS AS IDENTITY") || !strings.Contains(snapshot.Schema.DDL, "CACHE 13") || !strings.Contains(snapshot.Schema.DDL, "DEFERRABLE INITIALLY DEFERRED") {
		t.Fatal("full structural definition omitted identity or FK", snapshot.Schema.DDL)
	}
	var optional schemaddl.Clause
	for _, c := range snapshot.Schema.Table.Columns() {
		if c.Name == "optional" {
			optional = c
		}
	}
	if optional.Postgres == nil || optional.Postgres.Storage != "e" || optional.Postgres.Compression != "p" || optional.Postgres.Comment != "column comment" || len(optional.Postgres.ACL) == 0 || !strings.Contains(optional.SQL, `COLLATE pg_catalog."C"`) || !strings.Contains(optional.SQL, `back\slash;value`) {
		t.Fatalf("complete field definition/attributes omitted: %+v", optional)
	}
	// Complete ownership closure must be a fixed point: seeding captured
	// TOAST indexes, composite relations, default ACLs, operator members and
	// publication memberships cannot discover new records without DDL.
	stable := snapshot
	for attempt := 0; attempt < 2; attempt++ {
		stable = pgOriginalCapture(t, s, stable)
		if stable.Schema.Revision != snapshot.Schema.Revision {
			for _, e := range stable.Schema.Postgres.Entries {
				if old, exists := pgOriginalFind(snapshot.Schema.Postgres, e.Kind, e.OID, e.SubID); !exists {
					t.Logf("new reread identity: %s OID=%d sub=%d", e.Kind, e.OID, e.SubID)
				} else if string(old.Data) != string(e.Data) {
					t.Logf("changed reread identity: %s OID=%d sub=%d", e.Kind, e.OID, e.SubID)
				}
			}
			t.Fatal("unchanged original seeds expanded the catalog closure")
		}
	}
	// Data, sequence progress and optimizer statistics must not masquerade as DDL.
	run(t, s, `INSERT INTO nyadb_design."Full Table"(parent_id,amount,exact) VALUES(1,2,3); ANALYZE nyadb_design."Full Table"; VACUUM nyadb_design."Full Table";`)
	afterData := pgDesignRead(t, s, "nyadb_design", "Full Table")
	if afterData.Schema.Revision != snapshot.Schema.Revision {
		t.Fatal("ordinary data/ANALYZE/VACUUM changed schema revision")
	}
	changes := []string{
		`COMMENT ON COLUMN nyadb_design."Full Table".optional IS 'changed column'`,
		`ALTER TABLE nyadb_design."Full Table" ENABLE REPLICA TRIGGER on_change`,
		`ALTER POLICY visible_rows ON nyadb_design."Full Table" USING (parent_id>1)`,
		`REVOKE UPDATE(optional) ON nyadb_design."Full Table" FROM nyadb_design_reader`,
		`ALTER SEQUENCE nyadb_design."Full Table_id_seq" CACHE 17`,
		`CREATE OR REPLACE FUNCTION nyadb_design_external.valid_amount(numeric) RETURNS boolean LANGUAGE SQL IMMUTABLE AS $$SELECT $1>1$$`,
		`ALTER TYPE nyadb_design_external.mood ADD VALUE 'third'`,
		`ALTER TYPE nyadb_design_external.composite ADD ATTRIBUTE more text`,
		`CREATE OR REPLACE FUNCTION nyadb_design_external.mood_integer(nyadb_design_external.mood) RETURNS integer LANGUAGE SQL IMMUTABLE AS $$SELECT 2$$`,
		`ALTER OPERATOR FAMILY nyadb_design_external.int_family USING btree ADD FUNCTION 2 (integer,integer) btint4sortsupport(internal)`,
		`ALTER DOMAIN nyadb_design_external.positive ADD CONSTRAINT extra_positive CHECK (VALUE<>999) NOT VALID`,
		`ALTER TABLE nyadb_design_external.parent ADD COLUMN changed text`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA nyadb_design REVOKE SELECT ON TABLES FROM nyadb_design_reader`,
		`ALTER PUBLICATION nyadb_design_schema_publication DROP TABLES IN SCHEMA nyadb_design`,
	}
	if version < 150000 {
		changes[len(changes)-1] = `ALTER PUBLICATION nyadb_design_schema_publication DROP TABLE nyadb_design.inherit_parent`
	}
	previous := afterData.Schema.Revision
	for _, sql := range changes {
		run(t, s, sql)
		next := pgDesignRead(t, s, "nyadb_design", "Full Table")
		if next.Schema.Revision == previous {
			t.Fatal("schema mutation was invisible to revision:", sql)
		}
		previous = next.Schema.Revision
	}
	leaf := pgDesignRead(t, s, "nyadb_design_external", "partition_leaf")
	if !leaf.Schema.Table.Postgres.IsPartition || !strings.Contains(leaf.Schema.DDL, "PARTITION OF") || !strings.Contains(leaf.Schema.DDL, "FOR VALUES FROM (0) TO (10)") {
		t.Fatal("partition identity was replaced by a standalone table")
	}
	missing := pgDesignRead(t, s, "nyadb_design", "missing")
	if missing.Schema.Exists || missing.Schema.Postgres.RelationOID != 0 || missing.Schema.Table.Name != "missing" || missing.Schema.Revision == "" {
		t.Fatal("missing table did not retain the original namespace revision")
	}
	base := contract.SchemaDesignTarget{Object: s.Namespace("nyadb_design").Object("table", "Full Table"), Generation: s.Generation()}
	for _, mutation := range []func(*contract.SchemaDesignTarget){func(x *contract.SchemaDesignTarget) { x.Generation++ }, func(x *contract.SchemaDesignTarget) { x.Object.ProfileID++ }, func(x *contract.SchemaDesignTarget) { x.Object.Catalog = "other" }, func(x *contract.SchemaDesignTarget) { x.Object.Kind = "view" }, func(x *contract.SchemaDesignTarget) { x.Object.Name = strings.Repeat("x", 64) }} {
		x := base
		mutation(&x)
		if _, err := s.SchemaDesign().Snapshot(context.Background(), x); err == nil {
			t.Fatal("invalid original scope accepted", x)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.SchemaDesign().Snapshot(ctx, base); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled catalog read started", err)
	}
	start := time.Now()
	snap, err := s.SchemaDesign().Snapshot(context.Background(), base)
	if err != nil || snap.Schema.Revision == "" || time.Since(start) > 5*time.Second {
		t.Fatal("cancelled read left transaction/connection unusable", err)
	}
	// Preserve per-physical-connection settings and permit cancellation while
	// all connections are occupied; a snapshot must not borrow an editor's lease.
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if c != nil {
			_ = c.Close()
		}
	})
	if _, err = c.ExecContext(context.Background(), "SET search_path = nyadb_design_external"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ExecContext(context.Background(), "SET standard_conforming_strings = off"); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	_ = pgDesignRead(t, s, "nyadb_design", "Full Table")
	c, err = s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var search, standard string
	if err = c.QueryRowContext(context.Background(), "SELECT current_setting('search_path'),current_setting('standard_conforming_strings')").Scan(&search, &standard); err != nil {
		t.Fatal(err)
	}
	if search != "nyadb_design_external" || standard != "off" {
		t.Fatal("snapshot modified pooled connection settings", search, standard)
	}
	blocked, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	_, err = s.SchemaDesign().Snapshot(blocked, base)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("occupied pool did not acknowledge cancellation", err)
	}
	_ = c.Close()
	_ = pgDesignRead(t, s, "nyadb_design", "Full Table")
	s.db.SetMaxOpenConns(4)
	// Publish a concurrent DDL after the first read in this read-only MVCC
	// transaction. The catalog assembly still observes the original commit.
	c, err = s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ExecContext(context.Background(), "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ExecContext(context.Background(), "SET LOCAL search_path = pg_catalog"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ExecContext(context.Background(), "SET LOCAL standard_conforming_strings = on"); err != nil {
		t.Fatal(err)
	}
	q := &postgresSnapshotInterleave{queryer: c, before: func() { run(t, s, `COMMENT ON TABLE nyadb_design."Full Table" IS 'concurrent commit'`) }}
	mvcc, err := postgresCatalogSnapshot(context.Background(), q, base)
	if err != nil {
		t.Fatal(err)
	}
	if mvcc.Schema.Table.Postgres.Comment != "table comment" {
		t.Fatal("snapshot mixed two catalog commits")
	}
	if _, err = c.ExecContext(context.Background(), "COMMIT"); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	if pgDesignRead(t, s, "nyadb_design", "Full Table").Schema.Table.Postgres.Comment != "concurrent commit" {
		t.Fatal("subsequent snapshot did not see committed DDL")
	}
	// A bounded read fails explicitly; it never publishes a truncated routine
	// as an editable baseline, and rollback restores the connection for retry.
	run(t, s, "CREATE FUNCTION nyadb_design.large_source() RETURNS text LANGUAGE SQL AS $body$SELECT '"+strings.Repeat("x", 600<<10)+"'::text$body$")
	if partial, err := s.SchemaDesign().Snapshot(context.Background(), base); err == nil || !strings.Contains(err.Error(), "exceeds") || partial.Schema.Revision != "" || partial.Schema.Postgres != nil {
		t.Fatal("oversized catalog produced a partial baseline", err)
	}
	run(t, s, "DROP FUNCTION nyadb_design.large_source()")
	_ = pgDesignRead(t, s, "nyadb_design", "Full Table")
}

type postgresSnapshotInterleave struct {
	queryer
	before func()
}

func (q *postgresSnapshotInterleave) QueryContext(ctx context.Context, statement string, args ...any) (*sql.Rows, error) {
	if strings.HasPrefix(statement, "WITH RECURSIVE") && q.before != nil {
		before := q.before
		q.before = nil
		before()
	}
	return q.queryer.QueryContext(ctx, statement, args...)
}
