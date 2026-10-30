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

type streamImportConnector struct{ fixture *dataImportModesFixture }

func (c streamImportConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }
func (c streamImportConnector) Connect(ctx context.Context) (driver.Conn, error) {
	base, err := (dataImportModesConnector{c.fixture}).Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &streamImportConn{dataImportModesConn: base.(*dataImportModesConn)}, nil
}

type streamImportConn struct{ *dataImportModesConn }

func (c *streamImportConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, "@@SESSION.foreign_key_checks") {
		return &structureRows{cols: []string{"mode", "fk"}, values: [][]driver.Value{{"STRICT_TRANS_TABLES", int64(1)}}}, nil
	}
	return c.dataImportConn.QueryContext(ctx, q, args)
}
func streamImportManager(t *testing.T, f *dataImportModesFixture) *MySQLSessionManager {
	t.Helper()
	if f.base == nil {
		f.base = &dataImportFixture{}
	}
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(streamImportConnector{f}), nil
	}
	if err := m.Connect(context.Background(), connections.ActiveSettings{ProfileID: 1}, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

type streamRows struct {
	count, total int
	value        func(int) MySQLDataEdit
	closed       bool
}

func (r *streamRows) Next(ctx context.Context) (MySQLDataEdit, error) {
	if err := ctx.Err(); err != nil {
		return MySQLDataEdit{}, err
	}
	if r.count >= r.total {
		return MySQLDataEdit{}, io.EOF
	}
	r.count++
	return r.value(r.count), nil
}
func (r *streamRows) Close() error { r.closed = true; return nil }
func streamRequest(total int, value func(int) MySQLDataEdit) MySQLDataImportStreamRequest {
	return MySQLDataImportStreamRequest{Rows: int64(total), Open: func(context.Context) (MySQLDataImportRowSource, error) {
		return &streamRows{total: total, value: value}, nil
	}}
}

func TestMySQLDataImportStreamValidatesCompleteSourceBeforeDDL(t *testing.T) {
	f := &dataImportModesFixture{}
	m := streamImportManager(t, f)
	request := streamRequest(10001, func(i int) MySQLDataEdit {
		row := dataImportModesRows("ok")[0]
		if i == 10001 {
			row.Values["unknown"] = MySQLDataValue{Text: "invalid"}
		}
		return row
	})
	request.Request = MySQLDataImportRequest{NewTable: true, Columns: []MySQLDataImportColumn{{Name: "note", Type: "VARCHAR(80)", Nullable: true}}}
	result, err := m.ImportMySQLDataStream(context.Background(), 1, "db", "new", request)
	if err == nil || result.Created || f.base.creates != 0 || f.base.inserts != 0 {
		t.Fatal("last source row reached DDL/writes", result, err, f.base)
	}
}

func TestMySQLDataImportStreamCrossesLegacyRowLimitWithOneCommitAndBoundedReceipt(t *testing.T) {
	f := &dataImportModesFixture{}
	m := streamImportManager(t, f)
	request := streamRequest(10001, func(int) MySQLDataEdit { return dataImportModesRows("ok")[0] })
	records := 0
	request.Record = func(row MySQLDataImportRowResult) error {
		records++
		if row.Row != records || row.Status != "prepared" {
			return errors.New("invalid provisional receipt")
		}
		return nil
	}
	result, err := m.ImportMySQLDataStream(context.Background(), 1, "db", "records", request)
	if err != nil || !result.Committed || result.Progress.Prepared != 10001 || len(result.Rows) != 100 || records != 10001 || f.base.commits != 1 || f.base.inserts != 10001 {
		t.Fatal("stream was capped, cached all rows or split commits", result, err, records, f.base)
	}
}

func TestMySQLDataImportStreamJournalFailureRollsBackAllEarlierRows(t *testing.T) {
	f := &dataImportModesFixture{}
	m := streamImportManager(t, f)
	request := streamRequest(3, func(int) MySQLDataEdit { return dataImportModesRows("ok")[0] })
	request.Record = func(row MySQLDataImportRowResult) error {
		if row.Row == 2 {
			return io.ErrShortWrite
		}
		return nil
	}
	result, err := m.ImportMySQLDataStream(context.Background(), 1, "db", "records", request)
	if !errors.Is(err, io.ErrShortWrite) || result.Committed || f.base.commits != 0 || f.base.rollbacks != 1 || result.Rows[0].Status != "rolled_back" {
		t.Fatal("journal failure published earlier batches", result, err, f.base)
	}
}

func TestMySQLDataImportStreamUncertainCommitIsNeverReplayed(t *testing.T) {
	f := &dataImportModesFixture{base: &dataImportFixture{commitError: io.ErrUnexpectedEOF}}
	m := streamImportManager(t, f)
	request := streamRequest(1, func(int) MySQLDataEdit { return dataImportModesRows("ok")[0] })
	result, err := m.ImportMySQLDataStream(context.Background(), 1, "db", "records", request)
	if !errors.Is(err, ErrMySQLDataCommitUncertain) || !result.CommitUncertain || result.Committed || result.Rows[0].Status != "uncertain" || f.base.commits != 1 || f.base.inserts != 1 {
		t.Fatal("commit replay or false success", result, err, f.base)
	}
}

func TestMySQLDataImportStreamCancelWaitsForWriteAndRollback(t *testing.T) {
	f := &dataImportModesFixture{writeStarted: make(chan struct{}), writeRelease: make(chan struct{}), rollbackStarted: make(chan struct{}), rollbackEnd: make(chan struct{})}
	m := streamImportManager(t, f)
	request := streamRequest(1, func(int) MySQLDataEdit { return dataImportModesRows("cancel")[0] })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := m.ImportMySQLDataStream(ctx, 1, "db", "records", request); done <- err }()
	<-f.writeStarted
	cancel()
	select {
	case <-done:
		t.Fatal("early write acknowledgement")
	case <-time.After(20 * time.Millisecond):
	}
	close(f.writeRelease)
	<-f.rollbackStarted
	select {
	case <-done:
		t.Fatal("early rollback acknowledgement")
	case <-time.After(20 * time.Millisecond):
	}
	close(f.rollbackEnd)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
