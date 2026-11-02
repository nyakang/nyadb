package engines

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func pgMetadataRequest(t *testing.T, s *Session) contract.SchemaDesignRequest {
	t.Helper()
	base := pgDesignRead(t, s, "nyadb_metadata", "metadata_table")
	return contract.SchemaDesignRequest{Original: base, Desired: base.Schema.Table.Clone()}
}

// This connector still executes against the disposable PostgreSQL service.
// It drops just the successful COMMIT receipt to reproduce a lost acknowledgement;
// a subsequent real ROLLBACK outside the committed transaction succeeds.
type pgLostCommitConnector struct{ driver.Connector }

func (c pgLostCommitConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return pgLostCommitConnection{Conn: conn}, nil
}

type pgLostCommitConnection struct{ driver.Conn }

func (c pgLostCommitConnection) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
	if err == nil && query == "COMMIT" {
		return nil, io.ErrUnexpectedEOF
	}
	return result, err
}
func (c pgLostCommitConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func TestPostgreSQL17DesignMetadataLostCommitFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_lost_commit; CREATE TABLE nyadb_lost_commit.asset(id integer); INSERT INTO nyadb_lost_commit.asset VALUES(1)`)
	base := pgDesignRead(t, s, "nyadb_lost_commit", "asset")
	r := contract.SchemaDesignRequest{Original: base, Desired: base.Schema.Table.Clone()}
	r.Desired.Postgres.Comment = "committed without receipt"
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
	out := s.postgresDesignMetadataApply(context.Background(), r)
	if out.State != "uncertain" || out.Verified || out.Actual != nil || !errors.Is(out.Error, ErrUncertain) || !errors.Is(out.Error, io.ErrUnexpectedEOF) {
		t.Fatalf("lost acknowledgement acquired replay/replacement permission: %+v", out)
	}
	var comment string
	var rows int
	if err := serverDB.QueryRow(`SELECT obj_description('nyadb_lost_commit.asset'::regclass,'pg_class'),(SELECT count(*) FROM nyadb_lost_commit.asset)`).Scan(&comment, &rows); err != nil {
		t.Fatal(err)
	}
	if comment != r.Desired.Postgres.Comment || rows != 1 {
		t.Fatal("fixture did not commit before dropping acknowledgement")
	}
}

func pgMetadataField(t *testing.T, draft *schemaddl.Table, name string) *schemaddl.PostgreSQLColumnProperties {
	t.Helper()
	for i := range draft.Clauses {
		if draft.Clauses[i].Kind == "column" && draft.Clauses[i].Name == name {
			return draft.Clauses[i].Postgres
		}
	}
	t.Fatal("field absent", name)
	return nil
}

func pgMetadataRows(t *testing.T, s *Session) string {
	t.Helper()
	var id, value, payload, identity, sequence string
	var isNull bool
	err := s.db.QueryRow(`SELECT id::text,exact::text,nullable IS NULL,encode(payload,'hex'),ctid::text FROM nyadb_metadata.metadata_table`).Scan(&id, &value, &isNull, &payload, &identity)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT last_value::text||':'||is_called::text FROM nyadb_metadata.metadata_table_id_seq`).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	return schemaddl.Signature([]any{id, value, isNull, payload, identity, sequence})
}

