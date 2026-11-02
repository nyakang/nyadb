package engines

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
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

// Real-server fault injection only. It preserves the actual ALTER receipt,
// scope and catalog declaration while corrupting a newly populated value.
type pgNewValueFaultConnector struct {
	driver.Connector
	schema, mode string
}

func (c pgNewValueFaultConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &pgNewValueFaultConnection{Conn: conn, schema: c.schema, mode: c.mode}, nil
}

type pgNewValueFaultConnection struct {
	driver.Conn
	schema, mode string
	injected     bool
}

func (c *pgNewValueFaultConnection) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	executor := c.Conn.(driver.ExecerContext)
	result, err := executor.ExecContext(ctx, query, args)
	if err != nil || c.injected || !strings.HasPrefix(query, `ALTER TABLE ONLY "`+c.schema+`"."original" ADD COLUMN "extra"`) {
		return result, err
	}
	c.injected = true
	table := quote(c.schema) + `."original"`
	var statements []string
	if c.mode == "generated" {
		statements = append(statements, `UPDATE pg_catalog.pg_attribute SET attgenerated='' WHERE attrelid='`+c.schema+`.original'::regclass AND attname='extra'`)
	}
	if strings.HasPrefix(c.mode, "identity") {
		statements = append(statements, `SELECT pg_catalog.setval(pg_catalog.pg_get_serial_sequence('`+c.schema+`.original','extra')::regclass,1,true)`)
	} else {
		statements = append(statements, "UPDATE "+table+" SET extra=99 WHERE id=1")
	}
	if c.mode == "generated" {
		statements = append(statements, `UPDATE pg_catalog.pg_attribute SET attgenerated='s' WHERE attrelid='`+c.schema+`.original'::regclass AND attname='extra'`)
	}
	for _, statement := range statements {
		if _, err := executor.ExecContext(ctx, statement, nil); err != nil {
			return nil, err
		}
	}
	return result, nil
}
func (c *pgNewValueFaultConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func TestPostgreSQL17DesignNewValueTamperFixture(t *testing.T) {
	for _, mode := range []string{"default", "generated", "identity", "identity_empty"} {
		t.Run(mode, func(t *testing.T) {
			s := postgresDesignFixture(t)
			schema := "nyadb_new_value_tamper_" + mode
			run(t, s, "CREATE SCHEMA "+quote(schema)+"; CREATE TABLE "+quote(schema)+".original(id integer,value integer)")
			if mode != "identity_empty" {
				run(t, s, "INSERT INTO "+quote(schema)+".original VALUES(1,7),(2,7),(3,NULL)")
			}
			r := pgDDLRequest(t, s, schema, "original")
			field := "integer DEFAULT 9"
			if strings.HasPrefix(mode, "identity") {
				field = "bigint GENERATED ALWAYS AS IDENTITY (START WITH 101 INCREMENT BY 3 CACHE 7)"
			}
			if mode == "generated" {
				field = "integer GENERATED ALWAYS AS (value*2) STORED"
			}
			r.Desired.Clauses = append(r.Desired.Clauses, schemaddl.Clause{ID: "new:extra", Kind: "column", Name: "extra", SQL: field})
			before := schemaddl.Signature(r)
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
			wrappedDB := sql.OpenDB(pgNewValueFaultConnector{Connector: stdlib.GetConnector(*cfg), schema: schema, mode: mode})
			s.db = wrappedDB
			t.Cleanup(func() { _ = wrappedDB.Close() })
			out := s.postgresDesignDDLApply(context.Background(), r)
			if out.State != "rolled_back" || out.Verified || out.Actual != nil || !errors.Is(out.Error, schemaddl.ErrDataChanged) {
				t.Error("new-value corruption acquired commit permission", out.State, out.Error)
			}
			s.db = serverDB
			current := pgDDLRequest(t, s, schema, "original")
			if current.Original.Schema.Revision != r.Original.Schema.Revision || schemaddl.Signature(r) != before {
				t.Error("failed new-value proof changed catalog/request")
			}
		})
	}
}

func TestPostgreSQL17DesignNewValueDefaultsFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_new_value_defaults;
 CREATE TABLE nyadb_new_value_defaults.original(id integer,value numeric(40,20),payload bytea,label text,numbers integer[]);
 INSERT INTO nyadb_new_value_defaults.original VALUES
 (1,7.25,decode('00ff','hex'),NULL,ARRAY[1,NULL,3]),
 (2,-2.5,decode('00','hex'),'',ARRAY[]::integer[]),
 (2,-2.5,decode('00','hex'),'',ARRAY[]::integer[]),
 (3,NULL,NULL,NULL,NULL);`)
	r := pgDDLRequest(t, s, "nyadb_new_value_defaults", "original")
	for _, c := range []schemaddl.Clause{
		{ID: "new:default", Kind: "column", Name: "extra", SQL: "integer DEFAULT 9"},
		{ID: "new:exact", Kind: "column", Name: "exact", SQL: "numeric(40,20) DEFAULT 12345678901234567890.12345678901234567890"},
		{ID: "new:binary", Kind: "column", Name: "binary", SQL: "bytea DEFAULT decode('00ff','hex')"},
		{ID: "new:null", Kind: "column", Name: "null_value", SQL: "text"},
		{ID: "new:time", Kind: "column", Name: "created_at", SQL: "timestamptz DEFAULT CURRENT_TIMESTAMP(3)"},
		{ID: "new:array", Kind: "column", Name: "array_value", SQL: "integer[] DEFAULT ARRAY[1,NULL,3]"},
		{ID: "new:generated", Kind: "column", Name: "computed", SQL: "numeric(50,20) GENERATED ALWAYS AS (round(value)+id) STORED"},
		{ID: "new:encoded", Kind: "column", Name: "encoded", SQL: "text GENERATED ALWAYS AS (encode(payload,'hex')) STORED"},
	} {
		r.Desired.Clauses = append(r.Desired.Clauses, c)
	}
	before := schemaddl.Signature(r)
	out := s.postgresDesignDDLApply(context.Background(), r)
	if out.State != "committed" || out.Error != nil || !out.Verified || out.Actual == nil {
		t.Fatal("native new values failed", out)
	}
	var rows int
	var valid bool
	if err := s.db.QueryRow(`SELECT count(*),bool_and(extra=9 AND exact=12345678901234567890.12345678901234567890 AND encode("binary",'hex')='00ff' AND null_value IS NULL AND created_at IS NOT NULL AND array_value IS NOT DISTINCT FROM ARRAY[1,NULL,3] AND computed IS NOT DISTINCT FROM round(value)+id AND encoded IS NOT DISTINCT FROM encode(payload,'hex')) FROM nyadb_new_value_defaults.original`).Scan(&rows, &valid); err != nil || rows != 4 || !valid || schemaddl.Signature(r) != before {
		t.Fatal("new values lost typed data/NULL/duplicates/association", err)
	}
}

func TestPostgreSQL17DesignNewValueIdentityFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_new_value_identity; CREATE TABLE nyadb_new_value_identity.original(value integer); INSERT INTO nyadb_new_value_identity.original VALUES(7),(7),(NULL),(8)`)
	r := pgDDLRequest(t, s, "nyadb_new_value_identity", "original")
	r.Desired.Clauses = append(r.Desired.Clauses,
		schemaddl.Clause{ID: "new:a", Kind: "column", Name: "allocated", SQL: "bigint GENERATED ALWAYS AS IDENTITY (START WITH 41 INCREMENT BY 3 CACHE 7)"},
		schemaddl.Clause{ID: "new:b", Kind: "column", Name: "allocated_other", SQL: "integer GENERATED ALWAYS AS IDENTITY (START WITH 101 INCREMENT BY 7 CACHE 3)"},
		schemaddl.Clause{ID: "new:gen", Kind: "column", Name: "computed", SQL: "bigint GENERATED ALWAYS AS (allocated*2+value) STORED"})
	out := s.postgresDesignDDLApply(context.Background(), r)
	if out.State != "committed" || out.Error != nil || !out.Verified {
		t.Fatal("new identity/derived values failed", out)
	}
	var rows, unique, otherUnique int
	var valid bool
	if err := s.db.QueryRow(`SELECT count(*),count(DISTINCT allocated),count(DISTINCT allocated_other),bool_and(allocated IN(41,44,47,50) AND allocated_other IN(101,108,115,122) AND computed IS NOT DISTINCT FROM allocated*2+value) FROM nyadb_new_value_identity.original`).Scan(&rows, &unique, &otherUnique, &valid); err != nil || rows != 4 || unique != 4 || otherUnique != 4 || !valid {
		t.Fatal("new identity tuples or generated association changed", err)
	}
}

