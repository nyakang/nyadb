package kvworkspace

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type fakeEventsLease struct {
	scope                               contract.KVScope
	closed, lost                        atomic.Bool
	starts, histories, connects, clears atomic.Int32
	start                               func(context.Context, contract.KVWatchRequest) (contract.KVWatchRun, error)
	history                             func(context.Context, contract.KVHistoryRequest) (contract.KVHistoryResult, error)
	clear                               func() error
}

func (l *fakeEventsLease) Scope() contract.KVScope { return l.scope.Clone() }
func (l *fakeEventsLease) CheckKVLease() error {
	if l.closed.Load() || l.lost.Load() {
		return contract.ErrRevoked
	}
	return nil
}
func (l *fakeEventsLease) Preconnect(context.Context) error {
	l.connects.Add(1)
	return l.CheckKVLease()
}
func (l *fakeEventsLease) StartWatch(ctx context.Context, r contract.KVWatchRequest) (contract.KVWatchRun, error) {
	l.starts.Add(1)
	return l.start(ctx, r)
}
func (l *fakeEventsLease) History(ctx context.Context, r contract.KVHistoryRequest) (contract.KVHistoryResult, error) {
	l.histories.Add(1)
	return l.history(ctx, r)
}
func (*fakeEventsLease) LastHistory() *contract.KVHistoryResult { return nil }
func (l *fakeEventsLease) ClearReceipts() error {
	l.clears.Add(1)
	if l.clear != nil {
		return l.clear()
	}
	return nil
}
func (l *fakeEventsLease) Close() error { l.closed.Store(true); return nil }

type fakeEventsProvider struct {
	lease              *fakeEventsLease
	reserves, recovers atomic.Int32
	recover            func(contract.KVEventsLease) (contract.KVEventsRecovery, error)
}

func (p *fakeEventsProvider) KVScope() contract.KVScope { return p.lease.Scope() }
func (p *fakeEventsProvider) ReserveKVEvents(uint64) (contract.KVEventsLease, error) {
	p.reserves.Add(1)
	return p.lease, nil
}
func (p *fakeEventsProvider) RecoverKVEvents(old contract.KVEventsLease) (contract.KVEventsRecovery, error) {
	p.recovers.Add(1)
	return p.recover(old)
}

type fakeEventRun struct {
	request                   contract.KVWatchRequest
	mu                        sync.Mutex
	batches                   []contract.KVWatchBatch
	ended                     bool
	progresses, stops, closed atomic.Int32
	stop                      func()
}

func (w *fakeEventRun) Request() contract.KVWatchRequest { return w.request.Clone() }
func (w *fakeEventRun) Poll() contract.KVWatchBatch {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.batches) > 0 {
		b := w.batches[0]
		w.batches = w.batches[1:]
		return b.Clone()
	}
	return contract.KVWatchBatch{Ended: w.ended, Terminal: contract.KVEventTerminal{Reason: "stopped"}}
}
func (w *fakeEventRun) Progress() error { w.progresses.Add(1); return nil }
func (w *fakeEventRun) Stop(context.Context) error {
	w.stops.Add(1)
	if w.stop != nil {
		w.stop()
	}
	w.mu.Lock()
	w.ended = true
	w.mu.Unlock()
	return nil
}
func (w *fakeEventRun) Close() error { w.closed.Add(1); return nil }
func (w *fakeEventRun) push(b contract.KVWatchBatch) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.batches = append(w.batches, b.Clone())
}
func eventState(t *testing.T) (*State, *Document, *fakeEventsLease, *fakeEventsProvider) {
	t.Helper()
	s, d, _ := testState(t)
	l := &fakeEventsLease{scope: testScope()}
	p := &fakeEventsProvider{lease: l}
	s.deps.EventsProviderFor = func(id int64) (contract.KVEventsProvider, error) {
		if id != 7 {
			t.Error("foreign original profile")
		}
		return p, nil
	}
	s.SetMethod(d, WatchMethod)
	d.Editor.Buffer.Set(`{"key":"AP8=","watch_id":"9007199254740993","prev_kv":true}`)
	return s, d, l, p
}
func eventEventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("original event worker did not reach expected state")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestKVEventsChangedHTTPIdentityCannotReserveFromOriginalDocument(t *testing.T) {
	for _, change := range []string{"endpoint", "agent", "configuration", "availability"} {
		t.Run(change, func(t *testing.T) {
			s, d, l, p := eventState(t)
			d.scope = testScope()
			d.scope.HTTP = contract.KVHTTPIdentity{Protocol: "consul-http", Endpoint: "http://original", AgentID: "original-agent", AgentAvailable: true, Configuration: "captured-config"}
			l.scope = d.scope.Clone()
			switch change {
			case "endpoint":
				l.scope.HTTP.Endpoint = "http://foreign"
			case "agent":
				l.scope.HTTP.AgentID = "foreign-agent"
			case "configuration":
				l.scope.HTTP.Configuration = "foreign-config"
			case "availability":
				l.scope.HTTP.AgentAvailable = false
			}
			s.Run(d)
			if p.reserves.Load() != 0 || l.starts.Load() != 0 || d.eventLease != nil || !d.eventNeedsRecovery || d.Busy() {
				t.Fatal("changed HTTP provider reserved work for original document")
			}
		})
	}
}

