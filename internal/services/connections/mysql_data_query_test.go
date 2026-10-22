package connections

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
)

type queryOptionsConnector struct{ calls chan structureCall }

func (c queryOptionsConnector) Connect(context.Context) (driver.Conn, error) {
	return queryOptionsConn{c}, nil
}
func (queryOptionsConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }

type queryOptionsConn struct{ c queryOptionsConnector }

func (queryOptionsConn) Close() error { return nil }
func (queryOptionsConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("unexpected prepare")
}
func (queryOptionsConn) Begin() (driver.Tx, error) { return nil, fmt.Errorf("unexpected tx") }
func (c queryOptionsConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.c.calls <- structureCall{query: q, args: args}
	switch {
	case strings.HasPrefix(q, "SELECT COLUMN_NAME, DATA_TYPE, IS_NULLABLE"):
		return &structureRows{cols: []string{"name", "kind", "nullable"}, values: [][]driver.Value{
			{"id", "bigint", "NO"}, {"payload", "longblob", "YES"}, {"note", "varchar", "YES"},
		}}, nil
	case strings.Contains(q, "information_schema.STATISTICS"):
		return &structureRows{cols: []string{"index", "column", "seq", "sub", "nullable"}, values: [][]driver.Value{
			{"uk_id", "id", int64(1), nil, "NO"},
		}}, nil
	case strings.HasPrefix(q, "SELECT "):
		return &structureRows{cols: []string{"id", "payload", "note"}, values: [][]driver.Value{
			{int64(1), []byte{0, 1, 2}, "hello"}, {int64(2), nil, ""},
		}}, nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", q)
	}
}
func TestMySQLDataAdvancedProjectionsSortingAndSafeFilters(t *testing.T) {
	calls := make(chan structureCall, 10)
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(queryOptionsConnector{calls}), nil
	}
	if err := m.Connect(context.Background(), ActiveSettings{ProfileID: 19}, ""); err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	options := MySQLDataOptions{Limit: 2, FilterColumn: "note", FilterMode: "contains",
		FilterValue: "%' OR 1=1 --", SortColumn: "note", SortDescending: true}
	page, err := m.TableDataPageWithOptions(context.Background(), 19, "x` ; DROP DATABASE z", "t", options)
	if err != nil {
		t.Fatal(err)
	}
	if !page.StableOrder || len(page.Columns) != 3 || len(page.Rows) != 2 ||
		page.Rows[0][1].Text != "0x000102" || !page.Rows[1][1].Null {
		t.Fatalf("preview result: %+v", page)
	}
	meta, index, data := <-calls, <-calls, <-calls
	if len(meta.args) != 2 || len(index.args) != 2 ||
		meta.args[0].Value != "x` ; DROP DATABASE z" {
		t.Fatalf("metadata binding: %+v", meta)
	}
	if strings.Contains(data.query, "SELECT *") ||
		!strings.Contains(data.query, "SUBSTRING(`payload`, 1, 1025)") ||
		!strings.Contains(data.query, "SUBSTRING(`note`, 1, 2049)") ||
		!strings.Contains(data.query, "FROM `x`` ; DROP DATABASE z`.`t`") ||
		!strings.Contains(data.query, "WHERE LOCATE(?, `note`) > 0") ||
		!strings.Contains(data.query, "ORDER BY `note` DESC, `id` ASC") ||
		len(data.args) != 3 || data.args[0].Value != options.FilterValue ||
		data.args[1].Value != int64(3) || data.args[2].Value != int64(0) {
		t.Fatalf("query or bindings are unsafe: %+v", data)
	}
}
func TestMySQLDataAdvancedRejectsInvalidOptions(t *testing.T) {
	m := NewMySQLSessionManager()
	for _, o := range []MySQLDataOptions{
		{Limit: 200, SortDescending: true},
		{Limit: 100, FilterColumn: "a", FilterMode: "regex"},
		{Limit: 100, FilterColumn: "a", FilterMode: ""},
		{Limit: 100, FilterMode: "contains"},
		{Limit: 100, FilterColumn: "a", FilterMode: "equal", FilterValue: strings.Repeat("x", 4097)},
	} {
		if _, err := m.TableDataPageWithOptions(context.Background(), 1, "db", "tab", o); err == nil {
			t.Fatalf("accepted invalid %+v", o)
		}
	}
}
