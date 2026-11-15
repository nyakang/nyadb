package agentbridge

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
)

func testQueryConfig() QueryConfig {
	return QueryConfig{InitializeSQL: `SET SCHEMA "PUBLIC"`, DefaultSchema: "PUBLIC", MaxLeases: 2, MaxRows: 100, TimeoutSeconds: 5, Split: sqltext.SplitPortable}
}

func TestQueryPoolWaitsLocallyAndReleasesSharedCapacityAfterOriginalClose(t *testing.T) {
	c := helperClient(t, "query_pool", Options{})
	capacity := make(chan struct{}, 1)
	attempts := make(chan struct{}, 8)
	var releases atomic.Int32
	config := testQueryConfig()
	config.AcquireConnection = func(ctx context.Context) (func(), error) {
		attempts <- struct{}{}
		select {
		case capacity <- struct{}{}:
			return func() {
				raw, err := helperSession(t, c).Call(context.Background(), "pool_probe", nil)
				var state struct{ Opens, Closes int }
				if err != nil || json.Unmarshal(raw.Result, &state) != nil || state.Closes != int(releases.Load())+1 {
					t.Error("capacity released before original native close", err)
				}
				<-capacity
				releases.Add(1)
			}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	p, err := NewQueryPool(c, map[string]string{"password": "original"}, config)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	first, err := p.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 0 {
		t.Fatal("logical reservations acquired physical capacity")
	}
	if err := first.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-attempts
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- second.Preconnect(ctx) }()
	<-attempts
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) || errors.Is(err, contract.ErrSQLSessionLost) || !c.Alive() {
		t.Fatal("local admission cancellation retired an unrelated runtime", err)
	}
	if releases.Load() != 0 {
		t.Fatal("waiting document released another document's slot")
	}
	first.Release()
	if releases.Load() != 1 {
		t.Fatal("original release did not return shared capacity")
	}
	wait, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := second.Preconnect(wait); err != nil {
		t.Fatal("canceled local wait required reconnecting the original runtime", err)
	}
	second.Release()
	second.Release()
	if releases.Load() != 2 || len(capacity) != 0 {
		t.Fatal("shared capacity leaked or released twice")
	}
}

func TestQueryPoolFailedInitializationReturnsAdmissionOnce(t *testing.T) {
	c := helperClient(t, "query_pool", Options{})
	var releases atomic.Int32
	config := testQueryConfig()
	config.AcquireConnection = func(context.Context) (func(), error) {
		return func() { releases.Add(1) }, nil
	}
	config.Decoder = func(string, json.RawMessage) (contract.Cell, error) { return contract.Cell{}, ErrNativeResult }
	p, err := NewQueryPool(c, map[string]string{"password": "original"}, config)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	l, err := p.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Preconnect(context.Background()); !errors.Is(err, contract.ErrSQLSessionLost) || releases.Load() != 1 {
		t.Fatal("failed native initialization retained shared admission", err)
	}
	l.Release()
	l.Release()
	if releases.Load() != 1 || !c.Alive() {
		t.Fatal("failed initialization released capacity twice or retired the runtime")
	}
}

func TestQueryPoolReservationsDoNotDispatchAndRetireWithOriginalRuntime(t *testing.T) {
	c := helperClient(t, "query_pool", Options{})
	params := map[string]string{"password": "original"}
	p, err := NewQueryPool(c, params, testQueryConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	params["password"] = "changed"
	l, err := p.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	other, err := p.ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Release()
	if _, err = p.ReserveQuery(3); !errors.Is(err, contract.ErrSQLSessionLimit) {
		t.Fatal("original document reservation limit ignored")
	}
	probe := helperSession(t, c)
	r, err := probe.Call(context.Background(), "pool_probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Opens int `json:"opens"`
	}
	if json.Unmarshal(r.Result, &state) != nil || state.Opens != 1 {
		t.Fatal("reserving SQL documents dispatched native handshakes")
	}
	if err = l.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := l.Execute(context.Background(), "PUBLIC", "SELECT exact")
	if err != nil || result.Rows[0][0] != "9007199254740993" || result.NativeJSON == "" {
		t.Fatal("native scalar/result receipt lost", err)
	}
	raw := l.(interface{ OriginalQueryReceipts() []Receipt }).OriginalQueryReceipts()
	if len(raw) != 1 || !raw[0].ExecutorAcknowledged {
		t.Fatal("original complete executor receipt missing")
	}
	raw[0].Result[0] = ' '
	if l.(interface{ OriginalQueryReceipts() []Receipt }).OriginalQueryReceipts()[0].Result[0] != '{' {
		t.Fatal("receipt aliased the caller's snapshot")
	}
	_ = c.Close()
	if err = l.(contract.QueryLeaseStatus).CheckQueryLease(); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("original dead runtime admitted a document")
	}
	if _, err = l.Replace(); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("replacement reattached a lost original runtime")
	}
}

func TestQueryPoolKeepsCompletedWriteWhenResultSinkFails(t *testing.T) {
	c := helperClient(t, "query_pool", Options{})
	p, err := NewQueryPool(c, map[string]string{"password": "original"}, testQueryConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	l, err := p.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("destination write failed")
	result, err := l.ExecuteStream(context.Background(), "PUBLIC", "SELECT exact; SELECT later", func(_ int, _ []contract.Column, _ []contract.Cell) error { return failure })
	if !errors.Is(err, failure) || len(result.Steps) != 1 || result.Steps[0].State != "completed" || result.NativeJSON == "" {
		t.Fatal("sink error overwrote completed original execution", err)
	}
	receipts := l.(interface{ OriginalQueryReceipts() []Receipt }).OriginalQueryReceipts()
	if len(receipts) != 1 || !receipts[0].ExecutorAcknowledged {
		t.Fatal("sink error discarded original executor receipt")
	}
}

func TestQueryPoolSinkCopiesAndExplicitLimitsPreserveFullNativeReceipt(t *testing.T) {
	c := helperClient(t, "query_pool", Options{})
	config := testQueryConfig()
	config.MaxRows = 1
	p, err := NewQueryPool(c, map[string]string{"password": "original"}, config)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	l, err := p.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	result, err := l.ExecuteStream(context.Background(), "PUBLIC", "SELECT binary", func(_ int, columns []contract.Column, row []contract.Cell) error {
		columns[0].Name = "mutated"
		row[0].Bytes[0] = 1
		return nil
	})
	if err != nil || result.Sets[0].Columns[0].Name != "binary" || result.Sets[0].Rows[0][0].Bytes[0] != 0 || !result.Truncated {
		t.Fatal("sink copy or native limit lost", err)
	}
	if len(result.NativeJSON) < 65536 {
		t.Fatal("workbench truncation discarded complete original value")
	}
}
