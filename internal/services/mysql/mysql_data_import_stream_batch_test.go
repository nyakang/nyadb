package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type streamBatchStatement struct {
	query string
	args  []driver.NamedValue
	rows  int
}
type streamBatchFixture struct {
	modes                            dataImportModesFixture
	statements                       []streamBatchStatement
	events                           []string
	groupError                       error
	columns                          []string
	rollbackStarted, rollbackRelease chan struct{}
}
type streamBatchConnector struct{ f *streamBatchFixture }

func (c streamBatchConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }
func (c streamBatchConnector) Connect(ctx context.Context) (driver.Conn, error) {
	base, err := (streamImportConnector{&c.f.modes}).Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &streamBatchConn{streamImportConn: base.(*streamImportConn), f: c.f}, nil
}

type streamBatchConn struct {
	*streamImportConn
	f *streamBatchFixture
}

func (c *streamBatchConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if len(c.f.columns) != 0 && strings.Contains(q, "information_schema.COLUMNS") {
		values := make([][]driver.Value, len(c.f.columns))
		for i, name := range c.f.columns {
			values[i] = []driver.Value{name, "varchar(80)", "varchar", int64(i + 1), "YES", nil, "", "", "", int64(80), nil, nil, nil, "utf8mb4", "utf8mb4_0900_ai_ci"}
		}
		return &structureRows{cols: []string{"name", "type", "kind", "pos", "null", "default", "key", "extra", "comment", "length", "precision", "scale", "datetime", "charset", "collation"}, values: values}, nil
	}
	return c.streamImportConn.QueryContext(ctx, q, args)
}

