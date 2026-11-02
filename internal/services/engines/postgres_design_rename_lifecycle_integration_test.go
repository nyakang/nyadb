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

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func pgRenameRequest(t *testing.T, s *Session) contract.SchemaDesignRequest {
	t.Helper()
	base := pgDesignRead(t, s, "nyadb_rename_lifecycle", "original")
	draft := base.Schema.Table.Clone()
	draft.Name = "renamed"
	return contract.SchemaDesignRequest{Original: base, Desired: draft}
}

func pgRenameLocker(t *testing.T, s *Session) *sql.Conn {
	t.Helper()
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rollbackDesign(c); _ = c.Close() })
	if _, err := c.ExecContext(context.Background(), `BEGIN; LOCK TABLE ONLY nyadb_rename_lifecycle.original IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	return c
}

func pgRenameWaitForLock(t *testing.T, s *Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		var waiting bool
		err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE query=$1 AND wait_event_type='Lock')`, `LOCK TABLE ONLY "nyadb_rename_lifecycle"."original" IN ACCESS EXCLUSIVE MODE`).Scan(&waiting)
		if err != nil {
			t.Fatal("rename did not reach the captured table lock", err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("rename never entered lock wait")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestPostgreSQL17DesignRenameLifecycleFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_rename_lifecycle; CREATE SCHEMA nyadb_rename_lifecycle_external;
CREATE TABLE nyadb_rename_lifecycle.original(value integer); INSERT INTO nyadb_rename_lifecycle.original VALUES(1);
CREATE TABLE nyadb_rename_lifecycle.occupied(value integer);
CREATE FUNCTION nyadb_rename_lifecycle_external.parsed_reader() RETURNS integer LANGUAGE SQL RETURN (SELECT value FROM nyadb_rename_lifecycle.original LIMIT 1)`)
	r := pgRenameRequest(t, s)
	occupied := r.Clone()
	occupied.Desired.Name = "occupied"
	out := s.postgresDesignRenameApply(context.Background(), occupied)
	if out.State != "rolled_back" || out.Error == nil || out.Verified || out.Actual != nil {
		t.Fatal("occupied target yielded replacement permission", out)
	}
	if pgRenameRequest(t, s).Original.Schema.Revision != r.Original.Schema.Revision {
		t.Fatal("rejected rename changed its original catalog")
	}
	// Outside parsed routines participate in the original revision too.
	run(t, s, `CREATE OR REPLACE FUNCTION nyadb_rename_lifecycle_external.parsed_reader() RETURNS integer LANGUAGE SQL RETURN (SELECT value+1 FROM nyadb_rename_lifecycle.original LIMIT 1)`)
	out = s.postgresDesignRenameApply(context.Background(), r)
	if !errors.Is(out.Error, schemaddl.ErrConflict) || out.State != "rolled_back" || out.Verified || out.Actual != nil {
		t.Fatal("stale outside dependency acquired rename permission", out)
	}
	r = pgRenameRequest(t, s)
	locker := pgRenameLocker(t, s)
	if _, err := locker.ExecContext(context.Background(), `COMMENT ON TABLE nyadb_rename_lifecycle.original IS 'committed while waiting'`); err != nil {
		t.Fatal(err)
	}
	ch := make(chan contract.SchemaDesignOutcome, 1)
	go func() { ch <- s.postgresDesignRenameApply(context.Background(), r) }()
	pgRenameWaitForLock(t, s)
	if _, err := locker.ExecContext(context.Background(), "COMMIT"); err != nil {
		t.Fatal(err)
	}
	_ = locker.Close()
	out = pgMetadataAwait(t, ch)
	if !errors.Is(out.Error, schemaddl.ErrConflict) || out.State != "rolled_back" {
		t.Fatal("rename used a transaction snapshot from before its lock", out)
	}
	r = pgRenameRequest(t, s)
	locker = pgRenameLocker(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { ch <- s.postgresDesignRenameApply(ctx, r) }()
	pgRenameWaitForLock(t, s)
	cancel()
	out = pgMetadataAwait(t, ch)
	if out.Error == nil || out.Verified || out.Actual != nil || out.State != "rolled_back" && out.State != "uncertain" {
		t.Fatal("canceled rename retained replacement/replay permission", out)
	}
	if _, err := locker.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	_ = locker.Close()
	base := pgRenameRequest(t, s)
	if base.Original.Schema.Revision != r.Original.Schema.Revision {
		t.Fatal("canceled rename changed the original table")
	}
	wrong := base.Clone()
	wrong.Original.Target.Generation++
	out = s.postgresDesignRenameApply(context.Background(), wrong)
	if !errors.Is(out.Error, contract.ErrRevoked) || out.State != "not_executed" {
		t.Fatal("old session generation acquired rename permission", out)
	}
	// Lock observation happens after the worker's private deep request copy.
	r = base.Clone()
	locker = pgRenameLocker(t, s)
	go func() { ch <- s.postgresDesignRenameApply(context.Background(), r) }()
	pgRenameWaitForLock(t, s)
	r.Desired.Name = "late_mutation"
	r.Original.Schema.Postgres.Entries[0].Data[0] = '!'
	if _, err := locker.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	_ = locker.Close()
	out = pgMetadataAwait(t, ch)
	if out.Error != nil || out.State != "committed" || !out.Verified || out.Actual == nil || out.Actual.Target.Object.Name != "renamed" {
		t.Fatal("rename consumed a late mutable request", out)
	}
	var value int
	if err := s.db.QueryRow(`SELECT value FROM nyadb_rename_lifecycle.renamed`).Scan(&value); err != nil || value != 1 {
		t.Fatal("rename changed the table data", value, err)
	}
}

func TestPostgreSQL17DesignRenameLostCommitFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_rename_lost_receipt; CREATE TABLE nyadb_rename_lost_receipt.original(value integer); INSERT INTO nyadb_rename_lost_receipt.original VALUES(1)`)
	base := pgDesignRead(t, s, "nyadb_rename_lost_receipt", "original")
	draft := base.Schema.Table.Clone()
	draft.Name = "renamed"
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
	out := s.postgresDesignRenameApply(context.Background(), contract.SchemaDesignRequest{Original: base, Desired: draft})
	if out.State != "uncertain" || out.Verified || out.Actual != nil || !errors.Is(out.Error, ErrUncertain) || !errors.Is(out.Error, io.ErrUnexpectedEOF) {
		t.Fatal("lost rename COMMIT receipt acquired replacement permission", out)
	}
	var value int
	if err := serverDB.QueryRow(`SELECT value FROM nyadb_rename_lost_receipt.renamed`).Scan(&value); err != nil || value != 1 {
		t.Fatal("fixture did not commit before dropping its receipt", value, err)
	}
}