func TestKVEventsStrictRequestPrecisionAliasesBoundsAndVersionOneDrafts(t *testing.T) {
	w, err := ParseWatchRequest(`{"key":"AP8=","rangeEnd":"AA==","startRevision":9007199254740993,"watchId":"9223372036854775807","prevKv":true,"progressNotify":true,"filters":["NOPUT",1],"fragment":false}`)
	if err != nil || !bytes.Equal(w.Key, []byte{0, 255}) || w.StartRevision != 9007199254740993 || w.WatchID != math.MaxInt64 || !w.NoPut || !w.NoDelete || !w.PrevKV || !w.ProgressNotify {
		t.Fatal("native precision/aliases changed", err)
	}
	h, err := ParseHistoryRequest(`{"key":"AP8=","start_revision":"9007199254740993","endRevision":9223372036854775807,"limit":"500"}`)
	if err != nil || h.StartRevision != 9007199254740993 || h.EndRevision != math.MaxInt64 || h.Limit != 500 {
		t.Fatal("fixed history window narrowed", err)
	}
	for _, body := range []string{`null`, `[]`, `{}`, `{"key":null}`, `{"key":""}`, `{"key":"AP8=" ,"key":"AP8="}`, `{"key":"AP8=","range_end":"AA==","rangeEnd":"AA=="}`, `{"key":"AP8=","watch_id":0}`, `{"key":"AP8=","watch_id":"9223372036854775808"}`, `{"key":"AP8=","start_revision":1e3}`, `{"key":"AP8=","start_revision":1.0}`, `{"key":"AP8=","start_revision":"-1"}`, `{"key":"AP8=","prev_kv":"true"}`, `{"key":"AP8=","fragment":true}`, `{"key":"AP8=","filters":["NOPUT",0]}`, `{"key":"AP8=","filters":[2]}`, `{"key":"AP8=","range_end":"AP8="}`, `{"key":"AP9="}`, `{"key":"AP8=","Key":"AP8="}`} {
		if _, err := ParseWatchRequest(body); err == nil {
			t.Fatal("ambiguous/unsupported Watch accepted", body)
		}
	}
	for _, body := range []string{`{"key":"AP8=","range_end":"AA=="}`, `{"key":"AP8=","start_revision":8,"end_revision":7}`, `{"key":"AP8=","limit":0}`, `{"key":"AP8=","limit":501}`, `{"key":"AP8=","endRevision":2,"end_revision":2}`} {
		if _, err := ParseHistoryRequest(body); err == nil {
			t.Fatal("invalid History accepted", body)
		}
	}
	key := bytes.Repeat([]byte{255}, 4096)
	if _, err := ParseWatchRequest(`{"key":"` + base64.StdEncoding.EncodeToString(key) + `"}`); err != nil {
		t.Fatal("maximum native key rejected", err)
	}
	if _, err := ParseWatchRequest(`{"key":"` + base64.StdEncoding.EncodeToString(append(key, 0)) + `"}`); err == nil {
		t.Fatal("oversized native key accepted")
	}
	for _, method := range []string{WatchMethod, HistoryMethod} {
		r := contract.KVRequest{Method: method, JSON: "unfinished { 9007199254740993"}
		raw, err := EncodeRequest(r)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeRequest(raw)
		if err != nil || got != r {
			t.Fatal("version-one event draft changed", err)
		}
	}
	for _, m := range Methods() {
		if eventMethod(m.Path) {
			t.Fatal("private event stage published its UI prematurely")
		}
	}
}