func (c *streamBatchConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, "ROLLBACK TO SAVEPOINT nyadb_import_batch") {
		if c.f.rollbackStarted != nil {
			close(c.f.rollbackStarted)
			<-c.f.rollbackRelease
		}
		c.f.events = append(c.f.events, "batch rollback acknowledged")
	}
	if !strings.HasPrefix(q, "INSERT INTO ") {
		return c.streamImportConn.ExecContext(ctx, q, args)
	}
	rows := strings.Count(q, "),(") + 1
	c.f.statements = append(c.f.statements, streamBatchStatement{q, append([]driver.NamedValue(nil), args...), rows})
	if rows > 1 && c.f.groupError != nil {
		return nil, c.f.groupError
	}
	out, err := c.streamImportConn.ExecContext(ctx, q, args)
	if err != nil {
		return nil, err
	}
	if rows > 1 {
		return driver.RowsAffected(rows), nil
	}
	return out, nil
}
func streamBatchManager(t *testing.T, f *streamBatchFixture) *MySQLSessionManager {
	t.Helper()
	f.modes.base = &dataImportFixture{}
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(streamBatchConnector{f}), nil
	}
	if err := m.Connect(context.Background(), connections.ActiveSettings{ProfileID: 1}, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func TestMySQLDataImportStreamBatchBoundsAndOneCommit(t *testing.T) {
	for _, bounded := range []bool{false, true} {
		t.Run(map[bool]string{false: "row batch bound", true: "statement bound"}[bounded], func(t *testing.T) {
			f := &streamBatchFixture{}
			m := streamBatchManager(t, f)
			total, text := 10001, "ok"
			if bounded {
				total, text = 17, strings.Repeat("x", 40)
			}
			request := streamRequest(total, func(int) MySQLDataEdit { return dataImportModesRows(text)[0] })
			request.ExtendedInsert = true
			if bounded {
				request.StatementMaxBytes = 256
			}
			records := 0
			request.Record = func(row MySQLDataImportRowResult) error {
				records++
				if row.Row != records || row.Status != "prepared" {
					return ErrMySQLDataPage
				}
				return nil
			}
			result, err := m.ImportMySQLDataStream(context.Background(), 1, "db", "records", request)
			if err != nil || !result.Committed || result.Progress.Prepared != int64(total) || records != total || f.modes.base.commits != 1 {
				t.Fatal(result, err, records, f.modes.base)
			}
			rows, grouped := 0, false
			for _, statement := range f.statements {
				rows += statement.rows
				grouped = grouped || statement.rows > 1
				if statement.rows > MaxMySQLDataImportRows {
					t.Fatal("row batch limit ignored", statement.rows)
				}
				if bounded {
					args := make([]any, len(statement.args))
					for i, arg := range statement.args {
						args[i] = arg.Value
					}
					if len(statement.query)+mysqlDataImportArgumentBound(args) > request.StatementMaxBytes {
						t.Fatal("statement limit ignored")
					}
				}
			}
			if rows != total || !grouped || !bounded && len(f.statements) != 2 {
				t.Fatal("extended insert did not group or split", rows, len(f.statements))
			}
		})
	}
}

func TestMySQLDataImportStreamBatchServerFailureWaitsForRollbackBeforeIsolation(t *testing.T) {
	f := &streamBatchFixture{rollbackStarted: make(chan struct{}), rollbackRelease: make(chan struct{})}
	m := streamBatchManager(t, f)
	input := dataImportModesRows("first", "duplicate secret value", "last")
	request := streamRequest(len(input), func(i int) MySQLDataEdit { return input[i-1] })
	request.ExtendedInsert, request.Request.ContinueOnError = true, true
	var records []MySQLDataImportRowResult
	request.Record = func(row MySQLDataImportRowResult) error { records = append(records, row); return nil }
	type receipt struct {
		result *MySQLDataImportStreamResult
		err    error
	}
	done := make(chan receipt, 1)
	go func() {
		result, err := m.ImportMySQLDataStream(context.Background(), 1, "db", "records", request)
		done <- receipt{result, err}
	}()
	<-f.rollbackStarted
	select {
	case <-done:
		t.Fatal("returned before group rollback acknowledgement")
	case <-time.After(20 * time.Millisecond):
	}
	if len(f.statements) != 1 {
		t.Fatal("replayed group before rollback", len(f.statements))
	}
	close(f.rollbackRelease)
	out := <-done
	if out.err != nil || !out.result.Committed || out.result.Progress.Prepared != 2 || out.result.Progress.Failed != 1 || len(f.statements) != 4 || len(records) != 3 || f.modes.base.commits != 1 {
		t.Fatal(out.result, out.err, f.statements, records)
	}
	for i, row := range records {
		if row.Row != i+1 {
			t.Fatal("noncontiguous row journal", records)
		}
	}
	if records[1].Status != "failed" || records[1].Error != "MySQL error 1062" {
		t.Fatal("lost or leaked duplicate error", records)
	}
}

func TestMySQLDataImportStreamBatchUnknownFailureAndStopNeverReplay(t *testing.T) {
	for _, kind := range []string{"transport", "server stop", "lost savepoint"} {
		t.Run(kind, func(t *testing.T) {
			f := &streamBatchFixture{groupError: io.ErrUnexpectedEOF}
			m := streamBatchManager(t, f)
			request := streamRequest(3, func(int) MySQLDataEdit { return dataImportModesRows("ok")[0] })
			request.ExtendedInsert, request.Request.ContinueOnError = true, true
			if kind != "transport" {
				f.groupError = &mysqldriver.MySQLError{Number: 1062}
				request.Request.ContinueOnError = kind != "server stop"
			}
			if kind == "lost savepoint" {
				f.modes.rollbackError = &mysqldriver.MySQLError{Number: 1305}
			}
			var records []MySQLDataImportRowResult
			request.Record = func(row MySQLDataImportRowResult) error { records = append(records, row); return nil }
			result, err := m.ImportMySQLDataStream(context.Background(), 1, "db", "records", request)
			if err == nil || result.Committed || result.CommitUncertain || result.Progress.Failed != 3 || len(records) != 3 || len(f.statements) != 1 || f.modes.base.commits != 0 || f.modes.base.rollbacks != 1 {
				t.Fatal("terminal grouped write replayed or lost failures", result, err, records, len(f.statements))
			}
			for i, row := range records {
				if row.Row != i+1 || row.Status != "failed" {
					t.Fatal(records)
				}
			}
		})
	}
}

func TestMySQLDataImportStreamBatchCancelWaitsForWriteAndFullRollback(t *testing.T) {
	f := &streamBatchFixture{modes: dataImportModesFixture{writeStarted: make(chan struct{}), writeRelease: make(chan struct{}), rollbackStarted: make(chan struct{}), rollbackEnd: make(chan struct{})}}
	m := streamBatchManager(t, f)
	request := streamRequest(3, func(int) MySQLDataEdit { return dataImportModesRows("cancel")[0] })
	request.ExtendedInsert, request.Request.ContinueOnError = true, true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := m.ImportMySQLDataStream(ctx, 1, "db", "records", request); done <- err }()
	<-f.modes.writeStarted
	cancel()
	select {
	case <-done:
		t.Fatal("early native write acknowledgement")
	case <-time.After(20 * time.Millisecond):
	}
	close(f.modes.writeRelease)
	<-f.modes.rollbackStarted
	select {
	case <-done:
		t.Fatal("early full rollback acknowledgement")
	case <-time.After(20 * time.Millisecond):
	}
	close(f.modes.rollbackEnd)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(f.statements) != 1 {
		t.Fatal("canceled group replayed")
	}
}

