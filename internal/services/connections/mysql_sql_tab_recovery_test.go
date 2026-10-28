package connections

import (
	"context"
	"errors"
	"testing"
)

// Explicit recovery replaces exactly one SQL tab lease; no SQL is dispatched
// and other tabs keep their own session and quota.
func TestReplaceSQLTabTokenAfterLoss(t *testing.T) {
	m := newSQLTokenTestManager(t)
	ctx := context.Background()
	cfg := ActiveSettings{ProfileID: 720}
	if err := m.Connect(ctx, cfg, ""); err != nil {
		t.Fatal(err)
	}
	old, err := m.ReserveSQLTabToken(720, 1)
	if err != nil {
		t.Fatal(err)
	}
	other, err := m.ReserveSQLTabToken(720, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ExecuteSQLTabToken(ctx, old, "", "SELECT id,name FROM products"); err != nil {
		t.Fatal(err)
	}
	old.lease.mu.Lock()
	old.lease.lost = true
	old.lease.mu.Unlock()
	if _, err := m.ExecuteSQLTabToken(ctx, old, "", "SELECT id,name FROM products"); !errors.Is(err, ErrMySQLSQLSessionLost) {
		t.Fatalf("old lease should be lost: %v", err)
	}
	fresh, err := m.ReplaceSQLTabToken(old)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.generation != old.generation || fresh.lease == old.lease || fresh.tabID != old.tabID {
		t.Fatalf("replacement lost identity or reused physical lease: %+v", fresh)
	}
	m.ReleaseSQLTabToken(old)
	if _, err := m.ReplaceSQLTabToken(old); !errors.Is(err, ErrMySQLSQLSessionLost) {
		t.Fatalf("stale double recovery succeeded: %v", err)
	}
	if _, err := m.ExecuteSQLTabToken(ctx, old, "", "SELECT id,name FROM products"); !errors.Is(err, ErrMySQLSQLSessionLost) {
		t.Fatalf("old token executed after explicit recovery: %v", err)
	}
	if err := m.PreconnectSQLTabToken(ctx, fresh); err != nil {
		t.Fatalf("recovered lease did not connect: %v", err)
	}
	if _, err := m.ExecuteSQLTabToken(ctx, fresh, "", "SELECT id,name FROM products"); err != nil {
		t.Fatalf("new token did not execute: %v", err)
	}
	if _, err := m.ExecuteSQLTabToken(ctx, other, "", "SELECT id,name FROM products"); err != nil {
		t.Fatalf("another SQL tab was disturbed: %v", err)
	}
}

func TestReplaceSQLTabTokenRejectsStaleGeneration(t *testing.T) {
	m := newSQLTokenTestManager(t)
	ctx := context.Background()
	cfg := ActiveSettings{ProfileID: 721}
	if err := m.Connect(ctx, cfg, ""); err != nil {
		t.Fatal(err)
	}
	old, err := m.ReserveSQLTabToken(721, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Connect(ctx, cfg, ""); err != nil {
		t.Fatal(err)
	}
	fresh, err := m.ReserveSQLTabToken(721, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReplaceSQLTabToken(old); !errors.Is(err, ErrMySQLSQLSessionLost) {
		t.Fatalf("old generation replaced the new one: %v", err)
	}
	if err := m.PreconnectSQLTabToken(ctx, fresh); err != nil {
		t.Fatalf("stale recovery harmed connected lease: %v", err)
	}
}

func TestReplaceSQLTabTokenPreservesQuota(t *testing.T) {
	m := newSQLTokenTestManager(t)
	ctx := context.Background()
	adv := DefaultMySQLAdvanced()
	adv.LimitSessions = true
	adv.MaxSessions = 1
	if err := m.Connect(ctx, ActiveSettings{ProfileID: 722, MySQLAdvanced: adv}, ""); err != nil {
		t.Fatal(err)
	}
	old, err := m.ReserveSQLTabToken(722, 1)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := m.ReplaceSQLTabToken(old)
	if err != nil {
		t.Fatalf("explicit recovery should reuse its quota slot: %v", err)
	}
	if _, err := m.ReserveSQLTabToken(722, 2); !errors.Is(err, ErrMySQLSQLSessionLimit) {
		t.Fatalf("recovery bypassed session quota: %v", err)
	}
	m.ReleaseSQLTabToken(old)
	if err := m.PreconnectSQLTabToken(ctx, fresh); err != nil {
		t.Fatalf("stale close broke replacement: %v", err)
	}
	m.ReleaseSQLTabToken(fresh)
	if _, err := m.ReserveSQLTabToken(722, 2); err != nil {
		t.Fatalf("quota not released after close: %v", err)
	}
}
