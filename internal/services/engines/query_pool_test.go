package engines

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func verifyQueryPoolIsolation(t *testing.T, s *Session, schema string) {
	t.Helper()
	ctx := context.Background()
	var leases []contract.QueryLease
	t.Cleanup(func() {
		for _, l := range leases {
			l.Release()
		}
	})
	for i := 1; i <= cap(s.sqlSlots); i++ {
		l, err := s.ReserveQuery(uint64(i))
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, l)
		if err = l.Preconnect(ctx); err != nil {
			t.Fatal(err)
		}
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := s.Browse(bounded, s.Namespace(schema)); err != nil {
		t.Fatalf("idle SQL tabs exhausted background pool: %v", err)
	}
	extra, err := s.ReserveQuery(999)
	if err != nil {
		t.Fatal(err)
	}
	leases = append(leases, extra)
	if err = extra.Preconnect(bounded); !errors.Is(err, contract.ErrSQLSessionLimit) {
		t.Fatalf("wanted retryable quota error: %v", err)
	}
	leases[0].Release()
	leases[0].Release()
	if err = extra.Preconnect(bounded); err != nil {
		t.Fatalf("released quota not reusable: %v", err)
	}
	if _, err = extra.Execute(bounded, schema, "SELECT 1"); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteQueryPoolLeavesBackgroundConnectionsAvailable(t *testing.T) {
	s := adapterFixture(t)
	verifyQueryPoolIsolation(t, s, "main")
}

func TestSQLiteQuotaRejectsBeforeWaitingForMaintenance(t *testing.T) {
	s := adapterFixture(t)
	for i := 0; i < cap(s.sqlSlots); i++ {
		l, err := s.Reserve(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
	}
	unlock, err := s.schema.acquire(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = s.Reserve(ctx); !errors.Is(err, contract.ErrSQLSessionLimit) {
		t.Fatalf("full SQL quota waited behind maintenance: %v", err)
	}
}

func TestQueryReleaseCancelsPendingSQLitePreparation(t *testing.T) {
	s := adapterFixture(t)
	unlock, err := s.schema.acquire(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	l, err := s.ReserveQuery(7)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- l.Preconnect(context.Background()) }()
	l.Release()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) && !errors.Is(err, contract.ErrRevoked) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("released document left a preparation worker waiting")
	}
	if len(s.sqlSlots) != 0 {
		t.Fatal("canceled preparation leaked SQL quota")
	}
}

func TestSQLiteConcurrentLeaseCloseReturnsQuotaOnce(t *testing.T) {
	s := adapterFixture(t)
	lease, err := s.Reserve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() { _ = lease.Close() })
	}
	workers.Wait()
	if len(s.sqlSlots) != 0 {
		t.Fatal("concurrent close leaked quota")
	}
}

func TestPostgreSQL17QueryDialCancellationFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	original := s.sqlDB
	started := make(chan struct{})
	var once sync.Once
	blocked, err := (postgresDialect{}).open(s.backupProfile, "", func(ctx context.Context, _, _ string) (net.Conn, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	s.sqlDB = blocked
	t.Cleanup(func() { s.sqlDB = original; blocked.Close() })
	lease, err := s.ReserveQuery(333)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- lease.Preconnect(context.Background()) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("dial did not start")
	}
	lease.Release()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("canceled dial succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("closing a SQL tab did not cancel its dial")
	}
	if len(s.sqlSlots) != 0 {
		t.Fatal("failed PostgreSQL dial leaked quota")
	}
}

func TestPostgreSQL17QueryLeaseFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	t.Run("background pool", func(t *testing.T) { verifyQueryPoolIsolation(t, s, "public") })
	l, err := s.ReserveQuery(77)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	ctx := context.Background()
	run := func(q string) contract.SQLResult {
		t.Helper()
		r, err := l.Execute(ctx, "public", q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return r
	}
	run("SET search_path TO pg_catalog")
	if r := run("SHOW search_path"); r.Rows[0][0] != "pg_catalog" {
		t.Fatal("application overwrote user session settings", r.Rows)
	}
	run("BEGIN; SAVEPOINT original")
	if _, err = l.Execute(ctx, "public", "SELECT 1/0"); err == nil {
		t.Fatal("fixture did not fail")
	}
	run("ROLLBACK TO SAVEPOINT original")
	run("SELECT 1")
	if _, err = l.Execute(ctx, "public", "SELECT 1/0"); err == nil {
		t.Fatal("fixture did not fail")
	}
	run("ROLLBACK")
	run("SELECT 1")
	run("BEGIN; SAVEPOINT changed_schema")
	if _, err = l.Execute(ctx, "public", "SELECT 1/0"); err == nil {
		t.Fatal("fixture did not fail")
	}
	if _, err = l.Execute(ctx, "pg_catalog", "SELECT 42"); err == nil {
		t.Fatal("schema setup in failed transaction unexpectedly succeeded")
	}
	if _, err = l.Execute(ctx, "pg_catalog", "ROLLBACK TO SAVEPOINT changed_schema"); err != nil {
		t.Fatal("schema switch blocked savepoint recovery", err)
	}
	if _, err = l.Execute(ctx, "pg_catalog", "SELECT 1/0"); err == nil {
		t.Fatal("fixture did not fail")
	}
	if _, err = l.Execute(ctx, "public", "ROLLBACK"); err != nil {
		t.Fatal("schema switch blocked rollback", err)
	}
	if _, err = l.ExecuteStream(ctx, "pg_catalog", "SHOW search_path", func(_ int, _ []contract.Column, row []contract.Cell) error {
		if len(row) > 0 && row[0].Text != "pg_catalog" {
			t.Fatal("explicit schema switch failed", row)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fresh, err := l.Replace()
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Release()
	if r, err := fresh.Execute(ctx, "public", "SHOW search_path"); err != nil || r.Rows[0][0] != "public" {
		t.Fatal("replacement inherited old scope", r.Rows, err)
	}
}
