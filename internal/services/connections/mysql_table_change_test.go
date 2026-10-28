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
	if !strings.Contains(sqlText, "information_schema.TABLES") {
		return nil, errors.New("unexpected query")
	}
	c.rec.Lock()
	c.rec.args = append(c.rec.args, append([]driver.NamedValue(nil), args...))
	kind := c.rec.kind
	c.rec.Unlock()
	return &structureRows{cols: []string{"type"}, values: [][]driver.Value{{kind}}}, nil
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
		change        MySQLTableChange
		first, second string
	}{
		{MySQLTableChange{Action: MySQLTableCreate, Database: "db", Table: "new"},
			"CREATE TABLE `db`.`new` (`id` BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY) ENGINE=InnoDB", ""},
		{MySQLTableChange{Action: MySQLTableDrop, Database: "db", Table: "t`x"},
			"DROP TABLE `db`.`t``x`", ""},
		{MySQLTableChange{Action: MySQLTableDeleteRows, Database: "db", Table: "t"}, "DELETE FROM `db`.`t`", ""},
		{MySQLTableChange{Action: MySQLTableTruncate, Database: "db", Table: "t"}, "TRUNCATE TABLE `db`.`t`", ""},
		{MySQLTableChange{Action: MySQLTableRename, Database: "db", Table: "t", NewName: "new"}, "RENAME TABLE `db`.`t` TO `db`.`new`", ""},
		{MySQLTableChange{Action: MySQLTableCopyStructure, Database: "db", Table: "t", NewName: "copy"},
			"CREATE TABLE `db`.`copy` LIKE `db`.`t`", ""},
		{MySQLTableChange{Action: MySQLTableCopyData, Database: "db", Table: "t", NewName: "copy"},
			"CREATE TABLE `db`.`copy` LIKE `db`.`t`", "INSERT INTO `db`.`copy` SELECT * FROM `db`.`t`"},
	}
	for _, test := range cases {
		t.Run(string(test.change.Action), func(t *testing.T) {
			first, second, err := tableChangeSQL(test.change)
			if err != nil || first != test.first || second != test.second {
				t.Fatalf("SQL = %q %q, err=%v", first, second, err)
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
		if _, _, err := tableChangeSQL(change); err == nil {
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
	if len(rec.args) != 1 || len(rec.args[0]) != 2 || rec.args[0][0].Value != change.Database || rec.args[0][1].Value != change.Table {
		t.Errorf("table type query must bind names: %+v", rec.args)
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
