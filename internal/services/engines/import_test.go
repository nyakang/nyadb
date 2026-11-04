package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func importEdit(id int, text string) contract.DataEdit {
	return contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: fmt.Sprint(id)}, "value": {Kind: contract.Text, Text: text}}}
}
func verifyImports(t *testing.T, s *Session, db string) {
	t.Helper()
	ctx := context.Background()
	for _, mode := range []string{"append", "update", "upsert", "insert_missing", "delete", "replace"} {
		t.Run(mode, func(t *testing.T) {
			table := "import_" + mode
			q := quote(db) + "." + quote(table)
			if _, e := s.db.Exec("CREATE TABLE " + q + "(id INTEGER PRIMARY KEY, value TEXT NOT NULL); INSERT INTO " + q + " VALUES(1,'before')"); e != nil {
				t.Fatal(e)
			}
			r := contract.DataImportRequest{Mode: mode, Edits: []contract.DataEdit{importEdit(1, "after"), importEdit(2, "new")}}
			if mode == "append" {
				r.Edits = r.Edits[1:]
			}
			if mode != "append" && mode != "replace" {
				r.MatchColumns = []string{"id"}
			}
			o, e := s.importData(ctx, s.id, db, table, r)
			if e != nil || !o.Committed || o.CommitUncertain {
				t.Fatalf("import: %+v %v", o, e)
			}
			var count int
			if e = s.db.QueryRow("SELECT count(*) FROM " + q).Scan(&count); e != nil {
				t.Fatal(e)
			}
			want := 2
			if mode == "update" {
				want = 1
			}
			if mode == "delete" {
				want = 0
			}
			if count != want {
				t.Fatalf("rows=%d want=%d", count, want)
			}
			if mode != "delete" {
				var value string
				s.db.QueryRow("SELECT value FROM " + q + " WHERE id=1").Scan(&value)
				want := "after"
				if mode == "append" || mode == "insert_missing" {
					want = "before"
				}
				if value != want {
					t.Fatalf("value=%s want=%s", value, want)
				}
			}
		})
	}
	q := quote(db) + `."import_savepoints"`
	if _, e := s.db.Exec("CREATE TABLE " + q + "(id INTEGER PRIMARY KEY,value TEXT NOT NULL)"); e != nil {
		t.Fatal(e)
	}
	r := contract.DataImportRequest{ContinueOnError: true, Edits: []contract.DataEdit{importEdit(1, "a"), importEdit(1, "duplicate"), importEdit(2, "b")}}
	o, e := s.importData(ctx, s.id, db, "import_savepoints", r)
	if e != nil || !o.Committed || o.Rows[1].Status != "failed" {
		t.Fatalf("savepoint recovery: %+v %v", o, e)
	}
	r.Mode, r.ContinueOnError = "replace", false
	o, e = s.importData(ctx, s.id, db, "import_savepoints", r)
	if e == nil || o.Committed || o.CommitUncertain {
		t.Fatalf("expected acknowledged rollback: %+v %v", o, e)
	}
	var count int
	if e = s.db.QueryRow("SELECT count(*) FROM " + q).Scan(&count); e != nil || count != 2 {
		t.Fatal("replace did not roll back", count, e)
	}
	r = contract.DataImportRequest{NewTable: true, Columns: []contract.DataImportColumn{{Name: "id", Type: "BIGINT", PrimaryKey: true}, {Name: "value", Type: "TEXT", Nullable: true}}, Edits: []contract.DataEdit{importEdit(9, "9007199254740993.123456789")}}
	if s.kind == connections.SQLite {
		r.Columns[0].Type = "INTEGER"
	}
	o, e = s.importData(ctx, s.id, db, "import_new", r)
	if e != nil || !o.Created || !o.Committed {
		t.Fatalf("create import: %+v %v", o, e)
	}
	var exact string
	if e = s.db.QueryRow("SELECT value FROM " + quote(db) + `."import_new"`).Scan(&exact); e != nil || exact != "9007199254740993.123456789" {
		t.Fatal(exact, e)
	}
	parent, child := quote(db)+`."import_parent"`, quote(db)+`."import_child"`
	parentRef := parent
	if s.kind == connections.SQLite {
		parentRef = quote("import_parent")
	}
	if _, e = s.db.Exec("CREATE TABLE " + parent + "(a INTEGER,b INTEGER,PRIMARY KEY(a,b)); CREATE TABLE " + child + "(id INTEGER PRIMARY KEY,a INTEGER,b INTEGER,FOREIGN KEY(a,b) REFERENCES " + parentRef + "(a,b)); INSERT INTO " + parent + " VALUES(1,2); INSERT INTO " + child + " VALUES(1,1,2)"); e != nil {
		t.Fatal(e)
	}
	bad := contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"id": {Text: "2"}, "a": {Text: "9"}, "b": {Text: "9"}}}
	o, e = s.importData(ctx, s.id, db, "import_child", contract.DataImportRequest{Mode: "replace", Edits: []contract.DataEdit{bad}})
	if e == nil || o.Committed || o.CommitUncertain {
		t.Fatal("foreign key checks were disabled", o, e)
	}
	if e = s.db.QueryRow("SELECT count(*) FROM " + child + " WHERE id=1").Scan(&count); e != nil || count != 1 {
		t.Fatal("DELETE did not roll back", count, e)
	}
	o, e = s.importData(ctx, s.id, db, "import_parent", contract.DataImportRequest{Mode: "delete", MatchColumns: []string{"a"}, Edits: []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"a": {Text: "1"}}}}})
	if e == nil || o.Committed {
		t.Fatal("partial unique key accepted", o, e)
	}
	generated := quote(db) + `."import_generated"`
	if _, e = s.db.Exec("CREATE TABLE " + generated + "(id INTEGER PRIMARY KEY,value INTEGER GENERATED ALWAYS AS(id*2) STORED)"); e != nil {
		t.Fatal(e)
	}
	o, e = s.importData(ctx, s.id, db, "import_generated", contract.DataImportRequest{Edits: []contract.DataEdit{importEdit(1, "2")}})
	if e == nil || o.Committed {
		t.Fatal("generated column was writable", o, e)
	}
	if s.kind == connections.PostgreSQL {
		if _, e = s.db.Exec("CREATE TABLE " + quote(db) + `."import_identity"(id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,value text)`); e != nil {
			t.Fatal(e)
		}
		o, e = s.importData(ctx, s.id, db, "import_identity", contract.DataImportRequest{Edits: []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"value": {Text: "server identity"}}}}})
		if e != nil || !o.Committed {
			t.Fatal("omitted identity could not be imported", o, e)
		}
		o, e = s.importData(ctx, s.id, db, "import_identity", contract.DataImportRequest{Edits: []contract.DataEdit{importEdit(7, "must reject explicit identity")}})
		if e == nil || o.Committed {
			t.Fatal("ALWAYS identity was overwritten", o, e)
		}
	}
	// SQL quotas cannot prevent an import or background maintenance.
	var leases []contract.QueryLease
	for i := 0; i < cap(s.sqlSlots); i++ {
		l, e := s.ReserveQuery(uint64(i + 101))
		if e != nil {
			t.Fatal(e)
		}
		leases = append(leases, l)
		if e = l.Preconnect(ctx); e != nil {
			t.Fatal(e)
		}
	}
	defer func() {
		for _, l := range leases {
			l.Release()
		}
	}()
	o, e = s.importData(ctx, s.id, db, "import_new", contract.DataImportRequest{Edits: []contract.DataEdit{importEdit(10, "pool independent")}})
	if e != nil || !o.Committed {
		t.Fatal(o, e)
	}
	target := contract.SchemaDesignTarget{Object: s.Namespace(db).Object("table", "import_new"), Generation: s.Generation()}
	for _, action := range s.Maintenance().Actions(target) {
		plan, e := s.Maintenance().Plan(ctx, contract.MaintenanceRequest{Target: target, Action: action.ID})
		if e != nil {
			t.Fatal(e)
		}
		out := s.Maintenance().Execute(ctx, plan)
		if out.Error != nil || out.State != "completed" {
			t.Fatalf("maintenance %s: %+v", action.ID, out)
		}
	}
}
func TestSQLiteImportsAndMaintenance(t *testing.T) { verifyImports(t, adapterFixture(t), "main") }
func TestSQLiteImportUntypedReaderValues(t *testing.T) {
	s := adapterFixture(t)
	r := contract.DataImportRequest{NewTable: true, Columns: []contract.DataImportColumn{
		{Name: "id", Type: "INTEGER", PrimaryKey: true},
		{Name: "amount", Type: "TEXT"},
		{Name: "payload", Type: "BLOB"},
		{Name: "note", Type: "TEXT", Nullable: true},
	}, Edits: []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{
		"id": {Text: "1"}, "amount": {Text: "9007199254740993.123456789"},
		"payload": {Kind: contract.Binary, Bytes: []byte{0, 255}}, "note": {Kind: contract.Null},
	}}}}
	out, err := s.importData(context.Background(), s.id, "main", "reader_values", r)
	if err != nil || !out.Committed || len(out.Rows) != 1 || out.Rows[0].Status != "committed" {
		t.Fatalf("text reader import: %+v %v", out, err)
	}
	var amount, binary string
	var null bool
	if err = s.db.QueryRow("SELECT amount,hex(payload),note IS NULL FROM reader_values WHERE id=1").Scan(&amount, &binary, &null); err != nil || amount != "9007199254740993.123456789" || binary != "00FF" || !null {
		t.Fatal(amount, binary, null, err)
	}
	for _, check := range []struct {
		column string
		value  contract.DataValue
		want   any
	}{
		{"REAL", contract.DataValue{Kind: contract.Floating, Text: "0.1"}, float64(0.1)},
		{"TEXT", contract.DataValue{Kind: contract.Floating, Text: "0.10"}, "0.10"},
		{"INTEGER", contract.DataValue{Kind: contract.Boolean, Text: "true"}, int64(1)},
	} {
		got, err := s.importParameter(contract.StructureColumn{Type: check.column}, check.value)
		if err != nil || got != check.want {
			t.Fatal(check, got, err)
		}
	}
	if _, err = s.importParameter(contract.StructureColumn{Type: "REAL"}, contract.DataValue{Kind: contract.Decimal, Text: "0.1"}); err == nil {
		t.Fatal("exact decimal was rounded to float")
	}
}
func TestPostgreSQL17ImportsAndMaintenanceFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	if _, e := s.db.Exec("CREATE SCHEMA nyadb_import"); e != nil {
		t.Fatal(e)
	}
	defer s.db.Exec("DROP SCHEMA nyadb_import CASCADE")
	verifyImports(t, s, "nyadb_import")
}

