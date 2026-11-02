package clickhouse

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type blockingNativeConn struct {
	driver.Conn
	entered chan struct{}
	closed  atomic.Int32
}

func (c *blockingNativeConn) Exec(ctx context.Context, _ string, _ ...any) error {
	close(c.entered)
	<-ctx.Done()
	return ctx.Err()
}
func (c *blockingNativeConn) Close() error { c.closed.Add(1); return nil }

func TestClosingOriginalSessionCancelsOwningLeaseAndWaitsForAcknowledgement(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	metadata := &blockingNativeConn{}
	s := &Session{profile: connections.Profile{ID: 1, Kind: connections.ClickHouse, Database: "default"}, conn: metadata, ctx: ctx, cancel: cancel, leases: map[*queryLease]bool{}}
	lease, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	l := lease.(*queryLease)
	native := &blockingNativeConn{entered: make(chan struct{})}
	l.conn = native
	completed := make(chan error, 1)
	go func() {
		_, err := l.Execute(context.Background(), "default", "ALTER TABLE t DELETE WHERE id=1")
		completed <- err
	}()
	select {
	case <-native.entered:
	case <-time.After(time.Second):
		t.Fatal("mutation never entered")
	}
	closed := make(chan struct{})
	go func() { _ = s.Close(); close(closed) }()
	select {
	case err = <-completed:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, contract.ErrSQLSessionLost) {
			t.Fatal("unacknowledged write described as safe")
		}
	case <-time.After(time.Second):
		t.Fatal("close did not cancel captured operation")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not settle")
	}
	l.Release()
	_ = s.Close()
	if native.closed.Load() != 1 || metadata.closed.Load() != 1 || s.Alive() {
		t.Fatal("resources closed twice or original session stayed alive")
	}
	if _, err = l.Replace(); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed original profile silently recovered")
	}
}
