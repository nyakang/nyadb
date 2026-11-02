package engines

import (
	"context"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func pgValuesRead(t *testing.T, s *Session, c queryer, name string) contract.SchemaDesignSnapshot {
	t.Helper()
	snapshot, err := postgresCatalogSnapshot(context.Background(), c, contract.SchemaDesignTarget{Object: s.Namespace("nyadb_value_proof").Object("table", name), Generation: s.Generation()})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestPostgreSQL17DesignValueProofFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_value_proof;
CREATE DOMAIN nyadb_value_proof.amount AS numeric(40,20);
CREATE TYPE nyadb_value_proof.color AS ENUM('first','second');
CREATE TYPE nyadb_value_proof.composite AS(value numeric(20,5),label text);
CREATE TABLE nyadb_value_proof.values_table(
 id integer, exact numeric(40,20), label varchar(100), payload bytea, nullable text,
 values_array integer[], domain_value nyadb_value_proof.amount, color nyadb_value_proof.color,
 composite_value nyadb_value_proof.composite, day date, moment timestamptz, document json,
 uuid_value uuid, float4_value real, float8_value double precision
);
INSERT INTO nyadb_value_proof.values_table SELECT 2,12345678901234567890.12345678901234567890,'text',decode('00ff','hex'),NULL,
 '[2:3]={1,NULL}'::integer[],1.00000000000000000001,'first',ROW(1.25000,'composite')::nyadb_value_proof.composite,
 '2026-01-01'::date,'2026-01-01 00:00:00+08'::timestamptz,' { "n":9007199254740993 } '::json,
 '00000000-0000-0000-0000-000000000001'::uuid,0.5::real,0.5::double precision FROM generate_series(1,2);
CREATE TABLE nyadb_value_proof.array_only(values_array smallint[]);
INSERT INTO nyadb_value_proof.array_only VALUES('[0:1]={1,NULL}');`)
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	base := pgValuesRead(t, s, c, "values_table")
	var columns []string
	for _, column := range base.Schema.Table.Columns() {
		columns = append(columns, column.Name)
	}
	original, err := postgresValueDigest(context.Background(), c, base.Target.Object, columns, base.Schema.Postgres)
	if err != nil || original.Rows != 2 {
		t.Fatal("full binary values unavailable", original, err)
	}
	array := pgValuesRead(t, s, c, "array_only")
	if digest, err := postgresValueDigest(context.Background(), c, array.Target.Object, []string{"values_array"}, array.Schema.Postgres); err != nil || digest.Rows != 1 {
		t.Fatal("built-in array element was absent from complete catalog", digest, err)
	}
	if _, err = c.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	for _, query := range []string{
		`ALTER TABLE nyadb_value_proof.values_table ALTER COLUMN id TYPE bigint USING id::bigint`,
		`ALTER TABLE nyadb_value_proof.values_table ALTER COLUMN exact TYPE numeric(50,20) USING exact::numeric(50,20)`,
		`ALTER TABLE nyadb_value_proof.values_table ALTER COLUMN label TYPE text USING label::text`,
		`ALTER TABLE nyadb_value_proof.values_table ALTER COLUMN values_array TYPE bigint[] USING values_array::bigint[]`,
		`ALTER TABLE nyadb_value_proof.values_table ALTER COLUMN float4_value TYPE double precision USING float4_value::double precision`,
	} {
		if _, err = c.ExecContext(context.Background(), query); err != nil {
			t.Fatal(err)
		}
	}
	actual := pgValuesRead(t, s, c, "values_table")
	widened, err := postgresValueDigest(context.Background(), c, actual.Target.Object, columns, actual.Schema.Postgres)
	if err != nil || widened != original {
		t.Fatal("lossless widening changed precise or structured values", err)
	}
	if _, err = c.ExecContext(context.Background(), `ALTER TABLE nyadb_value_proof.values_table ALTER COLUMN exact TYPE numeric(50,10) USING exact::numeric(50,10)`); err != nil {
		t.Fatal(err)
	}
	actual = pgValuesRead(t, s, c, "values_table")
	lossy, err := postgresValueDigest(context.Background(), c, actual.Target.Object, columns, actual.Schema.Postgres)
	if err != nil || lossy == original {
		t.Fatal("successful numeric rounding evaded binary verification", err)
	}
	if _, err = c.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	actual = pgValuesRead(t, s, c, "values_table")
	restored, err := postgresValueDigest(context.Background(), c, actual.Target.Object, columns, actual.Schema.Postgres)
	if err != nil || restored != original {
		t.Fatal("rollback did not preserve original values", err)
	}
	// Equal row values count separately; deleting one duplicate changes the
	// multiset count and checksum without needing an editable primary key.
	if _, err = c.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ExecContext(context.Background(), `DELETE FROM nyadb_value_proof.values_table WHERE ctid=(SELECT ctid FROM nyadb_value_proof.values_table LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	deleted, err := postgresValueDigest(context.Background(), c, actual.Target.Object, columns, actual.Schema.Postgres)
	if err != nil || deleted.Rows != 1 || deleted == original {
		t.Fatal("duplicate row loss was hidden", err)
	}
	if _, err = c.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	// A cell/row limit is an explicit error, not an accepted truncated digest.
	run(t, s, `CREATE TABLE nyadb_value_proof.large_values(value text); INSERT INTO nyadb_value_proof.large_values VALUES(repeat('x',1048577))`)
	large := pgValuesRead(t, s, c, "large_values")
	if partial, err := postgresValueDigest(context.Background(), c, large.Target.Object, []string{"value"}, large.Schema.Postgres); err == nil || partial != (postgresDesignDigest{}) {
		t.Fatal("oversized cell yielded a partial accepted fingerprint", err)
	}
	run(t, s, `UPDATE nyadb_value_proof.large_values SET value=repeat('x',17825792)`)
	if partial, err := postgresValueDigest(context.Background(), c, large.Target.Object, []string{"value"}, large.Schema.Postgres); err == nil || partial != (postgresDesignDigest{}) {
		t.Fatal("oversized row yielded accepted truncated values", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := postgresValueDigest(ctx, c, base.Target.Object, columns, base.Schema.Postgres); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled binary read started", err)
	}
}
