package mysql

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// Runs without MySQL. A pair of SQL tabs must retain two reserved connections
// until disposal even when both use the same profile and an injected mock pool.
func TestSQLTabLeasesKeepPhysicalConnectionsAndRelease(t *testing.T) {
	m := NewMySQLSessionManager()
	var db *sql.DB
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		db = sql.OpenDB(sqlTestConnector{log: make(chan string, 64)})
		db.SetMaxOpenConns(2)
		db.SetMaxIdleConns(0)
		return db, nil
	}
	if err := m.Connect(context.Background(), connections.ActiveSettings{ProfileID: 101}, ""); err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for _, tab := range []uint64{11, 22} {
		_, err := m.ExecuteSQLTab(context.Background(), 101, tab, "", "SELECT id,name FROM products")
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := db.Stats().InUse; got != 2 {
		t.Fatalf("expected reserved physical connection per tab, got %d", got)
	}
	m.ReleaseSQLTab(101, 11)
	deadline := time.Now().Add(2 * time.Second)
	for db.Stats().InUse != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := db.Stats().InUse; got != 1 {
		t.Fatalf("closing one tab did not release only its connection: %d", got)
	}
	_, err := m.ExecuteSQLTab(context.Background(), 101, 22, "", "SELECT id,name FROM products")
	if err != nil {
		t.Fatalf("other tab lost its session: %v", err)
	}
	m.ReleaseSQLTab(101, 22)
	deadline = time.Now().Add(2 * time.Second)
	for db.Stats().InUse != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := db.Stats().InUse; n != 0 {
		t.Fatalf("tab lease leak: %d", n)
	}
	if _, err := m.ExecuteSQLTab(context.Background(), 101, 0, "", "SELECT 1"); !errors.Is(err, connections.ErrInvalid) {
		t.Fatalf("missing tab id accepted: %v", err)
	}
	if err := m.Disconnect(101); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ExecuteSQLTab(context.Background(), 101, 55, "", "SELECT 1"); !errors.Is(err, ErrMySQLNotConnected) {
		t.Fatalf("disconnected profile accepted SQL: %v", err)
	}
}