func TestPostgreSQL17DesignNewValueWithoutInputFixture(t *testing.T) {
	for _, mode := range []string{"default", "generated"} {
		t.Run(mode, func(t *testing.T) {
			s := postgresDesignFixture(t)
			schema := "nyadb_new_value_no_input_" + mode
			run(t, s, "CREATE SCHEMA "+quote(schema)+"; CREATE TABLE "+quote(schema)+".original(value integer); INSERT INTO "+quote(schema)+".original VALUES(7),(7),(NULL),(8)")
			r := pgDDLRequest(t, s, schema, "original")
			sql := "integer DEFAULT 9"
			if mode == "generated" {
				sql = "integer GENERATED ALWAYS AS (4+5) STORED"
			}
			r.Desired.Clauses = []schemaddl.Clause{{ID: "new:only", Kind: "column", Name: "only_value", SQL: sql}}
			r.AllowColumnDrop = true
			out := s.postgresDesignDDLApply(context.Background(), r)
			if out.State != "committed" || out.Error != nil || !out.Verified {
				t.Fatal("projection with no old ordinary inputs failed", out)
			}
			var rows int
			var valid bool
			if err := s.db.QueryRow("SELECT count(*),bool_and(only_value=9) FROM "+quote(schema)+".original").Scan(&rows, &valid); err != nil || rows != 4 || !valid {
				t.Fatal("all-new row projection lost count/value", err)
			}
		})
	}
}

