package engines

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func sqliteSession(t *testing.T) *Session {
	t.Helper()
	m := NewManager()
	t.Cleanup(func() { m.Close() })
	if e := m.Connect(context.Background(), connections.Profile{ID: 71, Kind: connections.SQLite, Database: filepath.Join(t.TempDir(), "user.sqlite")}, ""); e != nil {
		t.Fatal(e)
	}
	s, _ := m.Session(71)
	return s
}
func run(t *testing.T, s *Session, q string) contract.SQLResult {
	t.Helper()
	l, e := s.Reserve(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	r, e := l.Execute(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func object(s *Session, name string) contract.ObjectRef {
	schema := "public"
	if s.kind == connections.SQLite {
		schema = "main"
	}
	return contract.ObjectRef{ProfileID: s.id, Catalog: s.Catalog(), Schema: schema, Kind: "table", Name: name}
}
func TestSQLiteActualTypesReliableKeysAndExplicitTransactions(t *testing.T) {
	s := sqliteSession(t)
	run(t, s, `CREATE TABLE actual(id INTEGER PRIMARY KEY, v TEXT, bin BLOB, gen TEXT GENERATED ALWAYS AS (v || 'x') VIRTUAL); INSERT INTO actual(id,v,bin) VALUES(1,'before',x''),(2,NULL,x'00ff'); CREATE TABLE nullable(a INTEGER,b INTEGER,PRIMARY KEY(a,b)); CREATE TABLE compound(a INTEGER NOT NULL,b INTEGER NOT NULL,v,PRIMARY KEY(a,b)); INSERT INTO compound VALUES(1,2,'old'); CREATE VIEW readonly AS SELECT * FROM actual;`)
	p, e := s.Page(context.Background(), contract.PageRequest{Object: object(s, "actual"), Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	if !p.Table.Editable || p.Rows[0][2].Kind != contract.Binary || len(p.Rows[0][2].Bytes) != 0 || p.Rows[1][1].Kind != contract.Null {
		t.Fatalf("typed page %#v", p)
	}
	run(t, s, `CREATE TABLE nullable_desc(id INTEGER PRIMARY  KEY    DESC,v TEXT);INSERT INTO nullable_desc VALUES(NULL,'null key');`)
	desc, err := s.Structure(context.Background(), object(s, "nullable_desc"))
	if err != nil || desc.Editable {
		t.Fatalf("indexed INTEGER PRIMARY KEY DESC is nullable/read-only %+v %v", desc, err)
	}
	n, e := s.Structure(context.Background(), object(s, "nullable"))
	if e != nil || n.Editable {
		t.Fatalf("nullable composite PK must be read only: %#v %v", n, e)
	}
	cp, e := s.Page(context.Background(), contract.PageRequest{Object: object(s, "compound"), Limit: 100})
	if e != nil || len(cp.Table.Key) != 2 {
		t.Fatalf("composite %#v %v", cp, e)
	}
	tx, e := s.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	edit := contract.Edit{Kind: "update", Identity: cp.Identities[0], Original: cp.Rows[0], Values: map[string]contract.Cell{"v": {Kind: contract.Integer, Text: "9223372036854775807"}}}
	if _, e = tx.Apply(context.Background(), cp.Table.Object, []contract.Edit{edit}); e != nil {
		t.Fatal(e)
	}
	inside, e := tx.Page(context.Background(), contract.PageRequest{Object: cp.Table.Object, Limit: 100})
	if e != nil || inside.Rows[0][2].Kind != contract.Integer || inside.Rows[0][2].Text != "9223372036854775807" {
		t.Fatalf("actual runtime storage type %#v %v", inside, e)
	}
	if e = tx.Rollback(context.Background()); e != nil {
		t.Fatal(e)
	}
	after, e := s.Page(context.Background(), contract.PageRequest{Object: cp.Table.Object, Limit: 100})
	if e != nil || after.Rows[0][2].Text != "old" {
		t.Fatalf("rollback %#v %v", after, e)
	}
	tx, _ = s.Begin(context.Background())
	if _, e = tx.Apply(context.Background(), cp.Table.Object, []contract.Edit{edit}); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Commit(context.Background()); e != nil {
		t.Fatal(e)
	}
	tx, _ = s.Begin(context.Background())
	defer tx.Close()
	if _, e = tx.Apply(context.Background(), cp.Table.Object, []contract.Edit{edit}); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale original not rejected: %v", e)
	}
}
func TestSQLiteLockWaitCancellationAndNoJournalMutation(t *testing.T) {
	s := sqliteSession(t)
	run(t, s, `CREATE TABLE locked(id INTEGER PRIMARY KEY,v TEXT); INSERT INTO locked VALUES(1,'original');`)
	l, _ := s.Reserve(context.Background())
	defer l.Close()
	if _, e := l.Execute(context.Background(), "BEGIN IMMEDIATE"); e != nil {
		t.Fatal(e)
	}
	other, _ := s.Reserve(context.Background())
	defer other.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, e := other.Execute(ctx, "UPDATE locked SET v='changed' WHERE id=1")
	if e == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("bounded acknowledged lock wait %v %v", e, time.Since(start))
	}
	l.Execute(context.Background(), "ROLLBACK")
	r := run(t, s, "PRAGMA journal_mode")
	if r.Sets[0].Rows[0][0].Text != "delete" {
		t.Fatalf("unexpected implicit journal mode %#v", r)
	}
	after := run(t, s, "SELECT v FROM locked")
	if after.Sets[0].Rows[0][0].Text != "original" {
		t.Fatal("cancelled write changed row")
	}
}

type countSink struct{ rows int }

func (s *countSink) BeginResult(i int, c []contract.Column) error { return nil }
func (s *countSink) WriteRow(i int, r []contract.Cell) error      { s.rows++; return nil }
func (s *countSink) EndResult(i int, n int64) error               { return nil }
func TestSQLiteMultiResultsStreamingAndDollarLexer(t *testing.T) {
	s := sqliteSession(t)
	l, _ := s.Reserve(context.Background())
	defer l.Close()
	sink := &countSink{}
	r, e := l.Stream(context.Background(), `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<2001) SELECT i,NULL,'NULL','',x'' FROM n; SELECT 'second';`, sink)
	if e != nil || len(r.Sets) != 2 || sink.rows != 2002 || len(r.Sets[0].Rows) != 1000 {
		t.Fatalf("stream %#v %d %v", r, sink.rows, e)
	}
	parts, e := splitSQL("CREATE FUNCTION f() RETURNS text AS $body$BEGIN; RETURN 'a;b'; END$body$ LANGUAGE plpgsql; SELECT 1;")
	if e != nil || len(parts) != 2 {
		t.Fatalf("dollar scanner %#v %v", parts, e)
	}
}
func TestPostgreSQL17Fixture(t *testing.T) {
	if os.Getenv("NYADB_POSTGRES_IT") != "1" {
		t.Skip("requires dedicated disposable PostgreSQL fixture")
	}
	port, e := strconv.Atoi(os.Getenv("NYADB_POSTGRES_PORT"))
	if e != nil {
		t.Fatal(e)
	}
	m := NewManager()
	defer m.Close()
	ctx := context.Background()
	e = m.Connect(ctx, connections.Profile{ID: 72, Kind: connections.PostgreSQL, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_it", Database: "nyadb_it", TLSMode: "disable"}, "")
	if e != nil {
		t.Fatal(e)
	}
	s, _ := m.Session(72)
	run(t, s, `CREATE SCHEMA secondary;CREATE TABLE secondary.actual(id integer PRIMARY KEY,label text);INSERT INTO secondary.actual VALUES(9,'separate schema');`)
	schemas, e := s.Schemas(ctx)
	if e != nil || len(schemas) < 2 {
		t.Fatalf("PG schemas %v %v", schemas, e)
	}
	objs, e := s.Objects(ctx, "secondary")
	if e != nil || len(objs) != 1 || objs[0].Ref.Catalog != "nyadb_it" || objs[0].Ref.Schema != "secondary" {
		t.Fatalf("PG object identity %+v %v", objs, e)
	}
	lease, e := s.Reserve(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = lease.SetSchema(ctx, "secondary"); e != nil {
		t.Fatal(e)
	}
	scoped, e := lease.Execute(ctx, "SELECT label FROM actual")
	lease.Close()
	if e != nil || scoped.Sets[0].Rows[0][0].Text != "separate schema" {
		t.Fatalf("schema lease %+v %v", scoped, e)
	}
	run(t, s, `CREATE TABLE actual(a bigint NOT NULL,b bigint NOT NULL,n numeric(40,20),payload bytea,j json,gen bigint GENERATED ALWAYS AS (a+b) STORED,PRIMARY KEY(a,b)); INSERT INTO actual(a,b,n,payload,j) VALUES(1,2,12345678901234567890.12345678901234567890,decode('00ff','hex'),' {"x":1} '); CREATE VIEW readonly AS SELECT * FROM actual; CREATE TABLE unkeyed(v text);`)
	p, e := s.Page(ctx, contract.PageRequest{Object: object(s, "actual"), Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	if !p.Table.Editable || len(p.Table.Key) != 2 || p.Rows[0][2].Kind != contract.Decimal || p.Rows[0][2].Text != "12345678901234567890.12345678901234567890" || p.Rows[0][3].Kind != contract.Binary || p.Rows[0][4].Kind != contract.JSON {
		t.Fatalf("PG exact types %#v", p)
	}
	tx, _ := s.Begin(ctx)
	edit := contract.Edit{Kind: "update", Identity: p.Identities[0], Original: p.Rows[0], Values: map[string]contract.Cell{"n": {Kind: contract.Decimal, Text: "42.00000000000000000001"}, "j": {Kind: contract.JSON, Text: `{"x":2}`}}}
	if _, e = tx.Apply(ctx, p.Table.Object, []contract.Edit{edit}); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	tx, _ = s.Begin(ctx)
	if _, e = tx.Apply(ctx, p.Table.Object, []contract.Edit{edit}); !errors.Is(e, ErrConflict) {
		t.Fatalf("PG conflict %v", e)
	}
	tx.Close()
	r := run(t, s, `SELECT NULL,'NULL','',decode('','hex'); SELECT 2;`)
	if len(r.Sets) != 2 || r.Sets[0].Rows[0][0].Kind != contract.Null || r.Sets[0].Rows[0][3].Kind != contract.Binary {
		t.Fatalf("PG multi result %#v", r)
	}
	view := object(s, "readonly")
	view.Kind = "view"
	current, e := s.Page(ctx, contract.PageRequest{Object: object(s, "actual"), Limit: 100, Filters: []contract.Filter{{Column: "a", Operator: "=", Value: contract.Cell{Kind: contract.Integer, Text: "1"}}}, Sorts: []contract.Sort{{Column: "b", Descending: true}}})
	if e != nil || len(current.Rows) != 1 {
		t.Fatalf("PG filter/sort %+v %v", current, e)
	}
	rollback, _ := s.Begin(ctx)
	change := contract.Edit{Kind: "update", Identity: current.Identities[0], Original: current.Rows[0], Values: map[string]contract.Cell{"n": {Kind: contract.Decimal, Text: "8"}}}
	if _, e = rollback.Apply(ctx, current.Table.Object, []contract.Edit{change}); e != nil {
		t.Fatal(e)
	}
	if e = rollback.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	after, e := s.Page(ctx, contract.PageRequest{Object: current.Table.Object, Limit: 100})
	if e != nil || after.Rows[0][2].Text != "42.00000000000000000001" {
		t.Fatalf("PG rollback %+v %v", after, e)
	}
	meta, e := s.Structure(ctx, view)
	if e != nil || meta.Editable {
		t.Fatalf("view %#v %v", meta, e)
	}
}

func TestSQLiteCommitVerificationGeneratedKeyAndRepeatedUpdates(t *testing.T) {
	s := sqliteSession(t)
	run(t, s, `CREATE TABLE repeated(id INTEGER PRIMARY KEY,note TEXT,gen TEXT GENERATED ALWAYS AS (note||'x'));`)
	o := object(s, "repeated")
	tx, e := s.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	insert := contract.Edit{Kind: "insert", Values: map[string]contract.Cell{"note": {Kind: contract.Text, Text: "first"}}}
	if _, e = tx.Apply(context.Background(), o, []contract.Edit{insert}); e != nil {
		t.Fatal(e)
	}
	for _, text := range []string{"second", "third"} {
		p, e := tx.Page(context.Background(), contract.PageRequest{Object: o, Limit: 100})
		if e != nil {
			t.Fatal(e)
		}
		edit := contract.Edit{Kind: "update", Identity: p.Identities[0], Original: p.Rows[0], Values: map[string]contract.Cell{"note": {Kind: contract.Text, Text: text}}}
		if _, e = tx.Apply(context.Background(), o, []contract.Edit{edit}); e != nil {
			t.Fatal(e)
		}
	}
	r, e := tx.Commit(context.Background())
	if e != nil || !r.Verified || r.Incomplete || len(r.Saved) != 1 {
		t.Fatalf("commit did not verify final generated-key row: %#v %v", r, e)
	}
}
