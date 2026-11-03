package engines

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func pgDDLRequest(t *testing.T, s *Session, schema, table string) contract.SchemaDesignRequest {
	t.Helper()
	base := pgDesignRead(t, s, schema, table)
	return contract.SchemaDesignRequest{Original: base, Desired: base.Schema.Table.Clone()}
}

func pgDDLAwait(t *testing.T, ch <-chan contract.SchemaDesignOutcome) contract.SchemaDesignOutcome {
	t.Helper()
	select {
	case out := <-ch:
		return out
	case <-time.After(40 * time.Second):
		t.Fatal("DDL operation did not settle")
	}
	return contract.SchemaDesignOutcome{}
}

func pgDDLWaitForLock(t *testing.T, s *Session, target contract.ObjectRef) {
	t.Helper()
	statement := "LOCK TABLE ONLY " + qualified(target) + " IN ACCESS EXCLUSIVE MODE"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		var waiting bool
		if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_stat_activity WHERE query=$1 AND wait_event_type='Lock')", statement).Scan(&waiting); err != nil {
			t.Fatal("DDL did not reach its original lock", err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("DDL did not wait for the original lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func pgDDLLocker(t *testing.T, s *Session, target contract.ObjectRef) *sql.Conn {
	t.Helper()
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rollbackDesign(c); _ = c.Close() })
	for _, statement := range []string{"BEGIN", "LOCK TABLE ONLY " + qualified(target) + " IN ACCESS EXCLUSIVE MODE"} {
		if _, err := c.ExecContext(context.Background(), statement); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestPostgreSQL17DesignDDLTransactionFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_ddl_transaction`)
	r := pgDDLRequest(t, s, "nyadb_ddl_transaction", "asset")
	r.Desired = schemaddl.Table{Name: "asset", Clauses: []schemaddl.Clause{
		{ID: "new:id", Kind: "column", Name: "id", SQL: "integer NOT NULL"},
		{ID: "new:value", Kind: "column", Name: "value", SQL: "integer"},
		{ID: "new:label", Kind: "column", Name: "label", SQL: "text"},
		{ID: "new:exact", Kind: "column", Name: "exact", SQL: "numeric(40,20)"},
		{ID: "new:payload", Kind: "column", Name: "payload", SQL: "bytea"},
		{ID: "new:pk", Kind: "constraint", Name: "asset_pk", SQL: "CONSTRAINT asset_pk PRIMARY KEY(id)"},
	}, Postgres: &schemaddl.PostgreSQLTableProperties{Kind: "r", Persistence: "p", Comment: "created"}}
	before := schemaddl.Signature(r)
	out := s.postgresDesignDDLApply(context.Background(), r)
	if out.Error != nil || out.State != "committed" || !out.Verified || out.Actual == nil {
		t.Fatal("native CREATE transaction failed", out)
	}
	if schemaddl.Signature(r) != before {
		t.Fatal("CREATE changed its request")
	}
	run(t, s, `INSERT INTO nyadb_ddl_transaction.asset VALUES(1,7,NULL,12345678901234567890.12345678901234567890,decode('00ff','hex')); CREATE VIEW nyadb_ddl_transaction.asset_view AS SELECT id,exact,payload FROM nyadb_ddl_transaction.asset`)
	r = pgDDLRequest(t, s, "nyadb_ddl_transaction", "asset")
	pgNativeEditField(t, &r.Desired, "exact", func(f *schemaddl.PostgreSQLField) { f.Type = "numeric(50,20)" })
	r.Desired.Clauses = append(r.Desired.Clauses,
		schemaddl.Clause{ID: "new:extra", Kind: "column", Name: "extra", SQL: "integer DEFAULT 9"},
		schemaddl.Clause{ID: "new:computed", Kind: "column", Name: "computed", SQL: "integer GENERATED ALWAYS AS (value*2) STORED"})
	r.Desired.Postgres.Comment = "altered"
	before = schemaddl.Signature(r)
	out = s.postgresDesignDDLApply(context.Background(), r)
	var nativeError *pgconn.PgError
	if out.State != "rolled_back" || out.Verified || out.Actual != nil || !errors.As(out.Error, &nativeError) || nativeError.Code != "0A000" {
		t.Fatal("view-dependent type rejection did not rollback", out)
	}
	if current := pgDDLRequest(t, s, "nyadb_ddl_transaction", "asset"); current.Original.Schema.Revision != r.Original.Schema.Revision || schemaddl.Signature(r) != before {
		t.Fatal("rejected dependent type edit changed its original catalog/request")
	}
	// Keep a real inbound view over retained fields while the precision change
	// targets an independent column. Rebinding this fixture is separate setup,
	// never an implicit DROP/CREATE performed by the migration executor.
	run(t, s, `DROP VIEW nyadb_ddl_transaction.asset_view; CREATE VIEW nyadb_ddl_transaction.asset_view AS SELECT id,payload FROM nyadb_ddl_transaction.asset`)
	r.Original = pgDDLRequest(t, s, "nyadb_ddl_transaction", "asset").Original
	before = schemaddl.Signature(r)
	out = s.postgresDesignDDLApply(context.Background(), r)
	if out.Error != nil || out.State != "committed" || !out.Verified || out.Actual == nil {
		t.Fatal("native ALTER transaction failed", out)
	}
	var exact, payload string
	var isNull bool
	var extra, computed, viewRows int
	if err := s.db.QueryRow(`SELECT exact::text,encode(payload,'hex'),label IS NULL,extra,computed,(SELECT count(*) FROM nyadb_ddl_transaction.asset_view) FROM nyadb_ddl_transaction.asset`).Scan(&exact, &payload, &isNull, &extra, &computed, &viewRows); err != nil {
		t.Fatal(err)
	}
	if exact != "12345678901234567890.12345678901234567890" || payload != "00ff" || !isNull || extra != 9 || computed != 14 || viewRows != 1 || schemaddl.Signature(r) != before {
		t.Fatal("ALTER changed retained values, dependency or captured request")
	}
	// The independent empty-table definition is valid, while adding this CHECK
	// to original rows fails after the earlier ADD COLUMN. Both changes rollback.
	r = pgDDLRequest(t, s, "nyadb_ddl_transaction", "asset")
	r.Desired.Clauses = append(r.Desired.Clauses,
		schemaddl.Clause{ID: "new:rollback", Kind: "column", Name: "must_rollback", SQL: "integer DEFAULT 11"},
		schemaddl.Clause{ID: "new:check", Kind: "constraint", Name: "bad_check", SQL: "CONSTRAINT bad_check CHECK(value<0)"})
	out = s.postgresDesignDDLApply(context.Background(), r)
	if out.Error == nil || out.State != "rolled_back" || out.Verified || out.Actual != nil {
		t.Fatal("failed native batch did not rollback", out)
	}
	if current := pgDDLRequest(t, s, "nyadb_ddl_transaction", "asset"); current.Original.Schema.Revision != r.Original.Schema.Revision {
		t.Fatal("failed native batch changed original catalog")
	}
	for _, mutate := range []func(*contract.SchemaDesignRequest){
		func(x *contract.SchemaDesignRequest) { x.Original.Target.Generation++ },
		func(x *contract.SchemaDesignRequest) { x.Original.Target.Object.ProfileID++ },
		func(x *contract.SchemaDesignRequest) { x.Original.Target.Object.Schema = "wrong" },
	} {
		bad := r.Clone()
		mutate(&bad)
		if out := s.postgresDesignDDLApply(context.Background(), bad); out.Error == nil || out.State != "not_executed" {
			t.Fatal("invalid scope started a transaction", out)
		}
	}
}

func TestPostgreSQL17DesignDDLTransactionLifecycleFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_ddl_lifecycle; CREATE TABLE nyadb_ddl_lifecycle.asset(value integer); INSERT INTO nyadb_ddl_lifecycle.asset VALUES(7)`)
	r := pgDDLRequest(t, s, "nyadb_ddl_lifecycle", "asset")
	r.Desired.Clauses = append(r.Desired.Clauses, schemaddl.Clause{ID: "new:extra", Kind: "column", Name: "extra", SQL: "integer DEFAULT 9"})
	locker := pgDDLLocker(t, s, r.Original.Target.Object)
	if _, err := locker.ExecContext(context.Background(), `COMMENT ON TABLE nyadb_ddl_lifecycle.asset IS 'writer during wait'`); err != nil {
		t.Fatal(err)
	}
	ch := make(chan contract.SchemaDesignOutcome, 1)
	go func() { ch <- s.postgresDesignDDLApply(context.Background(), r) }()
	pgDDLWaitForLock(t, s, r.Original.Target.Object)
	if _, err := locker.ExecContext(context.Background(), "COMMIT"); err != nil {
		t.Fatal(err)
	}
	_ = locker.Close()
	if out := pgDDLAwait(t, ch); !errors.Is(out.Error, schemaddl.ErrConflict) || out.State != "rolled_back" || out.Verified || out.Actual != nil {
		t.Fatal("lock wait used old catalog snapshot", out)
	}
	r = pgDDLRequest(t, s, "nyadb_ddl_lifecycle", "asset")
	r.Desired.Clauses = append(r.Desired.Clauses, schemaddl.Clause{ID: "new:extra", Kind: "column", Name: "extra", SQL: "integer DEFAULT 9"})
	locker = pgDDLLocker(t, s, r.Original.Target.Object)
	go func() { ch <- s.postgresDesignDDLApply(context.Background(), r) }()
	pgDDLWaitForLock(t, s, r.Original.Target.Object)
	r.Desired.Clauses[len(r.Desired.Clauses)-1].SQL = "integer DEFAULT 99"
	r.Original.Schema.Postgres.Entries[0].Data[0] = '!'
	if _, err := locker.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	_ = locker.Close()
	if out := pgDDLAwait(t, ch); out.Error != nil || out.State != "committed" || out.Actual == nil || !out.Verified {
		t.Fatal("captured DDL request changed", out)
	}
	var extra int
	if err := s.db.QueryRow(`SELECT extra FROM nyadb_ddl_lifecycle.asset`).Scan(&extra); err != nil || extra != 9 {
		t.Fatal("late request changed committed value", err)
	}
	r = pgDDLRequest(t, s, "nyadb_ddl_lifecycle", "asset")
	r.Desired.Clauses = append(r.Desired.Clauses, schemaddl.Clause{ID: "new:cancel", Kind: "column", Name: "cancelled", SQL: "integer"})
	locker = pgDDLLocker(t, s, r.Original.Target.Object)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { ch <- s.postgresDesignDDLApply(ctx, r) }()
	pgDDLWaitForLock(t, s, r.Original.Target.Object)
	cancel()
	if out := pgDDLAwait(t, ch); out.Error == nil || out.Verified || out.Actual != nil || out.State != "rolled_back" && out.State != "uncertain" {
		t.Fatal("cancelled DDL did not settle", out)
	}
	_ = rollbackDesign(locker)
	_ = locker.Close()
	if current := pgDDLRequest(t, s, "nyadb_ddl_lifecycle", "asset"); current.Original.Schema.Revision != r.Original.Schema.Revision {
		t.Fatal("cancelled DDL changed original catalog")
	}
	// Successful DDL must restore the physical connection's settings as well
	// as return a fresh ordinary catalog, without leaking its private session.
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{"SET search_path=nyadb_ddl_lifecycle", "SET standard_conforming_strings=off", "SET row_security=on"} {
		if _, err := c.ExecContext(context.Background(), statement); err != nil {
			t.Fatal(err)
		}
	}
	_ = c.Close()
	r = pgDDLRequest(t, s, "nyadb_ddl_lifecycle", "asset")
	r.Desired.Clauses = append(r.Desired.Clauses, schemaddl.Clause{ID: "new:settings", Kind: "column", Name: "settings", SQL: "integer DEFAULT 3"})
	if out := s.postgresDesignDDLApply(context.Background(), r); out.Error != nil || out.State != "committed" || out.Actual == nil || !out.Verified {
		t.Fatal("DDL with original physical settings failed", out)
	}
	c, err = s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var search, standard, security, isolation string
	if err := c.QueryRowContext(context.Background(), "SELECT current_setting('search_path'),current_setting('standard_conforming_strings'),current_setting('row_security'),current_setting('transaction_isolation')").Scan(&search, &standard, &security, &isolation); err != nil {
		t.Fatal(err)
	}
	if search != "nyadb_ddl_lifecycle" || standard != "off" || security != "on" || isolation != "read committed" {
		t.Fatal("DDL transaction leaked connection settings")
	}
	poolContext, poolCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	if out := s.postgresDesignDDLApply(poolContext, r); out.State != "not_executed" || !errors.Is(out.Error, context.DeadlineExceeded) {
		t.Fatal("occupied pool cancellation started a transaction", out)
	}
	poolCancel()
	_ = c.Close()
	s.db.SetMaxOpenConns(8)
	r = pgDDLRequest(t, s, "nyadb_ddl_lifecycle", "asset")
	r.Desired.Clauses = append(r.Desired.Clauses, schemaddl.Clause{ID: "new:closed", Kind: "column", Name: "closed", SQL: "integer"})
	locker = pgDDLLocker(t, s, r.Original.Target.Object)
	go func() { ch <- s.postgresDesignDDLApply(context.Background(), r) }()
	pgDDLWaitForLock(t, s, r.Original.Target.Object)
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	if out := pgDDLAwait(t, ch); out.Error == nil || out.Verified || out.Actual != nil || out.State != "rolled_back" && out.State != "uncertain" {
		t.Fatal("closed session did not revoke DDL transaction", out)
	}
	_ = rollbackDesign(locker)
	_ = locker.Close()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DDL session close leaked owned connection")
	}
}

