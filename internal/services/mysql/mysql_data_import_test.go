package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLDataImportColumnsValidateTypesNamesAndExactMetadata(t *testing.T) {
	columns := []MySQLDataImportColumn{{Name: "id", Type: "BIGINT", PrimaryKey: true}, {Name: "报价", Type: " decimal ( 65 , 30 ) ", Nullable: true}, {Name: "note", Type: "VARCHAR(255)", Nullable: true}, {Name: "payload", Type: "VARBINARY(32)", Nullable: true}, {Name: "at", Type: "DATETIME(6)", Nullable: true}}
	metadata, err := MySQLDataImportColumnMetadata(columns)
	if err != nil {
		t.Fatal(err)
	}
	if metadata[0].Key != "PRI" || metadata[1].DataType != "decimal" || metadata[1].Type != "DECIMAL(65,30)" || *metadata[1].NumericPrecision != 65 || *metadata[1].NumericScale != 30 || *metadata[2].CharacterMaxLength != 255 || metadata[3].CharacterSet != nil || *metadata[4].DatetimePrecision != 6 {
		t.Fatal("lost reviewed schema", metadata)
	}
	if columns[1].Type != " decimal ( 65 , 30 ) " {
		t.Fatal("mutated reviewed source")
	}
	for _, field := range []MySQLDataImportColumn{
		{Name: "a", Type: "INT); DROP TABLE t; --"}, {Name: "a", Type: "VARCHAR(255) DEFAULT 'x'"}, {Name: "a", Type: "DECIMAL(66,0)"}, {Name: "a", Type: "DECIMAL(30,31)"}, {Name: "a", Type: "DECIMAL(2,3)"}, {Name: "a", Type: "VARCHAR(0)"}, {Name: "a", Type: "VARCHAR(16384)"}, {Name: "a", Type: "TIME(7)"}, {Name: "a", Type: "JSON", PrimaryKey: true}, {Name: "a", Type: "TEXT", PrimaryKey: true}, {Name: "a", Type: "BIGINT", Nullable: true, PrimaryKey: true}, {Name: "a ", Type: "INT"}, {Name: strings.Repeat("字", 65), Type: "INT"},
	} {
		if err := ValidateMySQLDataImportColumns([]MySQLDataImportColumn{field}); err == nil {
			t.Fatalf("accepted unsupported column %+v", field)
		}
	}
	if err := ValidateMySQLDataImportColumns([]MySQLDataImportColumn{{Name: "Name", Type: "INT"}, {Name: "name", Type: "INT"}}); err == nil {
		t.Fatal("accepted case-insensitive duplicate")
	}
	if err := ValidateMySQLDataImportColumns([]MySQLDataImportColumn{{Name: "a", Type: "VARCHAR(769)", PrimaryKey: true}}); err == nil {
		t.Fatal("oversized UTF-8 primary key")
	}
}

