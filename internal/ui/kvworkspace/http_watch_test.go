package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
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

type fakeHTTPWatchPlan struct{ r contract.KVHTTPWatchRequest }

func (p fakeHTTPWatchPlan) Snapshot() contract.KVHTTPWatchRequest { return p.r }

type fakeHTTPWatchRun struct {
	mu                      sync.Mutex
	r                       contract.KVHTTPWatchRequest
	batch                   contract.KVHTTPWatchBatch
	polls, reads, snapshots atomic.Int32
	ack                     chan struct{}
}

func (w *fakeHTTPWatchRun) Request() contract.KVHTTPWatchRequest { return w.r }
func (w *fakeHTTPWatchRun) Poll() contract.KVHTTPWatchBatch {
	panic("indexed presentation copied full pending bodies")
}
func (w *fakeHTTPWatchRun) Snapshot() contract.KVHTTPWatchSnapshot {
	w.snapshots.Add(1)
	w.mu.Lock()
	defer w.mu.Unlock()
	return contract.KVHTTPWatchSnapshot{Request: w.r, KVHTTPWatchBatch: w.batch.Clone()}
}
func (w *fakeHTTPWatchRun) PollStatus() contract.KVHTTPWatchStatus {
	w.polls.Add(1)
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.batch
	return contract.KVHTTPWatchStatus{Observations: len(b.Observations), Ended: b.Ended, RetainedBytes: b.RetainedBytes, TerminalOverflow: b.TerminalOverflow, Terminal: b.Terminal}
}
func (w *fakeHTTPWatchRun) Observation(i int) (contract.KVHTTPWatchObservation, bool) {
	w.reads.Add(1)
	w.mu.Lock()
	defer w.mu.Unlock()
	if i < 0 || i >= len(w.batch.Observations) {
		return contract.KVHTTPWatchObservation{}, false
	}
	return w.batch.Observations[i].Clone(), true
}
func (w *fakeHTTPWatchRun) Stop(ctx context.Context) error {
	select {
	case <-w.ack:
	case <-ctx.Done():
		return ctx.Err()
	}
	w.mu.Lock()
	w.batch.Ended = true
	w.mu.Unlock()
	return nil
}
func (w *fakeHTTPWatchRun) Close() error { return w.Stop(context.Background()) }

type fakeHTTPWatchLease struct {
	scope          contract.KVScope
	run            *fakeHTTPWatchRun
	starts, clears atomic.Int32
	lost           atomic.Bool
}

func (l *fakeHTTPWatchLease) Scope() contract.KVScope { return l.scope.Clone() }
func (l *fakeHTTPWatchLease) CheckKVLease() error {
	if l.lost.Load() {
		return contract.ErrRevoked
	}
	return nil
}
func (l *fakeHTTPWatchLease) Limits() contract.KVHTTPWatchLimits {
	return contract.KVHTTPWatchLimits{MinWait: time.Millisecond, MaxWait: time.Minute, RequestTimeout: 30 * time.Second, MaxObservations: 256, MaxBytes: 256 << 20, TerminalBytes: 128 << 20}
}
func (l *fakeHTTPWatchLease) Prepare(r contract.KVHTTPWatchRequest) (contract.KVHTTPWatchPreparation, error) {
	return fakeHTTPWatchPlan{r}, nil
}
func (l *fakeHTTPWatchLease) Start(_ context.Context, p contract.KVHTTPWatchPreparation) (contract.KVHTTPWatchRun, error) {
	l.starts.Add(1)
	l.run.r = p.Snapshot()
	return l.run, nil
}
func (l *fakeHTTPWatchLease) LastWatch() *contract.KVHTTPWatchSnapshot {
	s := l.run.Snapshot()
	return &s
}
func (l *fakeHTTPWatchLease) RetainedWatch() contract.KVHTTPWatchRun { return l.run }
func (l *fakeHTTPWatchLease) ClearObservations() error               { l.clears.Add(1); return nil }
func (l *fakeHTTPWatchLease) Close() error                           { return l.run.Close() }

type fakeHTTPWatchProvider struct {
	lease                *fakeHTTPWatchLease
	recovered            *fakeHTTPWatchLease
	reserves, recoveries atomic.Int32
	recover              func(contract.KVHTTPWatchLease) (contract.KVHTTPWatchRecovery, error)
}

func (p *fakeHTTPWatchProvider) KVScope() contract.KVScope { return p.lease.Scope() }
func (p *fakeHTTPWatchProvider) ReserveKVHTTPWatch(uint64) (contract.KVHTTPWatchLease, error) {
	p.reserves.Add(1)
	return p.lease, nil
}
func (p *fakeHTTPWatchProvider) RecoverKVHTTPWatch(old contract.KVHTTPWatchLease) (contract.KVHTTPWatchRecovery, error) {
	p.recoveries.Add(1)
	if p.recover != nil {
		return p.recover(old)
	}
	return contract.KVHTTPWatchRecovery{Lease: p.recovered, Watch: p.recovered.LastWatch()}, nil
}