func TestKVEventsWatchOriginalInputContextProgressAndExplicitDisposal(t *testing.T) {
	s, d, l, p := eventState(t)
	entered := make(chan struct{})
	var enteredOnce sync.Once
	var run *fakeEventRun
	l.start = func(ctx context.Context, r contract.KVWatchRequest) (contract.KVWatchRun, error) {
		run = &fakeEventRun{request: r.Clone()}
		run.push(contract.KVWatchBatch{Frames: []contract.KVEventFrame{{Native: []byte{0, 255}, Created: true, WatchID: r.WatchID, Header: testScope().Header}}})
		enteredOnce.Do(func() { close(entered) })
		return run, nil
	}
	authored := d.Request()
	s.Run(d)
	await(t, entered)
	d.Editor.Buffer.Set(`{"key":"Yg=="}`)
	eventEventually(t, func() bool { s.Consume(); return d.eventReceipt != nil && len(d.eventReceipt.Batch.Frames) == 1 })
	if !d.Busy() || d.eventJob == nil || !bytes.Equal(run.Request().Key, []byte{0, 255}) || d.eventReceipt.Authored != authored || p.reserves.Load() != 1 || l.connects.Load() != 0 {
		t.Fatal("handshake canceled flow or changed original input")
	}
	s.ProgressEvents(d)
	eventEventually(t, func() bool { return run.progresses.Load() == 1 })
	s.Run(d)
	if l.starts.Load() != 1 {
		t.Fatal("active flow replayed")
	}
	s.StopEvents(d)
	consume(t, s, d)
	if !d.eventReceipt.Batch.Ended || d.eventReceipt.Batch.Incomplete || run.closed.Load() != 1 {
		t.Fatal("stop lost original terminal")
	}
	r, ok := d.LastEventReceipt()
	if !ok {
		t.Fatal("missing original event receipt")
	}
	r.Batch.Frames[0].Native[0] = 9
	r.Scope.Prefix[0] = 9
	r.Watch.Key[0] = 9
	if d.eventReceipt.Batch.Frames[0].Native[0] != 0 || d.eventReceipt.Scope.Prefix[0] != 0 || d.eventReceipt.Watch.Key[0] != 0 {
		t.Fatal("event receipt aliases caller")
	}
	s.Run(d)
	if d.Busy() || l.starts.Load() != 1 {
		t.Fatal("new flow reused retained receipt")
	}
	s.ClearEventReceipt(d)
	consume(t, s, d)
	if d.eventReceipt != nil || l.clears.Load() != 1 {
		t.Fatal("explicit settled disposal failed")
	}
	s.Run(d)
	eventEventually(t, func() bool { return l.starts.Load() == 2 })
	s.StopEvents(d)
	consume(t, s, d)
}

