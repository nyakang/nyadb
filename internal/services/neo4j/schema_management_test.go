package neo4j

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type schemaCountersTest struct{ fakeCounters }

func (schemaCountersTest) IndexesAdded() int { return 1 }

type schemaSummaryTest struct{ fakeSummary }

func (schemaSummaryTest) Counters() nd.Counters { return schemaCountersTest{} }

func schemaManagementTest(t *testing.T) (*schemaPreparationLease, *fakeDriver, *atomic.Int32) {
	t.Helper()
	s := fakeSessionTest(t)
	c, driver := schemaClientTest(t)
	writes := &atomic.Int32{}
	driver.session.run = func(_ context.Context, q string, _ map[string]any, _ ...func(*nd.TransactionConfig)) (nd.Result, error) {
		if strings.HasPrefix(q, "CREATE ") {
			writes.Add(1)
			return &fakeResult{keys: []string{}, summary: schemaSummaryTest{fakeSummary{agent: "Neo4j/5.26.21"}}}, nil
		}
		return emptySchemaResultTest(q), nil
	}
	s.openClient = func(context.Context, connections.Neo4jConfig, string) (*Client, error) { return c, nil }
	lease, e := s.ReserveGraphSchemaManagement(schemaObjectTest(s), 1)
	if e != nil {
		t.Fatal(e)
	}
	return lease.(*schemaPreparationLease), driver, writes
}
func TestSchemaManagementOriginalSingleSubmissionAndSeparateFailedVerification(t *testing.T) {
	l, driver, writes := schemaManagementTest(t)
	before, e := l.Read(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	prepared, e := l.Prepare(before, schemaDraftTest())
	if e != nil {
		t.Fatal(e)
	}
	r, e := l.Submit(context.Background(), prepared, contract.GraphSchemaConfirmation{})
	if e != nil || r.State != "completed" || !r.ReceiptValid || r.HasVerification || writes.Load() != 1 {
		t.Fatal("write receipt was not independent of verification", r.State, e)
	}
	r.Receipt.Summary.Counters["indexes_added"] = 9
	if _, e := l.Read(context.Background()); !errors.Is(e, contract.ErrGraphSchemaBusy) {
		t.Fatal("unresolved write allowed a new read/prepare", e)
	}
	if _, e := l.Submit(context.Background(), prepared, contract.GraphSchemaConfirmation{}); !errors.Is(e, contract.ErrGraphSchemaBusy) {
		t.Fatal("original write replayable", e)
	}
	r, e = l.Verify(context.Background())
	if e != nil || !r.HasVerification || r.Matched || r.State != "diverged" || r.Receipt.Summary.Counters["indexes_added"] != 1 || writes.Load() != 1 {
		t.Fatal("empty desired catalog became successful write/verification", e)
	}
	driver.session.run = func(context.Context, string, map[string]any, ...func(*nd.TransactionConfig)) (nd.Result, error) {
		return nil, &nd.Neo4jError{Code: "Neo.ClientError.Security.Forbidden"}
	}
	last, e := l.Verify(context.Background())
	if e == nil || !last.Receipt.Completed || !last.HasVerification || last.State != "diverged" || writes.Load() != 1 {
		t.Fatal("verification failure replaced original receipt/evidence", e)
	}
}
func TestSchemaManagementCanceledPreflightConsumesPlanWithoutNativeDDL(t *testing.T) {
	l, _, writes := schemaManagementTest(t)
	before, e := l.Read(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	p, e := l.Prepare(before, schemaDraftTest())
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, e := l.Submit(ctx, p, contract.GraphSchemaConfirmation{})
	if !errors.Is(e, context.Canceled) || r.State != "not_submitted" || r.Receipt.Attempted || writes.Load() != 0 {
		t.Fatal("canceled original request dispatched", e)
	}
	if _, e := l.Submit(context.Background(), p, contract.GraphSchemaConfirmation{}); !errors.Is(e, contract.ErrGraphSchemaChanged) {
		t.Fatal("canceled consumed plan reused", e)
	}
}
func TestSchemaManagementForgedPlanAndDropAcknowledgementNeverDispatch(t *testing.T) {
	l, _, writes := schemaManagementTest(t)
	before, e := l.Read(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	p, e := l.Prepare(before, schemaDraftTest())
	if e != nil {
		t.Fatal(e)
	}
	forged := &schemaPreparation{preview: p.Preview()}
	if _, e := l.Submit(context.Background(), forged, contract.GraphSchemaConfirmation{}); !errors.Is(e, contract.ErrGraphSchemaChanged) {
		t.Fatal("caller preview became authorization", e)
	}
	original := p.(*schemaPreparation)
	// Exercise acknowledgement before preflight, independently of an existing index.
	original.preview.NonAtomicDrop = true
	if _, e := l.Submit(context.Background(), p, contract.GraphSchemaConfirmation{Name: "owned"}); !errors.Is(e, contract.ErrGraphSchemaConfirmation) {
		t.Fatal("DROP without native-race acknowledgement accepted", e)
	}
	if original.used.Load() || writes.Load() != 0 {
		t.Fatal("missing confirmation consumed or dispatched plan")
	}
}

func TestSchemaManagementCompletedCounterMissingNeverBecomesConfirmed(t *testing.T) {
	l, driver, writes := schemaManagementTest(t)
	original := driver.session.run
	driver.session.run = func(ctx context.Context, q string, p map[string]any, options ...func(*nd.TransactionConfig)) (nd.Result, error) {
		if strings.HasPrefix(q, "CREATE ") {
			writes.Add(1)
			return &fakeResult{keys: []string{}, summary: fakeSummary{agent: "Neo4j/5.26.21"}}, nil
		}
		return original(ctx, q, p, options...)
	}
	before, e := l.Read(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	p, e := l.Prepare(before, schemaDraftTest())
	if e != nil {
		t.Fatal(e)
	}
	r, e := l.Submit(context.Background(), p, contract.GraphSchemaConfirmation{})
	if !errors.Is(e, contract.ErrGraphSchemaReceipt) || r.State != "receipt_incomplete" || r.ReceiptValid || !r.Receipt.Completed {
		t.Fatal("missing counter became valid completion", e)
	}
	if _, e := l.Read(context.Background()); !errors.Is(e, contract.ErrGraphSchemaBusy) {
		t.Fatal("incomplete completion replayable", e)
	}
	r, e = l.Verify(context.Background())
	if e != nil || r.State != "receipt_incomplete" || r.ReceiptValid || writes.Load() != 1 {
		t.Fatal("verification invented completion", e)
	}
}

func TestSchemaManagementCloseBlocksLateOriginalVerificationReads(t *testing.T) {
	l, _, _ := schemaManagementTest(t)
	before, e := l.Read(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	p, e := l.Prepare(before, schemaDraftTest())
	if e != nil {
		t.Fatal(e)
	}
	if _, e := l.Submit(context.Background(), p, contract.GraphSchemaConfirmation{}); e != nil {
		t.Fatal(e)
	}
	if e := l.client.Close(); e != nil {
		t.Fatal(e)
	}
	c, driver := schemaClientTest(t)
	started, release := make(chan struct{}), make(chan struct{})
	l.s.openClient = func(context.Context, connections.Neo4jConfig, string) (*Client, error) {
		close(started)
		<-release
		return c, nil
	}
	result := make(chan contract.GraphSchemaManagementResult, 1)
	errs := make(chan error, 1)
	go func() { r, e := l.Verify(context.Background()); result <- r; errs <- e }()
	<-started
	closed := make(chan error, 1)
	go func() { closed <- l.Close() }()
	<-l.ctx.Done()
	close(release)
	r := <-result
	if e := <-errs; e == nil || !r.Receipt.Completed {
		t.Fatal("late verification discarded original receipt or published", e)
	}
	if e := <-closed; e != nil {
		t.Fatal(e)
	}
	if driver.session.runs.Load() != 0 || driver.closes.Load() != 1 || !l.s.Alive() {
		t.Fatal("late verification read after close or leaked captured client")
	}
}