func TestHTTPWatchCancelledRecoveryKeepsTransferredOriginalArchive(t *testing.T) {
	s, d, p := httpWatchTest(t)
	w := p.lease.run
	w.batch = contract.KVHTTPWatchBatch{Ended: true, Observations: []contract.KVHTTPWatchObservation{{HTTP: contract.KVHTTPResult{Body: []byte("original recovery evidence"), Captured: true, EOF: true}}}}
	close(w.ack)
	s.StartHTTPWatch(d)
	eventEventually(t, func() bool { s.Consume(); return d.httpWatch.run != nil })
	s.DisconnectProfile(d.profileID)
	next := &fakeHTTPWatchLease{scope: d.scope.Clone(), run: w}
	next.scope.Generation++
	p.lease = next
	entered, release := make(chan struct{}), make(chan struct{})
	p.recover = func(contract.KVHTTPWatchLease) (contract.KVHTTPWatchRecovery, error) {
		r := contract.KVHTTPWatchRecovery{Lease: next, Watch: next.LastWatch()}
		close(entered)
		<-release
		return r, nil
	}
	s.RecoverHTTPWatch(d)
	await(t, entered)
	s.RequestCancel(d)
	if !d.Busy() {
		t.Fatal("recovery cancellation implied transfer ACK")
	}
	close(release)
	eventEventually(t, func() bool { s.Consume(); return !d.Busy() })
	if d.httpWatch.run == nil || d.httpWatch.lease != next || !d.NeedsRecovery() || next.starts.Load() != 0 {
		t.Fatal("cancelled transfer lost archive or executed a read")
	}
	raw, err := s.HTTPWatchRecord(d)
	if err != nil || !bytes.Contains(raw, []byte("b3JpZ2luYWwgcmVjb3ZlcnkgZXZpZGVuY2U=")) {
		t.Fatal("cancelled recovery dropped original body", err)
	}
}

func httpWatchTest(t *testing.T) (*State, *Document, *fakeHTTPWatchProvider) {
	s, d, _ := testState(t)
	w := &fakeHTTPWatchRun{ack: make(chan struct{})}
	l := &fakeHTTPWatchLease{scope: d.scope.Clone(), run: w}
	p := &fakeHTTPWatchProvider{lease: l}
	s.deps.HTTPWatchProviderFor = func(id int64) (contract.KVHTTPWatchProvider, error) {
		if id != d.profileID {
			t.Error("foreign original profile")
		}
		return p, nil
	}
	d.httpWatchAvailable = true
	s.SetMethod(d, HTTPWatchMethod)
	d.Editor.Buffer.Set(DefaultHTTPWatchRequest().JSON)
	t.Cleanup(func() {
		select {
		case <-w.ack:
		default:
			close(w.ack)
		}
	})
	return s, d, p
}

func TestHTTPWatchRequestExplicitRangeAndUnsignedPrecision(t *testing.T) {
	base := DefaultHTTPWatchRequest().JSON
	text := strings.Replace(base, "\"start_index\": \"0\"", "\"start_index\": \"18446744073709551615\"", 1)
	r, err := ParseHTTPWatchRequest(text)
	if err != nil || r.StartIndex != math.MaxUint64 || r.ObservationPrefix != "" {
		t.Fatal(r, err)
	}
	for _, invalid := range []string{strings.Replace(base, "  \"observation_prefix\": \"\",\n", "", 1), strings.Replace(text, "18446744073709551615", "18446744073709551616", 1), strings.Replace(base, "\"prefix\": true", "\"prefix\": null", 1), strings.Replace(base, "\"wait\": \"1s\"", "\"wait\": 1", 1), strings.Replace(base, "\"target\": \"\"", "\"target\": \"\", \"target\": \"other\"", 1)} {
		if _, err := ParseHTTPWatchRequest(invalid); err == nil {
			t.Fatal("accepted invalid original request", invalid)
		}
	}
	raw, err := EncodeRequest(contract.KVRequest{Method: HTTPWatchMethod, JSON: text})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := DecodeRequest(raw)
	if err != nil || draft.JSON != text {
		t.Fatal("draft changed unsigned text", err)
	}
}