func TestPostgreSQL17DesignNewValueRepeatabilityFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_new_value_repeat; CREATE TABLE nyadb_new_value_repeat.original(value integer); INSERT INTO nyadb_new_value_repeat.original VALUES(7)`)
	for _, sample := range []struct{ typ, call string }{
		{"double precision", "random()"}, {"uuid", "gen_random_uuid()"}, {"timestamptz", "clock_timestamp()"}, {"timestamptz", "statement_timestamp()"},
	} {
		t.Run(sample.call, func(t *testing.T) {
			r := pgDDLRequest(t, s, "nyadb_new_value_repeat", "original")
			r.Desired.Clauses = append(r.Desired.Clauses, schemaddl.Clause{ID: "new:unproven", Kind: "column", Name: "unproven", SQL: sample.typ + " DEFAULT " + sample.call})
			out := s.postgresDesignDDLApply(context.Background(), r)
			if !errors.Is(out.Error, errPostgresValueRepeatability) || out.State != "rolled_back" || out.Verified || out.Actual != nil {
				t.Fatal("nonrepeatable output was claimed exactly verified", out)
			}
			if current := pgDDLRequest(t, s, "nyadb_new_value_repeat", "original"); current.Original.Schema.Revision != r.Original.Schema.Revision {
				t.Fatal("unproven default changed original catalog")
			}
		})
	}
}

func TestPostgreSQL17DesignNewValueChangedGenerationFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_new_value_changed; CREATE TABLE nyadb_new_value_changed.original(value numeric(40,20),computed numeric GENERATED ALWAYS AS (round(value)) STORED); INSERT INTO nyadb_new_value_changed.original(value) VALUES(7.25),(-2.5),(NULL)`)
	r := pgDDLRequest(t, s, "nyadb_new_value_changed", "original")
	pgNativeEditField(t, &r.Desired, "computed", func(f *schemaddl.PostgreSQLField) { f.Generation = "trunc(value)" })
	if r.Original.Schema.Postgres.ServerVersion < 170000 {
		if _, err := schemaddl.PlanPostgreSQL(r.Original.Schema, r.Desired, "nyadb_new_value_changed", false); !errors.Is(err, schemaddl.ErrUnsupported) {
			t.Fatal("old server acquired SET EXPRESSION plan", err)
		}
		return
	}
	out := s.postgresDesignDDLApply(context.Background(), r)
	if out.State != "committed" || out.Error != nil || !out.Verified {
		t.Fatal("new generated expression was not independently checked", out)
	}
	var valid bool
	if err := s.db.QueryRow(`SELECT bool_and(computed IS NOT DISTINCT FROM trunc(value)) FROM nyadb_new_value_changed.original`).Scan(&valid); err != nil || !valid {
		t.Fatal("changed generated values differ", err)
	}
}

type pgValueProjectionGateConnector struct {
	driver.Connector
	query string
}