func pgMetadataWaitForLock(t *testing.T, s *Session) {
	t.Helper()
	statement := `LOCK TABLE ONLY "nyadb_metadata"."metadata_table" IN ACCESS EXCLUSIVE MODE`
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	for {
		var waiting bool
		err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE query=$1 AND wait_event_type='Lock')", statement).Scan(&waiting)
		if err != nil {
			t.Fatal("metadata operation did not reach lock wait", err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("metadata operation never entered lock wait")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func pgMetadataLocker(t *testing.T, s *Session) *sql.Conn {
	t.Helper()
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rollbackDesign(c); _ = c.Close() })
	if _, err = c.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ExecContext(context.Background(), `LOCK TABLE nyadb_metadata.metadata_table IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	return c
}

func pgMetadataAwait(t *testing.T, ch <-chan contract.SchemaDesignOutcome) contract.SchemaDesignOutcome {
	t.Helper()
	select {
	case out := <-ch:
		return out
	case <-time.After(5 * time.Second):
		t.Fatal("metadata receipt did not settle")
	}
	return contract.SchemaDesignOutcome{}
}

func TestPostgreSQL17DesignMetadataFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_metadata;
CREATE ROLE nyadb_metadata_reader;
CREATE TABLE nyadb_metadata.metadata_table (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 exact numeric(40,20), nullable text, payload bytea,
 generated bigint GENERATED ALWAYS AS (id*2) STORED,
 CONSTRAINT positive CHECK (exact>0)
);
INSERT INTO nyadb_metadata.metadata_table(exact,nullable,payload) VALUES(12345678901234567890.12345678901234567890,NULL,decode('00ff','hex'));
CREATE INDEX metadata_idx ON nyadb_metadata.metadata_table((lower(nullable))) INCLUDE (payload) WHERE nullable IS NOT NULL;
CREATE VIEW nyadb_metadata.metadata_view AS SELECT id,exact,payload FROM nyadb_metadata.metadata_table;
CREATE TABLE nyadb_metadata.metadata_child(id bigint REFERENCES nyadb_metadata.metadata_table(id));
GRANT SELECT ON nyadb_metadata.metadata_table TO nyadb_metadata_reader;
GRANT UPDATE(nullable) ON nyadb_metadata.metadata_table TO nyadb_metadata_reader;
ALTER TABLE nyadb_metadata.metadata_table ENABLE ROW LEVEL SECURITY;
CREATE POLICY visible ON nyadb_metadata.metadata_table USING(id>0);
COMMENT ON TABLE nyadb_metadata.metadata_table IS 'original';`)
	rowsBefore := pgMetadataRows(t, s)
	r := pgMetadataRequest(t, s)
	r.Desired.Postgres.Comment = "table ' comment\\line;\n下一行"
	field := pgMetadataField(t, &r.Desired, "nullable")
	field.Comment, field.Storage, field.Compression, field.Statistics = "field ' comment\\line", "e", "p", 73
	out := s.postgresDesignMetadataApply(context.Background(), r)
	if out.Error != nil || out.State != "committed" || !out.Verified || out.Actual == nil {
		t.Fatalf("metadata commit failed: %+v", out)
	}
	if err := schemaddl.VerifyPostgreSQLMetadata(r.Original.Schema, r.Desired, out.Actual.Schema, "nyadb_metadata"); err != nil {
		t.Fatal(err)
	}
	if pgMetadataRows(t, s) != rowsBefore {
		t.Fatal("metadata plan changed precise values, NULL, binary, row identity or sequence state")
	}
	var viewRows int
	if err := s.db.QueryRow("SELECT count(*) FROM nyadb_metadata.metadata_view").Scan(&viewRows); err != nil || viewRows != 1 {
		t.Fatal("dependent view changed", err)
	}
	// Dropping comments is explicit NULL, not an empty replacement asset.
	r = pgMetadataRequest(t, s)
	r.Desired.Postgres.Comment = ""
	pgMetadataField(t, &r.Desired, "nullable").Comment = ""
	out = s.postgresDesignMetadataApply(context.Background(), r)
	if out.State != "committed" || out.Error != nil {
		t.Fatal(out)
	}
	var noComments bool
	if err := s.db.QueryRow(`SELECT obj_description('nyadb_metadata.metadata_table'::regclass,'pg_class') IS NULL AND col_description('nyadb_metadata.metadata_table'::regclass,3) IS NULL`).Scan(&noComments); err != nil || !noComments {
		t.Fatal("comments not removed", err)
	}

	// Native server rejection after a valid earlier statement rolls back the
	// whole batch. A fixed-size bigint cannot acquire EXTENDED storage.
	r = pgMetadataRequest(t, s)
	r.Desired.Postgres.Comment = "must roll back"
	pgMetadataField(t, &r.Desired, "id").Storage = "x"
	before := r.Original.Schema.Revision
	out = s.postgresDesignMetadataApply(context.Background(), r)
	if out.Error == nil || out.State != "rolled_back" || out.Verified || out.Actual != nil {
		t.Fatalf("failed batch was not rolled back: %+v", out)
	}
	if pgMetadataRequest(t, s).Original.Schema.Revision != before || pgMetadataRows(t, s) != rowsBefore {
		t.Fatal("failed batch replaced original metadata/data")
	}

	// A captured old revision cannot overwrite even a metadata-only change.
	r = pgMetadataRequest(t, s)
	r.Desired.Postgres.Comment = "stale overwrite"
	run(t, s, `COMMENT ON TABLE nyadb_metadata.metadata_table IS 'other writer'`)
	out = s.postgresDesignMetadataApply(context.Background(), r)
	if !errors.Is(out.Error, schemaddl.ErrConflict) || out.State != "rolled_back" {
		t.Fatal("stale revision acquired write permission", out)
	}

	// A writer that commits while this operation waits on the table lock must
	// be visible to the first repeatable-read SELECT after the lock is granted.
	r = pgMetadataRequest(t, s)
	r.Desired.Postgres.Comment = "waiting overwrite"
	locker := pgMetadataLocker(t, s)
	if _, err := locker.ExecContext(context.Background(), `COMMENT ON TABLE nyadb_metadata.metadata_table IS 'commit during wait'`); err != nil {
		t.Fatal(err)
	}
	ch := make(chan contract.SchemaDesignOutcome, 1)
	go func() { ch <- s.postgresDesignMetadataApply(context.Background(), r) }()
	pgMetadataWaitForLock(t, s)
	if _, err := locker.ExecContext(context.Background(), "COMMIT"); err != nil {
		t.Fatal(err)
	}
	_ = locker.Close()
	out = pgMetadataAwait(t, ch)
	if !errors.Is(out.Error, schemaddl.ErrConflict) || out.State != "rolled_back" {
		t.Fatal("lock wait used a stale transaction snapshot", out)
	}

	// Mutating the caller's draft after the worker is waiting cannot change
	// its private captured request; the test's lock observation orders the copy.
	r = pgMetadataRequest(t, s)
	r.Desired.Postgres.Comment = "captured draft"
	locker = pgMetadataLocker(t, s)
	go func() { ch <- s.postgresDesignMetadataApply(context.Background(), r) }()
	pgMetadataWaitForLock(t, s)
	r.Desired.Postgres.Comment = "late mutation"
	r.Original.Schema.Postgres.Entries[0].Data[0] = '!'
	if _, err := locker.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	_ = locker.Close()
	out = pgMetadataAwait(t, ch)
	if out.State != "committed" || out.Error != nil || out.Actual.Schema.Table.Postgres.Comment != "captured draft" {
		t.Fatal("worker read a mutable request", out)
	}

	r = pgMetadataRequest(t, s)
	r.Desired.Postgres.Comment = "cancelled draft"
	locker = pgMetadataLocker(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { ch <- s.postgresDesignMetadataApply(ctx, r) }()
	pgMetadataWaitForLock(t, s)
	cancel()
	out = pgMetadataAwait(t, ch)
	if out.State != "rolled_back" && out.State != "uncertain" || out.Error == nil || out.Verified || out.Actual != nil {
		t.Fatal("cancelled lock wait did not settle", out)
	}
	if _, err := locker.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	_ = locker.Close()
	if pgMetadataRequest(t, s).Original.Schema.Table.Postgres.Comment != "captured draft" {
		t.Fatal("cancelled operation changed comment")
	}

	// Existing DDL event hooks are unsupported by a metadata-only executor.
	run(t, s, `CREATE FUNCTION nyadb_metadata.on_ddl() RETURNS event_trigger LANGUAGE plpgsql AS $$BEGIN END$$; CREATE EVENT TRIGGER nyadb_metadata_hook ON ddl_command_start EXECUTE FUNCTION nyadb_metadata.on_ddl()`)
	r = pgMetadataRequest(t, s)
	if !pgCatalogHas(r.Original.Schema.Postgres, "event_trigger", "evtname", "nyadb_metadata_hook") {
		t.Fatal("complete baseline omitted the global event hook")
	}
	r.Desired.Postgres.Comment = "event hook edit"
	out = s.postgresDesignMetadataApply(context.Background(), r)
	if out.State != "rolled_back" || out.Error == nil || !strings.Contains(out.Error.Error(), "event triggers") {
		t.Fatal("enabled event hook accepted", out)
	}
	run(t, s, `ALTER EVENT TRIGGER nyadb_metadata_hook DISABLE`)
	r = pgMetadataRequest(t, s)
	r.Desired.Postgres.Comment = "disabled hook retained"
	out = s.postgresDesignMetadataApply(context.Background(), r)
	if out.State != "committed" || out.Error != nil {
		t.Fatal("disabled hook prevented a metadata-only edit", out)
	}
	run(t, s, `DROP EVENT TRIGGER nyadb_metadata_hook; DROP FUNCTION nyadb_metadata.on_ddl()`)

	// A private owned connection restores the user's physical settings even
	// after a batch error. It never adopts a SQL editor's transaction/lease.
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	c, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range []string{"SET search_path = nyadb_metadata", "SET standard_conforming_strings = off", "SET row_security = on"} {
		if _, err := c.ExecContext(context.Background(), setting); err != nil {
			t.Fatal(err)
		}
	}
	_ = c.Close()
	r = pgMetadataRequest(t, s)
	r.Desired.Postgres.Comment = "settings retained\\value"
	out = s.postgresDesignMetadataApply(context.Background(), r)
	if out.State != "committed" || out.Error != nil {
		t.Fatal(out)
	}
	c, err = s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var search, standard, security, isolation string
	if err := c.QueryRowContext(context.Background(), "SELECT current_setting('search_path'),current_setting('standard_conforming_strings'),current_setting('row_security'),current_setting('transaction_isolation')").Scan(&search, &standard, &security, &isolation); err != nil {
		t.Fatal(err)
	}
	if search != "nyadb_metadata" || standard != "off" || security != "on" || isolation != "read committed" {
		t.Fatal("transaction settings leaked", search, standard, security, isolation)
	}
	blocked, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	out = s.postgresDesignMetadataApply(blocked, r)
	stop()
	if out.State != "not_executed" || !errors.Is(out.Error, context.DeadlineExceeded) {
		t.Fatal("occupied pool cancellation failed", out)
	}
	_ = c.Close()
	s.db.SetMaxOpenConns(8)
	// No-op still checks the exact captured original revision under the lock.
	r = pgMetadataRequest(t, s)
	out = s.postgresDesignMetadataApply(context.Background(), r)
	if out.Error != nil || out.State != "not_executed" || !out.Verified || out.Actual == nil {
		t.Fatal("noop failed", out)
	}
	for _, mutate := range []func(*contract.SchemaDesignRequest){
		func(x *contract.SchemaDesignRequest) { x.Original.Target.Generation++ },
		func(x *contract.SchemaDesignRequest) { x.Original.Target.Object.ProfileID++ },
		func(x *contract.SchemaDesignRequest) { x.Original.Target.Object.Catalog = "wrong" },
		func(x *contract.SchemaDesignRequest) { x.Desired.Clauses[0].SQL = "text" },
	} {
		x := r.Clone()
		mutate(&x)
		out = s.postgresDesignMetadataApply(context.Background(), x)
		if out.Error == nil || out.State != "not_executed" {
			t.Fatal("invalid request started a transaction", out)
		}
	}
	if pgMetadataRows(t, s) != rowsBefore {
		t.Fatal("fixture data/identity changed")
	}

	// Session cancellation revokes the owned operation while parked on a lock.
	r.Desired.Postgres.Comment = "closed session"
	locker = pgMetadataLocker(t, s)
	go func() { ch <- s.postgresDesignMetadataApply(context.Background(), r) }()
	pgMetadataWaitForLock(t, s)
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	out = pgMetadataAwait(t, ch)
	if out.Error == nil || out.Verified || out.Actual != nil || out.State != "rolled_back" && out.State != "uncertain" {
		t.Fatal("closed session receipt retained permission", out)
	}
	if _, err := locker.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	_ = locker.Close()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session close leaked owned connection")
	}
}
