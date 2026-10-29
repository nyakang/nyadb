package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

type mysqlSessionMockConnector struct{}
type mysqlSessionMockConn struct{}
type mysqlSessionMockRows struct{ index int }

func (mysqlSessionMockConnector) Connect(context.Context) (driver.Conn, error) {
	return mysqlSessionMockConn{}, nil
}
func (mysqlSessionMockConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }

type mysqlSessionMockDriver struct{}

func (mysqlSessionMockDriver) Open(string) (driver.Conn, error) { return mysqlSessionMockConn{}, nil }
func (mysqlSessionMockConn) Close() error                       { return nil }
func (mysqlSessionMockConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements unsupported")
}
func (mysqlSessionMockConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions unsupported")
}
func (mysqlSessionMockConn) Ping(context.Context) error { return nil }
func (mysqlSessionMockConn) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	if q != "SHOW DATABASES" {
		return nil, errors.New("unexpected SQL query")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &mysqlSessionMockRows{}, nil
}
func (*mysqlSessionMockRows) Columns() []string { return []string{"Database"} }
func (*mysqlSessionMockRows) Close() error      { return nil }
func (r *mysqlSessionMockRows) Next(dst []driver.Value) error {
	switch r.index {
	case 0:
		dst[0] = "information_schema"
	case 1:
		dst[0] = "sample"
	default:
		return io.EOF
	}
	r.index++
	return nil
}

func TestMySQLSessionManagerLifecycleAndDatabaseMetadata(t *testing.T) {
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(mysqlSessionMockConnector{}), nil
	}
	s := connections.ActiveSettings{ProfileID: 7, Host: "localhost", Port: 3306}
	if err := m.Connect(context.Background(), s, "ephemeral-password"); err != nil {
		t.Fatal(err)
	}
	if !m.IsConnected(7) {
		t.Fatal("session not registered")
	}
	names, err := m.Databases(context.Background(), 7)
	if err != nil || len(names) != 2 || names[1] != "sample" {
		t.Fatalf("metadata query: %v %v", names, err)
	}
	if err := m.Disconnect(7); err != nil {
		t.Fatal(err)
	}
	if m.IsConnected(7) {
		t.Fatal("disconnect retained session")
	}
	if _, err := m.Databases(context.Background(), 7); !errors.Is(err, ErrMySQLNotConnected) {
		t.Fatal("query after disconnect allowed")
	}
	if err := m.Connect(context.Background(), s, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if m.IsConnected(7) {
		t.Fatal("closed manager kept connection")
	}
	if err := m.Connect(context.Background(), s, ""); !errors.Is(err, ErrMySQLManagerClosed) {
		t.Fatal("closed manager reconnected")
	}
}

func TestMySQLSessionManagerCancelledHandshakeNeverResurrects(t *testing.T) {
	m := NewMySQLSessionManager()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	m.opener = func(ctx context.Context, _ connections.ActiveSettings, _ string) (*sql.DB, error) {
		once.Do(func() { close(started) })
		<-release
		// Simulate a remote handshake ignoring context until completion.
		return sql.OpenDB(mysqlSessionMockConnector{}), nil
	}
	s := connections.ActiveSettings{ProfileID: 12}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Connect(ctx, s, "") }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("connect never started")
	}
	cancel()
	if err := m.Disconnect(12); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stale connection result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled handshake blocked")
	}
	if m.IsConnected(12) {
		t.Fatal("late connection resurrected session")
	}
	_ = m.Close()
}
