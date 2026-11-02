package engines

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestPostgreSQL17DesignRenameCatalogFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_rename; CREATE SCHEMA nyadb_rename_external;
CREATE ROLE nyadb_rename_reader;
CREATE TABLE nyadb_rename.original (
 id bigint GENERATED ALWAYS AS IDENTITY (START WITH 9007199254740993 CACHE 13) PRIMARY KEY,
 label text DEFAULT 'preserved', amount numeric(40,20), payload bytea,
 generated integer GENERATED ALWAYS AS (length(label)) STORED,
 parent_id bigint REFERENCES nyadb_rename.original(id),
 ref regclass DEFAULT 'nyadb_rename.original'::regclass,
 CONSTRAINT label_check CHECK(length(label)>0)
);
INSERT INTO nyadb_rename.original(amount,payload) VALUES(12345678901234567890.12345678901234567890,decode('00ff','hex'));
CREATE TABLE nyadb_rename.child(more text) INHERITS (nyadb_rename.original);
CREATE INDEX simple_idx ON nyadb_rename.original(label) INCLUDE(id);
CREATE INDEX expression_idx ON nyadb_rename.original((lower(label))) WHERE label<>'';
COMMENT ON TABLE nyadb_rename.original IS 'table';
COMMENT ON COLUMN nyadb_rename.original.label IS 'column';
COMMENT ON INDEX nyadb_rename.simple_idx IS 'index';
COMMENT ON CONSTRAINT label_check ON nyadb_rename.original IS 'constraint';
GRANT SELECT ON nyadb_rename.original TO nyadb_rename_reader;
GRANT UPDATE(label) ON nyadb_rename.original TO nyadb_rename_reader;
ALTER TABLE nyadb_rename.original ENABLE ROW LEVEL SECURITY;
ALTER TABLE nyadb_rename.original FORCE ROW LEVEL SECURITY;
CREATE POLICY visible ON nyadb_rename.original USING(length(label)>0);
CREATE VIEW nyadb_rename_external.outside_view AS SELECT label,amount,payload FROM nyadb_rename.original;
CREATE TABLE nyadb_rename_external.incoming(id bigint REFERENCES nyadb_rename.original(id));
CREATE TABLE nyadb_rename_external.composites(value nyadb_rename.original, array_values nyadb_rename.original[]);
CREATE FUNCTION nyadb_rename_external.parsed_reader() RETURNS text LANGUAGE SQL RETURN (SELECT label FROM nyadb_rename.original LIMIT 1);
CREATE FUNCTION nyadb_rename_external.on_change() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RETURN NEW; END$$;
CREATE TRIGGER on_change BEFORE UPDATE OF label ON nyadb_rename.original FOR EACH ROW WHEN (NEW.label<>OLD.label) EXECUTE FUNCTION nyadb_rename_external.on_change('original','label');
COMMENT ON TRIGGER on_change ON nyadb_rename.original IS 'trigger';
ALTER TABLE nyadb_rename.original ENABLE REPLICA TRIGGER on_change;
CREATE TYPE nyadb_rename._renamed AS ENUM ('collision')`)
	base := pgDesignRead(t, s, "nyadb_rename", "original")
	if !pgCatalogHas(base.Schema.Postgres, "relation", "relname", "composites") || !pgCatalogHas(base.Schema.Postgres, "routine", "proname", "parsed_reader") {
		t.Fatal("rename revision omitted outside row-type/SQL-function dependencies")
	}
	var identity, rowID, sequence string
	if err := s.db.QueryRow(`SELECT id::text,ctid::text FROM ONLY nyadb_rename.original`).Scan(&identity, &rowID); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT last_value::text||':'||is_called::text FROM nyadb_rename.original_id_seq`).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	r := contract.SchemaDesignRequest{Original: base, Desired: base.Schema.Table.Clone()}
	r.Desired.Name = "renamed"
	out := s.postgresDesignRenameApply(context.Background(), r)
	if out.Error != nil || out.State != "committed" || !out.Verified || out.Actual == nil {
		t.Fatalf("complete table rename failed: %+v", out)
	}
	if err := schemaddl.VerifyPostgreSQLRename(base.Schema, r.Desired, out.Actual.Schema, "nyadb_rename"); err != nil {
		t.Fatal(err)
	}
	var arrayType string
	if err := s.db.QueryRow(`SELECT t.typname FROM pg_type t JOIN pg_type b ON b.typarray=t.oid WHERE b.typrelid='nyadb_rename.renamed'::regclass`).Scan(&arrayType); err != nil || arrayType != "_renamed_1" {
		t.Fatal("rename fixture did not exercise array type name collision", arrayType, err)
	}
	base = *out.Actual
	r = contract.SchemaDesignRequest{Original: base, Desired: base.Schema.Table.Clone()}
	pgNativeField(t, &r.Desired, "label").Name = "renamed_label"
	out = s.postgresDesignRenameApply(context.Background(), r)
	if out.Error != nil || out.State != "committed" || !out.Verified || out.Actual == nil {
		t.Fatalf("complete column rename failed: %+v", out)
	}
	var id, ctid, last, label, amount, payload, reader string
	var generated int
	if err := s.db.QueryRow(`SELECT id::text,ctid::text,renamed_label,amount::text,encode(payload,'hex'),generated FROM ONLY nyadb_rename.renamed`).Scan(&id, &ctid, &label, &amount, &payload, &generated); err != nil || id != identity || ctid != rowID || label != "preserved" || amount != "12345678901234567890.12345678901234567890" || payload != "00ff" || generated != len(label) {
		t.Fatal("native rename changed retained rows or values", err)
	}
	if err := s.db.QueryRow(`SELECT last_value::text||':'||is_called::text FROM nyadb_rename.original_id_seq`).Scan(&last); err != nil || last != sequence {
		t.Fatal("native rename consumed/reset identity sequence", err)
	}
	if err := s.db.QueryRow(`SELECT nyadb_rename_external.parsed_reader()`).Scan(&reader); err != nil || reader != label {
		t.Fatal("native rename broke parsed outside SQL routine", err)
	}
	if err := s.db.QueryRow(`SELECT label FROM nyadb_rename_external.outside_view`).Scan(&reader); err != nil || reader != label {
		t.Fatal("native rename changed outside view output identity", err)
	}
	var childName string
	if err := s.db.QueryRow(`SELECT attname FROM pg_attribute WHERE attrelid='nyadb_rename.child'::regclass AND attnum=2`).Scan(&childName); err != nil || childName != "renamed_label" {
		t.Fatal("native rename did not propagate to qualified inherited child", childName, err)
	}
	// Renaming to another type's autogenerated array name moves only that
	// array, then allocates the renamed row type's own unique array name.
	run(t, s, `CREATE TYPE nyadb_rename.other AS ENUM ('preserved base')`)
	base = pgDesignRead(t, s, "nyadb_rename", "renamed")
	r = contract.SchemaDesignRequest{Original: base, Desired: base.Schema.Table.Clone()}
	r.Desired.Name = "_other"
	out = s.postgresDesignRenameApply(context.Background(), r)
	if out.Error != nil || out.State != "committed" || !out.Verified || out.Actual == nil {
		t.Fatal("rename could not preserve a conflicting associated array", out)
	}
	var displaced string
	if err := s.db.QueryRow(`SELECT a.typname FROM pg_type a JOIN pg_type b ON b.typarray=a.oid JOIN pg_namespace n ON n.oid=b.typnamespace WHERE b.typname='other' AND n.nspname='nyadb_rename'`).Scan(&displaced); err != nil || displaced != "__other" {
		t.Fatal("rename changed the wrong conflicting type", displaced, err)
	}
	if err := s.db.QueryRow(`SELECT t.typname FROM pg_type t JOIN pg_type b ON b.typarray=t.oid WHERE b.typrelid='nyadb_rename._other'::regclass`).Scan(&arrayType); err != nil || arrayType != "__other_1" {
		t.Fatal("row type array failed to avoid the moved array", arrayType, err)
	}
	// The row type can itself take its own array's name. PostgreSQL moves
	// that array once and skips a second recursive array rename.
	base = *out.Actual
	r = contract.SchemaDesignRequest{Original: base, Desired: base.Schema.Table.Clone()}
	r.Desired.Name = arrayType
	out = s.postgresDesignRenameApply(context.Background(), r)
	if out.Error != nil || out.State != "committed" || !out.Verified || out.Actual == nil {
		t.Fatal("rename failed while claiming its own array name", out)
	}
	if err := s.db.QueryRow(`SELECT nyadb_rename_external.parsed_reader()`).Scan(&reader); err != nil || reader != label {
		t.Fatal("array-name renames broke the original outside routine", err)
	}
}
