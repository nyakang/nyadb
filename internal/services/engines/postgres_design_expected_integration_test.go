package engines

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func pgProbeAbsent(t *testing.T, c *sql.Conn, schema string) {
	t.Helper()
	var count int
	if err := c.QueryRowContext(context.Background(), `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND starts_with(c.relname,'__nyadb_probe_')`, schema).Scan(&count); err != nil || count != 0 {
		t.Fatal("definition validation left probe objects", count, err)
	}
}

func TestPostgreSQL17DesignExpectedDefinitionFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_expected;
CREATE ROLE nyadb_expected_reader;
CREATE TABLE nyadb_expected.original(id bigint GENERATED ALWAYS AS IDENTITY (START WITH 9007199254740993 CACHE 13), label text DEFAULT 'original', amount numeric(40,20));
INSERT INTO nyadb_expected.original(label,amount) VALUES('keep',12345678901234567890.12345678901234567890);
GRANT SELECT ON nyadb_expected.original TO nyadb_expected_reader;
GRANT UPDATE(label) ON nyadb_expected.original TO nyadb_expected_reader;
ALTER TABLE nyadb_expected.original ALTER COLUMN label SET (n_distinct=0.25);
COMMENT ON COLUMN nyadb_expected.original.label IS 'preserved';
CREATE INDEX original_idx ON nyadb_expected.original(label);
COMMENT ON INDEX nyadb_expected.original_idx IS 'index comment'`)
	base := pgDesignRead(t, s, "nyadb_expected", "original")
	draft := base.Schema.Table.Clone()
	pgNativeEditField(t, &draft, "label", func(f *schemaddl.PostgreSQLField) { f.Default = "'changed'::text" })
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	// The probe must restore caller-owned transaction settings, not merely
	// remove its table. Sequence values and existing data stay untouched.
	if _, err := c.ExecContext(context.Background(), "SET LOCAL search_path=nyadb_expected,public"); err != nil {
		t.Fatal(err)
	}
	var initialSearchPath, sequenceBefore string
	if err := c.QueryRowContext(context.Background(), "SHOW search_path").Scan(&initialSearchPath); err != nil {
		t.Fatal(err)
	}
	if err := c.QueryRowContext(context.Background(), "SELECT last_value::text FROM nyadb_expected.original_id_seq").Scan(&sequenceBefore); err != nil {
		t.Fatal(err)
	}
	request := contract.SchemaDesignRequest{Original: base, Desired: draft}
	canonical, err := s.postgresCanonicalDesired(context.Background(), c, request)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.Probe.Postgres.RelationOID == 0 || canonical.Table.Name != "original" || schemaddl.Signature(canonical.Table.Postgres.ACL) != schemaddl.Signature(draft.Postgres.ACL) {
		t.Fatal("independent definition lost target identity/ACL")
	}
	label := pgNativeField(t, &canonical.Table, "label")
	wanted := pgNativeField(t, &draft, "label")
	if schemaddl.Signature(label.Postgres) != schemaddl.Signature(wanted.Postgres) || schemaddl.Signature(request.Desired) != schemaddl.Signature(draft) {
		t.Fatal("probe changed opaque column properties or its request")
	}
	pgProbeAbsent(t, c, "nyadb_expected")
	after, err := postgresCatalogSnapshot(context.Background(), c, base.Target)
	if err != nil || after.Schema.Revision != base.Schema.Revision {
		t.Fatal("definition probe changed original catalog", err)
	}
	var searchPath, sequenceAfter, text, amount string
	if err := c.QueryRowContext(context.Background(), "SHOW search_path").Scan(&searchPath); err != nil || searchPath != initialSearchPath {
		t.Fatal("probe leaked connection settings", searchPath, err)
	}
	if err := c.QueryRowContext(context.Background(), "SELECT last_value::text FROM nyadb_expected.original_id_seq").Scan(&sequenceAfter); err != nil || sequenceBefore != sequenceAfter {
		t.Fatal("probe consumed original sequence", err)
	}
	if err := c.QueryRowContext(context.Background(), "SELECT label,amount::text FROM nyadb_expected.original").Scan(&text, &amount); err != nil || text != "keep" || amount != "12345678901234567890.12345678901234567890" {
		t.Fatal("probe changed original data", err)
	}
	// Both local scope errors and server-side declaration errors return no
	// accepted baseline and preserve the caller's usable transaction.
	wrong := request.Clone()
	wrong.Original.Target.Object.Name = "another"
	if out, err := s.postgresCanonicalDesired(context.Background(), c, wrong); !errors.Is(err, schemaddl.ErrConflict) || out.Table.Name != "" {
		t.Fatal("probe accepted a different captured target", out, err)
	}
	invalid := request.Clone()
	pgNativeEditField(t, &invalid.Desired, "amount", func(f *schemaddl.PostgreSQLField) { f.HasDefault, f.Default = true, "'invalid numeric'" })
	if out, err := s.postgresCanonicalDesired(context.Background(), c, invalid); err == nil || errors.Is(err, ErrUncertain) || out.Table.Name != "" {
		t.Fatal("server rejection yielded an accepted/uncertain definition", out, err)
	}
	pgProbeAbsent(t, c, "nyadb_expected")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err := s.postgresCanonicalDesired(ctx, c, request); err == nil || out.Table.Name != "" {
		t.Fatal("canceled validation produced a baseline", out, err)
	}
	pgProbeAbsent(t, c, "nyadb_expected")
}

func TestPostgreSQL17DesignExpectedPermissionsFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_expected_permissions;
CREATE ROLE nyadb_expected_owner;
GRANT USAGE ON SCHEMA nyadb_expected_permissions TO nyadb_expected_owner;
CREATE TABLE nyadb_expected_permissions.original(value integer);
ALTER TABLE nyadb_expected_permissions.original OWNER TO nyadb_expected_owner`)
	base := pgDesignRead(t, s, "nyadb_expected_permissions", "original")
	draft := base.Schema.Table.Clone()
	pgNativeField(t, &draft, "value").Postgres.Comment = "changed"
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	if _, err := c.ExecContext(context.Background(), "SET LOCAL ROLE nyadb_expected_owner"); err != nil {
		t.Fatal(err)
	}
	out, err := s.postgresCanonicalDesired(context.Background(), c, contract.SchemaDesignRequest{Original: base, Desired: draft})
	if err == nil || errors.Is(err, ErrUncertain) || out.Table.Name != "" {
		t.Fatal("probe silently bypassed missing schema CREATE privilege", out, err)
	}
	pgProbeAbsent(t, c, "nyadb_expected_permissions")
	var user string
	if err := c.QueryRowContext(context.Background(), "SELECT current_user").Scan(&user); err != nil || user != "nyadb_expected_owner" {
		t.Fatal("probe lost caller role/transaction", user, err)
	}
	after, err := postgresCatalogSnapshot(context.Background(), c, base.Target)
	if err != nil || after.Schema.Revision != base.Schema.Revision {
		t.Fatal("permission rejection changed original catalog", err)
	}
}
