package engines

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestPostgreSQL17ProgramBrowseFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_programs; CREATE SCHEMA nyadb_programs_other;
CREATE SEQUENCE nyadb_programs.counter AS bigint INCREMENT BY -2 MINVALUE -9223372036854775807 MAXVALUE 9223372036854775807 START WITH 9223372036854775805 CACHE 17 CYCLE;
COMMENT ON SEQUENCE nyadb_programs.counter IS 'counter comment';
CREATE TABLE nyadb_programs.effects(value text);
CREATE VIEW nyadb_programs.visible AS SELECT value FROM nyadb_programs.effects;
CREATE FUNCTION nyadb_programs.over(arg integer) RETURNS bigint LANGUAGE SQL VOLATILE SECURITY DEFINER AS $$SELECT nextval('nyadb_programs.counter')$$;
CREATE FUNCTION nyadb_programs.over(arg text DEFAULT 'kept') RETURNS text LANGUAGE SQL AS $$SELECT arg$$;
COMMENT ON FUNCTION nyadb_programs.over(integer) IS 'integer overload';
CREATE FUNCTION nyadb_programs_other.over(arg integer) RETURNS integer LANGUAGE SQL AS $$SELECT arg$$;
CREATE TYPE nyadb_programs_other.mood AS ENUM ('first','second');
CREATE FUNCTION nyadb_programs.over(arg nyadb_programs_other.mood) RETURNS text LANGUAGE SQL AS $$SELECT arg::text$$;
CREATE FUNCTION nyadb_programs."Odd ""name"(OUT answer integer) LANGUAGE SQL AS $$SELECT 42$$;
CREATE FUNCTION nyadb_programs.vararg(VARIADIC args integer[]) RETURNS integer LANGUAGE SQL AS $$SELECT cardinality(args)$$;
CREATE PROCEDURE nyadb_programs.mutate(arg integer) LANGUAGE plpgsql AS $$BEGIN INSERT INTO nyadb_programs.effects VALUES ('executed'); END$$;
CREATE AGGREGATE nyadb_programs.total(bigint) (SFUNC=int8pl,STYPE=bigint,INITCOND='0');`)
	t.Cleanup(func() { run(t, s, `DROP SCHEMA nyadb_programs CASCADE; DROP SCHEMA nyadb_programs_other CASCADE;`) })
	namespace := s.Namespace("nyadb_programs")
	objects, err := s.Browse(context.Background(), namespace)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]contract.DBObject{}
	for _, object := range objects {
		if object.Ref.Namespace() != namespace || object.Ref.Name != object.Name || byName[object.Name].Name != "" {
			t.Fatal("lost namespace or overload identity", object.Ref)
		}
		byName[object.Name] = object
	}
	if len(objects) != 9 {
		t.Fatalf("objects: %d %+v", len(objects), objects)
	}
	for _, name := range []string{"over(integer)", "over(text)", "over(nyadb_programs_other.mood)", `"Odd ""name"()`, "vararg(integer[])", "mutate(integer)", "counter", "effects", "visible"} {
		if byName[name].Name != name {
			t.Fatal("object missing", name)
		}
	}
	function := byName["over(integer)"]
	if function.Kind != "函数" || function.Ref.Kind != "function" || function.SQLSecurity != "DEFINER" || function.Comment != "integer overload" || function.ReturnType != "bigint" || !strings.Contains(function.Definition, "nextval") || !strings.Contains(function.Parameters, "arg integer") {
		t.Fatal("routine metadata incomplete", function)
	}
	if byName["mutate(integer)"].Kind != "存储过程" || byName["mutate(integer)"].Ref.Kind != "procedure" || byName["mutate(integer)"].ReturnType != "" {
		t.Fatal("procedure classified as table/function")
	}
	if !strings.Contains(byName["over(text)"].Parameters, "DEFAULT 'kept'") {
		t.Fatal("argument defaults omitted")
	}
	sequence := byName["counter"]
	wantSequence := &contract.SequenceInfo{DataType: "bigint", Start: 9223372036854775805, Increment: -2, Min: -9223372036854775807, Max: 9223372036854775807, Cache: 17, Cycle: true}
	if sequence.Kind != "序列" || sequence.Ref.Kind != "sequence" || !reflect.DeepEqual(sequence.Sequence, wantSequence) || sequence.Comment != "counter comment" || !strings.Contains(sequence.Definition, `CREATE SEQUENCE "nyadb_programs"."counter"`) || !strings.Contains(sequence.Definition, "START WITH 9223372036854775805") {
		t.Fatal("sequence settings/precision omitted", sequence)
	}
	refs, err := s.Objects(context.Background(), namespace.Schema)
	if err != nil || len(refs) != len(objects) {
		t.Fatal("neutral object list differs", err)
	}
	for i, ref := range refs {
		if ref.Ref != objects[i].Ref {
			t.Fatal("object APIs disagree on identity")
		}
	}
	other, err := s.Browse(context.Background(), s.Namespace("nyadb_programs_other"))
	if err != nil || len(other) != 1 || other[0].Ref == function.Ref {
		t.Fatal("cross-schema routine identity collapsed", err, other)
	}
	var last int64
	var called bool
	if err = s.db.QueryRow(`SELECT last_value,is_called FROM nyadb_programs.counter`).Scan(&last, &called); err != nil || last != wantSequence.Start || called {
		t.Fatal("browsing advanced the sequence", err, last, called)
	}
	var effects int
	if err = s.db.QueryRow(`SELECT count(*) FROM nyadb_programs.effects`).Scan(&effects); err != nil || effects != 0 {
		t.Fatal("browsing executed a routine", err, effects)
	}
	// Stable signatures and read-only decompilation must restore pool settings.
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ExecContext(context.Background(), `SET search_path = nyadb_programs_other; SET standard_conforming_strings = off`); err != nil {
		c.Close()
		t.Fatal(err)
	}
	c.Close()
	stable, err := s.Browse(context.Background(), namespace)
	if err != nil || !reflect.DeepEqual(stable, objects) {
		t.Fatal("pool settings changed routine identity or definitions", err)
	}
	c, err = s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var path, standard string
	if err = c.QueryRowContext(context.Background(), `SELECT current_setting('search_path'),current_setting('standard_conforming_strings')`).Scan(&path, &standard); err != nil || path != "nyadb_programs_other" || standard != "off" {
		c.Close()
		t.Fatal("browse changed pool settings", err, path, standard)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	partial, err := s.Browse(ctx, namespace)
	cancel()
	c.Close()
	if !errors.Is(err, context.DeadlineExceeded) || partial != nil {
		t.Fatal("occupied pool did not cancel without a partial snapshot", err)
	}
	if _, err = s.Browse(context.Background(), namespace); err != nil {
		t.Fatal("cancelled read damaged next browse", err)
	}
	// An oversized body never becomes a truncated usable definition.
	run(t, s, "CREATE FUNCTION nyadb_programs.large() RETURNS text LANGUAGE SQL AS $body$SELECT '"+strings.Repeat("x", 1<<20)+"'::text$body$")
	if partial, err = s.Browse(context.Background(), namespace); err == nil || !strings.Contains(err.Error(), "1 MiB") || partial != nil {
		t.Fatal("oversized definition published partial metadata", err)
	}
	run(t, s, "DROP FUNCTION nyadb_programs.large()")
	if _, err = s.Browse(context.Background(), namespace); err != nil {
		t.Fatal(err)
	}
	foreign := namespace
	foreign.Catalog = "other"
	if partial, err = s.Browse(context.Background(), foreign); err == nil || partial != nil {
		t.Fatal("foreign catalog admitted")
	}
}

func TestPostgreSQL17ProgramBrowseCloseFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	s.db.SetMaxOpenConns(1)
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	result := make(chan error, 1)
	go func() {
		objects, err := s.Browse(context.Background(), s.Namespace("public"))
		if objects != nil {
			err = errors.New("closed session published metadata")
		}
		result <- err
	}()
	// Close cancels the pool wait, rather than transferring it to a new handle.
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-result:
		if err == nil {
			t.Fatal("closed browse succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("closed browse did not acknowledge cancellation")
	}
	if objects, err := s.Browse(context.Background(), s.Namespace("public")); err == nil || objects != nil {
		t.Fatal("closed session reused")
	}
}

func TestPostgreSQL17ProgramBrowseSnapshotLimitFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_program_limit`)
	t.Cleanup(func() { run(t, s, `DROP SCHEMA nyadb_program_limit CASCADE`) })
	// Each definition is individually allowed, but their combined snapshot is
	// too large. Do not publish an apparently complete, truncated directory.
	run(t, s, `DO $do$ BEGIN
 FOR i IN 1..34 LOOP
  EXECUTE format('CREATE FUNCTION nyadb_program_limit.f%s() RETURNS text LANGUAGE SQL AS %L', i, 'SELECT ' || quote_literal(repeat('x', 1000000)) || '::text');
 END LOOP;
 END $do$`)
	for _, browse := range []bool{true, false} {
		if browse {
			objects, err := s.Browse(context.Background(), s.Namespace("nyadb_program_limit"))
			if err == nil || !strings.Contains(err.Error(), "32 MiB") || objects != nil {
				t.Fatal("oversized browse published partial snapshot", err)
			}
		} else {
			objects, err := s.Objects(context.Background(), "nyadb_program_limit")
			if err == nil || !strings.Contains(err.Error(), "32 MiB") || objects != nil {
				t.Fatal("object APIs disagree on oversized snapshot", err)
			}
		}
	}
	run(t, s, `DROP FUNCTION nyadb_program_limit.f34(); DROP FUNCTION nyadb_program_limit.f33()`)
	objects, err := s.Browse(context.Background(), s.Namespace("nyadb_program_limit"))
	if err != nil || len(objects) != 32 {
		t.Fatal("snapshot below limit did not recover", len(objects), err)
	}
}