func TestMySQLDataImportStreamBatchOversizeStatementReportsBeforeStop(t *testing.T) {
	f := &streamBatchFixture{}
	m := streamBatchManager(t, f)
	request := streamRequest(1, func(int) MySQLDataEdit { return dataImportModesRows(strings.Repeat("x", 40))[0] })
	request.ExtendedInsert, request.StatementMaxBytes = true, 64
	result, err := m.ImportMySQLDataStream(context.Background(), 1, "db", "records", request)
	if !errors.Is(err, ErrMySQLDataEditLimit) || result.Committed || result.Progress.Failed != 1 || len(f.statements) != 0 || result.Rows[0].Status != "failed" {
		t.Fatal("single row exceeded configured cap or lost receipt", result, err)
	}
}

func TestMySQLDataImportStreamBatchSplitsChangedFieldSetsAndParameterLimit(t *testing.T) {
	t.Run("field sets", func(t *testing.T) {
		f := &streamBatchFixture{columns: []string{"note", "extra"}}
		m := streamBatchManager(t, f)
		request := streamRequest(6, func(i int) MySQLDataEdit {
			r := dataImportModesRows("ok")[0]
			if i == 3 || i == 4 {
				r.Values["extra"] = MySQLDataValue{Text: "optional"}
			}
			return r
		})
		request.ExtendedInsert = true
		result, err := m.ImportMySQLDataStream(context.Background(), 1, "db", "records", request)
		if err != nil || !result.Committed || len(f.statements) != 3 {
			t.Fatal(result, err, f.statements)
		}
		for _, s := range f.statements {
			if s.rows != 2 {
				t.Fatal("field set boundaries mixed", s)
			}
		}
	})
	t.Run("parameter limit", func(t *testing.T) {
		f := &streamBatchFixture{}
		for i := 0; i < 9; i++ {
			f.columns = append(f.columns, "field"+strconv.Itoa(i))
		}
		m := streamBatchManager(t, f)
		request := streamRequest(10000, func(int) MySQLDataEdit {
			r := MySQLDataEdit{Kind: "insert", Values: map[string]MySQLDataValue{}}
			for _, name := range f.columns {
				r.Values[name] = MySQLDataValue{Text: "x"}
			}
			return r
		})
		request.ExtendedInsert = true
		result, err := m.ImportMySQLDataStream(context.Background(), 1, "db", "records", request)
		if err != nil || !result.Committed || len(f.statements) != 2 || result.Progress.Prepared != 10000 {
			t.Fatal(result, err, len(f.statements))
		}
		for _, s := range f.statements {
			if len(s.args) > 65535 {
				t.Fatal("MySQL parameter limit exceeded", len(s.args))
			}
		}
	})
}
