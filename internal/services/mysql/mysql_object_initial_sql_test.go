package mysql

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"
)

type initialSQLRecorder struct {
	mu     sync.Mutex
	calls  []string
	closed int
	fail   bool
}
type initialSQLMockConnector struct{ r *initialSQLRecorder }

func (c initialSQLMockConnector) Connect(context.Context) (driver.Conn, error) {
	return initialSQLMockConn{c.r}, nil
}
func (initialSQLMockConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }

type initialSQLMockConn struct{ r *initialSQLRecorder }

func (c initialSQLMockConn) Close() error { c.r.mu.Lock(); c.r.closed++; c.r.mu.Unlock(); return nil }
func (initialSQLMockConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (initialSQLMockConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected begin") }
func (c initialSQLMockConn) QueryContext(ctx context.Context, text string, _ []driver.NamedValue) (driver.Rows, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if text == "SELECT @@SESSION.sql_mode" {
		return &structureRows{cols: []string{"mode"}, values: [][]driver.Value{{""}}}, nil
	}
	return &structureRows{cols: []string{"value"}, values: [][]driver.Value{{int64(1)}}}, nil
}
func (c initialSQLMockConn) ExecContext(_ context.Context, text string, _ []driver.NamedValue) (driver.Result, error) {
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	c.r.calls = append(c.r.calls, text)
	if c.r.fail && len(c.r.calls) == 2 {
		return nil, errors.New("private-token server error")
	}
	return driver.RowsAffected(0), nil
}
func TestPhysicalInitialSQLRunsOncePerNewConnection(t *testing.T) {
	r := &initialSQLRecorder{}
	connector := WrapMySQLInitialSQL(initialSQLMockConnector{r}, "SET @a='one;two'; SET @b=2;")
	for i := 0; i < 2; i++ {
		conn, e := connector.Connect(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		conn.Close()
	}
	if len(r.calls) != 4 || r.calls[0] != "SET @a='one;two'" || r.calls[2] != r.calls[0] || r.closed != 2 {
		t.Fatalf("initialization was skipped or replayed: %+v", r.calls)
	}
}
func TestPhysicalInitialSQLFailureClosesAndDoesNotReplay(t *testing.T) {
	r := &initialSQLRecorder{fail: true}
	connector := WrapMySQLInitialSQL(initialSQLMockConnector{r}, "SET @a=1; SET @secret='private-token'; SET @b=2;")
	conn, e := connector.Connect(context.Background())
	if e == nil || conn != nil || len(r.calls) != 2 || r.closed != 1 {
		t.Fatalf("failed initialization was retried or left open: %+v %v", r.calls, e)
	}
	if strings.Contains(e.Error(), "private-token") {
		t.Fatal("initializer leaked a secret in its error")
	}
}