func TestKVEventsParkedBuffersStopWaitsAndCloseOwnsOriginalWorker(t *testing.T) {
	s, d, l, _ := eventState(t)
	active := true
	s.deps.Active = func(doc *Document) bool { return active && doc == d }
	entered, stopping, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var run *fakeEventRun
	l.start = func(_ context.Context, request contract.KVWatchRequest) (contract.KVWatchRun, error) {
		run = &fakeEventRun{request: request.Clone(), stop: func() { close(stopping); <-release }}
		for i := 0; i < 3; i++ {
			run.push(contract.KVWatchBatch{Frames: []contract.KVEventFrame{{Native: []byte{byte(i)}}}})
		}
		close(entered)
		return run, nil
	}
	s.Run(d)
	await(t, entered)
	op := d.eventJob
	active = false
	eventEventually(t, func() bool { return len(op.updates) == 1 })
	s.Consume()
	if len(d.eventReceipt.Batch.Frames) != 0 {
		t.Fatal("parked packet consumed")
	}
	s.StopEvents(d)
	await(t, stopping)
	s.Consume()
	if !d.Busy() || !d.CancelRequested() {
		t.Fatal("stop settled before original acknowledgement")
	}
	once.Do(func() { close(release) })
	await(t, op.finished)
	s.Consume()
	if !d.Busy() {
		t.Fatal("parked terminal entered live document")
	}
	active = true
	consume(t, s, d)
	if len(d.eventReceipt.Batch.Frames) != 3 {
		t.Fatal("pending final packet lost/duplicated original frames", len(d.eventReceipt.Batch.Frames))
	}
	for i, f := range d.eventReceipt.Batch.Frames {
		if len(f.Native) != 1 || f.Native[0] != byte(i) {
			t.Fatal("original frame ordering changed")
		}
	}
	s.Close(d)
	if d.eventLease != nil || d.eventReceipt != nil || d.Editor != nil || !l.closed.Load() {
		t.Fatal("close retained original caches")
	}
}

func TestKVEventsCancellationHistoryWaitsAndKeepsActualCompletion(t *testing.T) {
	s, d, l, _ := eventState(t)
	s.SetMethod(d, HistoryMethod)
	d.Editor.Buffer.Set(`{"key":"AP8=","start_revision":"2","end_revision":"7","limit":3}`)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	l.history = func(ctx context.Context, r contract.KVHistoryRequest) (contract.KVHistoryResult, error) {
		close(entered)
		<-ctx.Done()
		<-release
		return contract.KVHistoryResult{Authored: r.Clone(), Window: r.Clone(), Complete: true, ProgressRequests: 1, ProgressHeader: contract.KVHeader{Revision: 7}, Initial: contract.KVResult{Native: []byte{255, 0}}, Frames: []contract.KVEventFrame{{Native: []byte{7}}}}, nil
	}
	s.Run(d)
	await(t, entered)
	s.RequestCancel(d)
	s.Consume()
	if !d.Busy() {
		t.Fatal("history cancellation forgot executor")
	}
	once.Do(func() { close(release) })
	consume(t, s, d)
	if d.eventReceipt.History == nil || !d.eventReceipt.History.Complete || d.eventReceipt.History.Window.EndRevision != 7 || !bytes.Equal(d.eventReceipt.History.Initial.Native, []byte{255, 0}) {
		t.Fatal("actual fixed-window completion erased by local cancel")
	}
}

