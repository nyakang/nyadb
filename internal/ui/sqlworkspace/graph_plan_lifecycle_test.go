package sqlworkspace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/queryplan"
)

type nativePlanStatusLease struct {
	*graphTestLease
	status error
}

func (l *nativePlanStatusLease) CheckQueryLease() error { return l.status }

func TestNativePlanParkedCompletionRetainsOriginalReceiptAcrossRevocation(t *testing.T) {
	active := true
	calls := 0
	l := &nativePlanStatusLease{graphTestLease: &graphTestLease{testLease: &testLease{}, graph: func(context.Context, contract.GraphQueryRequest) (contract.GraphResult, error) {
		calls++
		return nativePlanUITestResult().Graph.Clone(), nil
	}}}
	s := New(Dependencies{Runtime: func() Runtime { return &testRuntime{} }, Connected: func(int64) bool { return true }, Active: func(*Document) bool { return active }, QueryDialect: func(int64) string { return "neo4j" }, PlanParser: func(int64) func(contract.SQLResult) (*contract.ExecutionPlan, error) {
		return func(r contract.SQLResult) (*contract.ExecutionPlan, error) { return queryplan.Parse("neo4j", r) }
	}})
	defer s.CloseAll()
	d := s.Open(1, "PROFILE RETURN $exact", 85, "neo4j", false)
	d.lease = l
	s.Run(nil, d)
	active = false
	deadline := time.Now().Add(3 * time.Second)
	for len(d.results) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(d.results) != 1 {
		t.Fatal("original completion did not arrive")
	}
	s.Consume()
	if !d.Loading() || d.Result() != nil || d.plan != nil {
		t.Fatal("parked plan published")
	}
	l.status = contract.ErrRevoked
	active = true
	s.Consume()
	if d.Loading() || !d.NeedsRecovery() || d.plan != nil || d.planError == "" || d.Result() == nil || !d.Result().Graph.Completed || d.Result().Graph.Uncertain || d.Result().Graph.Summary.Plan.Parameters[0].Value.Text != "9223372036854775807" {
		t.Fatal("replacement erased confirmed original receipt or admitted stale plan", d.Error())
	}
	raw, err := nativePlanJSON(d)
	if err != nil || !strings.Contains(raw, "native-bookmark") {
		t.Fatal("original evidence unavailable", err)
	}
	s.Run(nil, d)
	if d.Loading() || calls != 1 {
		t.Fatal("revoked plan replayed")
	}
}

func TestNativePlanCancelWaitsForOriginalExecutorAndConfirmedReceipt(t *testing.T) {
	started, ack := make(chan struct{}), make(chan struct{})
	l := &nativePlanStatusLease{graphTestLease: &graphTestLease{testLease: &testLease{}, graph: func(ctx context.Context, _ contract.GraphQueryRequest) (contract.GraphResult, error) {
		close(started)
		<-ctx.Done()
		<-ack
		return nativePlanUITestResult().Graph.Clone(), errors.Join(ctx.Err(), contract.ErrSQLSessionLost)
	}}}
	s := New(Dependencies{Runtime: func() Runtime { return &testRuntime{} }, Connected: func(int64) bool { return true }, QueryDialect: func(int64) string { return "neo4j" }})
	defer s.CloseAll()
	d := s.Open(1, "PROFILE RETURN $exact", 85, "neo4j", false)
	d.lease = l
	s.Run(nil, d)
	awaitSignal(t, started)
	s.RequestCancel(d)
	s.Consume()
	if !d.Loading() || !d.CancelRequested() || d.result != nil {
		t.Fatal("cancel settled before executor acknowledgement")
	}
	close(ack)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	if !d.NeedsRecovery() || d.Result() == nil || !d.Result().Graph.Completed || d.Result().Graph.Uncertain {
		t.Fatal("cancellation changed confirmed original completion")
	}
}