func TestSQLiteImportRejectsRealAffinityIntegerRounding(t *testing.T) {
	s := adapterFixture(t)
	ctx := context.Background()
	for _, typ := range []string{"REAL", "DOUBLE PRECISION", "FLOAT", "NUMERIC", "TEXT"} {
		t.Run(typ, func(t *testing.T) {
			if _, err := s.db.Exec("CREATE TABLE precision_check(value " + typ + ")"); err != nil {
				t.Fatal(err)
			}
			defer s.db.Exec("DROP TABLE precision_check")
			for _, kind := range []contract.CellKind{"", contract.Integer, contract.Decimal} {
				request := contract.DataImportRequest{Edits: []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"value": {Kind: kind, Text: "9007199254740993"}}}}}
				out, err := s.importData(ctx, s.id, "main", "precision_check", request)
				if typ == "NUMERIC" || typ == "TEXT" {
					if err != nil || !out.Committed {
						t.Fatalf("exact target rejected: %+v %v", out, err)
					}
					var value string
					if err = s.db.QueryRow("SELECT value FROM precision_check").Scan(&value); err != nil || value != "9007199254740993" {
						t.Fatal(value, err)
					}
				} else {
					if err == nil || out.Committed || out.CommitUncertain {
						t.Fatalf("lossy target did not reject with confirmed rollback: %+v %v", out, err)
					}
					var count int
					if err = s.db.QueryRow("SELECT count(*) FROM precision_check").Scan(&count); err != nil || count != 0 {
						t.Fatal(count, err)
					}
				}
				if _, err = s.db.Exec("DELETE FROM precision_check"); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func verifyImportLostCommit(t *testing.T, s *Session, actual *sql.DB, schema string) {
	t.Helper()
	request := contract.DataImportRequest{NewTable: true, Columns: []contract.DataImportColumn{{Name: "id", Type: "INTEGER"}, {Name: "value", Type: "TEXT"}}, Edits: []contract.DataEdit{importEdit(1, "committed without acknowledgement")}}
	out, err := s.importData(context.Background(), s.id, schema, "import_lost_commit", request)
	if err == nil || out.Committed || !out.CommitUncertain || !out.CreationUncertain || len(out.Rows) != 1 || out.Rows[0].Status != "uncertain" {
		t.Fatalf("lost commit receipt permits replay: %+v %v", out, err)
	}
	var count int
	if err = actual.QueryRow("SELECT count(*) FROM " + quote(schema) + ".import_lost_commit").Scan(&count); err != nil || count != 1 {
		t.Fatal("fixture did not commit before losing acknowledgement", count, err)
	}
}

func TestSQLiteImportLostCommit(t *testing.T) {
	s := adapterFixture(t)
	actual := s.db
	t.Cleanup(func() { actual.Close() })
	driver, err := newSQLiteDataDriver()
	if err != nil {
		t.Fatal(err)
	}
	options := s.strategy.(sqliteDialect).options
	s.db = sql.OpenDB(pgLostCommitConnector{Connector: &sqliteConnector{driver: driver, dsn: sqliteConfiguredURI(s.catalog, options), options: options}})
	verifyImportLostCommit(t, s, actual, "main")
}

func TestPostgreSQL17ImportLostCommitFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	actual := s.db
	t.Cleanup(func() { actual.Close() })
	port, err := strconv.ParseUint(os.Getenv("NYADB_POSTGRES_PORT"), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := postgresConfig(connections.Profile{ID: s.id, Kind: connections.PostgreSQL, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_it", Database: "nyadb_it", TLSMode: "disable"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.db = sql.OpenDB(pgLostCommitConnector{Connector: stdlib.GetConnector(*cfg)})
	verifyImportLostCommit(t, s, actual, "public")
}

type generatedImportSource struct {
	index, count int
	closed       bool
	cancel       context.CancelFunc
}

func (r *generatedImportSource) Next(ctx context.Context) (contract.DataEdit, error) {
	if e := ctx.Err(); e != nil {
		return contract.DataEdit{}, e
	}
	if r.index == r.count {
		return contract.DataEdit{}, io.EOF
	}
	r.index++
	if r.cancel != nil && r.index == 1500 {
		r.cancel()
	}
	return importEdit(r.index, "bounded"), nil
}
func (r *generatedImportSource) Close() error {
	if r.closed {
		return errors.New("source closed twice")
	}
	r.closed = true
	return nil
}
func verifyStreamBoundedCancellationAndJournalFailure(t *testing.T, s *Session, schema string) {
	t.Helper()
	if _, e := s.db.Exec("CREATE TABLE imported(id INTEGER PRIMARY KEY,value TEXT)"); e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"success", "cancel", "journal"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			source := &generatedImportSource{count: 11000}
			if mode == "cancel" {
				source.cancel = cancel
			}
			journal := 0
			out, e := s.importStream(ctx, s.id, schema, "imported", contract.DataImportStreamRequest{Request: contract.DataImportRequest{Mode: "replace"}, Rows: int64(source.count), Open: func(context.Context) (contract.DataImportRowSource, error) { return source, nil }, Record: func(contract.DataImportRowResult) error {
				journal++
				if mode == "journal" && journal == 1500 {
					return errors.New("journal full")
				}
				return nil
			}})
			if mode == "success" {
				if e != nil || !out.Committed || journal != 11000 || len(out.Rows) != 1000 {
					t.Fatal(out, e, journal)
				}
			} else if e == nil || out.Committed || out.CommitUncertain && (mode != "cancel" || s.kind != connections.PostgreSQL) {
				t.Fatalf("failed source outcome: committed=%v uncertain=%v error=%v", out.Committed, out.CommitUncertain, e)
			}
			if mode != "success" {
				if out.CommitUncertain && (len(out.Rows) == 0 || out.Rows[0].Status != "uncertain") {
					t.Fatal("lost rollback acknowledgement did not prevent replay")
				}
				var count int
				if err := s.db.QueryRow("SELECT count(*) FROM imported").Scan(&count); err != nil || count != 11000 {
					t.Fatal("failed import changed committed data", count, err)
				}
			}
			if !source.closed {
				t.Fatal("source leaked")
			}
		})
	}
}

func TestSQLiteStreamBoundedCancellationAndJournalFailure(t *testing.T) {
	verifyStreamBoundedCancellationAndJournalFailure(t, adapterFixture(t), "main")
}
func TestPostgreSQL17ImportStreamFixture(t *testing.T) {
	verifyStreamBoundedCancellationAndJournalFailure(t, postgresDesignFixture(t), "public")
}
