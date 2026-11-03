package engines

import (
	"context"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestPostgreSQL17DesignProbeOperatorEffectFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_probe_operator;
CREATE SEQUENCE nyadb_probe_operator.counter START WITH 41;
CREATE FUNCTION nyadb_probe_operator.effect(integer,integer) RETURNS integer LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN PERFORM nextval('nyadb_probe_operator.counter'); RETURN 1; END$$;
CREATE OPERATOR pg_catalog.#@ (LEFTARG=integer,RIGHTARG=integer,FUNCTION=nyadb_probe_operator.effect);
CREATE TABLE nyadb_probe_operator.original(value integer);
INSERT INTO nyadb_probe_operator.original VALUES(7);`)
	t.Cleanup(func() { run(t, s, `DROP OPERATOR pg_catalog.#@(integer,integer)`) })
	base := pgDesignRead(t, s, "nyadb_probe_operator", "original")
	draft := base.Schema.Table.Clone()
	draft.Definitions = append(draft.Definitions, schemaddl.Definition{Kind: "index", Name: "effect_index", SQL: `CREATE INDEX effect_index ON nyadb_probe_operator.original ((1 #@ 2))`})
	if err := pgAssertTypeProbeRejected(t, s, base, draft, "nyadb_probe_operator"); !errors.Is(err, errPostgresProbeDispatch) {
		t.Fatal("operator was not rejected by dispatch preflight", err)
	}
}

func TestPostgreSQL17DesignProbeOperatorOverloadFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_probe_opoverload;
CREATE SEQUENCE nyadb_probe_opoverload.counter START WITH 41;
CREATE TYPE nyadb_probe_opoverload.mood AS ENUM('first','second');
CREATE FUNCTION nyadb_probe_opoverload.effect(nyadb_probe_opoverload.mood,integer) RETURNS integer LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN PERFORM nextval('nyadb_probe_opoverload.counter'); RETURN 1; END$$;
CREATE OPERATOR pg_catalog.+ (LEFTARG=nyadb_probe_opoverload.mood,RIGHTARG=integer,FUNCTION=nyadb_probe_opoverload.effect);
CREATE TABLE nyadb_probe_opoverload.original(value integer);
INSERT INTO nyadb_probe_opoverload.original VALUES(7);`)
	t.Cleanup(func() { run(t, s, `DROP OPERATOR pg_catalog.+(nyadb_probe_opoverload.mood,integer)`) })
	base := pgDesignRead(t, s, "nyadb_probe_opoverload", "original")
	draft := base.Schema.Table.Clone()
	draft.Definitions = append(draft.Definitions, schemaddl.Definition{Kind: "index", Name: "effect_index", SQL: `CREATE INDEX effect_index ON nyadb_probe_opoverload.original (('first'::nyadb_probe_opoverload.mood+1))`})
	if err := pgAssertTypeProbeRejected(t, s, base, draft, "nyadb_probe_opoverload"); !errors.Is(err, errPostgresProbeDispatch) && !errors.Is(err, errPostgresProbeTypeStability) {
		t.Fatal("overload was not rejected by dispatch preflight", err)
	}
}

func TestPostgreSQL17DesignProbeIndexClassFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_probe_class;
CREATE SEQUENCE nyadb_probe_class.counter START WITH 41;
CREATE FUNCTION nyadb_probe_class.compare(integer,integer) RETURNS integer LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN PERFORM nextval('nyadb_probe_class.counter'); RETURN $1-$2; END$$;
CREATE OPERATOR CLASS nyadb_probe_class.effect_ops FOR TYPE integer USING btree AS
OPERATOR 1 <(integer,integer),OPERATOR 2 <=(integer,integer),OPERATOR 3 =(integer,integer),OPERATOR 4 >=(integer,integer),OPERATOR 5 >(integer,integer),FUNCTION 1 nyadb_probe_class.compare(integer,integer);
CREATE TABLE nyadb_probe_class.original(value integer);
INSERT INTO nyadb_probe_class.original VALUES(7);`)
	base := pgDesignRead(t, s, "nyadb_probe_class", "original")
	draft := base.Schema.Table.Clone()
	draft.Definitions = append(draft.Definitions, schemaddl.Definition{Kind: "index", Name: "effect_index", SQL: `CREATE INDEX effect_index ON nyadb_probe_class.original (value nyadb_probe_class.effect_ops)`})
	if err := pgAssertTypeProbeRejected(t, s, base, draft, "nyadb_probe_class"); !errors.Is(err, errPostgresProbeDispatch) {
		t.Fatal("class was not rejected by dispatch preflight", err)
	}
}

