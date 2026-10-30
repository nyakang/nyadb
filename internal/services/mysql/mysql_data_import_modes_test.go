package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLDataImportReviewModesKeysLimitsAndOwnedValues(t *testing.T) {
	columns := []MySQLStructureColumn{{Name: "id", DataType: "bigint", Type: "bigint unsigned"}, {Name: "note", DataType: "varchar", Type: "varchar(80)", Nullable: true}}
	row := MySQLDataEdit{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: "18446744073709551615"}, "note": {Text: "中文"}}}
	for _, mode := range []string{"append", "update", "upsert", "insert_missing", "delete", "replace"} {
		request := MySQLDataImportRequest{Mode: mode, Edits: []MySQLDataEdit{row}}
		if mysqlDataImportNeedsKey(mode) {
			request.MatchColumns = []string{"id"}
		}
		if err := ValidateMySQLDataImportRequest(columns, request); err != nil {
			t.Fatalf("valid %s import: %v", mode, err)
		}
	}
	for _, value := range []MySQLDataValue{{Null: true}, {Text: "-1"}, {Text: "18446744073709551616"}} {
		request := MySQLDataImportRequest{Mode: "update", MatchColumns: []string{"id"}, ContinueOnError: true, Edits: []MySQLDataEdit{{Kind: "insert", Values: map[string]MySQLDataValue{"id": value}}}}
		if err := ValidateMySQLDataImportRequest(columns, request); err == nil {
			t.Fatal("continued an invalid/NULL matching key", value)
		}
	}
	if _, err := mysqlDataImportMatchKey([]string{"b", "a"}, [][]string{{"a", "b"}}); err != nil {
		t.Fatal("key selection order should not alter identity", err)
	}
	if _, err := mysqlDataImportMatchKey([]string{"a", "a"}, [][]string{{"a", "b"}}); err == nil {
		t.Fatal("accepted incomplete duplicate key")
	}
	payload := []byte{0, 0xff}
	request := MySQLDataImportRequest{Mode: "append", Edits: make([]MySQLDataEdit, 201)}
	for i := range request.Edits {
		request.Edits[i] = MySQLDataEdit{Kind: "insert", Values: map[string]MySQLDataValue{"payload": {Binary: true, Bytes: payload}}}
	}
	owned, err := mysqlDataImportOwnedRequest(request)
	if err != nil || len(owned.Edits) != 201 {
		t.Fatal("import incorrectly inherited the editor's 200-row cap", err)
	}
	payload[0] = 0x80
	request.Edits[0].Values["payload"] = MySQLDataValue{Text: "changed"}
	if owned.Edits[0].Values["payload"].Bytes[0] != 0 || owned.Edits[1].Values["payload"].Bytes[0] != 0 {
		t.Fatal("import worker shares row values with its source")
	}
	request.Edits = make([]MySQLDataEdit, MaxMySQLDataImportRows+1)
	if _, err := mysqlDataImportOwnedRequest(request); err == nil {
		t.Fatal("accepted oversized row count")
	}
	request.Edits = []MySQLDataEdit{{Kind: "insert", Values: map[string]MySQLDataValue{"note": {Text: strings.Repeat("x", MaxMySQLDataImportBytes+1)}}}}
	if _, err := mysqlDataImportOwnedRequest(request); !errors.Is(err, ErrMySQLDataEditLimit) {
		t.Fatal("accepted oversized input", err)
	}
}

type dataImportModesFixture struct {
	base                         *dataImportFixture
	savepoints, rowRollbacks     int
	rollbackError                error
	writeStarted, writeRelease   chan struct{}
	rollbackStarted, rollbackEnd chan struct{}
}

type dataImportModesConnector struct{ f *dataImportModesFixture }

func (c dataImportModesConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }
func (c dataImportModesConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := (dataImportConnector{c.f.base}).Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &dataImportModesConn{dataImportConn: conn.(*dataImportConn), fixture: c.f}, nil
}

type dataImportModesConn struct {
	*dataImportConn
	fixture *dataImportModesFixture
}

func (c *dataImportModesConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return dataImportModesTx{c}, nil
}

