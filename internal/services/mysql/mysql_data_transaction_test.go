package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

type dataTransactionFixture struct {
	mu                                 sync.Mutex
	value                              string
	writes, commits, rollbacks, closes int
	uncertain, applied                 bool
	commitStarted, commitRelease       chan struct{}
	rollbackStarted, rollbackRelease   chan struct{}
}
type dataTransactionConnector struct{ f *dataTransactionFixture }

func (c dataTransactionConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }
func (c dataTransactionConnector) Connect(context.Context) (driver.Conn, error) {
	return &dataTransactionConn{f: c.f}, nil
}

type dataTransactionConn struct {
	f                  *dataTransactionFixture
	pending, savepoint string
	inTx               bool
}

func (c *dataTransactionConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (c *dataTransactionConn) Close() error { c.f.mu.Lock(); c.f.closes++; c.f.mu.Unlock(); return nil }
func (c *dataTransactionConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *dataTransactionConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.f.mu.Lock()
	c.pending = c.f.value
	c.f.mu.Unlock()
	c.inTx = true
	return dataTransactionTx{c}, nil
}

type dataTransactionTx struct{ c *dataTransactionConn }

func (tx dataTransactionTx) Commit() error {
	f := tx.c.f
	if f.commitStarted != nil {
		close(f.commitStarted)
		<-f.commitRelease
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits++
	if !f.uncertain || f.applied {
		f.value = tx.c.pending
	}
	tx.c.inTx = false
	if f.uncertain {
		return errors.New("lost COMMIT reply")
	}
	return nil
}
func (tx dataTransactionTx) Rollback() error {
	if tx.c.f.rollbackStarted != nil {
		close(tx.c.f.rollbackStarted)
		<-tx.c.f.rollbackRelease
	}
	tx.c.f.mu.Lock()
	tx.c.f.rollbacks++
	tx.c.f.mu.Unlock()
	tx.c.inTx = false
	return nil
}
func (c *dataTransactionConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	switch {
	case strings.HasPrefix(q, "SET SESSION"):
		return driver.RowsAffected(0), nil
	case strings.HasPrefix(q, "SAVEPOINT"):
		c.savepoint = c.pending
		return driver.RowsAffected(0), nil
	case strings.HasPrefix(q, "ROLLBACK TO"):
		c.pending = c.savepoint
		return driver.RowsAffected(0), nil
	case strings.HasPrefix(q, "RELEASE SAVEPOINT"):
		return driver.RowsAffected(0), nil
	case strings.HasPrefix(q, "UPDATE "):
		c.f.mu.Lock()
		c.f.writes++
		c.f.mu.Unlock()
		c.pending = "afterhash"
		return driver.RowsAffected(1), nil
	}
	return nil, errors.New("unexpected exec")
}
func (c *dataTransactionConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, "information_schema.STATISTICS") && strings.Contains(q, "s.SEQ_IN_INDEX, s.SUB_PART") {
		return &structureRows{cols: []string{"index", "name", "seq", "prefix", "nullable"}, values: [][]driver.Value{{"PRIMARY", "id", int64(1), nil, "NO"}}}, nil
	}
	if strings.Contains(q, "__nyadb_identity_") {
		c.f.mu.Lock()
		value := c.f.value
		c.f.mu.Unlock()
		if c.inTx {
			value = c.pending
		}
		note := "before"
		if value == "afterhash" {
			note = "after"
		}
		return &structureRows{cols: []string{"id", "note", "key", "idhash", "notehash"}, values: [][]driver.Value{{int64(1), note, int64(1), "idhash", value}}}, nil
	}
	if strings.Contains(q, "OCTET_LENGTH") {
		return &structureRows{cols: []string{"size"}, values: [][]driver.Value{{int64(6)}}}, nil
	}
	if strings.HasPrefix(q, "SELECT `id`,`note`") {
		return &structureRows{cols: []string{"id", "note"}, values: [][]driver.Value{{int64(1), "after"}}}, nil
	}
	base := commitConn{f: &commitFixture{value: c.f.value}, pending: c.pending}
	return base.QueryContext(ctx, q, args)
}
func transactionTestManager(t *testing.T, f *dataTransactionFixture) *MySQLSessionManager {
	t.Helper()
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(dataTransactionConnector{f}), nil
	}
	if err := m.Connect(context.Background(), connections.ActiveSettings{ProfileID: 1}, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}
func transactionTestEdit(t *testing.T, m *MySQLSessionManager, token *MySQLDataTransaction) MySQLDataEdit {
	t.Helper()
	page, err := m.TableDataPageInTransaction(context.Background(), token, 1, "db", "records", MySQLDataOptions{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	return MySQLDataEdit{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]MySQLDataValue{"note": {Text: "after"}}}
}
func TestDataTransactionBatchesStayPendingAndRollbackAtomically(t *testing.T) {
	f := &dataTransactionFixture{value: "beforehash"}
	m := transactionTestManager(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	token, err := m.BeginMySQLDataTransaction(ctx, 1, "db", "records")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	edit := transactionTestEdit(t, m, token)
	if _, err = m.ApplyMySQLDataEditsInTransaction(context.Background(), token, 1, "db", "records", []MySQLDataEdit{edit}); err != nil {
		t.Fatal(err)
	}
	page, err := m.TableDataPageInTransaction(context.Background(), token, 1, "db", "records", MySQLDataOptions{Limit: 20})
	if err != nil || page.Rows[0][1].Text != "after" {
		t.Fatalf("pending visibility: %v %v", page, err)
	}
	outside, err := m.TableDataPageWithOptions(context.Background(), 1, "db", "records", MySQLDataOptions{Limit: 20})
	if err != nil || outside.Rows[0][1].Text != "before" {
		t.Fatalf("metadata pool inherited transaction: %v %v", outside, err)
	}
	bad := edit
	bad.Original = edit.Original
	if _, err = m.ApplyMySQLDataEditsInTransaction(context.Background(), token, 1, "db", "records", []MySQLDataEdit{bad}); !errors.Is(err, ErrMySQLDataConflict) {
		t.Fatalf("missing conflict: %v", err)
	}
	if err = m.RollbackMySQLDataTransaction(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if _, err = m.ApplyMySQLDataEditsInTransaction(context.Background(), token, 1, "db", "records", []MySQLDataEdit{edit}); !errors.Is(err, ErrMySQLDataTransactionLost) {
		t.Fatalf("rolled back token reused: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.value != "beforehash" || f.writes != 1 || f.commits != 0 || f.rollbacks != 1 {
		t.Fatalf("writes escaped rollback: value=%s writes=%d commits=%d rollbacks=%d", f.value, f.writes, f.commits, f.rollbacks)
	}
}
func TestDataTransactionCommitUncertaintyNeverReplays(t *testing.T) {
	f := &dataTransactionFixture{value: "beforehash", uncertain: true, applied: true}
	m := transactionTestManager(t, f)
	token, err := m.BeginMySQLDataTransaction(context.Background(), 1, "db", "records")
	if err != nil {
		t.Fatal(err)
	}
	edit := transactionTestEdit(t, m, token)
	if _, err = m.ApplyMySQLDataEditsInTransaction(context.Background(), token, 1, "db", "records", []MySQLDataEdit{edit}); err != nil {
		t.Fatal(err)
	}
	if err = m.CommitMySQLDataTransaction(context.Background(), token); !errors.Is(err, ErrMySQLDataCommitUncertain) {
		t.Fatal(err)
	}
	if err = m.CommitMySQLDataTransaction(context.Background(), token); !errors.Is(err, ErrMySQLDataTransactionLost) {
		t.Fatalf("commit replayed: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.commits != 1 || f.writes != 1 {
		t.Fatalf("write replayed: writes=%d commits=%d", f.writes, f.commits)
	}
}
func TestDataTransactionDisconnectAndQualifiedIdentityRevokeToken(t *testing.T) {
	f := &dataTransactionFixture{value: "beforehash"}
	m := transactionTestManager(t, f)
	token, err := m.BeginMySQLDataTransaction(context.Background(), 1, "db", "records")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.TableDataPageInTransaction(context.Background(), token, 1, "db", "other", MySQLDataOptions{Limit: 20}); !errors.Is(err, ErrMySQLDataTransactionLost) {
		t.Fatalf("token crossed table identity: %v", err)
	}
	if err = m.Disconnect(1); err != nil {
		t.Fatal(err)
	}
	token.Close()
	if _, err = m.TableDataPageInTransaction(context.Background(), token, 1, "db", "records", MySQLDataOptions{Limit: 20}); !errors.Is(err, ErrMySQLDataTransactionLost) {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rollbacks != 1 || f.closes == 0 {
		t.Fatalf("disconnect leaked connection: rollbacks=%d closes=%d", f.rollbacks, f.closes)
	}
}
func TestDataTransactionCommitCancellationWaitsForAcknowledgement(t *testing.T) {
	f := &dataTransactionFixture{value: "beforehash", commitStarted: make(chan struct{}), commitRelease: make(chan struct{})}
	m := transactionTestManager(t, f)
	token, err := m.BeginMySQLDataTransaction(context.Background(), 1, "db", "records")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.CommitMySQLDataTransaction(ctx, token) }()
	<-f.commitStarted
	cancel()
	select {
	case err := <-done:
		t.Fatalf("cancellation skipped driver acknowledgement: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(f.commitRelease)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDataTransactionCloseWaitsForRollbackAcknowledgement(t *testing.T) {
	f := &dataTransactionFixture{value: "beforehash", rollbackStarted: make(chan struct{}), rollbackRelease: make(chan struct{})}
	m := transactionTestManager(t, f)
	token, err := m.BeginMySQLDataTransaction(context.Background(), 1, "db", "records")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { token.Close(); close(done) }()
	<-f.rollbackStarted
	select {
	case <-done:
		t.Fatal("Close returned before rollback acknowledgement")
	case <-time.After(20 * time.Millisecond):
	}
	close(f.rollbackRelease)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not settle after rollback")
	}
}