func TestPostgreSQL17DesignProbeDefaultIndexClassFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_probe_default_class;
CREATE SEQUENCE nyadb_probe_default_class.counter START WITH 41;
CREATE TYPE nyadb_probe_default_class.mood AS ENUM('first','second');
CREATE FUNCTION nyadb_probe_default_class.compare(nyadb_probe_default_class.mood,nyadb_probe_default_class.mood) RETURNS integer LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN PERFORM nextval('nyadb_probe_default_class.counter'); RETURN 1; END$$;
CREATE OPERATOR CLASS nyadb_probe_default_class.effect_ops DEFAULT FOR TYPE nyadb_probe_default_class.mood USING btree AS
OPERATOR 1 <(anyenum,anyenum),OPERATOR 2 <=(anyenum,anyenum),OPERATOR 3 =(anyenum,anyenum),OPERATOR 4 >=(anyenum,anyenum),OPERATOR 5 >(anyenum,anyenum),FUNCTION 1 nyadb_probe_default_class.compare(nyadb_probe_default_class.mood,nyadb_probe_default_class.mood);
CREATE TABLE nyadb_probe_default_class.original(value integer,mood nyadb_probe_default_class.mood);
INSERT INTO nyadb_probe_default_class.original(value) VALUES(7);`)
	base := pgDesignRead(t, s, "nyadb_probe_default_class", "original")
	draft := base.Schema.Table.Clone()
	draft.Definitions = append(draft.Definitions, schemaddl.Definition{Kind: "index", Name: "effect_index", SQL: `CREATE INDEX effect_index ON nyadb_probe_default_class.original (mood)`})
	if err := pgAssertTypeProbeRejected(t, s, base, draft, "nyadb_probe_default_class"); !errors.Is(err, errPostgresProbeTypeStability) {
		t.Fatal("mutable enum reached default-class evaluation", err)
	}
}

func TestPostgreSQL17DesignProbeAccessMethodFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_probe_access;
CREATE SEQUENCE nyadb_probe_access.counter START WITH 41;
CREATE ACCESS METHOD nyadb_probe_index_alias TYPE INDEX HANDLER pg_catalog.bthandler;
CREATE ACCESS METHOD nyadb_probe_table_alias TYPE TABLE HANDLER pg_catalog.heap_tableam_handler;
CREATE TABLE nyadb_probe_access.original(value integer);
INSERT INTO nyadb_probe_access.original VALUES(7);`)
	base := pgDesignRead(t, s, "nyadb_probe_access", "original")
	draft := base.Schema.Table.Clone()
	draft.Definitions = append(draft.Definitions, schemaddl.Definition{Kind: "index", Name: "effect_index", SQL: `CREATE INDEX effect_index ON nyadb_probe_access.original USING nyadb_probe_index_alias (value)`})
	if err := pgAssertTypeProbeRejected(t, s, base, draft, "nyadb_probe_access"); !errors.Is(err, errPostgresProbeDispatch) {
		t.Fatal("index method was not rejected by dispatch preflight", err)
	}
	draft = base.Schema.Table.Clone()
	draft.Postgres.AccessMethod = "nyadb_probe_table_alias"
	if err := pgAssertTypeProbeRejected(t, s, base, draft, "nyadb_probe_access"); !errors.Is(err, errPostgresProbeDispatch) {
		t.Fatal("table method was not rejected by dispatch preflight", err)
	}
	missing := pgDesignRead(t, s, "nyadb_probe_access", "created")
	create := schemaddl.Table{Name: "created", Clauses: []schemaddl.Clause{{ID: "new:value", Kind: "column", Name: "value", SQL: "integer"}}, Postgres: &schemaddl.PostgreSQLTableProperties{Kind: "r", Persistence: "p"}}
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	if _, err = c.ExecContext(context.Background(), "SET LOCAL default_table_access_method=nyadb_probe_table_alias"); err != nil {
		t.Fatal(err)
	}
	out, probeErr := s.postgresCanonicalDesired(context.Background(), c, contract.SchemaDesignRequest{Original: missing, Desired: create})
	if probeErr == nil || out.Table.Name != "" || out.Probe.Exists {
		t.Error("caller default table access method bypassed the preflight")
	}
	var method string
	if err = c.QueryRowContext(context.Background(), "SHOW default_table_access_method").Scan(&method); err != nil || method != "nyadb_probe_table_alias" {
		t.Fatal("preflight changed caller access method", err)
	}
	pgProbeAbsent(t, c, "nyadb_probe_access")
}