func TestMySQLDataImportSQLQuotesNewDestinationAndNeverReplacesTables(t *testing.T) {
	columns := []MySQLDataImportColumn{{Name: "i`d", Type: "INT", PrimaryKey: true}, {Name: "c; DROP TABLE other", Type: "VARCHAR(80)", Nullable: true}}
	metadata, err := MySQLDataImportColumnMetadata(columns)
	if err != nil {
		t.Fatal(err)
	}
	query, err := mysqlDataImportCreateSQL("d`b", "t;name", columns, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(query, "CREATE TABLE `d``b`.`t;name` (") || !strings.Contains(query, "`i``d` INT NOT NULL") || !strings.Contains(query, "PRIMARY KEY (`i``d`)") || strings.Contains(query, "IF NOT EXISTS") {
		t.Fatal("incorrect destination contract", query)
	}
	if _, err := mysqlDataImportCreateSQL("db", strings.Repeat("x", 65), columns, metadata); err == nil {
		t.Fatal("accepted too-long table name")
	}
}

func TestMySQLDataImportPreflightRejectsInvalidWholeBatchBeforeDDL(t *testing.T) {
	columns := []MySQLDataImportColumn{{Name: "id", Type: "BIGINT"}, {Name: "note", Type: "VARCHAR(2)", Nullable: true}, {Name: "amount", Type: "DECIMAL(5,2)", Nullable: true}, {Name: "payload", Type: "BLOB", Nullable: true}, {Name: "at", Type: "DATETIME(6)", Nullable: true}}
	for _, values := range []map[string]MySQLDataValue{
		{"id": {Text: "1"}, "unknown": {Text: "x"}}, {"id": {Null: true}}, {"id": {Default: true}}, {"id": {Text: "1 OR 1=1"}}, {"id": {Text: "9223372036854775808"}},
		{"id": {Text: "1"}, "note": {Text: "中文超长"}}, {"id": {Text: "1"}, "note": {Text: "\xff"}}, {"id": {Text: "1"}, "note": {Binary: true, Bytes: []byte{1}}},
		{"id": {Text: "1"}, "amount": {Text: ""}}, {"id": {Text: "1"}, "amount": {Text: "1000.00"}}, {"id": {Text: "1"}, "amount": {Text: "0.001"}},
		{"id": {Text: "1"}, "payload": {Text: "0xff"}}, {"id": {Text: "1"}, "payload": {Binary: true, Bytes: make([]byte, 65536)}},
		{"id": {Text: "1"}, "at": {Text: "2026-02-30 12:00:00"}}, {"id": {Text: "1"}, "at": {Text: "2026-10-30 12:00:00.1234567"}}, {"note": {Text: "ok"}},
	} {
		if err := ValidateMySQLDataNewTableImport(columns, []MySQLDataEdit{{Kind: "insert", Values: values}}); err == nil {
			t.Fatalf("review accepted invalid batch: %+v", values)
		}
		m := NewMySQLSessionManager()
		result, err := m.ImportMySQLDataIntoNewTable(context.Background(), 1, "db", "new", columns, []MySQLDataEdit{{Kind: "insert", Values: values}})
		if err == nil || errors.Is(err, ErrMySQLNotConnected) || result.Created || result.CreationUncertain {
			t.Fatalf("preflight failed: %+v %+v %v", values, result, err)
		}
	}
	metadata, _ := MySQLDataImportColumnMetadata(columns)
	value := []MySQLDataEdit{{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: "1"}, "note": {Text: "中文"}, "amount": {Text: "999.99"}, "payload": {Binary: true, Bytes: []byte{0, 255}}, "at": {Text: "2026-10-30 12:00:00.123456"}}}}
	if err := ValidateMySQLDataNewTableImport(columns, value); err != nil {
		t.Fatal("review rejected valid batch", err)
	}
	owned, err := mysqlDataImportValidatedEdits(metadata, value)
	if err != nil {
		t.Fatal(err)
	}
	value[0].Values["payload"].Bytes[0] = 9
	value[0].Values["note"] = MySQLDataValue{Text: "changed"}
	if !reflect.DeepEqual(owned[0].Values["payload"].Bytes, []byte{0, 255}) || owned[0].Values["note"].Text != "中文" {
		t.Fatal("worker borrowed caller values")
	}
	value[0].Values["payload"] = MySQLDataValue{Binary: true}
	value[0].Values["note"] = MySQLDataValue{Text: "中文"}
	owned, err = mysqlDataImportValidatedEdits(metadata, value)
	if err != nil || owned[0].Values["payload"].Bytes == nil || len(owned[0].Values["payload"].Bytes) != 0 {
		t.Fatal("empty binary value would bind as SQL NULL", owned, err)
	}
}

type dataImportFixture struct {
	mu                                                                     sync.Mutex
	createError, insertError, commitError                                  error
	createStarted, createRelease                                           chan struct{}
	creates, inserts, commits, rollbacks, nextConn, createConn, insertConn int
	existed                                                                bool
	query                                                                  string
}
type dataImportConnector struct{ f *dataImportFixture }

