package dbruntime_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type handle struct {
	id     int64
	closed atomic.Int32
	caps   contract.Capabilities
}

func (h *handle) ProfileID() int64                    { return h.id }
func (h *handle) Generation() uint64                  { return 1 }
func (h *handle) Capabilities() contract.Capabilities { return h.caps }
func (h *handle) Close() error                        { h.closed.Add(1); return nil }
func profile(id int64) connections.Profile {
	return connections.Profile{ID: id, Name: "Runtime fixture", Kind: connections.SQLite, Database: "fixture.sqlite"}
}

func TestLateOpenClosesOnlyCapturedHandle(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	started, release := make(chan struct{}), make(chan struct{})
	old, newer := &handle{id: 1}, &handle{id: 1}
	var calls atomic.Int32
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return old, nil
		}
		return newer, nil
	}))
	done := make(chan error, 1)
	go func() { done <- m.Connect(context.Background(), profile(1), "") }()
	<-started
	if e := m.Disconnect(1); e != nil {
		t.Fatal(e)
	}
	published, e := m.ConnectSession(context.Background(), profile(1), "")
	if e != nil {
		t.Fatal(e)
	}
	close(release)
	if e := <-done; !errors.Is(e, contract.ErrRevoked) {
		t.Fatal(e)
	}
	current, e := m.Session(1)
	if e != nil || current != published || newer.closed.Load() != 0 || old.closed.Load() != 1 {
		t.Fatalf("late close damaged replacement: %v old=%d new=%d", e, old.closed.Load(), newer.closed.Load())
	}
	if e = published.Close(); e != nil {
		t.Fatal(e)
	}
	published.Close()
	if newer.closed.Load() != 1 {
		t.Fatal("handle close not idempotent")
	}
}
func TestConcurrentProfilesCancellationAndShutdown(t *testing.T) {
	m := dbruntime.New()
	started, release := make(chan struct{}), make(chan struct{})
	canceled := &handle{id: 1}
	second := &handle{id: 2}
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(ctx context.Context, p connections.Profile, _ string) (contract.Session, error) {
		if p.ID == 1 {
			close(started)
			<-release
			return canceled, nil
		}
		return second, nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Connect(ctx, profile(1), "") }()
	<-started
	if e := m.Connect(context.Background(), profile(2), ""); e != nil {
		t.Fatal(e)
	}
	cancel()
	close(release)
	if e := <-done; !errors.Is(e, contract.ErrRevoked) {
		t.Fatal(e)
	}
	if m.IsConnected(1) || !m.IsConnected(2) || canceled.closed.Load() != 1 {
		t.Fatal("profile cancellation leaked")
	}
	if e := m.Close(); e != nil {
		t.Fatal(e)
	}
	m.Close()
	if second.closed.Load() != 1 || m.IsConnected(2) {
		t.Fatal("shutdown leaked published handle")
	}
	if e := m.Connect(context.Background(), profile(2), ""); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal(e)
	}
}
func TestOldPublishedCloseCannotRevokeReplacement(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	var handles []*handle
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := &handle{id: p.ID}
		handles = append(handles, h)
		return h, nil
	}))
	old, e := m.ConnectSession(context.Background(), profile(1), "")
	if e != nil {
		t.Fatal(e)
	}
	next, e := m.ConnectSession(context.Background(), profile(1), "")
	if e != nil {
		t.Fatal(e)
	}
	old.Close()
	if !m.IsConnected(1) || next.Generation() <= old.Generation() || handles[0].closed.Load() != 1 || handles[1].closed.Load() != 0 {
		t.Fatal("replacement lost identity")
	}
}
func TestProductAndImplementationCapabilitiesIntersect(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		return &handle{id: p.ID, caps: contract.Capabilities{Query: true, Backup: true}}, nil
	}))
	s, e := m.ConnectSession(context.Background(), profile(1), "")
	if e != nil {
		t.Fatal(e)
	}
	if s.Capabilities().Query || s.Capabilities().Backup || s.Capabilities().EditableTables {
		t.Fatal("unimplemented capability enabled")
	}
	query, e := contract.Require[contract.QueryProvider](s)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = query.ReserveQuery(4); !errors.Is(e, contract.ErrUnsupported) {
		t.Fatal(e)
	}
}
func TestSQLiteLeasesAreLazyIsolatedAndRevokedWithOwner(t *testing.T) {
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	defer m.Close()
	p := profile(1)
	p.Database = filepath.Join(t.TempDir(), "runtime.sqlite")
	original, e := m.ConnectSession(context.Background(), p, "")
	if e != nil {
		t.Fatal(e)
	}
	q, e := contract.Require[contract.QueryProvider](original)
	if e != nil {
		t.Fatal(e)
	}
	a, e := q.ReserveQuery(10)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Release()
	b, e := q.ReserveQuery(11)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Release()
	ctx := context.Background()
	if _, e = a.Execute(ctx, "main", "CREATE TEMP TABLE isolated(id INTEGER); INSERT INTO isolated VALUES(7)"); e != nil {
		t.Fatal(e)
	}
	if _, e = b.Execute(ctx, "main", "SELECT * FROM isolated"); e == nil {
		t.Fatal("physical SQL connections shared TEMP state")
	}
	replacement, e := a.Replace()
	if e != nil {
		t.Fatal(e)
	}
	defer replacement.Release()
	if _, e = a.Execute(ctx, "main", "SELECT 1"); e == nil {
		t.Fatal("old lease survived replacement")
	}
	if _, e = replacement.Execute(ctx, "main", "SELECT 1"); e != nil {
		t.Fatal(e)
	}
	if e = m.Disconnect(1); e != nil {
		t.Fatal(e)
	}
	if _, e = replacement.Execute(ctx, "main", "SELECT 1"); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal(e)
	}
	next, e := m.ConnectSession(ctx, p, "")
	if e != nil {
		t.Fatal(e)
	}
	if next.Generation() <= original.Generation() {
		t.Fatal("published generation regressed")
	}
	metadata, e := contract.Require[contract.MetadataProvider](original)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = metadata.Namespaces(ctx); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal(e)
	}
}

