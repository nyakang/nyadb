package kvworkspace

import (
	"bytes"
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type fakeRenewalPlan struct {
	owner   *fakeRenewalLease
	request contract.KVRenewalRequest
}

func (p *fakeRenewalPlan) Snapshot() contract.KVRenewalRequest { return p.request }

type fakeRenewalLease struct {
	scope                              contract.KVScope
	closed, lost                       atomic.Bool
	starts, connects, clears, prepares atomic.Int32
	start                              func(context.Context, contract.KVRenewalPreparation) (contract.KVRenewalRun, error)
}

func (l *fakeRenewalLease) Scope() contract.KVScope { return l.scope.Clone() }
func (l *fakeRenewalLease) CheckKVLease() error {
	if l.closed.Load() || l.lost.Load() {
		return contract.ErrRevoked
	}
	return nil
}
func (l *fakeRenewalLease) Prepare(r contract.KVRenewalRequest) (contract.KVRenewalPreparation, error) {
	l.prepares.Add(1)
	return &fakeRenewalPlan{l, r}, l.CheckKVLease()
}
func (l *fakeRenewalLease) Preconnect(context.Context) error {
	l.connects.Add(1)
	return l.CheckKVLease()
}
func (l *fakeRenewalLease) Start(ctx context.Context, p contract.KVRenewalPreparation) (contract.KVRenewalRun, error) {
	plan, ok := p.(*fakeRenewalPlan)
	if !ok || plan.owner != l {
		return nil, contract.ErrKVPlanUsed
	}
	l.starts.Add(1)
	return l.start(ctx, p)
}
func (l *fakeRenewalLease) ClearReceipts() error { l.clears.Add(1); return nil }
func (l *fakeRenewalLease) Close() error         { l.closed.Store(true); return nil }

type fakeRenewalProvider struct {
	lease              *fakeRenewalLease
	reserves, recovers atomic.Int32
	recover            func(contract.KVRenewalLease) (contract.KVRenewalRecovery, error)
}

func (p *fakeRenewalProvider) KVScope() contract.KVScope { return p.lease.Scope() }
func (p *fakeRenewalProvider) ReserveKVRenewal(uint64) (contract.KVRenewalLease, error) {
	p.reserves.Add(1)
	return p.lease, nil
}
func (p *fakeRenewalProvider) RecoverKVRenewal(l contract.KVRenewalLease) (contract.KVRenewalRecovery, error) {
	p.recovers.Add(1)
	return p.recover(l)
}

type fakeRenewalRun struct {
	request       contract.KVRenewalRequest
	mu            sync.Mutex
	batches       []contract.KVRenewalBatch
	terminal      contract.KVRenewalTerminal
	ended         bool
	stops, closes atomic.Int32
	stop          func()
}

func (r *fakeRenewalRun) Request() contract.KVRenewalRequest { return r.request }
func (r *fakeRenewalRun) Poll() contract.KVRenewalBatch {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.batches) > 0 {
		b := r.batches[0]
		r.batches = r.batches[1:]
		return b.Clone()
	}
	return contract.KVRenewalBatch{Request: r.request, Ended: r.ended, Terminal: r.terminal}
}
func (r *fakeRenewalRun) Stop(context.Context) error {
	r.stops.Add(1)
	if r.stop != nil {
		r.stop()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ended {
		r.ended = true
		r.terminal.Reason = "stopped"
	}
	return nil
}
func (r *fakeRenewalRun) Close() error { r.closes.Add(1); return nil }
func (r *fakeRenewalRun) push(b contract.KVRenewalBatch) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b.Request = r.request
	for _, v := range b.Receipts {
		r.terminal.Attempts = v.Sequence
		if v.Completed {
			r.terminal.Acknowledged++
		}
	}
	if b.Ended {
		r.ended = true
		r.terminal = b.Terminal
	}
	r.batches = append(r.batches, b.Clone())
}
func renewalState(t *testing.T) (*State, *Document, *fakeRenewalLease, *fakeRenewalProvider) {
	s, d, _ := testState(t)
	l := &fakeRenewalLease{scope: testScope()}
	p := &fakeRenewalProvider{lease: l}
	s.deps.RenewalProviderFor = func(id int64) (contract.KVRenewalProvider, error) {
		if id != 7 {
			t.Error("foreign renewal profile")
		}
		return p, nil
	}
	s.ChooseMethod(d, RenewalMethod)
	d.Editor.Buffer.Set(`{"ID":"9007199254740993"}`)
	return s, d, l, p
}
func renewalInitial(p contract.KVRenewalPreparation) *fakeRenewalRun {
	r := &fakeRenewalRun{request: p.Snapshot()}
	r.push(contract.KVRenewalBatch{Receipts: []contract.KVRenewalReceipt{{Sequence: 1, Attempted: true, Completed: true, LeaseID: r.request.LeaseID, TTL: 3, Native: []byte{0, 255}, JSON: `{"ID":"9007199254740993","TTL":"3"}`}}})
	return r
}
func TestKVRenewalExactSignedIDsAndRequestFilesRequireSeparateMethod(t *testing.T) {
	for _, text := range []string{`{"ID":"9223372036854775807"}`, `{"id":-9223372036854775808}`, `{"ID":9007199254740993}`} {
		r, err := ParseRenewalRequest(text)
		if err != nil || r.LeaseID == 0 {
			t.Fatal("native ID narrowed/rejected", text, err)
		}
		input := contract.KVRequest{Method: RenewalMethod, JSON: text}
		raw, err := EncodeRequest(input)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeRequest(raw)
		if err != nil || decoded != input {
			t.Fatal("file replayed/narrowed input", err)
		}
	}
	for _, text := range []string{`{}`, `{"ID":0}`, `{"ID":"0"}`, `{"ID":1.1}`, `{"ID":1e1}`, `{"ID":"9223372036854775808"}`, `{"ID":null}`, `{"ID":1,"id":1}`, `{"ID":1,"ttl":3}`, `{"ID":1,"ID":2}`} {
		if _, err := ParseRenewalRequest(text); err == nil {
			t.Fatal("invalid continuous authorization", text)
		}
	}
	for _, m := range Methods() {
		if m.Path == RenewalMethod {
			t.Fatal("renewal published without optional provider")
		}
	}
	if draftFilename(RenewalMethod) != "LeaseRenewal.nyakv" {
		t.Fatal("missing independent request file")
	}
}
func TestKVRenewalConfirmationCapturesOriginalTextIDLeaseAndPublication(t *testing.T) {
	s, d, l, _ := renewalState(t)
	s.Run(d)
	p, ok := d.RenewalConfirmation()
	if !ok || p.LeaseID != 9007199254740993 || l.starts.Load() != 0 || l.connects.Load() != 0 {
		t.Fatal("prepare performed I/O")
	}
	s.ConfirmRenewal(d, false)
	if d.Busy() {
		t.Fatal("unchecked continuous run dispatched")
	}
	d.Editor.Buffer.Set(`{"ID":"9007199254740994"}`)
	s.ConfirmRenewal(d, true)
	if d.Busy() || d.renewalConfirmation != nil || l.starts.Load() != 0 {
		t.Fatal("changed ID confirmed")
	}
	s.Run(d)
	l.lost.Store(true)
	s.ConfirmRenewal(d, true)
	if d.Busy() || d.renewalConfirmation != nil || !d.NeedsRecovery() {
		t.Fatal("replaced lease used old confirmation")
	}
}
func TestKVRenewalParkedPacketsStopWaitAndKnownReceiptDisposal(t *testing.T) {
	s, d, l, _ := renewalState(t)
	var run *fakeRenewalRun
	l.start = func(_ context.Context, p contract.KVRenewalPreparation) (contract.KVRenewalRun, error) {
		run = renewalInitial(p)
		return run, nil
	}
	s.Run(d)
	s.ConfirmRenewal(d, true)
	eventEventually(t, func() bool { s.Consume(); return renewalAcknowledged(d.renewalReceipt) == 1 })
	authored := d.Request()
	active := true
	s.deps.Active = func(*Document) bool { return active }
	active = false
	run.push(contract.KVRenewalBatch{Receipts: []contract.KVRenewalReceipt{{Sequence: 2, Attempted: true, Completed: true, LeaseID: run.request.LeaseID, TTL: 3, Native: []byte{0, 255, 7}}}})
	time.Sleep(50 * time.Millisecond)
	s.Consume()
	if len(d.renewalReceipt.Batch.Receipts) != 1 {
		t.Fatal("parked completion consumed")
	}
	s.SetMethod(d, DefaultRequest().Method)
	if d.Request() != authored {
		t.Fatal("active original authorization changed method")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	run.stop = func() { once.Do(func() { close(entered) }); <-release }
	s.StopRenewal(d)
	await(t, entered)
	if !d.Busy() || !d.CancelRequested() {
		t.Fatal("stop completed before original acknowledgement")
	}
	close(release)
	op := d.renewalJob
	await(t, op.finished)
	s.Consume()
	if d.renewalJob != op {
		t.Fatal("parked terminal consumed")
	}
	active = true
	consume(t, s, d)
	if !d.renewalReceipt.Batch.Ended || d.renewalReceipt.Batch.Terminal.WriteOutcomeUnknown || len(d.renewalReceipt.Batch.Receipts) != 2 || d.renewalReceipt.Authored != authored {
		t.Fatal("original stopped receipts changed")
	}
	s.ClearRenewalReceipt(d)
	consume(t, s, d)
	if d.renewalReceipt != nil || l.clears.Load() != 1 {
		t.Fatal("settled receipt disposal failed")
	}
}
func TestKVRenewalUnknownIsIndependentAndBlocksAllDocumentWritesAfterRecovery(t *testing.T) {
	s, d, l, p := renewalState(t)
	l.start = func(_ context.Context, plan contract.KVRenewalPreparation) (contract.KVRenewalRun, error) {
		r := &fakeRenewalRun{request: plan.Snapshot()}
		r.push(contract.KVRenewalBatch{Receipts: []contract.KVRenewalReceipt{{Sequence: 1, Attempted: true, WriteOutcomeUnknown: true, LeaseID: r.request.LeaseID, Native: []byte{0, 255}}}, Ended: true, Terminal: contract.KVRenewalTerminal{Reason: "interrupted", Attempts: 1, WriteOutcomeUnknown: true}})
		return r, errors.New("actual original acknowledgement lost")
	}
	s.Run(d)
	s.ConfirmRenewal(d, true)
	consume(t, s, d)
	original, ok := d.UnknownRenewalReceipt()
	if !ok || !original.Batch.Terminal.WriteOutcomeUnknown {
		t.Fatal("lost renewal reclassified")
	}
	l.lost.Store(true)
	s.Consume()
	fresh := &fakeRenewalLease{scope: testScope()}
	fresh.scope.Generation++
	p.lease = fresh
	p.recover = func(old contract.KVRenewalLease) (contract.KVRenewalRecovery, error) {
		old.Close()
		return contract.KVRenewalRecovery{Lease: fresh, Batch: contract.KVRenewalBatch{Request: original.Batch.Request, Ended: true, Terminal: original.Batch.Terminal}}, nil
	}
	s.Recover(d)
	consume(t, s, d)
	if d.NeedsRecovery() || fresh.starts.Load() != 0 || fresh.connects.Load() != 0 || d.renewalUnknown == nil {
		t.Fatal("recovery resumed/reclassified run")
	}
	s.ClearRenewalReceipt(d)
	if d.Busy() || fresh.clears.Load() != 0 {
		t.Fatal("clear erased unknown barrier")
	}
	s.SetMethod(d, "/etcdserverpb.KV/Range")
	s.Run(d)
	consume(t, s, d)
	if d.last == nil || d.renewalUnknown == nil || !bytes.Equal(d.renewalUnknown.Batch.Receipts[0].Native, original.Batch.Receipts[0].Native) {
		t.Fatal("read replaced unknown renewal")
	}
	s.SetMethod(d, "/etcdserverpb.KV/Put")
	s.Run(d)
	if d.confirmation != nil || d.Busy() {
		t.Fatal("native write bypassed unknown renewal")
	}
	s.SetMethod(d, RenewalMethod)
	s.Run(d)
	if d.renewalConfirmation != nil || fresh.prepares.Load() != 0 {
		t.Fatal("renewal bypassed unknown barrier")
	}
	d.unknown = &Receipt{Result: contract.KVResult{WriteOutcomeUnknown: true, Native: []byte{7}}}
	d.renewalUnknown = nil
	d.renewalReceipt = nil
	s.Run(d)
	if d.renewalConfirmation != nil {
		t.Fatal("renewal bypassed one-shot unknown")
	}
}
func TestKVRenewalCloseCancelsPendingFirstAttemptAndWaitsWithoutUsingReplacement(t *testing.T) {
	s, d, l, _ := renewalState(t)
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	l.start = func(ctx context.Context, p contract.KVRenewalPreparation) (contract.KVRenewalRun, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		r := &fakeRenewalRun{request: p.Snapshot(), ended: true, terminal: contract.KVRenewalTerminal{WriteOutcomeUnknown: true}}
		return r, ctx.Err()
	}
	s.Run(d)
	s.ConfirmRenewal(d, true)
	await(t, entered)
	s.StopRenewal(d)
	await(t, canceled)
	if !d.Busy() {
		t.Fatal("pending first stop settled early")
	}
	close(release)
	consume(t, s, d)
	if d.renewalUnknown == nil {
		t.Fatal("pending first stop lost original unknown outcome")
	}
	s.Close(d)
	if d.Editor != nil || d.renewalLease != nil || d.renewalReceipt != nil || d.renewalUnknown != nil || !l.closed.Load() {
		t.Fatal("close retained resources")
	}
}
func TestKVRenewalReceiptArchivePreservesUnknownNativeFieldsAndExactFullCapture(t *testing.T) {
	s, d, _, _ := renewalState(t)
	native := bytes.Repeat([]byte{0, 255, 7}, 24000)
	d.renewalReceipt = &RenewalReceipt{Scope: testScope(), Authored: d.Request(), Batch: contract.KVRenewalBatch{Request: contract.KVRenewalRequest{LeaseID: math.MaxInt64}, Receipts: []contract.KVRenewalReceipt{{Sequence: math.MaxUint64, Native: native, TTL: math.MaxInt64, LeaseID: math.MaxInt64, WriteOutcomeUnknown: true}}, Ended: true, Terminal: contract.KVRenewalTerminal{WriteOutcomeUnknown: true, Attempts: math.MaxUint64}}}
	raw, err := s.RenewalRecord(d)
	if err != nil || !strings.Contains(string(raw), `"9223372036854775807"`) || !strings.Contains(string(raw), `"18446744073709551615"`) {
		t.Fatal("complete archive narrowed native values", err)
	}
	d.renewalReceipt.preview = renewalPreview(d.renewalReceipt.Batch)
	if len(s.RenewalPreview(d)) > previewLimit+10 || strings.Contains(s.RenewalPreview(d), "AP8HAP8H") {
		t.Fatal("preview included complete wire bytes")
	}
	d.renewalUnknown = d.renewalReceipt
	d.saved = d.Request()
	if !s.GuardWorkspaceClose(func(ui.Services) {}) {
		t.Fatal("unknown renewal bypassed workspace leave guard")
	}
	s.DecideLeave(ui.Services{}, true)
	if s.GuardLeave(d, func(ui.Services) {}) {
		t.Fatal("explicit discard decision lost")
	}
	d.renewalReceipt = &RenewalReceipt{}
	if !s.GuardLeave(d, func(ui.Services) {}) {
		t.Fatal("new receipt reused old leave permission")
	}
}