func (c dataImportConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }
func (c dataImportConnector) Connect(context.Context) (driver.Conn, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.nextConn++
	return &dataImportConn{f: c.f, id: c.f.nextConn}, nil
}

type dataImportConn struct {
	f  *dataImportFixture
	id int
}

func (*dataImportConn) Close() error { return nil }
func (*dataImportConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (c *dataImportConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *dataImportConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return dataImportTx{c}, nil
}

type dataImportTx struct{ c *dataImportConn }

func (tx dataImportTx) Commit() error {
	tx.c.f.mu.Lock()
	defer tx.c.f.mu.Unlock()
	tx.c.f.commits++
	return tx.c.f.commitError
}
func (tx dataImportTx) Rollback() error {
	tx.c.f.mu.Lock()
	defer tx.c.f.mu.Unlock()
	tx.c.f.rollbacks++
	return nil
}

type dataImportResult struct{}

func (dataImportResult) LastInsertId() (int64, error) { return 0, nil }
func (dataImportResult) RowsAffected() (int64, error) { return 1, nil }
func (c *dataImportConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, "SET SESSION") {
		return dataImportResult{}, nil
	}
	if strings.HasPrefix(q, "CREATE TABLE ") {
		c.f.mu.Lock()
		c.f.creates++
		c.f.createConn = c.id
		c.f.query = q
		existed, err := c.f.existed, c.f.createError
		c.f.mu.Unlock()
		if c.f.createStarted != nil {
			close(c.f.createStarted)
			<-ctx.Done()
			<-c.f.createRelease
			return nil, ctx.Err()
		}
		if existed {
			return nil, &mysqldriver.MySQLError{Number: 1050, Message: "table exists"}
		}
		if err != nil {
			return nil, err
		}
		c.f.mu.Lock()
		c.f.existed = true
		c.f.mu.Unlock()
		return dataImportResult{}, nil
	}
	if strings.HasPrefix(q, "INSERT INTO ") {
		c.f.mu.Lock()
		defer c.f.mu.Unlock()
		c.f.inserts++
		c.f.insertConn = c.id
		return dataImportResult{}, c.f.insertError
	}
	return nil, errors.New("unexpected exec")
}
func (c *dataImportConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(q, "@@SESSION.sql_mode"):
		return &structureRows{cols: []string{"mode"}, values: [][]driver.Value{{"STRICT_TRANS_TABLES"}}}, nil
	case strings.HasPrefix(q, "SELECT 1 FROM "):
		return &structureRows{cols: []string{"one"}}, nil
	case strings.Contains(q, "information_schema.TABLES"):
		return &structureRows{cols: []string{"engine", "kind"}, values: [][]driver.Value{{"InnoDB", "BASE TABLE"}}}, nil
	case strings.Contains(q, "information_schema.STATISTICS"):
		return &structureRows{cols: []string{"index", "name", "prefix", "nullable"}}, nil
	case strings.Contains(q, "information_schema.COLUMNS"):
		return &structureRows{cols: []string{"name", "type", "kind", "pos", "null", "default", "key", "extra", "comment", "length", "precision", "scale", "datetime", "charset", "collation"}, values: [][]driver.Value{{"note", "varchar(80)", "varchar", int64(1), "YES", nil, "", "", "", int64(80), nil, nil, nil, "utf8mb4", "utf8mb4_0900_ai_ci"}}}, nil
	case strings.HasPrefix(q, "SHOW WARNINGS"):
		return &structureRows{cols: []string{"level", "code", "message"}}, nil
	}
	return nil, errors.New("unexpected query")
}
func dataImportTestManager(t *testing.T, f *dataImportFixture) *MySQLSessionManager {
	t.Helper()
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(dataImportConnector{f}), nil
	}
	if err := m.Connect(context.Background(), connections.ActiveSettings{ProfileID: 1}, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}
