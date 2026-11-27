package prestosql

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
)

type nativeReadBarrier struct {
	role, method    string
	arrived, resume chan struct{}
	once            sync.Once
}

func (b *nativeReadBarrier) release() { b.once.Do(func() { close(b.resume) }) }

func TestPrestoSQLPrivateReadNativeCancellationRefreshAndClose(t *testing.T) {
	f, p, c, e := nativeProvider(t)
	observe := p.Observe
	var pending atomic.Pointer[nativeReadBarrier]
	p.Observe = func(method string, receipt jdbcplugin.Receipt) {
		observe(method, receipt)
		b := pending.Load()
		if b != nil && b.role == "data-field-" {
			// A new JDBC process first runs a version probe. The barrier must
			// hold the generated field response, never that handshake query.
			result, err := decodeResult(receipt)
			if err != nil || len(result.columns) != 1 || result.columns[0] != "__nyadb_field" {
				return
			}
		}
		if b != nil && strings.HasPrefix(method, b.role) && strings.HasSuffix(method, "/"+b.method) && pending.CompareAndSwap(b, nil) {
			close(b.arrived)
			<-b.resume
		}
	}
	s, q, ctx := nativeDataSession(t, p, c)
	queryNative(t, q, "", `CREATE TABLE memory.default.read_lifecycle66 AS SELECT n AS id, rpad('',2049,'猫') AS note FROM UNNEST(sequence(1,3)) t(n)`)
	l, r, label := nativeDataDocument(t, s, "read_lifecycle66")
	page, err := r.Loader(ctx, 65, label, "read_lifecycle66", 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	arm := func(role, method string) *nativeReadBarrier {
		b := &nativeReadBarrier{role: role, method: method, arrived: make(chan struct{}), resume: make(chan struct{})}
		t.Cleanup(b.release)
		pending.Store(b)
		return b
	}
	wait := func(b *nativeReadBarrier) {
		t.Helper()
		select {
		case <-b.arrived:
		case <-time.After(45 * time.Second):
			t.Fatal("original read acknowledgement did not arrive")
		}
	}
	await := func(done <-chan error) error {
		t.Helper()
		select {
		case err := <-done:
			return err
		case <-time.After(30 * time.Second):
			t.Fatal("original read did not settle")
			return nil
		}
	}
	// Cancellation after a native cursor ACK must consume no row, close that
	// cursor and preserve the independent original page process.
	b := arm("data-export-", "executeQueryPage")
	active, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	var consumed atomic.Int32
	go func() {
		done <- r.Exporter(active, 65, label, "read_lifecycle66", contract.DataOptions{}, nil, func([]contract.DataValue) error { consumed.Add(1); return nil })
	}()
	wait(b)
	cancel()
	b.release()
	if err = await(done); !errors.Is(err, context.Canceled) || consumed.Load() != 0 {
		t.Fatal("cancelled cursor published rows", err, consumed.Load())
	}
	b = arm("data-field-", "executeQuery")
	active, cancelField := context.WithCancel(ctx)
	defer cancelField()
	go func() {
		_, err := r.Chunk(active, 65, label, "read_lifecycle66", "note", *page.RowRefs[0], 0, 1024)
		done <- err
	}()
	wait(b)
	cancelField()
	b.release()
	if err = await(done); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled field published data", err)
	}
	// An actual field response is parked while a new page replaces its grant.
	b = arm("data-field-", "executeQuery")
	go func() {
		_, err := r.Chunk(ctx, 65, label, "read_lifecycle66", "note", *page.RowRefs[0], 0, 1024)
		done <- err
	}()
	wait(b)
	fresh, err := r.Loader(ctx, 65, label, "read_lifecycle66", 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	b.release()
	if err = await(done); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("late field survived page replacement", err)
	}
	// Close waits for the captured executor before releasing caches and leases.
	b = arm("data-field-", "executeQuery")
	go func() {
		_, err := r.Chunk(ctx, 65, label, "read_lifecycle66", "note", *fresh.RowRefs[0], 0, 1024)
		done <- err
	}()
	wait(b)
	closed := make(chan error, 1)
	go func() { closed <- l.Close() }()
	select {
	case <-l.ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("document close did not revoke requests")
	}
	select {
	case err := <-closed:
		t.Fatal("close passed original executor", err)
	default:
	}
	b.release()
	if err = await(done); err == nil {
		t.Fatal("closed document published late field")
	}
	if err = await(closed); err != nil {
		t.Fatal(err)
	}
	if len(l.grants) != 0 {
		t.Fatal("close retained observations")
	}
	_, recovery, _ := nativeDataDocument(t, s, "read_lifecycle66")
	recovered, err := recovery.Loader(ctx, 65, label, "read_lifecycle66", 0, 3)
	if err != nil || len(recovered.Rows) != 3 || !s.Alive() {
		t.Fatal("read cancellation retired original session", err)
	}
	e.mu.Lock()
	cursorClosed := 0
	for _, item := range e.receipts {
		method := item["method"].(string)
		receipt := item["receipt"].(jdbcplugin.Receipt)
		if strings.HasPrefix(method, "data-export-") && strings.HasSuffix(method, "/closeQuerySession") && receipt.ExecutorAcknowledged && string(receipt.Result) == `{"ok":true}` {
			cursorClosed++
		}
	}
	e.mu.Unlock()
	if cursorClosed != 1 {
		t.Fatal("cancelled original cursor not closed", cursorClosed)
	}
	providerProof(t, f, "data-read-lifecycle.json", map[string]any{"cancelled_export_rows": consumed.Load(), "original_cursor_closed": cursorClosed, "cancelled_field_refused": true, "late_field_refused_after_refresh": true, "close_waited_for_original_executor": true, "closed_grants_released": true, "explicit_recovery_page": recovered})
	queryNative(t, q, "", `DROP TABLE memory.default.read_lifecycle66`)
	_ = s.Close()
	if err = p.Store.Remove(ctx, "prestosql", c.DriverVersion); err != nil {
		t.Fatal(err)
	}
}
