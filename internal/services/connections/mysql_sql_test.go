package connections

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type sqlTestConnector struct{ log chan string }

func (s sqlTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &sqlTestConn{log: s.log}, nil
}
func (sqlTestConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }

type sqlTestConn struct{ log chan string }

func (*sqlTestConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not used") }
func (*sqlTestConn) Begin() (driver.Tx, error)           { return nil, errors.New("not used") }
func (*sqlTestConn) Close() error                        { return nil }
func (*sqlTestConn) Ping(context.Context) error          { return nil }
func (c *sqlTestConn) ExecContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.log <- "exec " + q
	if strings.HasPrefix(q, "USE ") {
		return driver.RowsAffected(0), nil
	}
	if q == "UPDATE products SET value=2" {
		return driver.RowsAffected(3), nil
	}
	return nil, fmt.Errorf("unexpected exec: %s", q)
}
func (c *sqlTestConn) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.log <- "query " + q
	if q == "SELECT id,name FROM products" {
		return &programMetadataRows{cols: []string{"id", "name"}, data: [][]driver.Value{
			{int64(1), "alfa"}, {int64(2), nil},
		}}, nil
	}
	if q == "SELECT id FROM products LIMIT 2000" {
		items := make([][]driver.Value, 2000)
		for i := range items {
			items[i] = []driver.Value{int64(i)}
		}
		return &programMetadataRows{cols: []string{"id"}, data: items}, nil
	}
	return nil, fmt.Errorf("unexpected query: %s", q)
}
func TestMySQLExecuteSQLBoundedResultsAndAffectedRows(t *testing.T) {
	manager := NewMySQLSessionManager()
	log := make(chan string, 16)
	manager.opener = func(context.Context, ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(sqlTestConnector{log: log}), nil
	}
	if err := manager.Connect(context.Background(), ActiveSettings{ProfileID: 15}, ""); err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	result, err := manager.ExecuteSQL(context.Background(), 15, "app`tenant", "SELECT id,name FROM products")
	if err != nil {
		t.Fatal(err)
	}
	// Very fast mock queries may complete within one Windows clock tick;
	// zero elapsed duration is valid and should not fail cross-platform CI.
	if len(result.Columns) != 2 || len(result.Rows) != 2 || result.Rows[1][1] != "NULL" || result.Duration < 0 {
		t.Fatalf("result missing columns, null, or timing: %+v", result)
	}
	for _, want := range []string{"exec USE `app``tenant`", "query SELECT id,name FROM products"} {
		if got := <-log; got != want {
			t.Fatalf("wrong statement: want %q got %q", want, got)
		}
	}
	modified, err := manager.ExecuteSQL(context.Background(), 15, "app", "UPDATE products SET value=2")
	if err != nil || modified.AffectedRows != 3 || len(modified.Columns) != 0 {
		t.Fatalf("DML: %+v %v", modified, err)
	}
	<-log
	<-log
	limited, err := manager.ExecuteSQL(context.Background(), 15, "app", "SELECT id FROM products LIMIT 2000")
	if err != nil || len(limited.Rows) != MaxSQLResultRows || !limited.Truncated {
		t.Fatalf("unbounded result %+v %v", limited, err)
	}
}
func TestMySQLExecuteSQLRejectsEmptyCancelledAndWrongSession(t *testing.T) {
	manager := NewMySQLSessionManager()
	if _, err := manager.ExecuteSQL(context.Background(), 10, "", " "); !errors.Is(err, ErrEmptySQL) {
		t.Fatal(err)
	}
	if _, err := manager.ExecuteSQL(context.Background(), 10, "", "SELECT 1"); !errors.Is(err, ErrMySQLNotConnected) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manager.opener = func(context.Context, ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(sqlTestConnector{log: make(chan string, 16)}), nil
	}
	if err := manager.Connect(context.Background(), ActiveSettings{ProfileID: 11}, ""); err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if _, err := manager.ExecuteSQL(ctx, 11, "app", "SELECT id,name FROM products"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error: %v", err)
	}
}
func TestSQLStatementClassification(t *testing.T) {
	for _, tc := range []struct {
		sql  string
		rows bool
	}{
		{"-- comment\nSELECT 1", true}, {" /*note*/ SHOW DATABASES", true},
		{"WITH cte AS (SELECT 1) SELECT * FROM cte", true},
		{"CALL proc()", true}, {"UPDATE x SET v=1", false}, {"CREATE TABLE x (id int)", false},
	} {
		if got := sqlReturnsRows(tc.sql); got != tc.rows {
			t.Fatalf("%q got %v", tc.sql, got)
		}
	}
}
