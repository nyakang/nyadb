package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

type sqlBoundsConnector struct{ started chan string }

func (c sqlBoundsConnector) Connect(context.Context) (driver.Conn, error) {
	return &sqlBoundsConn{started: c.started}, nil
}
func (sqlBoundsConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }

type sqlBoundsConn struct{ started chan string }

func (*sqlBoundsConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not used") }
func (*sqlBoundsConn) Begin() (driver.Tx, error)           { return nil, errors.New("not used") }
func (*sqlBoundsConn) Close() error                        { return nil }
func (*sqlBoundsConn) Ping(context.Context) error          { return nil }
func (*sqlBoundsConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}
func (c *sqlBoundsConn) QueryContext(ctx context.Context, statement string, _ []driver.NamedValue) (driver.Rows, error) {
	if c.started != nil {
		c.started <- statement
	}
	switch statement {
	case "SELECT broad":
		payload := strings.Repeat("x", maxSQLCellBytes)
		records := make([][]driver.Value, 150)
		for i := range records {
			records[i] = []driver.Value{payload}
		}
		return &programMetadataRows{cols: []string{"blob"}, data: records}, nil
	case "SELECT one_huge_cell":
		return &programMetadataRows{cols: []string{"blob"}, data: [][]driver.Value{{strings.Repeat("x", maxSQLCellBytes+512)}}}, nil
	case "SELECT too_wide":
		cols := make([]string, maxSQLResultColumns+1)
		for i := range cols {
			cols[i] = fmt.Sprintf("c%d", i)
		}
		return &programMetadataRows{cols: cols}, nil
	case "SELECT WAIT":
		<-ctx.Done()
		return nil, ctx.Err()
	case "SELECT ERROR":
		return nil, errors.New("synthetic SQL error")
	}
	return nil, fmt.Errorf("unexpected statement: %s", statement)
}

func newSQLBoundsManager(t *testing.T, started chan string) *MySQLSessionManager {
	t.Helper()
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(sqlBoundsConnector{started: started}), nil
	}
	if err := m.Connect(context.Background(), connections.ActiveSettings{ProfileID: 87}, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestSQLResultByteAndCellCaps(t *testing.T) {
	m := newSQLBoundsManager(t, nil)
	result, err := m.ExecuteSQL(context.Background(), 87, "", "SELECT broad")
	if err != nil || !result.Truncated || len(result.Rows) == 0 || len(result.Rows) >= maxSQLResultBytes/maxSQLCellBytes {
		t.Fatalf("byte cap rows=%d truncated=%t err=%v", len(result.Rows), result.Truncated, err)
	}
	for _, r := range result.Rows {
		if len(r) != 1 || len(r[0]) != maxSQLCellBytes {
			t.Fatal("partial or oversized record")
		}
	}
	cell, err := m.ExecuteSQL(context.Background(), 87, "", "SELECT one_huge_cell")
	if err != nil || !cell.Truncated || len(cell.Rows) != 1 || len(cell.Rows[0][0]) != maxSQLCellBytes {
		t.Fatalf("cell cap truncated=%t rows=%d err=%v", cell.Truncated, len(cell.Rows), err)
	}
	if _, err = m.ExecuteSQL(context.Background(), 87, "", "SELECT too_wide"); err == nil {
		t.Fatal("column cap not enforced")
	}
	if _, err = m.ExecuteSQL(context.Background(), 87, "", "SELECT ERROR"); err == nil || !strings.Contains(err.Error(), "synthetic SQL error") {
		t.Fatalf("missing driver error: %v", err)
	}
	if _, err = m.ExecuteSQL(context.Background(), 999, "", "SELECT broad"); !errors.Is(err, ErrMySQLNotConnected) {
		t.Fatalf("wrong profile reused SQL session: %v", err)
	}
	if _, err = m.ExecuteSQL(context.Background(), 87, "bad\x00name", "SELECT broad"); err == nil {
		t.Fatal("invalid schema accepted")
	}
}

func TestSQLDisconnectCancelsPendingExecution(t *testing.T) {
	started := make(chan string, 1)
	m := newSQLBoundsManager(t, started)
	done := make(chan error, 1)
	go func() { _, err := m.ExecuteSQL(context.Background(), 87, "", "SELECT WAIT"); done <- err }()
	select {
	case name := <-started:
		if name != "SELECT WAIT" {
			t.Fatalf("unexpected query: %s", name)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("query did not start")
	}
	if err := m.Disconnect(87); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("disconnect returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("query was not canceled on disconnect")
	}
}
