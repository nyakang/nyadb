package mysql

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func newSQLTokenTestManager(t *testing.T) *MySQLSessionManager {
	t.Helper()
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		db := sql.OpenDB(sqlTestConnector{log: make(chan string, 128)})
		db.SetMaxOpenConns(8)
		db.SetMaxIdleConns(0)
		return db, nil
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestMySQLSQLTabTokenRejectsPriorProfileGeneration(t *testing.T) {
	m := newSQLTokenTestManager(t)
	ctx := context.Background()
	settings := connections.ActiveSettings{ProfileID: 617}
	if err := m.Connect(ctx, settings, ""); err != nil {
		t.Fatal(err)
	}
	old, err := m.ReserveSQLTabToken(617, 88)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.PreconnectSQLTabToken(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ExecuteSQLTabToken(ctx, old, "", "SELECT id,name FROM products"); err != nil {
		t.Fatal(err)
	}
	if err := m.Connect(ctx, settings, ""); err != nil {
		t.Fatal(err)
	}

	// The same numeric ID in the new session must be a completely different
	// lease; a delayed old worker must never execute against or close it.
	fresh, err := m.ReserveSQLTabToken(617, 88)
	if err != nil {
		t.Fatal(err)
	}
	if old.generation == fresh.generation || old.lease == fresh.lease {
		t.Fatal("reconnected session reused old generation or lease identity")
	}
	for _, operation := range []struct {
		name string
		err  error
	}{
		{"stale preconnect", m.PreconnectSQLTabToken(ctx, old)},
		{"stale execute", func() error {
			_, e := m.ExecuteSQLTabToken(ctx, old, "", "SELECT id,name FROM products")
			return e
		}()},
		{"legacy reserve", m.ReserveSQLTab(617, 99)},
		{"legacy preconnect", m.PreconnectSQLTab(ctx, 617, 88)},
		{"legacy execute", func() error {
			_, e := m.ExecuteSQLTab(ctx, 617, 88, "", "SELECT id,name FROM products")
			return e
		}()},
	} {
		if !errors.Is(operation.err, ErrMySQLSQLSessionLost) {
			t.Fatalf("%s bypassed the profile generation: %v", operation.name, operation.err)
		}
	}
	m.ReleaseSQLTabToken(old)
	m.ReleaseSQLTab(617, 88) // stale unversioned release cannot revoke new token
	if err := m.PreconnectSQLTabToken(ctx, fresh); err != nil {
		t.Fatalf("stale release invalidated new session: %v", err)
	}
	if _, err := m.ExecuteSQLTabToken(ctx, fresh, "", "SELECT id,name FROM products"); err != nil {
		t.Fatalf("new token could not execute SQL: %v", err)
	}
	m.ReleaseSQLTabToken(fresh)
	if err := m.PreconnectSQLTabToken(ctx, fresh); !errors.Is(err, ErrMySQLSQLSessionLost) {
		t.Fatalf("released token was reused: %v", err)
	}
}

func TestMySQLSQLTabTokenRejectsReusedTabIDInSameGeneration(t *testing.T) {
	m := newSQLTokenTestManager(t)
	ctx := context.Background()
	if err := m.Connect(ctx, connections.ActiveSettings{ProfileID: 618}, ""); err != nil {
		t.Fatal(err)
	}
	old, err := m.ReserveSQLTabToken(618, 1)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseSQLTabToken(old)
	fresh, err := m.ReserveSQLTabToken(618, 1)
	if err != nil {
		t.Fatal(err)
	}
	if old.generation != fresh.generation || old.lease == fresh.lease {
		t.Fatal("same-generation tab re-opened without fresh lease")
	}
	m.ReleaseSQLTabToken(old)
	if err := m.PreconnectSQLTabToken(ctx, fresh); err != nil {
		t.Fatalf("old token revoked a different lease: %v", err)
	}
	if _, err := m.ExecuteSQLTabToken(ctx, old, "", "SELECT 1"); !errors.Is(err, ErrMySQLSQLSessionLost) {
		t.Fatalf("old lease could execute: %v", err)
	}
	m.ReleaseSQLTabToken(fresh)
}

func TestMySQLSQLTabTokenFailsAfterDisconnectAndClose(t *testing.T) {
	m := newSQLTokenTestManager(t)
	ctx := context.Background()
	if err := m.Connect(ctx, connections.ActiveSettings{ProfileID: 619}, ""); err != nil {
		t.Fatal(err)
	}
	token, err := m.ReserveSQLTabToken(619, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Disconnect(619); err != nil {
		t.Fatal(err)
	}
	if err := m.PreconnectSQLTabToken(ctx, token); !errors.Is(err, ErrMySQLSQLSessionLost) {
		t.Fatalf("stale token preconnected after disconnect: %v", err)
	}
	if err := m.Connect(ctx, connections.ActiveSettings{ProfileID: 619}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ExecuteSQLTabToken(ctx, token, "", "SELECT 1"); !errors.Is(err, ErrMySQLSQLSessionLost) {
		t.Fatalf("stale token executed after reconnect: %v", err)
	}
	m.ReleaseSQLTabToken(token)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReserveSQLTabToken(619, 7); !errors.Is(err, ErrMySQLNotConnected) {
		t.Fatalf("closed manager reserved SQL token: %v", err)
	}
}

func TestMySQLSQLTabTokenDelayedPreconnectAndReconnect(t *testing.T) {
	m := newSQLTokenTestManager(t)
	ctx := context.Background()
	if err := m.Connect(ctx, connections.ActiveSettings{ProfileID: 620}, ""); err != nil {
		t.Fatal(err)
	}
	old, err := m.ReserveSQLTabToken(620, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Connect(ctx, connections.ActiveSettings{ProfileID: 620}, ""); err != nil {
		t.Fatal(err)
	}
	fresh, err := m.ReserveSQLTabToken(620, 4)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate old work starting after Connect returns.
	done := make(chan error, 1)
	go func() { done <- m.PreconnectSQLTabToken(ctx, old) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrMySQLSQLSessionLost) {
			t.Fatalf("old worker preconnected new session: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("old preconnect worker never returned")
	}
	if err := m.PreconnectSQLTabToken(ctx, fresh); err != nil {
		t.Fatalf("new preconnect impacted by stale worker: %v", err)
	}
}