func TestPostgreSQL17DesignDDLTransactionLostCommitFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_ddl_lost_commit; CREATE TABLE nyadb_ddl_lost_commit.asset(value integer); INSERT INTO nyadb_ddl_lost_commit.asset VALUES(7)`)
	r := pgDDLRequest(t, s, "nyadb_ddl_lost_commit", "asset")
	r.Desired.Clauses = append(r.Desired.Clauses, schemaddl.Clause{ID: "new:extra", Kind: "column", Name: "extra", SQL: "integer DEFAULT 9"})
	port, err := strconv.ParseUint(os.Getenv("NYADB_POSTGRES_PORT"), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := postgresConfig(connections.Profile{ID: s.id, Kind: connections.PostgreSQL, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_it", Database: "nyadb_it", TLSMode: "disable"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	serverDB := s.db
	t.Cleanup(func() { _ = serverDB.Close() })
	s.db = sql.OpenDB(pgLostCommitConnector{Connector: stdlib.GetConnector(*cfg)})
	out := s.SchemaDesign().Apply(context.Background(), r)
	if out.State != "uncertain" || out.Verified || out.Actual != nil || !errors.Is(out.Error, ErrUncertain) || !errors.Is(out.Error, io.ErrUnexpectedEOF) {
		t.Fatal("lost DDL receipt acquired replacement permission", out)
	}
	if s.Alive() || s.SchemaDesign().CanApply(r.Original.Target) {
		t.Fatal("public lost commit left the original generation replayable")
	}
	var value, extra int
	if err := serverDB.QueryRow(`SELECT value,extra FROM nyadb_ddl_lost_commit.asset`).Scan(&value, &extra); err != nil || value != 7 || extra != 9 {
		t.Fatal("real COMMIT did not precede lost receipt", err)
	}
}
