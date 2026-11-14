package consul

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

var archiveLeaseRequest = contract.KVHTTPArchiveRequest{CompressedBytes: 1 << 20, ExpandedBytes: 2 << 20}

func TestHTTPArchiveLeasePurePlansOwnedReadersAndPrivateRetention(t *testing.T) {
	original := snapshotTestOriginal(t)
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(original) })
	lease, _ := s.ReserveKV(1)
	defer lease.Close()
	l := lease.(*documentLease)
	before := calls.Load()
	plan, err := l.PrepareHTTPArchive(archiveLeaseRequest)
	if err != nil || calls.Load() != before || plan.Scope().DocumentID != 1 || plan.Scope().Original.Namespace.ProfileID != 1 || plan.TargetURL() != s.cfg.Endpoint+"/v1/snapshot" {
		t.Fatal("archive preparation changed original scope or performed I/O", err)
	}
	foreign, _ := s.ReserveKV(2)
	defer foreign.Close()
	if _, _, err := foreign.(contract.KVHTTPArchiveCapturing).CaptureHTTPArchive(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) || calls.Load() != before {
		t.Fatal("foreign archive authority accepted")
	}
	receiptBudgetMu.Lock()
	baseline := s.retained
	receiptBudgetMu.Unlock()
	archive, result, err := l.CaptureHTTPArchive(context.Background(), plan)
	if err != nil || archive == nil || !result.SourceComplete || !result.Ended || result.Scope.DocumentID != 1 || result.OriginalBytes != uint64(len(original)) {
		t.Fatal("original archive capture incomplete", err)
	}
	defer archive.Close()
	if !reflect.DeepEqual(archive.Receipt(), result) {
		t.Fatal("returned receipt differs from retained original")
	}
	result.Metadata[0] = '!'
	result.Header["X-Consul-Index"][0] = "0"
	if archive.Receipt().Metadata[0] == '!' || archive.Receipt().Header["X-Consul-Index"][0] == "0" || l.LastResult().HTTP != nil {
		t.Fatal("private archive inspection aliases original or generic cache")
	}
	reader, _ := archive.OpenOriginal()
	second, _ := archive.OpenOriginal()
	raw, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(raw, original) {
		t.Fatal("original reader incomplete")
	}
	clear(raw)
	other, _ := io.ReadAll(second)
	if !bytes.Equal(other, original) {
		t.Fatal("readers share position or mutable bytes")
	}
	_ = reader.Close()
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed reader remained usable")
	}
	if _, _, err := l.CaptureHTTPArchive(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("original archive plan replayed")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if archive.Receipt().LocalRecordID == "" {
		t.Fatal("lease close erased caller-owned original")
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	_ = archive.Close()
	if _, err := second.Read(make([]byte, 1)); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("artifact close did not revoke original reader")
	}
	if _, err := archive.OpenOriginal(); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed source opened")
	}
	receiptBudgetMu.Lock()
	retained, reserved, owned := s.retained, s.reserved, l.archiveBytes()
	receiptBudgetMu.Unlock()
	if retained != baseline || reserved != 0 || owned != 0 {
		t.Fatal("original source reservation leaked")
	}
}

func TestHTTPArchiveLeaseFailedOriginalSurvivesPureRecoveryAndUnknownWrite(t *testing.T) {
	original := snapshotTestOriginal(t)
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "999999")
		_, _ = w.Write(original[:len(original)/2])
	})
	lease, _ := s.ReserveKV(1)
	l := lease.(*documentLease)
	defer l.Close()
	l.storeOwned(contract.KVResult{WriteOutcomeUnknown: true, HTTP: &contract.KVHTTPResult{Body: []byte("unknown-write-original")}})
	l.writeUnknown.Store(true)
	plan, _ := l.PrepareHTTPArchive(archiveLeaseRequest)
	archive, result, err := l.CaptureHTTPArchive(context.Background(), plan)
	if !errors.Is(err, contract.ErrKVLeaseLost) || archive == nil || !result.Attempted || !result.Ended || result.HTTPComplete || result.SourceComplete {
		t.Fatal("failed original response not retained", err)
	}
	defer archive.Close()
	before := calls.Load()
	recovery, err := s.RecoverKV(l)
	if err != nil || recovery.Lease == nil || calls.Load() != before || recovery.Unknown == nil || !bytes.Equal(recovery.Unknown.HTTP.Body, []byte("unknown-write-original")) {
		t.Fatal("recovery performed I/O or erased unknown write", err)
	}
	defer recovery.Lease.Close()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := archive.OpenOriginal()
	if err != nil {
		t.Fatal("retained source unavailable after recovery")
	}
	defer reader.Close()
	body, _ := io.ReadAll(reader)
	if !bytes.Equal(body, original[:len(original)/2]) || !reflect.DeepEqual(result, archive.Receipt()) {
		t.Fatal("original failed prefix changed on recovery")
	}
	write, _ := DocumentRequest(putRequest("original-write", []byte("value")))
	if _, err := recovery.Lease.Prepare(write); !errors.Is(err, contract.ErrKVWriteUnknown) {
		t.Fatal("recovery accepted an unauthorized write")
	}
}