type dataImportModesTx struct{ c *dataImportModesConn }

func (tx dataImportModesTx) Commit() error { return (dataImportTx{tx.c.dataImportConn}).Commit() }
func (tx dataImportModesTx) Rollback() error {
	if tx.c.fixture.rollbackStarted != nil {
		close(tx.c.fixture.rollbackStarted)
		<-tx.c.fixture.rollbackEnd
	}
	return (dataImportTx{tx.c.dataImportConn}).Rollback()
}

func (c *dataImportModesConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	switch {
	case strings.HasPrefix(query, "SAVEPOINT "):
		c.fixture.savepoints++
		return dataImportResult{}, nil
	case strings.HasPrefix(query, "ROLLBACK TO SAVEPOINT "):
		c.fixture.rowRollbacks++
		return dataImportResult{}, c.fixture.rollbackError
	case strings.HasPrefix(query, "RELEASE SAVEPOINT "):
		return dataImportResult{}, nil
	case strings.HasPrefix(query, "INSERT INTO "):
		for _, arg := range args {
			if arg.Value == "duplicate secret value" {
				c.fixture.base.mu.Lock()
				c.fixture.base.inserts++
				c.fixture.base.mu.Unlock()
				return nil, &mysqldriver.MySQLError{Number: 1062, Message: "Duplicate entry 'duplicate secret value' for key 'hidden'"}
			}
			if arg.Value == "cancel" && c.fixture.writeStarted != nil {
				close(c.fixture.writeStarted)
				<-ctx.Done()
				<-c.fixture.writeRelease
				return nil, ctx.Err()
			}
		}
	}
	return c.dataImportConn.ExecContext(ctx, query, args)
}

