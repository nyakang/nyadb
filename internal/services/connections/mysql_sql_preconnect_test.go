package connections

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestSQLTabPreconnectReservesPhysicalConnectionBeforeFirstSQL(t *testing.T) {
	m := NewMySQLSessionManager()
	var db *sql.DB
	m.opener = func(context.Context, ActiveSettings, string) (*sql.DB, error) {
		db = sql.OpenDB(sqlTestConnector{log: make(chan string, 64)})
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(0)
		return db, nil
	}
	const profileID int64 = 303
	if err := m.Connect(context.Background(), ActiveSettings{ProfileID: profileID}, ""); err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.ReserveSQLTab(profileID, 11); err != nil {
		t.Fatal(err)
	}
	if n := db.Stats().InUse; n != 0 {
		t.Fatalf("reserve performed network I/O on the UI thread: %d", n)
	}
	if err := m.PreconnectSQLTab(context.Background(), profileID, 11); err != nil {
		t.Fatal(err)
	}
	if n := db.Stats().InUse; n != 1 {
		t.Fatalf("preconnect did not reserve a physical connection: %d", n)
	}
	if err := m.PreconnectSQLTab(context.Background(), profileID, 11); err != nil {
		t.Fatalf("repeat preconnect: %v", err)
	}
	if n := db.Stats().InUse; n != 1 {
		t.Fatalf("repeat preconnect allocated another connection: %d", n)
	}
	if _, err := m.ExecuteSQLTab(context.Background(), profileID, 11, "", "SELECT id,name FROM products"); err != nil {
		t.Fatal(err)
	}
	if n := db.Stats().InUse; n != 1 {
		t.Fatalf("SQL did not reuse the preconnected session: %d", n)
	}
	if err := m.ReserveSQLTab(profileID, 22); err != nil {
		t.Fatal(err)
	}
	if err := m.PreconnectSQLTab(context.Background(), profileID, 22); err != nil {
		t.Fatal(err)
	}
	if n := db.Stats().InUse; n != 2 {
		t.Fatalf("tabs did not retain distinct sessions: %d", n)
	}
	m.ReleaseSQLTab(profileID, 11)
	waitSQLTabConnectionCount(t, db, 1)
	if err := m.PreconnectSQLTab(context.Background(), profileID, 11); !errors.Is(err, ErrMySQLSQLSessionLost) {
		t.Fatalf("released lease was recreated by preconnect: %v", err)
	}
	m.ReleaseSQLTab(profileID, 22)
	waitSQLTabConnectionCount(t, db, 0)
}

func TestSQLTabPreconnectRevokedBeforeWorkerStarts(t *testing.T) {
	m := NewMySQLSessionManager()
	var db *sql.DB
	m.opener = func(context.Context, ActiveSettings, string) (*sql.DB, error) {
		db = sql.OpenDB(sqlTestConnector{log: make(chan string, 16)})
		return db, nil
	}
	const profileID int64 = 404
	if err := m.Connect(context.Background(), ActiveSettings{ProfileID: profileID}, ""); err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.ReserveSQLTab(profileID, 44); err != nil {
		t.Fatal(err)
	}
	m.ReleaseSQLTab(profileID, 44)
	if err := m.PreconnectSQLTab(context.Background(), profileID, 44); !errors.Is(err, ErrMySQLSQLSessionLost) {
		t.Fatalf("preconnect revived a released tab: %v", err)
	}
	if n := db.Stats().OpenConnections; n != 0 {
		t.Fatalf("released tab opened a connection: %d", n)
	}
	if err := m.ReserveSQLTab(profileID, 45); err != nil {
		t.Fatal(err)
	}
	if err := m.Disconnect(profileID); err != nil {
		t.Fatal(err)
	}
	if err := m.PreconnectSQLTab(context.Background(), profileID, 45); !errors.Is(err, ErrMySQLNotConnected) {
		t.Fatalf("disconnect did not reject preconnect: %v", err)
	}
}

func TestSQLTabPreconnectRejectsInvalidAndCanceledContext(t *testing.T) {
	m := NewMySQLSessionManager()
	if err := m.ReserveSQLTab(123, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero tab ID accepted: %v", err)
	}
	if err := m.PreconnectSQLTab(context.Background(), 123, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero tab ID preconnected: %v", err)
	}
	if err := m.ReserveSQLTab(123, 1); !errors.Is(err, ErrMySQLNotConnected) {
		t.Fatalf("disconnected profile reserved tab: %v", err)
	}
	m.opener = func(context.Context, ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(sqlTestConnector{log: make(chan string, 16)}), nil
	}
	if err := m.Connect(context.Background(), ActiveSettings{ProfileID: 123}, ""); err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.ReserveSQLTab(123, 7); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.PreconnectSQLTab(ctx, 123, 7); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preconnect did not propagate: %v", err)
	}
	if _, err := m.ExecuteSQLTab(context.Background(), 123, 7, "", "SELECT id,name FROM products"); !errors.Is(err, ErrMySQLSQLSessionLost) {
		t.Fatalf("canceled lease was silently replaced: %v", err)
	}
}

func waitSQLTabConnectionCount(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for db.Stats().InUse != want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := db.Stats().InUse; got != want {
		t.Fatalf("SQL tab connection leak: got %d, want %d", got, want)
	}
}