func TestHTTPArchiveLeaseBudgetRejectionIsPredispatchAndCloseReleasesCredit(t *testing.T) {
	original := snapshotTestOriginal(t)
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(original) })
	lease, _ := s.ReserveKV(1)
	l := lease.(*documentLease)
	defer l.Close()
	plan, _ := l.PrepareHTTPArchive(archiveLeaseRequest)
	receiptBudgetMu.Lock()
	s.budget = s.retained + pendingReceiptBudget - 1
	receiptBudgetMu.Unlock()
	before := calls.Load()
	if a, _, err := l.CaptureHTTPArchive(context.Background(), plan); !errors.Is(err, ErrBounds) || a != nil || calls.Load() != before {
		t.Fatal("failed reservation connected or captured")
	}
	receiptBudgetMu.Lock()
	s.budget = sessionReceiptBudget
	receiptBudgetMu.Unlock()
	a, _, err := l.CaptureHTTPArchive(context.Background(), plan)
	if err != nil {
		t.Fatal("predispatch budget failure consumed plan", err)
	}
	defer a.Close()
	receiptBudgetMu.Lock()
	old := l.archiveBudget.retained
	l.archiveBudget.retained = documentReceiptBudget - pendingReceiptBudget + 1
	receiptBudgetMu.Unlock()
	if err := l.reserveReceipt(); !errors.Is(err, ErrBounds) {
		t.Fatal("ordinary request ignored original archive budget")
	}
	if err := l.reserveDestroyPlan(1); !errors.Is(err, ErrBounds) {
		t.Fatal("impact plan ignored original archive budget")
	}
	receiptBudgetMu.Lock()
	l.archiveBudget.retained = old
	receiptBudgetMu.Unlock()
}

func TestHTTPArchiveLeaseScopeModesAndCancellationStayExplicit(t *testing.T) {
	s, calls := documentSession(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid request dispatched") })
	lease, _ := s.ReserveKV(1)
	l := lease.(*documentLease)
	defer l.Close()
	before := calls.Load()
	for _, input := range []contract.KVHTTPArchiveRequest{{}, {CompressedBytes: ^uint64(0), ExpandedBytes: 1}, {CompressedBytes: 1, ExpandedBytes: 257 << 20}} {
		if _, err := l.PrepareHTTPArchive(input); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid original bound accepted")
		}
	}
	if _, err := snapshotArchiveTarget(Config{Consistency: "consistent"}); !errors.Is(err, ErrUnsupported) {
		t.Fatal("unmeasured consistency silently accepted")
	}
	plan, _ := l.PrepareHTTPArchive(archiveLeaseRequest)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if a, _, err := l.CaptureHTTPArchive(ctx, plan); !errors.Is(err, context.Canceled) || a != nil || calls.Load() != before {
		t.Fatal("cancelled lease capture dispatched")
	}
}