func dataImportTestRequest() ([]MySQLDataImportColumn, []MySQLDataEdit) {
	return []MySQLDataImportColumn{{Name: "note", Type: "VARCHAR(80)", Nullable: true}}, []MySQLDataEdit{{Kind: "insert", Values: map[string]MySQLDataValue{"note": {Text: "hello"}}}}
}

func TestMySQLDataImportPinsOriginalConnectionAndSupportsNullableTableWithoutKey(t *testing.T) {
	f := &dataImportFixture{}
	m := dataImportTestManager(t, f)
	columns, edits := dataImportTestRequest()
	result, err := m.ImportMySQLDataIntoNewTable(context.Background(), 1, "db", "new", columns, edits)
	if err != nil || !result.Created || result.CreationUncertain || result.Saved == nil || len(result.Saved.Outcomes) != 1 {
		t.Fatalf("import: %+v %v", result, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createConn != f.insertConn || f.creates != 1 || f.inserts != 1 || f.commits != 1 || f.rollbacks != 0 {
		t.Fatalf("connection/transaction changed: %+v", f)
	}
}

func TestMySQLDataImportExistingTableNeverReceivesRows(t *testing.T) {
	f := &dataImportFixture{existed: true}
	m := dataImportTestManager(t, f)
	columns, edits := dataImportTestRequest()
	result, err := m.ImportMySQLDataIntoNewTable(context.Background(), 1, "db", "new", columns, edits)
	if err == nil || result.Created || result.CreationUncertain || f.inserts != 0 {
		t.Fatalf("existing table mutated: %+v %v", result, err)
	}
}

func TestMySQLDataImportPreservesCreatedTableOnRowsFailureAndUnknownCommit(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "rows_failed", true: "commit_uncertain"}[commit], func(t *testing.T) {
			f := &dataImportFixture{}
			if commit {
				f.commitError = errors.New("lost commit reply")
			} else {
				f.insertError = &mysqldriver.MySQLError{Number: 1062, Message: "duplicate key"}
			}
			m := dataImportTestManager(t, f)
			columns, edits := dataImportTestRequest()
			result, err := m.ImportMySQLDataIntoNewTable(context.Background(), 1, "db", "new", columns, edits)
			if err == nil || !result.Created || !f.existed || result.CreationUncertain || f.creates != 1 || f.inserts != 1 {
				t.Fatalf("DDL outcome lost: %+v %v", result, err)
			}
			if commit && (result.Saved == nil || !result.Saved.CommitUncertain || !errors.Is(err, ErrMySQLDataCommitUncertain) || f.commits != 1) {
				t.Fatal("commit uncertainty lost", result, err)
			}
			if !commit && (result.Saved != nil || f.rollbacks != 1 || f.commits != 0) {
				t.Fatal("row rollback failed", result, err)
			}
		})
	}
}

func TestMySQLDataImportCreateUncertaintyAndCancelWaitForDriverReply(t *testing.T) {
	f := &dataImportFixture{createStarted: make(chan struct{}), createRelease: make(chan struct{})}
	m := dataImportTestManager(t, f)
	columns, edits := dataImportTestRequest()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *MySQLDataNewTableImportResult, 1)
	go func() {
		result, _ := m.ImportMySQLDataIntoNewTable(ctx, 1, "db", "new", columns, edits)
		done <- result
	}()
	<-f.createStarted
	cancel()
	select {
	case <-done:
		t.Fatal("cancellation skipped CREATE acknowledgement")
	case <-time.After(20 * time.Millisecond):
	}
	close(f.createRelease)
	result := <-done
	if result.Created || !result.CreationUncertain || f.creates != 1 || f.inserts != 0 {
		t.Fatal("CREATE uncertainty lost", result)
	}
	before := f.creates
	result, err := m.ImportMySQLDataIntoNewTable(ctx, 1, "db", "new", columns, edits)
	if !errors.Is(err, context.Canceled) || result.CreationUncertain || result.Created || f.creates != before {
		t.Fatal("pre-dispatch cancellation issued DDL", result, err)
	}
}
