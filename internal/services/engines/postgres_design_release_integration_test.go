package engines

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"testing"
	"time"
)

func TestPostgreSQL17DesignProbeReturnTypeClosureFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_return_closure;
CREATE SEQUENCE nyadb_return_closure.counter;
CREATE FUNCTION nyadb_return_closure.cast_value(timestamp with time zone) RETURNS bytea LANGUAGE plpgsql IMMUTABLE AS $$BEGIN PERFORM nextval('nyadb_return_closure.counter'); RETURN decode('00ff','hex'); END$$;
CREATE CAST(timestamp with time zone AS bytea) WITH FUNCTION nyadb_return_closure.cast_value(timestamp with time zone) AS ASSIGNMENT;
CREATE TABLE nyadb_return_closure.original(value bytea); INSERT INTO nyadb_return_closure.original VALUES (decode('00ff','hex'));
CREATE VIEW nyadb_return_closure.hidden AS SELECT nyadb_return_closure.cast_value(now()) AS value;`)
	t.Cleanup(func() { run(t, s, "DROP CAST(timestamp with time zone AS bytea)") })
	for _, expression := range []string{`now()`, `(SELECT value FROM nyadb_return_closure.hidden)`} {
		r := pgDDLRequest(t, s, "nyadb_return_closure", "original")
		pgNativeEditField(t, &r.Desired, "value", func(f *schemaddl.PostgreSQLField) { f.HasDefault = true; f.Default = expression })
		out := s.postgresDesignDDLApply(context.Background(), r)
		if out.Error == nil || out.Verified || out.State != "rolled_back" && out.State != "not_executed" {
			t.Fatal("hidden callbacks accepted", out.State, out.Error)
		}
		var called bool
		if err := s.db.QueryRow(`SELECT is_called FROM nyadb_return_closure.counter`).Scan(&called); err != nil || called {
			t.Fatal("preflight consumed sequence", expression, err)
		}
	}
}

func TestPostgreSQL17DesignReferenceLockFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_reference_lock; CREATE SCHEMA nyadb_reference_parent;
CREATE TABLE nyadb_reference_lock.original(id integer);
CREATE TABLE nyadb_reference_parent.parent(id integer PRIMARY KEY);`)
	r := pgDDLRequest(t, s, "nyadb_reference_lock", "original")
	r.Desired.Clauses = append(r.Desired.Clauses, schemaddl.Clause{ID: "new:fk", Kind: "constraint", Name: "locked_fk", SQL: `CONSTRAINT locked_fk FOREIGN KEY(id) REFERENCES nyadb_reference_parent.parent(id)`})
	locker := pgDDLLocker(t, s, s.Namespace("nyadb_reference_parent").Object("table", "parent"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan contract.SchemaDesignOutcome, 1)
	go func() { ch <- s.postgresDesignDDLApply(ctx, r) }()
	waitCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	for {
		var waiting bool
		if err := s.db.QueryRowContext(waitCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE query='LOCK TABLE ONLY "nyadb_reference_parent"."parent" IN SHARE MODE' AND wait_event_type='Lock')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-waitCtx.Done():
			t.Fatal("reference was not locked before probe")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if out := pgDDLAwait(t, ch); out.State != "rolled_back" && out.State != "uncertain" || !errors.Is(out.Error, context.Canceled) || out.Verified || out.Actual != nil {
		t.Fatal("reference lock cancellation lost acknowledgement", out.State, out.Error)
	}
	if err := rollbackDesign(locker); err != nil {
		t.Fatal(err)
	}
	if out := s.postgresDesignDDLApply(context.Background(), r); out.State != "committed" || !out.Verified || out.Error != nil {
		t.Fatal("qualified reference apply failed", out.State, out.Error)
	}
}

func TestPostgreSQL17DesignColumnNotNullFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_column_nn; CREATE TABLE nyadb_column_nn.original(id bigint, value text NOT NULL); INSERT INTO nyadb_column_nn.original VALUES(7,'exact')`)
	r := pgDDLRequest(t, s, "nyadb_column_nn", "original")
	modern := r.Original.Schema.Postgres.ServerVersion >= 180000
	if modern {
		run(t, s, `ALTER TABLE nyadb_column_nn.original RENAME CONSTRAINT original_value_not_null TO named_not_null; COMMENT ON CONSTRAINT named_not_null ON nyadb_column_nn.original IS 'retained name and comment'`)
		r = pgDDLRequest(t, s, "nyadb_column_nn", "original")
	}
	pgNativeEditField(t, &r.Desired, "value", func(f *schemaddl.PostgreSQLField) { f.Type = "character varying(100)" })
	out := s.postgresDesignDDLApply(context.Background(), r)
	if out.State != "committed" || !out.Verified || out.Error != nil {
		t.Fatal("NOT NULL type change failed", out.State, out.Error)
	}
	if modern {
		var name, comment string
		if err := s.db.QueryRow(`SELECT conname,obj_description(oid,'pg_constraint') FROM pg_constraint WHERE conrelid='nyadb_column_nn.original'::regclass AND contype='n'`).Scan(&name, &comment); err != nil || name != "named_not_null" || comment != "retained name and comment" {
			t.Fatal("NOT NULL metadata lost", name, comment, err)
		}
	}
	r.Original = *out.Actual
	r.Desired = out.Actual.Schema.Table.Clone()
	pgNativeEditField(t, &r.Desired, "value", func(f *schemaddl.PostgreSQLField) { f.NotNull = false })
	out = s.postgresDesignDDLApply(context.Background(), r)
	if out.State != "committed" || out.Error != nil {
		t.Fatal("DROP NOT NULL failed", out.State, out.Error)
	}
	r.Original = *out.Actual
	r.Desired = out.Actual.Schema.Table.Clone()
	pgNativeEditField(t, &r.Desired, "value", func(f *schemaddl.PostgreSQLField) { f.NotNull = true })
	out = s.postgresDesignDDLApply(context.Background(), r)
	if out.State != "committed" || out.Error != nil {
		t.Fatal("SET NOT NULL failed", out.State, out.Error)
	}
	r.Original = *out.Actual
	r.Desired = out.Actual.Schema.Table.Clone()
	r.AllowColumnDrop = true
	r.Desired.Clauses = r.Desired.Clauses[:1]
	out = s.postgresDesignDDLApply(context.Background(), r)
	if out.State != "committed" || out.Error != nil {
		t.Fatal("DROP NOT NULL column failed", out.State, out.Error)
	}
	if modern {
		run(t, s, `ALTER TABLE nyadb_column_nn.original ADD CONSTRAINT pending_not_null NOT NULL id NOT VALID`)
		r = pgDDLRequest(t, s, "nyadb_column_nn", "original")
		r.Desired.Clauses = append(r.Desired.Clauses, schemaddl.Clause{ID: "new:nullable", Kind: "column", Name: "nullable", SQL: "text"})
		out = s.postgresDesignDDLApply(context.Background(), r)
		if out.State != "committed" || out.Error != nil {
			t.Fatal("unchanged NOT VALID was not retained", out.State, out.Error)
		}
		r = pgDDLRequest(t, s, "nyadb_column_nn", "original")
		pgNativeEditField(t, &r.Desired, "id", func(f *schemaddl.PostgreSQLField) { f.NotNull = false })
		out = s.postgresDesignDDLApply(context.Background(), r)
		if !errors.Is(out.Error, schemaddl.ErrPostgreSQLNotNull) || out.State != "not_executed" {
			t.Fatal("unvalidated NOT NULL became editable", out.State, out.Error)
		}
	}
}
