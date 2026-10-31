package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
)

type nontransactionalFixture struct {
	base              dataImportFixture
	failAt, warningAt int
	err               error
}
type nontransactionalConnector struct{ f *nontransactionalFixture }

func (c nontransactionalConnector) Connect(context.Context) (driver.Conn, error) {
	return &nontransactionalConn{dataImportConn: &dataImportConn{f: &c.f.base, id: 1}, f: c.f}, nil
}
func (nontransactionalConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }

type nontransactionalConn struct {
	*dataImportConn
	f *nontransactionalFixture
}

func (c *nontransactionalConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, "SET ") {
		return dataImportResult{}, nil
	}
	if strings.HasPrefix(q, "INSERT INTO ") && c.f.failAt > 0 && c.f.base.inserts+1 == c.f.failAt {
		c.f.base.inserts++
		return nil, c.f.err
	}
	return c.dataImportConn.ExecContext(ctx, q, args)
}
func (c *nontransactionalConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, "information_schema.TABLES") {
		return &structureRows{cols: []string{"engine", "type"}, values: [][]driver.Value{{"MyISAM", "BASE TABLE"}}}, nil
	}
	if strings.HasPrefix(q, "SHOW WARNINGS") && c.f.warningAt > 0 && c.f.base.inserts == c.f.warningAt {
		return &structureRows{cols: []string{"Level", "Code", "Message"}, values: [][]driver.Value{{"Warning", int64(1265), "Data truncated"}}}, nil
	}
	return c.dataImportConn.QueryContext(ctx, q, args)
}

type nontransactionalRows struct {
	edits         []MySQLDataEdit
	index, closed int
}

func (r *nontransactionalRows) Next(context.Context) (MySQLDataEdit, error) {
	if r.index >= len(r.edits) {
		return MySQLDataEdit{}, io.EOF
	}
	e := r.edits[r.index]
	r.index++
	return e, nil
}
func (r *nontransactionalRows) Close() error { r.closed++; return nil }
func TestNontransactionalRestoreKeepsAcknowledgedRowsAndNeverRetries(t *testing.T) {
	for _, tc := range []struct {
		name            string
		f               *nontransactionalFixture
		state, rowState string
		applied         int64
	}{{"success", &nontransactionalFixture{}, "executed", "executed", 2}, {"server rejection", &nontransactionalFixture{failAt: 2, err: &mysqldriver.MySQLError{Number: 1062, Message: "duplicate"}}, "partial", "not_executed", 1}, {"lost write receipt", &nontransactionalFixture{failAt: 2, err: errors.New("connection lost")}, "uncertain", "uncertain", 1}, {"written with warning", &nontransactionalFixture{warningAt: 1}, "partial", "executed", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			db := sql.OpenDB(nontransactionalConnector{tc.f})
			defer db.Close()
			c, e := db.Conn(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			rows := &nontransactionalRows{edits: []MySQLDataEdit{{Kind: "insert", Values: map[string]MySQLDataValue{"note": {Text: "one"}}}, {Kind: "insert", Values: map[string]MySQLDataValue{"note": {Text: "two"}}}}}
			out := restoreNontransactionalData(context.Background(), c, "db", "table", rows, 2, nil)
			if out.State != tc.state || out.RowState != tc.rowState || out.RowsApplied != tc.applied {
				t.Fatalf("wrong no-rollback outcome: %+v", out)
			}
			if rows.closed != 1 || tc.f.base.rollbacks != 0 || tc.f.base.commits != 0 || tc.f.base.inserts > 2 {
				t.Fatal("restore replayed or claimed a transaction")
			}
		})
	}
}
