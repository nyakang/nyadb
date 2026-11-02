package engines

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestPostgreSQL17DesignProbeDomainEffectFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_probe_domain;
CREATE SEQUENCE nyadb_probe_domain.counter START WITH 41;
CREATE FUNCTION nyadb_probe_domain.effect(integer) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN PERFORM nextval('nyadb_probe_domain.counter'); RETURN true; END$$;
CREATE DOMAIN nyadb_probe_domain.guarded AS integer CHECK(nyadb_probe_domain.effect(VALUE));
CREATE TABLE nyadb_probe_domain.original(value integer);
INSERT INTO nyadb_probe_domain.original VALUES(7);`)
	base := pgDesignRead(t, s, "nyadb_probe_domain", "original")
	draft := base.Schema.Table.Clone()
	draft.Definitions = append(draft.Definitions, schemaddl.Definition{Kind: "index", Name: "effect_index", SQL: `CREATE INDEX effect_index ON nyadb_probe_domain.original ((CAST('1' AS nyadb_probe_domain.guarded)))`})
	pgAssertTypeProbeRejected(t, s, base, draft, "nyadb_probe_domain")
}

func TestPostgreSQL17DesignProbeCastEffectFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_probe_cast;
CREATE SEQUENCE nyadb_probe_cast.counter START WITH 41;
CREATE TYPE nyadb_probe_cast.mood AS ENUM('first','second');
CREATE FUNCTION nyadb_probe_cast.effect(nyadb_probe_cast.mood) RETURNS integer LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN PERFORM nextval('nyadb_probe_cast.counter'); RETURN 1; END$$;
CREATE CAST(nyadb_probe_cast.mood AS integer) WITH FUNCTION nyadb_probe_cast.effect(nyadb_probe_cast.mood);
CREATE TABLE nyadb_probe_cast.original(value integer);
INSERT INTO nyadb_probe_cast.original VALUES(7);`)
	base := pgDesignRead(t, s, "nyadb_probe_cast", "original")
	draft := base.Schema.Table.Clone()
	draft.Definitions = append(draft.Definitions, schemaddl.Definition{Kind: "index", Name: "effect_index", SQL: `CREATE INDEX effect_index ON nyadb_probe_cast.original ((CAST('first'::nyadb_probe_cast.mood AS integer)))`})
	pgAssertTypeProbeRejected(t, s, base, draft, "nyadb_probe_cast")
}