func TestKVEventsRecoveryKeepsOriginalHistoryAndUnknownWriteWithoutReplay(t *testing.T) {
	s, d, l, _ := eventState(t)
	s.SetMethod(d, HistoryMethod)
	d.Editor.Buffer.Set(`{"key":"AP8=","end_revision":"7"}`)
	originalUnknown := &Receipt{Scope: testScope(), Authored: contract.KVRequest{Method: "Put", JSON: "original unknown"}, Result: contract.KVResult{WriteOutcomeUnknown: true, Native: []byte{4}}}
	d.unknown = originalUnknown
	d.last = originalUnknown
	l.history = func(_ context.Context, r contract.KVHistoryRequest) (contract.KVHistoryResult, error) {
		return contract.KVHistoryResult{Authored: r.Clone(), Window: r.Clone(), Complete: true, Initial: contract.KVResult{Native: []byte{1}}, Frames: []contract.KVEventFrame{{Native: []byte{2}}}}, nil
	}
	s.Run(d)
	consume(t, s, d)
	old := d.eventReceipt
	scope := old.Scope.Clone()
	l.lost.Store(true)
	s.Consume()
	fresh := &fakeEventsLease{scope: testScope()}
	fresh.scope.Generation++
	p := &fakeEventsProvider{lease: fresh, recover: func(previous contract.KVEventsLease) (contract.KVEventsRecovery, error) {
		if previous != l {
			t.Error("substituted original lease")
		}
		_ = previous.Close()
		return contract.KVEventsRecovery{Lease: fresh, History: old.History}, nil
	}}
	s.deps.EventsProviderFor = func(int64) (contract.KVEventsProvider, error) { return p, nil }
	s.Recover(d)
	consume(t, s, d)
	if d.NeedsRecovery() || d.eventReceipt != old || !sameEventScope(old.Scope, scope) || fresh.histories.Load() != 0 || fresh.starts.Load() != 0 || fresh.connects.Load() != 0 || p.recovers.Load() != 1 || d.unknown != originalUnknown || d.last != originalUnknown {
		t.Fatal("recovery replayed/reclassified original evidence")
	}
	s.SetMethod(d, "/etcdserverpb.KV/Put")
	d.Editor.Buffer.Set(`{"key":"AP8="}`)
	s.Run(d)
	if d.confirmation != nil || d.Busy() {
		t.Fatal("event recovery enabled replay of unknown write")
	}
}

func TestKVEventsCompleteExportCapturesOriginalBeforePickerAndPreservesExactValues(t *testing.T) {
	s, d, l, _ := eventState(t)
	s.SetMethod(d, HistoryMethod)
	d.Editor.Buffer.Set(`{"key":"AP8=","end_revision":"9007199254740993"}`)
	value := bytes.Repeat([]byte{0, 255, 7}, 1000)
	l.history = func(_ context.Context, r contract.KVHistoryRequest) (contract.KVHistoryResult, error) {
		entry := contract.KVEntry{Key: r.Key, Value: value, CreateRevision: 9007199254740993, ModRevision: math.MaxInt64, Version: 9007199254740993, Lease: 9007199254740993}
		return contract.KVHistoryResult{Authored: r.Clone(), Window: r.Clone(), Complete: true, ProgressHeader: contract.KVHeader{ClusterID: math.MaxUint64, Revision: 9007199254740993}, Initial: contract.KVResult{Native: []byte{0, 255}, Header: testScope().Header}, Frames: []contract.KVEventFrame{{Native: []byte{255, 0, 9}, WatchID: math.MaxInt64, Events: []contract.KVEvent{{Type: "PUT", Entry: entry, Previous: &entry}}}}, Events: []contract.KVEvent{{Type: "PUT", Entry: entry}}}, nil
	}
	s.Run(d)
	consume(t, s, d)
	original, err := s.EventRecord(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(original), `"18446744073709551615"`) || !strings.Contains(string(original), `"9223372036854775807"`) || !strings.Contains(string(original), base64.StdEncoding.EncodeToString(value)) || len(s.EventPreview(d)) > previewLimit+10 {
		t.Fatal("complete event record narrowed/truncated values")
	}
	picked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var written []byte
	s.SetFiles(Files{SavePath: func(string) (string, error) { close(picked); <-release; return "original-events.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var b bytes.Buffer
		err := write(&b)
		written = bytes.Clone(b.Bytes())
		return err
	}})
	s.ExportEventRecord(d)
	await(t, picked)
	s.ClearEventReceipt(d)
	eventEventually(t, func() bool { s.Consume(); return d.eventJob == nil })
	if d.eventReceipt != nil {
		t.Fatal("clear retained original cache")
	}
	once.Do(func() { close(release) })
	consume(t, s, d)
	if !bytes.Equal(written, original) {
		t.Fatal("picker substituted current scope/receipt")
	}
	var parsed map[string]json.RawMessage
	if json.Unmarshal(written, &parsed) != nil {
		t.Fatal("event record is not valid JSON")
	}
}