func (c pgValueProjectionGateConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return pgValueProjectionGateConnection{Conn: conn, query: c.query}, nil
}

type pgValueProjectionGateConnection struct {
	driver.Conn
	query string
}

func (c pgValueProjectionGateConnection) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	executor := c.Conn.(driver.ExecerContext)
	if strings.HasPrefix(query, `INSERT INTO `) && strings.Contains(query, `"__nyadb_values_`) {
		// The INSERT gate is a real server advisory lock wait; cancellation must
		// wait for PostgreSQL/driver acknowledgement and release the owned probe.
		if _, err := executor.ExecContext(ctx, c.query, nil); err != nil {
			return nil, err
		}
	}
	return executor.ExecContext(ctx, query, args)
}
func (c pgValueProjectionGateConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func TestPostgreSQL17DesignNewValueProjectionLifecycleFixture(t *testing.T) {
	for _, mode := range []string{"cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			s := postgresDesignFixture(t)
			schema := "nyadb_value_projection_" + mode
			run(t, s, "CREATE SCHEMA "+quote(schema)+"; CREATE TABLE "+quote(schema)+".original(value integer); INSERT INTO "+quote(schema)+".original VALUES(7),(NULL)")
			r := pgDDLRequest(t, s, schema, "original")
			r.Desired.Clauses = append(r.Desired.Clauses, schemaddl.Clause{ID: "new:extra", Kind: "column", Name: "extra", SQL: "integer DEFAULT 9"})
			serverDB := s.db
			t.Cleanup(func() { _ = serverDB.Close() })
			locker, err := serverDB.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer locker.Close()
			defer rollbackDesign(locker)
			key := int64(79412301)
			if mode == "close" {
				key++
			}
			query := "SELECT pg_catalog.pg_advisory_xact_lock(" + strconv.FormatInt(key, 10) + ")"
			for _, statement := range []string{"BEGIN", query} {
				if _, err := locker.ExecContext(context.Background(), statement); err != nil {
					t.Fatal(err)
				}
			}
			port, err := strconv.ParseUint(os.Getenv("NYADB_POSTGRES_PORT"), 10, 16)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := postgresConfig(connections.Profile{ID: s.id, Kind: connections.PostgreSQL, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_it", Database: "nyadb_it", TLSMode: "disable"}, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			wrappedDB := sql.OpenDB(pgValueProjectionGateConnector{Connector: stdlib.GetConnector(*cfg), query: query})
			t.Cleanup(func() { _ = wrappedDB.Close() })
			s.db = wrappedDB
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan contract.SchemaDesignOutcome, 1)
			go func() { result <- s.postgresDesignDDLApply(ctx, r) }()
			// The lock gate follows the full independent catalog/projection
			// preparation. Allow that work to finish before judging the real wait.
			wait, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			for {
				var waiting bool
				if err := serverDB.QueryRowContext(wait, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_stat_activity WHERE query=$1 AND wait_event_type='Lock')`, query).Scan(&waiting); err != nil {
					t.Fatal("projection did not enter actual lock wait", err)
				}
				if waiting {
					break
				}
				select {
				case out := <-result:
					t.Fatal("projection returned before the real INSERT gate", out.State, out.Error)
				case <-wait.Done():
					t.Fatal("projection did not reach real INSERT gate")
				case <-time.After(10 * time.Millisecond):
				}
			}
			closed := make(chan error, 1)
			if mode == "close" {
				go func() { closed <- s.Close() }()
			} else {
				cancel()
			}
			out := pgDDLAwait(t, result)
			if out.Error == nil || out.Verified || out.Actual != nil || out.State != "rolled_back" && out.State != "uncertain" {
				t.Fatal("cancelled projection acquired commit permission", out)
			}
			if mode == "close" {
				select {
				case err := <-closed:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("projection close did not release its owned connection")
				}
			}
			_ = rollbackDesign(locker)
			_ = locker.Close()
			current, err := postgresCatalogSnapshot(context.Background(), serverDB, r.Original.Target)
			if err != nil || current.Schema.Revision != r.Original.Schema.Revision {
				t.Fatal("cancel/close changed original catalog", err)
			}
			var probes int
			if err := serverDB.QueryRow(`SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname LIKE '__nyadb_values_%'`, schema).Scan(&probes); err != nil || probes != 0 {
				t.Fatal("cancel/close leaked value projection", err)
			}
		})
	}
}
