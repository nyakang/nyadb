package engines

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// A rollback-only CREATE is not inherently free of nontransactional effects:
// PostgreSQL may fold a user routine which incorrectly declares IMMUTABLE.
// Production acceptance must reject it before executing the independent probe.
func TestPostgreSQL17DesignProbeFunctionSideEffectFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_probe_effect;
CREATE SEQUENCE nyadb_probe_effect.counter START WITH 41;
CREATE FUNCTION nyadb_probe_effect.effect() RETURNS integer LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN RETURN nextval('nyadb_probe_effect.counter')::integer; END$$;
CREATE TABLE nyadb_probe_effect.original(value integer);
INSERT INTO nyadb_probe_effect.original VALUES(7);`)
	base := pgDesignRead(t, s, "nyadb_probe_effect", "original")
	draft := base.Schema.Table.Clone()
	pgNativeEditField(t, &draft, "value", func(f *schemaddl.PostgreSQLField) {
		f.HasDefault, f.Default = true, "nyadb_probe_effect.effect()"
	})
	draft.Clauses = append(draft.Clauses, schemaddl.Clause{ID: "new:effect", Kind: "constraint", Name: "effect_check", SQL: "CONSTRAINT effect_check CHECK(nyadb_probe_effect.effect()>0)"})
	draft.Definitions = append(draft.Definitions, schemaddl.Definition{Kind: "index", Name: "effect_index", SQL: "CREATE INDEX effect_index ON nyadb_probe_effect.original ((nyadb_probe_effect.effect()))"})
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	var before, after int64
	var calledBefore, calledAfter bool
	if err := c.QueryRowContext(context.Background(), "SELECT last_value,is_called FROM nyadb_probe_effect.counter").Scan(&before, &calledBefore); err != nil {
		t.Fatal(err)
	}
	baseline, probeErr := s.postgresCanonicalDesired(context.Background(), c, contract.SchemaDesignRequest{Original: base, Desired: draft})
	if err := c.QueryRowContext(context.Background(), "SELECT last_value,is_called FROM nyadb_probe_effect.counter").Scan(&after, &calledAfter); err != nil {
		t.Fatal(err)
	}
	pgProbeAbsent(t, c, "nyadb_probe_effect")
	if before != after || calledBefore != calledAfter {
		t.Error("rollback-only definition probe executed a nontransactional user routine")
	}
	if probeErr == nil || baseline.Table.Name != "" || baseline.Probe.Exists {
		t.Error("untrusted user routine produced an accepted independent definition")
	}
	var value int
	if err := c.QueryRowContext(context.Background(), "SELECT value FROM nyadb_probe_effect.original").Scan(&value); err != nil || value != 7 {
		t.Fatal("definition probe changed original rows", err)
	}
}

func TestPostgreSQL17DesignProbeCoreOverloadFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_probe_overload;
CREATE SEQUENCE nyadb_probe_overload.counter START WITH 41;
CREATE FUNCTION pg_catalog.lower(integer) RETURNS text LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN PERFORM nextval('nyadb_probe_overload.counter'); RETURN 'fixture'; END$$;
CREATE TABLE nyadb_probe_overload.original(value integer);
INSERT INTO nyadb_probe_overload.original VALUES(7);`)
	// This function exists only inside this disposable server. Remove it
	// before subsequent tests of normal builtins, while the owned session lives.
	t.Cleanup(func() { run(t, s, "DROP FUNCTION pg_catalog.lower(integer)") })
	base := pgDesignRead(t, s, "nyadb_probe_overload", "original")
	draft := base.Schema.Table.Clone()
	draft.Definitions = append(draft.Definitions, schemaddl.Definition{Kind: "index", Name: "effect_index", SQL: "CREATE INDEX effect_index ON nyadb_probe_overload.original ((lower(value)))"})
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	out, err := s.postgresCanonicalDesired(context.Background(), c, contract.SchemaDesignRequest{Original: base, Desired: draft})
	if err == nil || out.Table.Name != "" || out.Probe.Exists {
		t.Fatal("builtin name accepted a user-created overload")
	}
	var called bool
	if err := c.QueryRowContext(context.Background(), "SELECT is_called FROM nyadb_probe_overload.counter").Scan(&called); err != nil || called {
		t.Fatal("core overload check invoked the fixture routine", err)
	}
	pgProbeAbsent(t, c, "nyadb_probe_overload")
}
