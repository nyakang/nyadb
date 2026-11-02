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

// All statements still execute against the real disposable service. The
// wrapper orders a second backend's actual replacement after the captured
// identity row closes and before the state SELECT; it does not forge rows.
type pgSequenceReplaceConnector struct {
	driver.Connector
	afterIdentity func()
}

func (c pgSequenceReplaceConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return pgSequenceReplaceConnection{Conn: conn, afterIdentity: c.afterIdentity}, nil
}

type pgSequenceReplaceConnection struct {
	driver.Conn
	afterIdentity func()
}

func (c pgSequenceReplaceConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil || !strings.HasPrefix(query, "SELECT current_database(),c.oid::bigint") {
		return rows, err
	}
	return &pgSequenceReplaceRows{Rows: rows, afterIdentity: c.afterIdentity}, nil
}

type pgSequenceReplaceRows struct {
	driver.Rows
	afterIdentity func()
	once          sync.Once
}

func (r *pgSequenceReplaceRows) Close() error {
	err := r.Rows.Close()
	if err == nil {
		r.once.Do(r.afterIdentity)
	}
	return err
}

func TestPostgreSQL17DesignSequenceIdentityReplacementFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_sequence_identity;
CREATE SEQUENCE nyadb_sequence_identity.original;
CREATE TABLE nyadb_sequence_identity.asset(value bigint DEFAULT nextval('nyadb_sequence_identity.original'));
SELECT setval('nyadb_sequence_identity.original',42,false)`)
	base := pgDesignRead(t, s, "nyadb_sequence_identity", "asset")
	// Remove the dependency so a real independent DROP need not change or
	// wait for the table. The reader specifically verifies its captured OID.
	run(t, s, `ALTER TABLE nyadb_sequence_identity.asset ALTER COLUMN value DROP DEFAULT`)
	port, err := strconv.ParseUint(os.Getenv("NYADB_POSTGRES_PORT"), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := postgresConfig(connections.Profile{ID: s.id, Kind: connections.PostgreSQL, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_it", Database: "nyadb_it", TLSMode: "disable"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	replaced := false
	db := sql.OpenDB(pgSequenceReplaceConnector{Connector: stdlib.GetConnector(*cfg), afterIdentity: func() {
		run(t, s, `DROP SEQUENCE nyadb_sequence_identity.original;
CREATE SEQUENCE nyadb_sequence_identity.original;
SELECT setval('nyadb_sequence_identity.original',42,false)`)
		replaced = true
	}})
	defer db.Close()
	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	states, err := postgresSequenceStates(context.Background(), c, base.Schema.Postgres)
	if !replaced || !errors.Is(err, schemaddl.ErrConflict) || states != nil {
		t.Fatal("same-named sequence supplied the original's state", err)
	}
	var current int64
	if err := s.db.QueryRow(`SELECT 'nyadb_sequence_identity.original'::regclass::oid::bigint`).Scan(&current); err != nil {
		t.Fatal(err)
	}
	sources, err := postgresSequenceSources(base.Schema.Postgres)
	if err != nil || len(sources) != 1 || current == sources[0].OID {
		t.Fatal("fixture did not replace its actual sequence identity", err)
	}
}
