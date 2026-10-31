package sqlresult

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type connector struct{}

func (connector) Driver() driver.Driver                        { return drv{} }
func (connector) Connect(context.Context) (driver.Conn, error) { return conn{}, nil }

type drv struct{}

func (drv) Open(string) (driver.Conn, error) { return conn{}, nil }

type conn struct{}

func (conn) Prepare(string) (driver.Stmt, error) { return nil, io.EOF }
func (conn) Close() error                        { return nil }
func (conn) Begin() (driver.Tx, error)           { return nil, io.EOF }
func (conn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &multiRows{}, nil
}

type multiRows struct{ set, row int }

func (r *multiRows) Columns() []string { return []string{"value", "bytes", "amount"} }
func (r *multiRows) ColumnTypeDatabaseTypeName(i int) string {
	return []string{"VARCHAR", "VARBINARY", "DECIMAL"}[i]
}
func (r *multiRows) Close() error { return nil }
func (r *multiRows) Next(out []driver.Value) error {
	if r.row == 2 {
		return io.EOF
	}
	if r.row == 0 {
		out[0] = nil
		out[1] = []byte{}
		out[2] = "9007199254740993.0000000001"
	} else {
		out[0] = "NULL"
		out[1] = []byte{0, 255}
		out[2] = ""
	}
	r.row++
	return nil
}
func (r *multiRows) HasNextResultSet() bool { return r.set == 0 }
func (r *multiRows) NextResultSet() error {
	if r.set > 0 {
		return io.EOF
	}
	r.set++
	r.row = 0
	return nil
}
func TestTypedSQLMultiResultsPreserveNullBinaryAndPrecision(t *testing.T) {
	db := sql.OpenDB(connector{})
	defer db.Close()
	rows, e := db.QueryContext(context.Background(), "SELECT")
	if e != nil {
		t.Fatal(e)
	}
	r, e := Scan(context.Background(), rows, Preview, nil)
	if e != nil || len(r.Sets) != 2 {
		t.Fatalf("sets=%d %v", len(r.Sets), e)
	}
	first := r.Sets[0].Rows
	if first[0][0].Kind != contract.Null || first[1][0].Kind != contract.Text || first[1][0].Text != "NULL" || first[0][1].Kind != contract.Binary || len(first[0][1].Bytes) != 0 || first[0][2].Text != "9007199254740993.0000000001" {
		t.Fatalf("lost types: %+v", first)
	}
}
func TestStreamContinuesBeyondPreviewAndPropagatesSinkFailure(t *testing.T) {
	db := sql.OpenDB(connector{})
	defer db.Close()
	rows, _ := db.QueryContext(context.Background(), "SELECT")
	n := 0
	r, e := Scan(context.Background(), rows, Limits{Rows: 1, Bytes: 4096, CellBytes: 64, Columns: 10}, func(_ int, _ []contract.Column, row []contract.Cell) error {
		if row != nil {
			n++
		}
		return nil
	})
	if e != nil || n != 4 || !r.Truncated {
		t.Fatalf("n=%d %v %+v", n, e, r)
	}
	rows, _ = db.QueryContext(context.Background(), "SELECT")
	if _, e = Scan(context.Background(), rows, Preview, func(int, []contract.Column, []contract.Cell) error { return io.ErrClosedPipe }); e != io.ErrClosedPipe {
		t.Fatal(e)
	}
}
