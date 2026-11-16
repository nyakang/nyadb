package agentbridge

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestQueryFactoryRetainsOriginalCapacityAndRejectsForeignSessionWithoutClosingIt(t *testing.T) {
	c := helperClient(t, "query_pool", Options{})
	config := testQueryConfig()
	var released atomic.Int32
	config.OpenConnection = func(ctx context.Context) (*Session, func(), error) {
		s, _, err := c.OpenSession(ctx, map[string]string{"password": "original"})
		if err != nil {
			return nil, nil, err
		}
		_, err = s.Call(ctx, "execute_query", map[string]any{"sql": config.InitializeSQL})
		return s, func() { _, _ = s.Close(); released.Add(1) }, err
	}
	p, err := NewQueryPool(c, map[string]any{}, config)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	l, err := p.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Execute(context.Background(), "PUBLIC", "SELECT exact"); err != nil || released.Load() != 0 {
		t.Fatal("factory capacity released while document remained active", err)
	}
	l.Release()
	l.Release()
	if released.Load() != 1 {
		t.Fatal("original factory release repeated or lost")
	}
	foreign := helperClient(t, "query_pool", Options{})
	borrowed, _, err := foreign.OpenSession(context.Background(), map[string]string{"password": "original"})
	if err != nil {
		t.Fatal(err)
	}
	defer borrowed.Close()
	config.OpenConnection = func(context.Context) (*Session, func(), error) { return borrowed, func() { released.Add(100) }, nil }
	p2, err := NewQueryPool(c, map[string]any{}, config)
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()
	l2, err := p2.ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	if err := l2.Preconnect(context.Background()); !errors.Is(err, ErrRequest) || !borrowed.Alive() || released.Load() != 1 {
		t.Fatal("faulty factory closed/released another runtime's session", err)
	}
}

func TestQueryObserverOwnsReceiptCopyAndCancellationRetainsUnsentSuffix(t *testing.T) {
	c := helperClient(t, "query_pool", Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := testQueryConfig()
	config.RetainUnsentSteps = true
	var observations int
	config.ObserveStatement = func(source string, receipt Receipt) {
		observations++
		if source != "SELECT exact" || !receipt.ExecutorAcknowledged {
			t.Error("observer did not receive original executor and captured statement")
		}
		receipt.Result[0] = '?'
		cancel()
	}
	p, err := NewQueryPool(c, map[string]string{"password": "original"}, config)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	l, err := p.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := l.Execute(ctx, "PUBLIC", "SELECT exact; SELECT later; SELECT unsent")
	if !errors.Is(err, context.Canceled) || observations != 1 || len(r.Steps) != 3 || r.Steps[0].State != "completed" || r.Steps[1].State != "not_started" || r.Steps[2].State != "not_started" || !json.Valid([]byte(r.NativeJSON)) {
		t.Fatal("between-statement cancellation lost completed/unsent evidence or observer changed result", err, r.Steps)
	}
	receipts := l.(interface{ OriginalQueryReceipts() []Receipt }).OriginalQueryReceipts()
	if len(receipts) != 1 || !json.Valid(receipts[0].Result) {
		t.Fatal("observer mutated retained original receipts")
	}
}

func TestQueryFactoryFailedOriginalOpenRequiresExplicitReplacement(t *testing.T) {
	c := helperClient(t, "query_pool", Options{})
	config := testQueryConfig()
	var called, released atomic.Int32
	config.OpenConnection = func(context.Context) (*Session, func(), error) {
		called.Add(1)
		return nil, func() { released.Add(1) }, contract.ErrSQLSessionLost
	}
	p, err := NewQueryPool(c, map[string]any{}, config)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	l, err := p.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Preconnect(context.Background()); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal(err)
	}
	if _, err := l.Execute(context.Background(), "PUBLIC", "SELECT later"); !errors.Is(err, contract.ErrSQLSessionLost) || called.Load() != 1 || released.Load() != 1 {
		t.Fatal("failed original opening was automatically retried", err)
	}
	replacement, err := l.Replace()
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Preconnect(context.Background()); !errors.Is(err, contract.ErrSQLSessionLost) || called.Load() != 2 {
		t.Fatal("explicit replacement did not own its independent attempt", err)
	}
}