func TestPostgreSQL17DesignProbeImplicitLiteralCastFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_probe_literal;
CREATE SEQUENCE nyadb_probe_literal.counter START WITH 41;
CREATE FUNCTION nyadb_probe_literal.effect(numeric) RETURNS uuid LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN PERFORM nextval('nyadb_probe_literal.counter'); RETURN '00000000-0000-0000-0000-000000000001'::uuid; END$$;
CREATE CAST(numeric AS uuid) WITH FUNCTION nyadb_probe_literal.effect(numeric);
CREATE FUNCTION nyadb_probe_literal.time_effect(time) RETURNS uuid LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN PERFORM nextval('nyadb_probe_literal.counter'); RETURN '00000000-0000-0000-0000-000000000001'::uuid; END$$;
CREATE CAST(time AS uuid) WITH FUNCTION nyadb_probe_literal.time_effect(time);
CREATE TABLE nyadb_probe_literal.original(value integer);
INSERT INTO nyadb_probe_literal.original VALUES(7);`)
	base := pgDesignRead(t, s, "nyadb_probe_literal", "original")
	for _, expression := range []string{`CAST(1.25 AS uuid)`, `CAST(TIME '12:34' AS uuid)`} {
		draft := base.Schema.Table.Clone()
		draft.Definitions = append(draft.Definitions, schemaddl.Definition{Kind: "index", Name: "effect_index", SQL: "CREATE INDEX effect_index ON nyadb_probe_literal.original ((" + expression + "))"})
		pgAssertTypeProbeRejected(t, s, base, draft, "nyadb_probe_literal")
	}
}

func pgAssertTypeProbeRejected(t *testing.T, s *Session, base contract.SchemaDesignSnapshot, draft schemaddl.Table, schema string) {
	t.Helper()
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	before := schemaddl.Signature(draft)
	out, probeErr := s.postgresCanonicalDesired(context.Background(), c, contract.SchemaDesignRequest{Original: base, Desired: draft})
	var called bool
	if err = c.QueryRowContext(context.Background(), "SELECT is_called FROM "+quote(schema)+".counter").Scan(&called); err != nil {
		t.Fatal("caller transaction unusable", err)
	}
	if called {
		t.Error("definition probe executed a nontransactional type or cast callback")
	}
	if probeErr == nil || out.Table.Name != "" || out.Probe.Exists {
		t.Error("untrusted type/cast produced an accepted independent definition")
	}
	pgProbeAbsent(t, c, schema)
	var value int
	if err = c.QueryRowContext(context.Background(), "SELECT value FROM "+quote(schema)+".original").Scan(&value); err != nil || value != 7 {
		t.Fatal("type probe changed original rows", err)
	}
	if schemaddl.Signature(draft) != before {
		t.Fatal("type probe mutated request")
	}
}

func TestPostgreSQL17DesignProbeStructuralTypesFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_probe_structural;
CREATE DOMAIN nyadb_probe_structural.amount AS numeric(40,20) CHECK(VALUE>=0);
CREATE TYPE nyadb_probe_structural.mood AS ENUM('first','second');
CREATE TYPE nyadb_probe_structural.composite AS(value nyadb_probe_structural.amount,label text);
CREATE TYPE nyadb_probe_structural.custom_range AS RANGE(subtype=integer);
CREATE TABLE nyadb_probe_structural.original(value integer);`)
	base := pgDesignRead(t, s, "nyadb_probe_structural", "original")
	for _, typ := range []string{"integer", "numeric(40,20)", "text[]", "timestamp(6) with time zone", "interval day to second(3)", "jsonb", "bytea", "uuid", "regclass", "int4range", "int4multirange", "nyadb_probe_structural.amount", "nyadb_probe_structural.mood[]", "nyadb_probe_structural.composite", "nyadb_probe_structural.custom_range"} {
		t.Run(typ, func(t *testing.T) {
			draft := base.Schema.Table.Clone()
			draft.Clauses = append(draft.Clauses, schemaddl.Clause{ID: "new:type", Kind: "column", Name: "typed", SQL: typ})
			c, err := s.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err = c.ExecContext(context.Background(), "BEGIN"); err != nil {
				t.Fatal(err)
			}
			defer rollbackDesign(c)
			out, err := s.postgresCanonicalDesired(context.Background(), c, contract.SchemaDesignRequest{Original: base, Desired: draft})
			if err != nil || out.Table.Name != "original" || !out.Probe.Exists {
				t.Fatal("supported type lost independent definition", err)
			}
			pgProbeAbsent(t, c, "nyadb_probe_structural")
		})
	}
}