func TestHTTPWatchParkedOriginalBufferIndexedPreviewAndUnknownWrite(t *testing.T) {
	s, d, p := httpWatchTest(t)
	active := true
	s.deps.Active = func(*Document) bool { return active }
	unknown := &Receipt{Result: contract.KVResult{WriteOutcomeUnknown: true, Native: []byte{0, 255}}}
	d.unknown = unknown
	body := bytes.Repeat([]byte("original-"), 20000)
	w := p.lease.run
	w.batch = contract.KVHTTPWatchBatch{Observations: []contract.KVHTTPWatchObservation{{HTTP: contract.KVHTTPResult{Body: body, Metadata: contract.KVHTTPMetadata{}}, ProjectionComplete: true}}, RetainedBytes: int64(len(body))}
	s.StartHTTPWatch(d)
	op := d.httpWatch.job
	await(t, op.finished)
	active = false
	s.Consume()
	if d.httpWatch.job != op || w.polls.Load() != 0 || w.snapshots.Load() != 0 {
		t.Fatal("parked buffer consumed")
	}
	active = true
	s.Consume()
	if w.polls.Load() != 1 || w.snapshots.Load() != 0 || len(d.httpWatch.preview) > previewLimit+5 || d.unknown != unknown {
		t.Fatal("archive copied, preview unbounded or unknown write lost")
	}
	s.SelectHTTPWatchObservation(d, 0)
	if w.reads.Load() != 2 || w.snapshots.Load() != 0 {
		t.Fatal("selection copied complete history")
	}
	s.StartHTTPWatch(d)
	if p.lease.starts.Load() != 1 {
		t.Fatal("automatic new start")
	}
	s.StopHTTPWatch(d)
	s.Consume()
	if !d.Busy() || !d.CancelRequested() {
		t.Fatal("cancel cleared before executor ACK")
	}
	close(w.ack)
	eventEventually(t, func() bool { s.Consume(); return !d.Busy() })
	if d.unknown != unknown {
		t.Fatal("read confirmed unknown write")
	}
	s.StartHTTPWatch(d)
	if p.lease.starts.Load() != 1 {
		t.Fatal("archive silently evicted")
	}
	s.ClearHTTPWatch(d)
	if d.httpWatch.run != nil || p.lease.clears.Load() != 1 || d.unknown != unknown {
		t.Fatal("explicit clear affected write receipts")
	}
}

func TestHTTPWatchExportCapturesBeforePickerAndRetainsCompleteBody(t *testing.T) {
	s, d, p := httpWatchTest(t)
	w := p.lease.run
	w.batch = contract.KVHTTPWatchBatch{Ended: true, Observations: []contract.KVHTTPWatchObservation{{HTTP: contract.KVHTTPResult{Body: bytes.Repeat([]byte{0, 255}, 40000), Captured: true, EOF: true}, IndexRelation: "initial"}}}
	s.StartHTTPWatch(d)
	eventEventually(t, func() bool { s.Consume(); return d.httpWatch.run != nil })
	entered, release := make(chan struct{}), make(chan struct{})
	var written bytes.Buffer
	s.SetFiles(Files{SavePath: func(string) (string, error) { close(entered); <-release; return "original.json", nil }, WriteStream: func(_ string, fn func(io.Writer) error) error { return fn(&written) }})
	s.ExportHTTPWatchRecord(d)
	await(t, entered)
	w.mu.Lock()
	w.batch.Observations[0].HTTP.Body = []byte("changed after picker")
	w.mu.Unlock()
	s.ClearHTTPWatch(d)
	if p.lease.clears.Load() != 0 {
		t.Fatal("released evidence during captured file operation")
	}
	close(release)
	eventEventually(t, func() bool { s.Consume(); return !d.FileBusy() })
	var record struct {
		Observations []struct {
			Original struct{ HTTP struct{ Body []byte } } `json:"original_http"`
		}
	}
	if json.Unmarshal(written.Bytes(), &record) != nil || len(record.Observations) != 1 || len(record.Observations[0].Original.HTTP.Body) != 80000 {
		t.Fatal("file did not preserve complete original snapshot")
	}
}

func TestHTTPWatchRecoveryOriginalIdentityNoExecutionAndLeaveEvidence(t *testing.T) {
	s, d, p := httpWatchTest(t)
	w := p.lease.run
	w.batch.Ended = true
	close(w.ack)
	s.StartHTTPWatch(d)
	eventEventually(t, func() bool { s.Consume(); return d.httpWatch.run != nil })
	s.DisconnectProfile(d.profileID)
	next := &fakeHTTPWatchLease{scope: d.scope.Clone(), run: w}
	next.scope.Generation++
	p.lease = next
	p.recovered = next
	text, originalScope := d.Request(), d.httpWatch.evidenceScope.Clone()
	s.RecoverHTTPWatch(d)
	eventEventually(t, func() bool { s.Consume(); return !d.Busy() })
	if d.NeedsRecovery() || p.recoveries.Load() != 1 || next.starts.Load() != 0 || d.Request() != text || !sameEventScope(d.httpWatch.evidenceScope, originalScope) {
		t.Fatal("recovery executed or rewrote original evidence")
	}
	d.saved = d.Request()
	if !s.GuardLeave(d, nil) {
		t.Fatal("settled evidence omitted from leave decision")
	}
	s.DecideLeave(ui.Services{}, false)
	if d.httpWatch.run == nil {
		t.Fatal("cancel leave dropped original evidence")
	}
}

func TestHTTPWatchRejectsReplacementBeforeInitialReserve(t *testing.T) {
	s, d, p := httpWatchTest(t)
	p.lease.scope.Generation++
	s.StartHTTPWatch(d)
	if p.reserves.Load() != 0 || d.httpWatch.lease != nil || !d.NeedsRecovery() {
		t.Fatal("rebound initial document without recovery")
	}
}
