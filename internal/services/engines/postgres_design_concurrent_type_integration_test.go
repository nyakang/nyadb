package engines

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// The fixture orders actual DDL from a second physical connection, either
// after the type-name lookup closes or just before the hidden CREATE. It never
// substitutes catalog rows or changes the captured caller request.
type pgConcurrentDomainConnector struct {
	driver.Connector
	once     *sync.Once
	change   func(context.Context) error
	atLookup bool
}

func (c pgConcurrentDomainConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &pgConcurrentDomainConnection{Conn: conn, once: c.once, change: c.change, atLookup: c.atLookup}, nil
}

type pgConcurrentDomainConnection struct {
	driver.Conn
	once     *sync.Once
	change   func(context.Context) error
	atLookup bool
}

func (c *pgConcurrentDomainConnection) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !c.atLookup && strings.HasPrefix(query, "CREATE TABLE") && strings.Contains(query, `"__nyadb_probe_`) {
		var err error
		c.once.Do(func() { err = c.change(ctx) })
		if err != nil {
			return nil, err
		}
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}
func (c *pgConcurrentDomainConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil || !c.atLookup || !strings.HasPrefix(query, "SELECT t.oid::bigint,t.typarray::bigint") || len(args) != 2 || args[1].Value != "amount" {
		return rows, err
	}
	return &pgConcurrentTypeRows{Rows: rows, afterLookup: func() (err error) {
		c.once.Do(func() { err = c.change(ctx) })
		return
	}}, nil
}

type pgConcurrentTypeRows struct {
	driver.Rows
	afterLookup func() error
}

func (r *pgConcurrentTypeRows) Close() error {
	if err := r.Rows.Close(); err != nil {
		return err
	}
	return r.afterLookup()
}

func TestPostgreSQL17DesignConcurrentDomainCallbackFixture(t *testing.T) {
	for _, stage := range []string{"captured_lookup", "hidden_create"} {
		t.Run(stage, func(t *testing.T) {
			pgConcurrentTypeRejected(t, stage == "captured_lookup", false)
		})
	}
}

func TestPostgreSQL17DesignConcurrentEnumReplacedByDomainFixture(t *testing.T) {
	pgConcurrentTypeRejected(t, true, true)
}

func pgConcurrentTypeRejected(t *testing.T, atLookup, replaceEnum bool) {
	t.Helper()
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_concurrent_domain;
 CREATE SEQUENCE nyadb_concurrent_domain.counter;
 CREATE FUNCTION nyadb_concurrent_domain.effect(integer) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
 BEGIN PERFORM nextval('nyadb_concurrent_domain.counter'); RETURN true; END $$;
 CREATE TABLE nyadb_concurrent_domain.original(id integer);
 INSERT INTO nyadb_concurrent_domain.original VALUES(7);`)
	server := s.db
	defer func() {
		if _, err := server.ExecContext(context.Background(), `DROP SCHEMA nyadb_concurrent_domain CASCADE`); err != nil {
			t.Error("own concurrency fixture cleanup:", err)
		}
	}()
	typeSQL := `CREATE DOMAIN nyadb_concurrent_domain.amount AS integer`
	declaration := "nyadb_concurrent_domain.amount DEFAULT 1"
	changeSQL := `ALTER DOMAIN nyadb_concurrent_domain.amount ADD CONSTRAINT changed CHECK (nyadb_concurrent_domain.effect(VALUE))`
	if replaceEnum {
		typeSQL = `CREATE TYPE nyadb_concurrent_domain.amount AS ENUM('first')`
		declaration = "nyadb_concurrent_domain.amount DEFAULT 'first'"
		changeSQL = `DROP TYPE nyadb_concurrent_domain.amount;
CREATE DOMAIN nyadb_concurrent_domain.amount AS text CHECK (nyadb_concurrent_domain.effect(1))`
	}
	run(t, s, typeSQL)
	r := pgDDLRequest(t, s, "nyadb_concurrent_domain", "original")
	r.Desired.Clauses = append(r.Desired.Clauses, schemaddl.Clause{ID: "new:domain", Kind: "column", Name: "amount", SQL: declaration})
	before := schemaddl.Signature(r)
	port, err := strconv.ParseUint(os.Getenv("NYADB_POSTGRES_PORT"), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := postgresConfig(connections.Profile{ID: s.id, Kind: connections.PostgreSQL, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_it", Database: "nyadb_it", TLSMode: "disable"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	changed := false
	wrapped := sql.OpenDB(pgConcurrentDomainConnector{Connector: stdlib.GetConnector(*cfg), once: &sync.Once{}, atLookup: atLookup, change: func(ctx context.Context) error {
		_, err := server.ExecContext(ctx, changeSQL)
		changed = err == nil
		return err
	}})
	s.db = wrapped
	t.Cleanup(func() { _ = wrapped.Close() })
	out := s.postgresDesignDDLApply(context.Background(), r)
	if changed != atLookup {
		t.Fatal("fixture did not change at captured lookup, or unsafe hidden CREATE was reached", changed, atLookup, out.Error)
	}
	var called bool
	if err := server.QueryRow(`SELECT is_called FROM nyadb_concurrent_domain.counter`).Scan(&called); err != nil {
		t.Fatal(err)
	}
	if called || !errors.Is(out.Error, errPostgresProbeTypeStability) || out.Verified || out.Actual != nil || out.State != "rolled_back" || schemaddl.Signature(r) != before {
		t.Fatal("concurrent unchecked domain callback ran or acquired commit permission", called, out.State, out.Error)
	}
	var id int
	if err := server.QueryRow(`SELECT id FROM nyadb_concurrent_domain.original`).Scan(&id); err != nil || id != 7 {
		t.Fatal("rejected type changed original values", id, err)
	}
	actual := pgDesignRead(t, s, "nyadb_concurrent_domain", "original")
	// The fixture intentionally changes another type in the same namespace;
	// its complete namespace fingerprint may change. The original table's
	// declaration/properties and rows must remain untouched.
	if schemaddl.Signature(actual.Schema.Table) != schemaddl.Signature(r.Original.Schema.Table) {
		t.Fatal("rejected type changed original catalog")
	}
	c, err := server.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	pgProbeAbsent(t, c, "nyadb_concurrent_domain")
}
