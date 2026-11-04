package engines

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestPostgreSQL17ObjectOperationsFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	ctx := context.Background()
	if _, e := s.db.Exec(`CREATE SCHEMA nyadb_objects; CREATE TABLE nyadb_objects.original(id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, legacy serial UNIQUE, parent bigint REFERENCES nyadb_objects.original(id), value numeric(30,8) NOT NULL, computed numeric GENERATED ALWAYS AS (value*2) STORED); CREATE INDEX original_value ON nyadb_objects.original(value); COMMENT ON TABLE nyadb_objects.original IS 'copied table'; COMMENT ON COLUMN nyadb_objects.original.value IS 'exact value'; INSERT INTO nyadb_objects.original(parent,value) VALUES(NULL,9007199254740993.12345678),(1,2.3); CREATE FUNCTION nyadb_objects.modify_insert() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN NEW.value=NEW.value+1; RETURN NEW; END$$; CREATE TRIGGER original_insert BEFORE INSERT ON nyadb_objects.original FOR EACH ROW EXECUTE FUNCTION nyadb_objects.modify_insert()`); e != nil {
		t.Fatal(e)
	}
	defer s.db.Exec("DROP SCHEMA nyadb_objects CASCADE")
	if _, e := s.db.Exec(`COMMENT ON CONSTRAINT original_pkey ON nyadb_objects.original IS 'primary key comment'; COMMENT ON INDEX nyadb_objects.original_pkey IS 'backing index comment'; COMMENT ON INDEX nyadb_objects.original_value IS 'standalone index comment'; COMMENT ON TRIGGER original_insert ON nyadb_objects.original IS 'trigger comment'; COMMENT ON SEQUENCE nyadb_objects.original_id_seq IS 'identity sequence comment'; COMMENT ON SEQUENCE nyadb_objects.original_legacy_seq IS 'serial sequence comment'`); e != nil {
		t.Fatal(e)
	}
	original := pgDesignRead(t, s, "nyadb_objects", "original")
	request := contract.SchemaCopyRequest{Original: original, Destination: "copied", WithData: true, RebindSelfReferences: true}
	plan, e := schemaddl.PlanPostgreSQLCopy(original.Schema, request.Destination, true, true)
	if e != nil {
		t.Fatal(e)
	}
	out := s.SchemaDesign().Copy(ctx, request)
	if out.Error != nil || !out.Verified || out.State != "committed" {
		t.Fatalf("copy %+v\nplan=%v", out, plan.SQL)
	}
	var exact, comment string
	if e = s.db.QueryRow("SELECT value::text,obj_description('nyadb_objects.copied'::regclass) FROM nyadb_objects.copied WHERE id=1").Scan(&exact, &comment); e != nil || exact != "9007199254740993.12345678" || comment != "copied table" {
		t.Fatal("copied data/comment or trigger timing", exact, comment, e)
	}
	for _, check := range []struct{ sql, want string }{
		{`SELECT col_description('nyadb_objects.copied'::regclass,attnum) FROM pg_attribute WHERE attrelid='nyadb_objects.copied'::regclass AND attname='value'`, "exact value"},
		{`SELECT obj_description(oid,'pg_constraint') FROM pg_constraint WHERE conrelid='nyadb_objects.copied'::regclass AND contype='p'`, "primary key comment"},
		{`SELECT obj_description(indexrelid,'pg_class') FROM pg_index WHERE indrelid='nyadb_objects.copied'::regclass AND indisprimary`, "backing index comment"},
		{`SELECT obj_description(indexrelid,'pg_class') FROM pg_index WHERE indrelid='nyadb_objects.copied'::regclass AND NOT indisunique`, "standalone index comment"},
		{`SELECT obj_description(oid,'pg_trigger') FROM pg_trigger WHERE tgrelid='nyadb_objects.copied'::regclass AND NOT tgisinternal`, "trigger comment"},
		{`SELECT obj_description(pg_get_serial_sequence('nyadb_objects.copied','id')::regclass,'pg_class')`, "identity sequence comment"},
		{`SELECT obj_description(pg_get_serial_sequence('nyadb_objects.copied','legacy')::regclass,'pg_class')`, "serial sequence comment"},
	} {
		if e = s.db.QueryRow(check.sql).Scan(&comment); e != nil || comment != check.want {
			t.Fatalf("lost copied metadata: %s: got %q want %q: %v", check.sql, comment, check.want, e)
		}
	}
	var sourceSeq, copySeq string
	for _, column := range []string{"id", "legacy"} {
		if e = s.db.QueryRow("SELECT pg_get_serial_sequence('nyadb_objects.original',$1),pg_get_serial_sequence('nyadb_objects.copied',$1)", column).Scan(&sourceSeq, &copySeq); e != nil || sourceSeq == copySeq || copySeq == "" {
			t.Fatal("sequence ownership", sourceSeq, copySeq, e)
		}
	}
	var id int
	if e = s.db.QueryRow("INSERT INTO nyadb_objects.copied(value) VALUES(8) RETURNING id").Scan(&id); e != nil || id != 3 {
		t.Fatal("copied sequence state", id, e)
	}
	if e = s.db.QueryRow("SELECT value::text FROM nyadb_objects.copied WHERE id=3").Scan(&exact); e != nil || exact != "9.00000000" {
		t.Fatal("trigger not installed", exact, e)
	}
	original = pgDesignRead(t, s, "nyadb_objects", "original")
	if out = s.SchemaDesign().Drop(ctx, original); out.Error != nil || out.State != "committed" {
		t.Fatal("self FK was not rebound", out)
	}
	viewTarget := contract.SchemaDesignTarget{Object: s.Namespace("nyadb_objects").Object("view", "summary"), Generation: s.Generation()}
	ops := s.ViewDesign()
	base, e := ops.Snapshot(ctx, viewTarget)
	if e != nil {
		t.Fatal(e)
	}
	v := schemaddl.View{Schema: "nyadb_objects", Name: "summary", SelectSQL: "SELECT id,value FROM nyadb_objects.copied"}
	result := ops.Apply(ctx, contract.ViewDesignRequest{Original: base, Action: "create", Desired: v})
	if result.Error != nil || !result.Verified {
		t.Fatal(result)
	}
	if _, e = s.db.Exec("CREATE VIEW nyadb_objects.dependent AS SELECT id FROM nyadb_objects.summary"); e != nil {
		t.Fatal(e)
	}
	base, e = ops.Snapshot(ctx, viewTarget)
	if e != nil {
		t.Fatal(e)
	}
	v.SelectSQL = "SELECT id,(value+1)::numeric(30,8) AS value FROM nyadb_objects.copied"
	result = ops.Apply(ctx, contract.ViewDesignRequest{Original: base, Action: "alter", Desired: v})
	if result.Error != nil || !result.Verified {
		t.Fatal(result)
	}
	base, e = ops.Snapshot(ctx, viewTarget)
	if e != nil {
		t.Fatal(e)
	}
	v.SelectSQL = "SELECT value FROM nyadb_objects.copied"
	result = ops.Apply(ctx, contract.ViewDesignRequest{Original: base, Action: "alter", Desired: v})
	if result.Error == nil || result.State != "rolled_back" {
		t.Fatal("incompatible view columns accepted", result)
	}
	result = ops.Apply(ctx, contract.ViewDesignRequest{Original: base, Action: "drop"})
	if result.Error == nil {
		t.Fatal("view drop cascaded", result)
	}
}
