package connections

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
)

type mysqlMetadataConnector struct {
	observed chan metadataObservation
	count    int
}
type metadataObservation struct{ query, database string }

func (c mysqlMetadataConnector) Connect(context.Context) (driver.Conn, error) {
	return mysqlMetadataConn{observed: c.observed, count: c.count}, nil
}
func (c mysqlMetadataConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }

type mysqlMetadataConn struct {
	observed chan metadataObservation
	count    int
}

func (mysqlMetadataConn) Close() error { return nil }
func (mysqlMetadataConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("no prepared statements")
}
func (mysqlMetadataConn) Begin() (driver.Tx, error)  { return nil, errors.New("no transactions") }
func (mysqlMetadataConn) Ping(context.Context) error { return nil }
func (c mysqlMetadataConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !strings.Contains(query, "information_schema.TABLES") {
		return nil, errors.New("unexpected metadata query")
	}
	if len(args) != 1 {
		return nil, errors.New("expected a single bound schema")
	}
	database, ok := args[0].Value.(string)
	if !ok {
		return nil, errors.New("database is not a bound string")
	}
	c.observed <- metadataObservation{query: query, database: database}
	return &mysqlMetadataRows{count: c.count}, nil
}

type mysqlMetadataRows struct {
	index int
	count int
}

func (*mysqlMetadataRows) Columns() []string {
	return []string{"TABLE_NAME", "TABLE_TYPE", "ENGINE", "TABLE_COMMENT"}
}
func (*mysqlMetadataRows) Close() error { return nil }
func (r *mysqlMetadataRows) Next(dest []driver.Value) error {
	if r.count > 0 {
		if r.index >= r.count {
			return io.EOF
		}
		dest[0], dest[1], dest[2], dest[3] = "table", "BASE TABLE", "InnoDB", ""
		r.index++
		return nil
	}
	entries := [][4]string{
		{"orders", "BASE TABLE", "InnoDB", "Customer orders"},
		{"report", "VIEW", "", "Derived sales report"},
		{"temp", "SYSTEM VIEW", "", "ignore unrequested kinds"},
	}
	if r.index >= len(entries) {
		return io.EOF
	}
	for i, val := range entries[r.index] {
		dest[i] = val
	}
	r.index++
	return nil
}

func TestMySQLObjectsRealMetadataBoundSchema(t *testing.T) {
	m := NewMySQLSessionManager()
	observed := make(chan metadataObservation, 1)
	m.opener = func(context.Context, ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(mysqlMetadataConnector{observed: observed}), nil
	}
	s := ActiveSettings{ProfileID: 9, Host: "localhost", Port: 3306}
	if err := m.Connect(context.Background(), s, ""); err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	// Database names are NEVER interpolated as identifiers or SQL literals.
	database := "demo' OR 1=1 --"
	objects, err := m.Objects(context.Background(), 9, database)
	if err != nil {
		t.Fatal(err)
	}
	o := <-observed
	if o.database != database || strings.Contains(o.query, database) ||
		!strings.Contains(o.query, "TABLE_SCHEMA = ?") {
		t.Fatalf("unbound schema, potential SQL injection: %+v", o)
	}
	if len(objects) != 2 || objects[0].Name != "orders" || objects[0].Kind != "表" ||
		objects[0].Engine != "InnoDB" || objects[1].Name != "report" ||
		objects[1].Kind != "视图" || objects[1].Comment != "Derived sales report" {
		t.Fatalf("wrong types or object metadata: %+v", objects)
	}
}

func TestMySQLObjectsRejectInvalidOrDisconnected(t *testing.T) {
	m := NewMySQLSessionManager()
	for _, bad := range []string{"", "bad\x00schema", strings.Repeat("a", 257)} {
		if _, err := m.Objects(context.Background(), 1, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted invalid schema: %q (%v)", bad, err)
		}
	}
	if _, err := m.Objects(context.Background(), 1, "app"); !errors.Is(err, ErrMySQLNotConnected) {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestMySQLObjectsBoundedMetadataResult(t *testing.T) {
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(mysqlMetadataConnector{observed: make(chan metadataObservation, 1), count: maxMySQLMetadataObjects + 1}), nil
	}
	if err := m.Connect(context.Background(), ActiveSettings{ProfileID: 17}, ""); err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	got, err := m.Objects(context.Background(), 17, "large_db")
	if !errors.Is(err, ErrMySQLMetadataLimit) || got != nil {
		t.Fatalf("oversized metadata was not bounded: %d objects, %v", len(got), err)
	}
}
