package connections

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type dataFixtureConnector struct {
	seen  chan structureCall
	block <-chan struct{}
}

func (c dataFixtureConnector) Connect(context.Context) (driver.Conn, error) {
	return dataFixtureConn{c}, nil
}
func (dataFixtureConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }

type dataFixtureConn struct{ cfg dataFixtureConnector }

func (dataFixtureConn) Close() error { return nil }
func (dataFixtureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (dataFixtureConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected transaction") }
func (c dataFixtureConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
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
	if strings.Contains(query, "FROM information_schema.COLUMNS") {
		return &structureRows{cols: []string{"name", "type", "kind", "pos", "nullable", "default", "key", "extra", "comment", "len", "precision", "scale", "datetime", "charset", "collation"}, values: [][]driver.Value{
			{"id", "bigint", "bigint", int64(1), "NO", nil, "PRI", "auto_increment", "", nil, int64(20), int64(0), nil, nil, nil},
			{"note", "varchar(255)", "varchar", int64(2), "YES", nil, "", "", "", int64(255), nil, nil, nil, "utf8mb4", "utf8mb4_0900_ai_ci"},
			{"blank", "varchar(30)", "varchar", int64(3), "YES", "NULL", "", "", "", int64(30), nil, nil, nil, "utf8mb4", "utf8mb4_0900_ai_ci"},
		}}, nil
	}
	if strings.Contains(query, "information_schema.STATISTICS") {
		return &structureRows{cols: []string{"index", "column", "seq", "sub", "nullable"}, values: [][]driver.Value{{"PRIMARY", "id", int64(1), nil, "NO"}}}, nil
	}
	if !strings.HasPrefix(query, "SELECT ") || len(args) != 2 {
		return nil, fmt.Errorf("unexpected data query: %q", query)
	}
	if args[0].Value != int64(3) || args[1].Value != int64(0) {
		return nil, fmt.Errorf("unexpected bound paging: %+v", args)
	}
	return &structureRows{cols: []string{"id", "note", "blank"}, values: [][]driver.Value{
		{int64(1), nil, ""}, {int64(2), "hello", "NULL"}, {int64(3), "中文", "ok"},
	}}, nil
}

func connectedDataFixture(t *testing.T, c dataFixtureConnector) *MySQLSessionManager {
	t.Helper()
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, ActiveSettings, string) (*sql.DB, error) { return sql.OpenDB(c), nil }
	if err := m.Connect(context.Background(), ActiveSettings{ProfileID: 15}, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}
func TestMySQLTableDataUsesEscapedNamesBoundPageAndPreservesNULL(t *testing.T) {
	seen := make(chan structureCall, 4)
	m := connectedDataFixture(t, dataFixtureConnector{seen: seen})
	schema, table := "strange`schema", "the.table; DROP DATABASE demo"
	got, err := m.TableDataPage(context.Background(), 15, schema, table, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got.Database != schema || got.Table != table || !got.StableOrder || !got.HasMore || len(got.Rows) != 2 || len(got.Columns) != 3 {
		t.Fatalf("page: %+v", got)
	}
	if !got.Rows[0][1].Null || got.Rows[0][2].Null || got.Rows[0][2].Text != "" || got.Rows[1][2].Text != "NULL" {
		t.Fatalf("NULL and empty-string handling lost: %+v", got.Rows)
	}
	if len(got.ColumnMetadata) != 3 || got.ColumnMetadata[0].Type != "bigint" ||
		got.ColumnMetadata[0].Key != "PRI" || got.ColumnMetadata[0].Extra != "auto_increment" ||
		got.ColumnMetadata[1].Type != "varchar(255)" || got.ColumnMetadata[1].CharacterSet == nil ||
		*got.ColumnMetadata[1].CharacterSet != "utf8mb4" || got.ColumnMetadata[1].Default != nil ||
		got.ColumnMetadata[2].Default == nil || *got.ColumnMetadata[2].Default != "NULL" ||
		got.ColumnMetadata[2].Position != 3 {
		t.Fatalf("column metadata lost NULL/default/type/key semantics: %+v", got.ColumnMetadata)
	}
	meta := <-seen
	indexes := <-seen
	row := <-seen
	if !strings.Contains(meta.query, "information_schema.COLUMNS") ||
		!strings.Contains(meta.query, "COLUMN_TYPE") ||
		!strings.Contains(meta.query, "DATETIME_PRECISION") ||
		!strings.Contains(meta.query, "COLLATION_NAME") ||
		!strings.Contains(indexes.query, "information_schema.STATISTICS") ||
		meta.args[0].Value != schema || indexes.args[1].Value != table {
		t.Fatalf("metadata not bound: %+v %+v", meta, indexes)
	}
	if !strings.Contains(row.query, "FROM `strange``schema`.`the.table; DROP DATABASE demo`") ||
		!strings.Contains(row.query, "ORDER BY `id` ASC LIMIT ? OFFSET ?") ||
		strings.Contains(row.query, "SELECT *") || strings.Contains(row.query, "SELECT COUNT") {
		t.Fatalf("table data SQL not safely quoted or paged: %s", row.query)
	}
}
func TestMySQLDataPreviewCellBoundsBinaryAndUTF8(t *testing.T) {
	if v := mysqlDataPreviewCell(nil, "TEXT"); !v.Null {
		t.Fatal(v)
	}
	if v := mysqlDataPreviewCell([]byte{0, 0xff, 0x08}, "BLOB"); v.Text != "0x00ff08" {
		t.Fatalf("binary: %+v", v)
	}
	large := strings.Repeat("中", 900)
	v := mysqlDataPreviewCell([]byte(large), "VARCHAR")
	if !v.Truncated || len(v.Text) > mysqlDataCellMaxBytes || !strings.HasPrefix(large, v.Text) || len(v.Text)%3 != 0 {
		t.Fatalf("unbounded or broken UTF8 preview: %+v", v)
	}
}
func TestMySQLDataPageValidationDisconnectAndCancellation(t *testing.T) {
	m := NewMySQLSessionManager()
	for _, tc := range []struct {
		db, table string
		offset    int64
		limit     int
	}{
		{"", "t", 0, 2}, {"db", "", 0, 2}, {"db\nname", "t", 0, 2}, {"db", "t", -1, 2},
		{"db", "t", 0, 0}, {"db", "t", 0, 201}, {"db", "t", 1_000_000_001, 3},
	} {
		if _, err := m.TableDataPage(context.Background(), 12, tc.db, tc.table, tc.offset, tc.limit); err == nil {
			t.Fatalf("accepted invalid %+v", tc)
		}
	}
	if _, err := m.TableDataPage(context.Background(), 12, "db", "tab", 0, 10); !errors.Is(err, ErrMySQLNotConnected) {
		t.Fatal(err)
	}
	block := make(chan struct{})
	done := make(chan error, 1)
	connected := connectedDataFixture(t, dataFixtureConnector{block: block})
	go func() { _, err := connected.TableDataPage(context.Background(), 15, "db", "tab", 0, 2); done <- err }()
	select {
	case <-time.After(30 * time.Millisecond):
	case err := <-done:
		t.Fatalf("query unexpectedly returned: %v", err)
	}
	if err := connected.Disconnect(15); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("disconnect did not cancel data query: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect did not cancel")
	}
}