func TestPostgreSQL17DesignProbeCoreDispatchFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_probe_core_dispatch;
CREATE TABLE nyadb_probe_core_dispatch.original(value integer,label text,payload jsonb,position point);
INSERT INTO nyadb_probe_core_dispatch.original(value) VALUES(7);`)
	base := pgDesignRead(t, s, "nyadb_probe_core_dispatch", "original")
	for _, sample := range []struct{ method, key string }{
		{"btree", `(value OPERATOR(pg_catalog.+) 1)`},
		{"btree", `label pg_catalog.text_pattern_ops`},
		{"hash", `value`}, {"gin", `payload`}, {"gist", `position`}, {"spgist", `position`}, {"brin", `value`},
		{"brin", `value pg_catalog.int4_minmax_multi_ops(values_per_range=64)`},
	} {
		t.Run(sample.method+sample.key, func(t *testing.T) {
			draft := base.Schema.Table.Clone()
			draft.Definitions = append(draft.Definitions, schemaddl.Definition{Kind: "index", Name: "core_index", SQL: "CREATE INDEX core_index ON nyadb_probe_core_dispatch.original USING " + sample.method + " (" + sample.key + ")"})
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
				t.Fatal("supported core dispatch unavailable", err)
			}
			pgProbeAbsent(t, c, "nyadb_probe_core_dispatch")
		})
	}
}

func TestPostgreSQL17DesignProbeCoreOperatorCatalogFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, symbol := range []string{"+", "*", "=", "<>", "<", ">", "<=", ">=", "~~", "~~*", "!~~", "!~~*", "~"} {
		rows, err := c.QueryContext(context.Background(), `SELECT o.oid::bigint,o.oprcode::bigint,o.oprleft::bigint,o.oprright::bigint FROM pg_catalog.pg_operator o JOIN pg_catalog.pg_namespace n ON n.oid=o.oprnamespace WHERE n.nspname='pg_catalog' AND o.oid<16384 AND o.oprname=$1`, symbol)
		if err != nil {
			t.Fatal(err)
		}
		var values [][4]int64
		for rows.Next() {
			var value [4]int64
			if err = rows.Scan(&value[0], &value[1], &value[2], &value[3]); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			values = append(values, value)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range values {
			p := postgresProbeTypeCheck{ctx: context.Background(), c: c, types: map[int64]bool{}, routines: map[int64]string{}, operators: map[int64]bool{}, opclasses: map[int64]bool{}}
			if err = p.operatorID(value[0], 0); err != nil {
				// Only fixed core catalog identities are diagnostic evidence.
				// Never print user definitions or values from requested SQL.
				t.Errorf("core operator %s OID %d routine %d operand types %d/%d unavailable: %v", symbol, value[0], value[1], value[2], value[3], err)
			}
		}
	}
}

func TestPostgreSQL17DesignProbeChangedCoreForwarderFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_probe_changed_forwarder;
CREATE SEQUENCE nyadb_probe_changed_forwarder.counter START WITH 41;
CREATE FUNCTION nyadb_probe_changed_forwarder.effect(time,date) RETURNS timestamp LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN PERFORM nextval('nyadb_probe_changed_forwarder.counter'); RETURN timestamp '2000-01-01'; END$$;
CREATE TABLE nyadb_probe_changed_forwarder.original(value integer);
INSERT INTO nyadb_probe_changed_forwarder.original VALUES(7);`)
	var original string
	if err := s.db.QueryRowContext(context.Background(), "SELECT pg_catalog.pg_get_functiondef(1296)").Scan(&original); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run(t, s, original) })
	run(t, s, `CREATE OR REPLACE FUNCTION pg_catalog.timedate_pl(time,date) RETURNS timestamp LANGUAGE sql IMMUTABLE
RETURN nyadb_probe_changed_forwarder.effect($1,$2);`)
	base := pgDesignRead(t, s, "nyadb_probe_changed_forwarder", "original")
	draft := base.Schema.Table.Clone()
	draft.Definitions = append(draft.Definitions, schemaddl.Definition{Kind: "index", Name: "effect_index", SQL: `CREATE INDEX effect_index ON nyadb_probe_changed_forwarder.original ((time '12:00' + date '2000-01-01'))`})
	if err := pgAssertTypeProbeRejected(t, s, base, draft, "nyadb_probe_changed_forwarder"); !errors.Is(err, errPostgresProbeDispatch) {
		t.Fatal("changed core body was not rejected by dispatch preflight", err)
	}
}
