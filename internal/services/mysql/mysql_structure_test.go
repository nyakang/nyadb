package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

type structureCall struct {
	query string
	args  []driver.NamedValue
}
type structureConnector struct {
	seen  chan structureCall
	block <-chan struct{}
	empty bool
}

func (c structureConnector) Connect(context.Context) (driver.Conn, error) {
	return structureConn{c}, nil
}
func (structureConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }

type structureConn struct{ cfg structureConnector }

func (structureConn) Close() error { return nil }
func (structureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (structureConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected transaction") }
func (c structureConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if len(args) != 2 {
		return nil, errors.New("not exactly two bound names")
	}
	if strings.Contains(query, "information_schema") == false {
		return nil, errors.New("not metadata")
	}
	if c.cfg.seen != nil {
		c.cfg.seen <- structureCall{query: query, args: args}
	}
	if c.cfg.block != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.cfg.block:
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch {
	case strings.Contains(query, "information_schema.TABLES"):
		if c.cfg.empty {
			return &structureRows{cols: []string{"kind", "engine", "comment", "collation", "rows"}}, nil
		}
		return &structureRows{cols: []string{"kind", "engine", "comment", "collation", "rows"},
			values: [][]driver.Value{{"BASE TABLE", "InnoDB", "sales", "utf8mb4_general_ci", int64(42)}}}, nil
	case strings.Contains(query, "information_schema.COLUMNS"):
		return &structureRows{cols: []string{"name", "type", "data_type", "ordinal", "nullable", "default", "key", "extra", "comment", "length", "precision", "scale", "datetime_precision", "charset", "collation"},
			values: [][]driver.Value{
				{"id", "bigint unsigned", "bigint", int64(1), "NO", nil, "PRI", "auto_increment", "identity", nil, int64(20), int64(0), nil, nil, nil},
				{"sku", "varchar(64)", "varchar", int64(2), "YES", "", "", "", "product code", int64(64), nil, nil, nil, "utf8mb4", "utf8mb4_0900_ai_ci"},
			}}, nil
	case strings.Contains(query, "information_schema.STATISTICS"):
		return &structureRows{cols: []string{"name", "unique", "kind", "seq", "column", "sub", "direction"},
			values: [][]driver.Value{
				{"PRIMARY", int64(0), "BTREE", int64(1), "id", nil, "A"},
				{"sku_index", int64(1), "BTREE", int64(1), "sku", int64(10), "D"},
			}}, nil
	case strings.Contains(query, "information_schema.KEY_COLUMN_USAGE"):
		return &structureRows{cols: []string{"key", "pos", "col", "refdb", "reftable", "refcol", "update", "delete"},
			values: [][]driver.Value{
				{"fk_item", int64(1), "id", "catalog", "parent", "parent_id", "CASCADE", "RESTRICT"},
				{"fk_item", int64(2), "sku", "catalog", "parent", "parent_sku", "CASCADE", "RESTRICT"},
			}}, nil
	default:
		return nil, errors.New("unexpected metadata SQL")
	}
}

type structureRows struct {
	cols   []string
	values [][]driver.Value
	index  int
}

func (r *structureRows) Columns() []string { return r.cols }
func (*structureRows) Close() error        { return nil }
func (r *structureRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}
func connectedStructureManager(t *testing.T, cfg structureConnector) *MySQLSessionManager {
	t.Helper()
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(cfg), nil
	}
	if err := m.Connect(context.Background(), connections.ActiveSettings{ProfileID: 7}, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestMySQLTableStructureReadsBoundOrderedMetadata(t *testing.T) {
	seen := make(chan structureCall, 8)
	m := connectedStructureManager(t, structureConnector{seen: seen})
	schema, table := "app' OR 1=1 --", "t` ; DROP TABLE users"
	got, err := m.TableStructure(context.Background(), 7, schema, table)
	if err != nil {
		t.Fatal(err)
	}
	if got.Database != schema || got.Name != table || got.Kind != "BASE TABLE" || got.Engine != "InnoDB" ||
		got.EstimatedRows == nil || *got.EstimatedRows != 42 {
		t.Fatalf("table metadata: %+v", got)
	}
	if len(got.Columns) != 2 || got.Columns[0].Name != "id" || got.Columns[0].Nullable ||
		got.Columns[0].Default != nil || got.Columns[0].Extra != "auto_increment" ||
		got.Columns[1].Default == nil || *got.Columns[1].Default != "" ||
		got.Columns[1].CharacterMaxLength == nil || *got.Columns[1].CharacterMaxLength != 64 ||
		got.Columns[1].CharacterSet == nil || *got.Columns[1].CharacterSet != "utf8mb4" ||
		got.Columns[1].Collation == nil || *got.Columns[1].Collation != "utf8mb4_0900_ai_ci" {
		t.Fatalf("column metadata: %+v", got.Columns)
	}
	if len(got.Indexes) != 2 || !got.Indexes[0].Unique || got.Indexes[0].Name != "PRIMARY" ||
		got.Indexes[1].Unique || len(got.Indexes[1].Columns) != 1 ||
		*got.Indexes[1].Columns[0].SubPart != 10 {
		t.Fatalf("indexes: %+v", got.Indexes)
	}
	if len(got.ForeignKeys) != 1 || len(got.ForeignKeys[0].Columns) != 2 ||
		got.ForeignKeys[0].Columns[1].ReferencedName != "parent_sku" ||
		got.ForeignKeys[0].OnUpdate != "CASCADE" {
		t.Fatalf("foreign keys: %+v", got.ForeignKeys)
	}
	for i := 0; i < 4; i++ {
		call := <-seen
		if len(call.args) != 2 || call.args[0].Value != schema || call.args[1].Value != table ||
			strings.Contains(call.query, schema) || strings.Contains(call.query, table) ||
			!strings.Contains(call.query, " = ?") {
			t.Fatalf("non-bound query: %+v", call)
		}
	}
}
func TestMySQLTableStructureInvalidMissingAndDisconnected(t *testing.T) {
	m := NewMySQLSessionManager()
	for _, pair := range [][2]string{{"", "t"}, {"db", ""}, {"bad\x00db", "t"}, {"db", strings.Repeat("x", 257)}} {
		if _, err := m.TableStructure(context.Background(), 7, pair[0], pair[1]); !errors.Is(err, connections.ErrInvalid) {
			t.Fatalf("accepted name %+v: %v", pair, err)
		}
	}
	if _, err := m.TableStructure(context.Background(), 7, "app", "orders"); !errors.Is(err, ErrMySQLNotConnected) {
		t.Fatalf("disconnected error: %v", err)
	}
	m = connectedStructureManager(t, structureConnector{empty: true})
	if _, err := m.TableStructure(context.Background(), 7, "app", "missing"); !errors.Is(err, ErrMySQLTableNotFound) {
		t.Fatalf("missing table error: %v", err)
	}
}
func TestMySQLTableStructureCancelsOnDisconnect(t *testing.T) {
	seen := make(chan structureCall, 1)
	block := make(chan struct{})
	m := connectedStructureManager(t, structureConnector{seen: seen, block: block})
	done := make(chan error, 1)
	go func() { _, err := m.TableStructure(context.Background(), 7, "app", "orders"); done <- err }()
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("metadata request was not started")
	}
	if err := m.Disconnect(7); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("request was not canceled: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect failed to cancel metadata request")
	}
}
