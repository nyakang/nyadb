package connections

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

type tableChangeRecorder struct {
	sync.Mutex
	statements []string
	args       [][]driver.NamedValue
	kind       string
	failInsert bool
	columns    [][]driver.Value
	columnErr  error
}
type tableChangeConnector struct{ rec *tableChangeRecorder }

func (c tableChangeConnector) Connect(context.Context) (driver.Conn, error) {
	return tableChangeConn{c.rec}, nil
}
func (tableChangeConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }

type tableChangeConn struct{ rec *tableChangeRecorder }

func (tableChangeConn) Close() error { return nil }
func (tableChangeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected Prepare")
}
func (tableChangeConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected Begin") }
func (c tableChangeConn) QueryContext(ctx context.Context, sqlText string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.rec.Lock()
	defer c.rec.Unlock()
	c.rec.args = append(c.rec.args, append([]driver.NamedValue(nil), args...))
	switch {
	case strings.Contains(sqlText, "information_schema.TABLES"):
		return &structureRows{cols: []string{"type"}, values: [][]driver.Value{{c.rec.kind}}}, nil
	case strings.Contains(sqlText, "information_schema.COLUMNS"):
		if !strings.Contains(sqlText, "ORDER BY ORDINAL_POSITION") {
			return nil, errors.New("copy columns must retain ordinal order")
		}
		if c.rec.columnErr != nil {
			return nil, c.rec.columnErr
		}
		columns := c.rec.columns
		if columns == nil {
			columns = [][]driver.Value{{"id", ""}, {"note", ""}}
		}
		return &structureRows{cols: []string{"name", "expression"}, values: columns}, nil
	default:
		return nil, errors.New("unexpected query")
	}
}
func (c tableChangeConn) ExecContext(ctx context.Context, sqlText string, _ []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.rec.Lock()
	c.rec.statements = append(c.rec.statements, sqlText)
	fail := c.rec.failInsert && strings.HasPrefix(sqlText, "INSERT")
	c.rec.Unlock()
	if fail {
		return nil, errors.New("injected insert failure")
	}
	return driver.RowsAffected(1), nil
}
func connectedTableManager(t *testing.T, rec *tableChangeRecorder) *MySQLSessionManager {
	t.Helper()
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(tableChangeConnector{rec: rec}), nil
	}
	if err := m.Connect(context.Background(), ActiveSettings{ProfileID: 83}, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}
func TestMySQLTableActionSQLQuotesIdentifiersAndRejectsInvalidActions(t *testing.T) {
	cases := []struct {
		change MySQLTableChange
		first  string
	}{
		{MySQLTableChange{Action: MySQLTableCreate, Database: "db", Table: "new"},
			"CREATE TABLE `db`.`new` (`id` BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY) ENGINE=InnoDB"},
		{MySQLTableChange{Action: MySQLTableDrop, Database: "db", Table: "t`x"},
			"DROP TABLE `db`.`t``x`"},
		{MySQLTableChange{Action: MySQLTableDeleteRows, Database: "db", Table: "t"}, "DELETE FROM `db`.`t`"},
		{MySQLTableChange{Action: MySQLTableTruncate, Database: "db", Table: "t"}, "TRUNCATE TABLE `db`.`t`"},
		{MySQLTableChange{Action: MySQLTableRename, Database: "db", Table: "t", NewName: "new"}, "RENAME TABLE `db`.`t` TO `db`.`new`"},
		{MySQLTableChange{Action: MySQLTableCopyStructure, Database: "db", Table: "t", NewName: "copy"},
			"CREATE TABLE `db`.`copy` LIKE `db`.`t`"},
		{MySQLTableChange{Action: MySQLTableCopyData, Database: "db", Table: "t", NewName: "copy"},
			"CREATE TABLE `db`.`copy` LIKE `db`.`t`"},
	}
	for _, test := range cases {
		t.Run(string(test.change.Action), func(t *testing.T) {
			first, err := tableChangeSQL(test.change)
			if err != nil || first != test.first {
				t.Fatalf("SQL = %q, err=%v", first, err)
			}
		})
	}
	for _, change := range []MySQLTableChange{
		{Action: MySQLTableDrop, Database: "", Table: "t"},
		{Action: MySQLTableDrop, Database: "db", Table: "x\ny"},
		{Action: MySQLTableRename, Database: "db", Table: "t", NewName: "t"},
		{Action: MySQLTableRename, Database: "db", Table: "t"},
		{Action: "EXEC", Database: "db", Table: "t"},
	} {
		if _, err := tableChangeSQL(change); err == nil {
			t.Fatalf("invalid SQL accepted: %+v", change)
		}
	}
}

func TestMySQLTableActionsUseBoundTypeGuardAndHandlePartialCopy(t *testing.T) {
	rec := &tableChangeRecorder{kind: "BASE TABLE"}
	m := connectedTableManager(t, rec)
	change := MySQLTableChange{Action: MySQLTableCopyData, Database: "d' OR 1=1 --", Table: "t`x", NewName: "copy"}
	if err := m.ChangeTable(context.Background(), 83, change); err != nil {
		t.Fatal(err)
	}
	rec.Lock()
	if len(rec.args) != 2 {
		t.Errorf("expected bound type and columns queries: %+v", rec.args)
	}
	for _, args := range rec.args {
		if len(args) != 2 || args[0].Value != change.Database || args[1].Value != change.Table {
			t.Errorf("metadata query must bind names: %+v", args)
		}
	}
	if len(rec.statements) != 2 || !strings.Contains(rec.statements[0], "`t``x`") {
		t.Errorf("unexpected executed SQL: %v", rec.statements)
	}
	rec.kind = "VIEW"
	rec.Unlock()
	if err := m.ChangeTable(context.Background(), 83, MySQLTableChange{Action: MySQLTableDrop, Database: "db", Table: "view"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("view accepted for drop: %v", err)
	}
	rec.Lock()
	if len(rec.statements) != 2 {
		t.Errorf("view triggered SQL: %v", rec.statements)
	}
	rec.kind = "BASE TABLE"
	rec.failInsert = true
	rec.Unlock()
	err := m.ChangeTable(context.Background(), 83, change)
	if err == nil || !strings.Contains(err.Error(), "new table") {
		t.Fatal(fmt.Sprintf("partial copy not reported: %v", err))
	}
	if err := m.Disconnect(83); err != nil {
		t.Fatal(err)
	}
	if err := m.ChangeTable(context.Background(), 83, change); !errors.Is(err, ErrMySQLNotConnected) {
		t.Fatalf("operation after disconnect: %v", err)
	}
}

func TestMySQLTableCopyUsesExplicitWritableColumns(t *testing.T) {
	rec := &tableChangeRecorder{kind: "BASE TABLE", columns: [][]driver.Value{
		{"id", ""},
		{"doubled", "(`amount` * 2)"},
		{"odd`name", ""},
		{"created_at", ""}, // DEFAULT_GENERATED has no generation expression.
		{"nullable_metadata", nil},
	}}
	m := connectedTableManager(t, rec)
	if err := m.ChangeTable(context.Background(), 83, MySQLTableChange{
		Action: MySQLTableCopyData, Database: "d`b", Table: "source", NewName: "target",
	}); err != nil {
		t.Fatal(err)
	}
	rec.Lock()
	defer rec.Unlock()
	want := "INSERT INTO `d``b`.`target` (`id`, `odd``name`, `created_at`, `nullable_metadata`) SELECT `id`, `odd``name`, `created_at`, `nullable_metadata` FROM `d``b`.`source`"
	if len(rec.statements) != 2 || rec.statements[1] != want {
		t.Fatalf("copy SQL must preserve writable values and quote column names: %v", rec.statements)
	}
}

func TestMySQLTableCopyMetadataFailureDoesNotCreateDestination(t *testing.T) {
	for _, test := range []struct {
		name    string
		columns [][]driver.Value
		err     error
	}{
		{name: "query error", err: errors.New("columns unavailable")},
		{name: "empty metadata", columns: [][]driver.Value{}},
		{name: "no writable columns", columns: [][]driver.Value{{"generated", "(1)"}}},
		{name: "invalid column", columns: [][]driver.Value{{"bad\nname", ""}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := &tableChangeRecorder{kind: "BASE TABLE", columns: test.columns, columnErr: test.err}
			m := connectedTableManager(t, rec)
			if err := m.ChangeTable(context.Background(), 83, MySQLTableChange{
				Action: MySQLTableCopyData, Database: "db", Table: "source", NewName: "target",
			}); err == nil {
				t.Fatal("invalid metadata must reject the copy")
			}
			rec.Lock()
			defer rec.Unlock()
			if len(rec.statements) != 0 {
				t.Fatalf("metadata failure created a destination: %v", rec.statements)
			}
		})
	}
}