func TestPostgreSQL17DesignProbeNestedTypeCallbacksFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_probe_nested;
CREATE SEQUENCE nyadb_probe_nested.counter START WITH 41;
CREATE FUNCTION nyadb_probe_nested.effect(integer) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN PERFORM nextval('nyadb_probe_nested.counter'); RETURN true; END$$;
CREATE FUNCTION nyadb_probe_nested.default_effect() RETURNS integer LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN PERFORM nextval('nyadb_probe_nested.counter'); RETURN 1; END$$;
CREATE FUNCTION nyadb_probe_nested.range_effect(integer,integer) RETURNS double precision LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN PERFORM nextval('nyadb_probe_nested.counter'); RETURN 1; END$$;
CREATE DOMAIN nyadb_probe_nested.guarded AS integer CHECK(nyadb_probe_nested.effect(VALUE));
CREATE DOMAIN nyadb_probe_nested.inherited AS integer DEFAULT nyadb_probe_nested.default_effect();
CREATE TYPE nyadb_probe_nested.composite AS(value nyadb_probe_nested.guarded);
CREATE TYPE nyadb_probe_nested.custom_range AS RANGE(subtype=integer,subtype_diff=nyadb_probe_nested.range_effect);
CREATE TYPE nyadb_probe_nested.representation;
CREATE FUNCTION nyadb_probe_nested.input(cstring) RETURNS nyadb_probe_nested.representation AS 'int4in' LANGUAGE internal IMMUTABLE STRICT;
CREATE FUNCTION nyadb_probe_nested.output(nyadb_probe_nested.representation) RETURNS cstring AS 'int4out' LANGUAGE internal IMMUTABLE STRICT;
CREATE TYPE nyadb_probe_nested.representation(INPUT=nyadb_probe_nested.input,OUTPUT=nyadb_probe_nested.output,INTERNALLENGTH=4,PASSEDBYVALUE,ALIGNMENT=int4);
CREATE TABLE nyadb_probe_nested.original(value integer);
INSERT INTO nyadb_probe_nested.original VALUES(7);`)
	base := pgDesignRead(t, s, "nyadb_probe_nested", "original")
	for _, typ := range []string{"nyadb_probe_nested.guarded[]", "nyadb_probe_nested.composite", "nyadb_probe_nested.inherited", "nyadb_probe_nested.custom_range", "nyadb_probe_nested.representation"} {
		t.Run(typ, func(t *testing.T) {
			draft := base.Schema.Table.Clone()
			draft.Clauses = append(draft.Clauses, schemaddl.Clause{ID: "new:callback", Kind: "column", Name: "typed", SQL: typ})
			pgAssertTypeProbeRejected(t, s, base, draft, "nyadb_probe_nested")
		})
	}
}

func TestPostgreSQL17DesignProbeTypeCallbacksRefreshAndCancelFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_probe_refresh;
CREATE SEQUENCE nyadb_probe_refresh.counter START WITH 41;
CREATE DOMAIN nyadb_probe_refresh.amount AS integer CHECK(VALUE>0);
CREATE TABLE nyadb_probe_refresh.original(value integer);
INSERT INTO nyadb_probe_refresh.original VALUES(7);`)
	base := pgDesignRead(t, s, "nyadb_probe_refresh", "original")
	draft := base.Schema.Table.Clone()
	draft.Clauses = append(draft.Clauses, schemaddl.Clause{ID: "new:domain", Kind: "column", Name: "typed", SQL: "nyadb_probe_refresh.amount"})
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = postgresCheckProbeTypes(context.Background(), c, draft, "nyadb_probe_refresh"); err != nil {
		t.Fatal("initial safe type unavailable", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = postgresCheckProbeTypes(ctx, c, draft, "nyadb_probe_refresh"); err == nil {
		t.Fatal("canceled type check accepted a baseline")
	}
	run(t, s, `CREATE FUNCTION nyadb_probe_refresh.effect(integer) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN PERFORM nextval('nyadb_probe_refresh.counter'); RETURN true; END$$;
ALTER DOMAIN nyadb_probe_refresh.amount ADD CONSTRAINT late_effect CHECK(nyadb_probe_refresh.effect(VALUE));`)
	pgAssertTypeProbeRejected(t, s, base, draft, "nyadb_probe_refresh")
}

func TestPostgreSQL17DesignProbeUnrelatedCastFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_probe_unrelated;
CREATE TYPE nyadb_probe_unrelated.mood AS ENUM('first','second');
CREATE FUNCTION nyadb_probe_unrelated.cast(nyadb_probe_unrelated.mood) RETURNS text LANGUAGE SQL IMMUTABLE AS $$SELECT 'first'$$;
CREATE CAST(nyadb_probe_unrelated.mood AS text) WITH FUNCTION nyadb_probe_unrelated.cast(nyadb_probe_unrelated.mood);
CREATE TABLE nyadb_probe_unrelated.original(value text);`)
	base := pgDesignRead(t, s, "nyadb_probe_unrelated", "original")
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = postgresCheckProbeTypes(context.Background(), c, base.Schema.Table, "nyadb_probe_unrelated"); err != nil {
		t.Fatal("unrelated cast disabled ordinary text declarations", err)
	}
}