func dataImportModesTestManager(t *testing.T, f *dataImportModesFixture) *MySQLSessionManager {
	t.Helper()
	if f.base == nil {
		f.base = &dataImportFixture{}
	}
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(dataImportModesConnector{f}), nil
	}
	if err := m.Connect(context.Background(), connections.ActiveSettings{ProfileID: 1}, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func dataImportModesRows(notes ...string) []MySQLDataEdit {
	rows := make([]MySQLDataEdit, len(notes))
	for i, note := range notes {
		rows[i] = MySQLDataEdit{Kind: "insert", Values: map[string]MySQLDataValue{"note": {Text: note}}}
	}
	return rows
}

func TestMySQLDataImportModesContinueAndRollbackReportActualRows(t *testing.T) {
	for _, keepGoing := range []bool{false, true} {
		t.Run(map[bool]string{false: "atomic", true: "continue"}[keepGoing], func(t *testing.T) {
			f := &dataImportModesFixture{}
			m := dataImportModesTestManager(t, f)
			result, err := m.ImportMySQLData(context.Background(), 1, "db", "records", MySQLDataImportRequest{ContinueOnError: keepGoing, Edits: dataImportModesRows("first", "duplicate secret value", "last")})
			if result.Rows[1].Kind != "error" || result.Rows[1].Status != "failed" || result.Rows[1].Error != "MySQL error 1062" || strings.Contains(result.Rows[1].Error, "secret") {
				t.Fatal("row error leaked values or lost status", result)
			}
			if keepGoing {
				if err != nil || !result.Committed || result.CommitUncertain || len(result.Saved.Outcomes) != 2 || result.Rows[0].Status != "committed" || result.Rows[2].Status != "committed" || f.savepoints != 3 || f.rowRollbacks != 1 || f.base.commits != 1 {
					t.Fatalf("continued row results: %+v %v, %+v", result, err, f)
				}
			} else if err == nil || result.Committed || result.Saved != nil || result.Rows[0].Status != "rolled_back" || result.Rows[2].Status != "not_processed" || f.base.commits != 0 || f.base.rollbacks != 1 {
				t.Fatalf("atomic row rollback results: %+v %v, %+v", result, err, f)
			}
		})
	}
}

func TestMySQLDataImportModesLostSavepointAndCommitNeverReplay(t *testing.T) {
	for _, lostCommit := range []bool{false, true} {
		t.Run(map[bool]string{false: "savepoint_lost", true: "commit_lost"}[lostCommit], func(t *testing.T) {
			f := &dataImportModesFixture{base: &dataImportFixture{}}
			rows := dataImportModesRows("first", "duplicate secret value", "last")
			if lostCommit {
				f.base.commitError = errors.New("lost commit response")
				rows = dataImportModesRows("first")
			} else {
				f.rollbackError = &mysqldriver.MySQLError{Number: 1305, Message: "savepoint no longer exists"}
			}
			m := dataImportModesTestManager(t, f)
			result, err := m.ImportMySQLData(context.Background(), 1, "db", "records", MySQLDataImportRequest{ContinueOnError: true, Edits: rows})
			if lostCommit {
				if !errors.Is(err, ErrMySQLDataCommitUncertain) || !result.CommitUncertain || result.Committed || result.Rows[0].Status != "uncertain" || !result.Saved.CommitUncertain || f.base.commits != 1 || f.base.inserts != 1 {
					t.Fatal("uncertain commit was replayed or claimed success", result, err)
				}
			} else if err == nil || result.Committed || result.CommitUncertain || result.Saved != nil || result.Rows[0].Status != "rolled_back" || result.Rows[2].Status != "not_processed" || f.base.inserts != 2 || f.base.rollbacks != 1 {
				t.Fatal("lost savepoint continued/replayed rows", result, err)
			}
		})
	}
}

func TestMySQLDataImportModesCancelWaitsForWriteAndRollbackAcknowledgement(t *testing.T) {
	f := &dataImportModesFixture{writeStarted: make(chan struct{}), writeRelease: make(chan struct{}), rollbackStarted: make(chan struct{}), rollbackEnd: make(chan struct{})}
	m := dataImportModesTestManager(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := m.ImportMySQLData(ctx, 1, "db", "records", MySQLDataImportRequest{ContinueOnError: true, Edits: dataImportModesRows("cancel")})
		done <- err
	}()
	<-f.writeStarted
	cancel()
	select {
	case <-done:
		t.Fatal("cancellation returned before driver's write reply")
	case <-time.After(20 * time.Millisecond):
	}
	close(f.writeRelease)
	<-f.rollbackStarted
	select {
	case <-done:
		t.Fatal("cancellation returned before driver's rollback reply")
	case <-time.After(20 * time.Millisecond):
	}
	close(f.rollbackEnd)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
}

func TestMySQLDataImportModesNewTablePreflightAndLargeReviewedBatch(t *testing.T) {
	columns := []MySQLDataImportColumn{{Name: "note", Type: "VARCHAR(80)", Nullable: true}}
	request := MySQLDataImportRequest{NewTable: true, Columns: columns, ContinueOnError: true, Edits: dataImportModesRows("first", strings.Repeat("x", 81), "last")}
	f := &dataImportModesFixture{}
	m := dataImportModesTestManager(t, f)
	result, err := m.ImportMySQLData(context.Background(), 1, "db", "new", request)
	if err != nil || !result.Created || !result.Committed || result.CreationUncertain || len(result.Saved.Outcomes) != 2 || result.Rows[1].Status != "failed" || f.base.creates != 1 || f.base.inserts != 2 || f.base.createConn != f.base.insertConn {
		t.Fatal("new table row preflight/owned connection lost", result, err)
	}
	request.Edits = dataImportModesRows("first")
	request.Edits[0].Values["unknown"] = MySQLDataValue{Text: "x"}
	result, err = m.ImportMySQLData(context.Background(), 1, "db", "other", request)
	if err == nil || result.Created || result.CreationUncertain || f.base.creates != 1 {
		t.Fatal("mismapped column reached CREATE", result, err)
	}
	request.Edits = make([]MySQLDataEdit, 201)
	for i := range request.Edits {
		request.Edits[i] = dataImportModesRows("valid")[0]
	}
	if err := ValidateMySQLDataImportRequest(nil, request); err != nil {
		t.Fatal("large new-table review inherited editor limits", err)
	}
	if !reflect.DeepEqual(columns, []MySQLDataImportColumn{{Name: "note", Type: "VARCHAR(80)", Nullable: true}}) {
		t.Fatal("new-table review changed source schema")
	}
}