func TestKVEventsInjectedCaptureBoundsRetainEarlierFramesAndStopOriginal(t *testing.T) {
	s, d, l, _ := eventState(t)
	entered := make(chan struct{})
	var run *fakeEventRun
	l.start = func(_ context.Context, r contract.KVWatchRequest) (contract.KVWatchRun, error) {
		run = &fakeEventRun{request: r.Clone()}
		for i := 0; i < 2; i++ {
			run.push(contract.KVWatchBatch{Frames: []contract.KVEventFrame{{Native: bytes.Repeat([]byte{byte(i)}, 9<<20)}}})
		}
		close(entered)
		return run, nil
	}
	s.Run(d)
	await(t, entered)
	consume(t, s, d)
	if !d.eventReceipt.Batch.Ended || !d.eventReceipt.Batch.Incomplete || d.eventReceipt.Batch.Terminal.Reason != "document_bounds" || len(d.eventReceipt.Batch.Frames) != 1 || len(d.eventReceipt.Batch.Frames[0].Native) != 9<<20 || run.stops.Load() != 1 {
		t.Fatal("capture bound replayed/lost earlier original receipt")
	}
	if !strings.Contains(d.Error(), "bounds") {
		t.Fatal("bounded native capture was claimed complete")
	}
}

func TestKVEventsCloseWaitsOnCanceledHandshakeAndDiscardsLateFreshRecovery(t *testing.T) {
	s, d, l, _ := eventState(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	l.start = func(ctx context.Context, r contract.KVWatchRequest) (contract.KVWatchRun, error) {
		close(entered)
		<-ctx.Done()
		<-release
		return nil, ctx.Err()
	}
	s.Run(d)
	await(t, entered)
	op := d.eventJob
	s.RequestCancel(d)
	if !s.GuardLeave(d, func(ui.Services) {}) {
		t.Fatal("pending handshake bypassed close guard")
	}
	s.DecideLeave(ui.Services{}, true)
	if s.leave == nil {
		t.Fatal("leave ignored original worker")
	}
	once.Do(func() { close(release) })
	await(t, op.finished)
	s.Close(d)
	if !d.closed || d.eventJob != nil || d.eventReceipt != nil {
		t.Fatal("closed handshake kept original cache")
	}
	s2, d2, l2, _ := eventState(t)
	d2.eventLease = l2
	d2.eventScope = l2.Scope()
	d2.eventNeedsRecovery = true
	fresh := &fakeEventsLease{scope: testScope()}
	recovered, releaseRecovery := make(chan struct{}), make(chan struct{})
	var onceRecovery sync.Once
	t.Cleanup(func() { onceRecovery.Do(func() { close(releaseRecovery) }) })
	p := &fakeEventsProvider{lease: fresh, recover: func(contract.KVEventsLease) (contract.KVEventsRecovery, error) {
		close(recovered)
		<-releaseRecovery
		return contract.KVEventsRecovery{Lease: fresh}, nil
	}}
	s2.deps.EventsProviderFor = func(int64) (contract.KVEventsProvider, error) { return p, nil }
	s2.Recover(d2)
	await(t, recovered)
	job := d2.eventJob
	s2.RequestCancel(d2)
	onceRecovery.Do(func() { close(releaseRecovery) })
	await(t, job.finished)
	s2.Close(d2)
	if !fresh.closed.Load() {
		t.Fatal("canceled recovery leaked fresh lease")
	}
}

func TestKVEventsForeignInitialPublicationRejectedBeforeReserve(t *testing.T) {
	s, d, l, p := eventState(t)
	l.scope.Generation++
	s.Run(d)
	if d.Busy() || d.eventLease != nil || !d.NeedsRecovery() || p.reserves.Load() != 0 {
		t.Fatal("replacement publication silently rebound original document")
	}
	if !errors.Is(l.CheckKVLease(), nil) {
		t.Fatal("foreign lease was damaged")
	}
}