func TestHTTPArchiveLeaseRecoveryTransfersOwnedBudgetsWithoutIO(t *testing.T) {
	original := snapshotTestOriginal(t)
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(original) })
	lease, _ := s.ReserveKV(1)
	l := lease.(*documentLease)
	defer l.Close()
	p, _ := l.PrepareHTTPArchive(archiveLeaseRequest)
	a, originalReceipt, err := l.CaptureHTTPArchive(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	receiptBudgetMu.Lock()
	charge := l.archiveBytes()
	retained := s.retained
	receiptBudgetMu.Unlock()
	if charge <= 0 {
		t.Fatal("original source was not charged")
	}
	before := calls.Load()
	r, err := s.RecoverKV(l)
	if err != nil || calls.Load() != before {
		t.Fatal("same-session recovery performed I/O", err)
	}
	next := r.Lease.(*documentLease)
	defer next.Close()
	receiptBudgetMu.Lock()
	if next.archiveBytes() != charge || l.archiveBytes() != 0 || s.retained != retained {
		receiptBudgetMu.Unlock()
		t.Fatal("same-session recovery lost or duplicated original budget")
	}
	// A retained original must participate in the recovered document limit.
	oldRetained := next.archiveBudget.retained
	next.archiveBudget.retained = documentReceiptBudget - pendingReceiptBudget + 1
	receiptBudgetMu.Unlock()
	if err := next.reserveReceipt(); !errors.Is(err, ErrBounds) {
		t.Fatal("recovered document ignored original archive credit")
	}
	receiptBudgetMu.Lock()
	next.archiveBudget.retained = oldRetained
	receiptBudgetMu.Unlock()

	destination, err := OpenNativeSession(context.Background(), 1, s.cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	receiptBudgetMu.Lock()
	destinationBase := destination.retained
	destination.budget = destination.retained + charge - 1
	receiptBudgetMu.Unlock()
	before = calls.Load()
	if failed, err := destination.RecoverKV(next); !errors.Is(err, ErrBounds) || failed.Lease != nil || calls.Load() != before {
		t.Fatal("destination budget rejected after I/O or discarded original", err)
	}
	if !reflect.DeepEqual(a.Receipt(), originalReceipt) {
		t.Fatal("failed recovery changed retained original")
	}
	receiptBudgetMu.Lock()
	if next.archiveBytes() != charge || s.retained != retained || destination.retained != destinationBase {
		receiptBudgetMu.Unlock()
		t.Fatal("failed recovery moved source accounting")
	}
	destination.budget = sessionReceiptBudget
	receiptBudgetMu.Unlock()
	r, err = destination.RecoverKV(next)
	if err != nil || calls.Load() != before {
		t.Fatal("cross-session recovery performed I/O", err)
	}
	fresh := r.Lease.(*documentLease)
	defer fresh.Close()
	receiptBudgetMu.Lock()
	if fresh.archiveBytes() != charge || next.archiveBytes() != 0 || s.retained != retained-charge || destination.retained != destinationBase+charge {
		receiptBudgetMu.Unlock()
		t.Fatal("cross-session recovery lost or duplicated original source")
	}
	receiptBudgetMu.Unlock()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reader, _ := a.OpenOriginal()
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(body, original) || !reflect.DeepEqual(a.Receipt(), originalReceipt) {
		t.Fatal("original evidence changed when its physical session closed")
	}
	_ = a.Close()
	receiptBudgetMu.Lock()
	remaining, owned := destination.retained, fresh.archiveBytes()
	receiptBudgetMu.Unlock()
	if remaining != destinationBase || owned != 0 {
		t.Fatal("original close returned credit to the wrong session")
	}
}

type archiveBlockingBody struct {
	entered, release chan struct{}
	once             sync.Once
}

func (b *archiveBlockingBody) Read([]byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return 0, context.Canceled
}
func (b *archiveBlockingBody) Close() error { return nil }

type archiveTransport func(*http.Request) (*http.Response, error)

func (f archiveTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTTPArchiveLeaseCloseWaitsForOriginalExecutorAndRejectsBusy(t *testing.T) {
	s, _ := documentSession(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected dispatch") })
	lease, _ := s.ReserveKV(1)
	l := lease.(*documentLease)
	defer l.Close()
	if err := l.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := &archiveBlockingBody{entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(body.release) }) }
	defer release()
	l.client.http.Transport = archiveTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: body, Header: http.Header{}}, nil
	})
	p, _ := l.PrepareHTTPArchive(archiveLeaseRequest)
	type completion struct {
		archive contract.KVHTTPArchive
		result  contract.KVHTTPArchiveResult
		err     error
	}
	finished := make(chan completion, 1)
	go func() {
		a, r, err := l.CaptureHTTPArchive(context.Background(), p)
		finished <- completion{a, r, err}
	}()
	select {
	case <-body.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("original executor did not enter")
	}
	pending, _ := l.PrepareHTTPArchive(archiveLeaseRequest)
	if a, _, err := l.CaptureHTTPArchive(context.Background(), pending); !errors.Is(err, contract.ErrKVBusy) || a != nil {
		t.Fatal("busy original accepted another capture")
	}
	closed := make(chan error, 1)
	go func() { closed <- l.Close() }()
	select {
	case <-l.ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("original close did not cancel its executor")
	}
	// The executor deliberately ignores cancellation until release. Closing
	// cannot report completion or publish a receipt ahead of that acknowledgement.
	select {
	case <-closed:
		t.Fatal("close returned ahead of original acknowledgement")
	case <-finished:
		t.Fatal("capture published ahead of original acknowledgement")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	c := <-finished
	if c.archive == nil || !c.result.Ended || !c.result.Canceled || !c.result.Attempted || c.result.SourceComplete || !errors.Is(c.err, context.Canceled) {
		t.Fatal("cancelled original evidence was replaced", c.err)
	}
	defer c.archive.Close()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.archive.Receipt(), c.result) {
		t.Fatal("close changed transferred cancellation receipt")
	}
	if a, _, err := l.CaptureHTTPArchive(context.Background(), pending); !errors.Is(err, contract.ErrRevoked) || a != nil {
		t.Fatal("closed original accepted a pending plan")
	}
}
