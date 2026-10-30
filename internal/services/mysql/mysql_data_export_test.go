package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

type exportFixtureConnector struct {
	calls     chan structureCall
	rows      int
	oversized bool
}

func (c exportFixtureConnector) Connect(context.Context) (driver.Conn, error) {
	return exportFixtureConn{c}, nil
}
func (exportFixtureConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }

type exportFixtureConn struct{ fixture exportFixtureConnector }

func (exportFixtureConn) Close() error { return nil }
func (exportFixtureConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("unexpected prepare")
}
func (exportFixtureConn) Begin() (driver.Tx, error) { return nil, fmt.Errorf("unexpected transaction") }
func (c exportFixtureConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, "FROM information_schema.COLUMNS") {
		return (queryOptionsConn{queryOptionsConnector{c.fixture.calls}}).QueryContext(ctx, q, args)
	}
	c.fixture.calls <- structureCall{query: q, args: args}
	if !strings.HasPrefix(q, "SELECT ") {
		return nil, fmt.Errorf("unexpected query")
	}
	return &exportFixtureRows{total: c.fixture.rows, oversized: c.fixture.oversized}, nil
}

type exportFixtureRows struct {
	next, total int
	oversized   bool
}

func (*exportFixtureRows) Columns() []string { return []string{"note", "payload"} }
func (*exportFixtureRows) Close() error      { return nil }
func (r *exportFixtureRows) Next(dest []driver.Value) error {
	if r.next >= r.total {
		return io.EOF
	}
	r.next++
	length := 6000
	if r.oversized {
		length = MySQLDataExportFieldMaxBytes + 1
	}
	dest[0] = strings.Repeat("z", length)
	dest[1] = []byte{0, 1, 255}
	return nil
}
func exportTestManager(t *testing.T, rows int, oversized bool) (*MySQLSessionManager, chan structureCall) {
	t.Helper()
	calls := make(chan structureCall, 10)
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(exportFixtureConnector{calls: calls, rows: rows, oversized: oversized}), nil
	}
	if err := m.Connect(context.Background(), connections.ActiveSettings{ProfileID: 12}, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m, calls
}
func TestMySQLDataExportStreamsAllRowsAndCompleteValuesWithBoundFilters(t *testing.T) {
	m, calls := exportTestManager(t, 501, false)
	count := 0
	attack := "%' OR 1=1 --"
	err := m.ExportMySQLData(context.Background(), 12, "d`b", "t", MySQLDataOptions{FilterColumn: "note", FilterMode: "contains", FilterValue: attack, SortColumn: "id"}, []string{"note", "payload"}, func(values []MySQLDataValue) error {
		count++
		if len(values[0].Text) != 6000 || !values[1].Binary || string(values[1].Bytes) != string([]byte{0, 1, 255}) {
			t.Fatal("truncated or changed full values")
		}
		return nil
	})
	if err != nil || count != 501 {
		t.Fatal(count, err)
	}
	<-calls
	query := <-calls
	if strings.Contains(query.query, " LIMIT ") || strings.Contains(query.query, attack) || !strings.Contains(query.query, "FROM `d``b`.`t`") || !strings.Contains(query.query, "LOCATE(?, `note`) > 0") || len(query.args) != 1 || query.args[0].Value != attack {
		t.Fatalf("incomplete or unsafe export query: %+v", query)
	}
	if !strings.Contains(query.query, "SUBSTRING(CAST(`note` AS BINARY),1,1048577)") {
		t.Fatal("full-field transfer is unbounded")
	}
}
func TestMySQLDataExportAbortsOnOversizeCallbackErrorAndCancellation(t *testing.T) {
	m, _ := exportTestManager(t, 1, true)
	called := false
	err := m.ExportMySQLData(context.Background(), 12, "db", "t", MySQLDataOptions{}, []string{"note", "payload"}, func([]MySQLDataValue) error { called = true; return nil })
	if !errors.Is(err, ErrMySQLDataEditLimit) || called {
		t.Fatal("published incomplete field", err)
	}
	m, _ = exportTestManager(t, 501, false)
	sentinel := errors.New("disk full")
	count := 0
	err = m.ExportMySQLData(context.Background(), 12, "db", "t", MySQLDataOptions{}, []string{"note", "payload"}, func([]MySQLDataValue) error { count++; return sentinel })
	if !errors.Is(err, sentinel) || count != 1 {
		t.Fatal("ignored callback error", err, count)
	}
	m, _ = exportTestManager(t, 501, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	count = 0
	err = m.ExportMySQLData(ctx, 12, "db", "t", MySQLDataOptions{}, []string{"note", "payload"}, func([]MySQLDataValue) error { count++; cancel(); return nil })
	if !errors.Is(err, context.Canceled) || count != 1 {
		t.Fatal("cancel not acknowledged", err, count)
	}
}
func TestMySQLDataExportRejectsUnknownColumnsAndInvalidScopes(t *testing.T) {
	m, _ := exportTestManager(t, 1, false)
	for _, test := range []struct {
		options MySQLDataOptions
		fields  []string
	}{{MySQLDataOptions{}, []string{"note; DROP TABLE t"}}, {MySQLDataOptions{}, []string{"note", "note"}}, {MySQLDataOptions{Offset: 2}, []string{"note"}}, {MySQLDataOptions{Limit: 201}, []string{"note"}}, {MySQLDataOptions{UseKeyset: true}, []string{"note"}}} {
		if err := m.ExportMySQLData(context.Background(), 12, "db", "t", test.options, test.fields, func([]MySQLDataValue) error { return nil }); err == nil {
			t.Fatal("accepted unsafe export", test)
		}
	}
}