func TestTransactionTokenCannotCrossProfilesOrReplacement(t *testing.T) {
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	defer m.Close()
	ctx := context.Background()
	for _, id := range []int64{1, 2} {
		p := profile(id)
		p.Database = filepath.Join(t.TempDir(), "token.sqlite")
		if e := m.Connect(ctx, p, ""); e != nil {
			t.Fatal(e)
		}
	}
	one, two := m.Data(1).Transactions, m.Data(2).Transactions
	token, e := one.Begin(ctx, 1, "main", "items")
	if e != nil {
		t.Fatal(e)
	}
	if e = two.Commit(ctx, token); !errors.Is(e, connections.ErrInvalid) {
		t.Fatalf("foreign token dispatched: %v", e)
	}
	two.Close(token)
	if e = one.Rollback(ctx, token); e != nil {
		t.Fatalf("foreign close revoked owner transaction: %v", e)
	}
	token, e = one.Begin(ctx, 1, "main", "items")
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Disconnect(1); e != nil {
		t.Fatal(e)
	}
	replacement := profile(1)
	replacement.Database = filepath.Join(t.TempDir(), "replacement.sqlite")
	if e = m.Connect(ctx, replacement, ""); e != nil {
		t.Fatal(e)
	}
	if e = one.Commit(ctx, token); !errors.Is(e, contract.ErrRevoked) {
		t.Fatalf("old transaction survived replacement: %v", e)
	}
	if e = m.Data(1).Transactions.Commit(ctx, token); !errors.Is(e, connections.ErrInvalid) {
		t.Fatalf("replacement accepted old token: %v", e)
	}
	one.Close(token)
}
