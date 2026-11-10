package kvworkspace

import (
	"context"
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

type fakePlan struct {
	request contract.KVRequest
	write   bool
}

func (p *fakePlan) Snapshot() contract.KVRequest { return p.request }
func (p *fakePlan) Writes() bool                 { return p.write }

type fakeLease struct {
	scope              contract.KVScope
	closed, lost       atomic.Bool
	executes, connects atomic.Int32
	execute            func(context.Context, contract.KVPreparation) (contract.KVResult, error)
	close              func() error
}

func (l *fakeLease) Scope() contract.KVScope { return l.scope.Clone() }
func (l *fakeLease) CheckKVLease() error {
	if l.closed.Load() {
		return contract.ErrRevoked
	}
	if l.lost.Load() {
		return contract.ErrKVLeaseLost
	}
	return nil
}
func (l *fakeLease) Prepare(r contract.KVRequest) (contract.KVPreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	return &fakePlan{r, strings.Contains(r.Method, "Put")}, nil
}
func (l *fakeLease) Preconnect(context.Context) error { l.connects.Add(1); return l.CheckKVLease() }
func (l *fakeLease) Execute(ctx context.Context, p contract.KVPreparation) (contract.KVResult, error) {
	l.executes.Add(1)
	if l.execute != nil {
		return l.execute(ctx, p)
	}
	return contract.KVResult{Request: p.Snapshot(), Attempted: true, Completed: true, JSON: `{"header":{"revision":"9007199254740993"}}`, Native: []byte{1, 2, 3}, Header: contract.KVHeader{ClusterID: math.MaxUint64, MemberID: math.MaxUint64, Revision: 9007199254740993}}, nil
}
func (l *fakeLease) LastResult() contract.KVResult { return contract.KVResult{} }
func (l *fakeLease) Close() error {
	l.closed.Store(true)
	if l.close != nil {
		return l.close()
	}
	return nil
}

type fakeProvider struct {
	lease   *fakeLease
	reserve func(uint64) (contract.KVRequestLease, error)
	recover func(contract.KVRequestLease) (contract.KVRecovery, error)
}

func (p *fakeProvider) KVScope() contract.KVScope { return p.lease.Scope() }
func (p *fakeProvider) ReserveKV(id uint64) (contract.KVRequestLease, error) {
	if p.reserve != nil {
		return p.reserve(id)
	}
	return p.lease, nil
}
func (p *fakeProvider) RecoverKV(l contract.KVRequestLease) (contract.KVRecovery, error) {
	return p.recover(l)
}
func testScope() contract.KVScope {
	return contract.KVScope{Namespace: contract.NamespaceRef{ProfileID: 7, Catalog: "kv"}, Generation: 11, Prefix: []byte{0, 255}, Header: contract.KVHeader{ClusterID: math.MaxUint64, MemberID: 3, Revision: 2, RaftTerm: 4}, Version: "3.7.3"}
}
func testState(t *testing.T) (*State, *Document, *fakeLease) {
	t.Helper()
	lease := &fakeLease{scope: testScope()}
	p := &fakeProvider{lease: lease}
	s := New(Dependencies{ProviderFor: func(id int64) (contract.KVRequestsProvider, error) {
		if id != 7 {
			t.Error("foreign profile")
		}
		return p, nil
	}})
	d := s.Open(1, 7, DefaultRequest())
	t.Cleanup(s.CloseAll)
	return s, d, lease
}
func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("original worker did not finish")
	}
}
func consume(t *testing.T, s *State, d *Document) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for d.Busy() || d.FileBusy() {
		s.Consume()
		if time.Now().After(deadline) {
			t.Fatal("original document did not settle")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestKVWriteConfirmationOwnsOriginalInputAndLease(t *testing.T) {
	s, d, l := testState(t)
	s.SetMethod(d, "/etcdserverpb.KV/Put")
	d.Editor.Buffer.Set(`{"key":"YQ==","value":"Yg=="}`)
	s.Run(d)
	if _, ok := d.Confirmation(); !ok || l.executes.Load() != 0 || l.connects.Load() != 0 {
		t.Fatal("write preparation dispatched")
	}
	s.Confirm(d, false)
	if d.Busy() || l.executes.Load() != 0 {
		t.Fatal("unchecked confirmation dispatched")
	}
	d.Editor.Buffer.Set(`{"key":"YQ==","value":"Yw=="}`)
	s.Confirm(d, true)
	if l.executes.Load() != 0 || d.confirmation != nil {
		t.Fatal("changed input submitted old preview")
	}
	s.Run(d)
	s.Confirm(d, true)
	consume(t, s, d)
	r, ok := d.LastReceipt()
	if !ok || !r.Result.Completed || r.Authored != d.Request() || l.executes.Load() != 1 {
		t.Fatal("missing original receipt")
	}
	r.Result.Native[0] = 9
	r.Scope.Prefix[0] = 9
	if d.last.Result.Native[0] != 1 || d.Scope().Prefix[0] != 0 {
		t.Fatal("receipt aliases owning document")
	}
	raw, err := s.OperationRecord(d)
	if err != nil || !strings.Contains(string(raw), `"18446744073709551615"`) || !strings.Contains(string(raw), `"9007199254740993"`) {
		t.Fatal("operation export narrowed integers", err)
	}
}
func TestKVCancellationWaitsForOriginalAcknowledgementAndUnknownWriteStaysSeparate(t *testing.T) {
	s, d, l := testState(t)
	entered, released := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(released) }) })
	l.execute = func(ctx context.Context, p contract.KVPreparation) (contract.KVResult, error) {
		close(entered)
		<-ctx.Done()
		<-released
		l.lost.Store(true)
		return contract.KVResult{Request: p.Snapshot(), Attempted: true, WriteOutcomeUnknown: true, Code: "Canceled"}, ctx.Err()
	}
	s.SetMethod(d, "/etcdserverpb.KV/Put")
	d.Editor.Buffer.Set(`{"key":"YQ=="}`)
	s.Run(d)
	s.Confirm(d, true)
	await(t, entered)
	s.RequestCancel(d)
	s.Consume()
	if !d.Busy() || !d.CancelRequested() {
		t.Fatal("cancellation settled before original acknowledgement")
	}
	once.Do(func() { close(released) })
	consume(t, s, d)
	if !d.needsRecovery || d.unknown == nil || !d.unknown.Result.WriteOutcomeUnknown {
		t.Fatal("unknown original outcome narrowed")
	}
	provider := &fakeProvider{lease: l}
	fresh := &fakeLease{scope: testScope()}
	provider.recover = func(old contract.KVRequestLease) (contract.KVRecovery, error) {
		_ = old.Close()
		return contract.KVRecovery{Lease: fresh}, nil
	}
	s.deps.ProviderFor = func(int64) (contract.KVRequestsProvider, error) { return provider, nil }
	s.Recover(d)
	consume(t, s, d)
	if fresh.executes.Load() != 0 || fresh.connects.Load() != 0 || d.needsRecovery {
		t.Fatal("recovery dispatched/authenticated automatically")
	}
	s.Run(d)
	if d.confirmation != nil || fresh.executes.Load() != 0 {
		t.Fatal("unknown write became a retry")
	}
	s.SetMethod(d, "/etcdserverpb.KV/Range")
	d.Editor.Buffer.Set(`{"key":"YQ=="}`)
	s.Run(d)
	consume(t, s, d)
	if !d.last.Result.Completed || d.unknown == d.last || !d.unknown.Result.WriteOutcomeUnknown || d.unknown.Authored.Method != "/etcdserverpb.KV/Put" {
		t.Fatal("readback overwrote original unknown receipt")
	}
	d.selectedUnknown = true
	record, err := s.OperationRecord(d)
	if err != nil || !strings.Contains(string(record), `"write_outcome_unknown": true`) {
		t.Fatal("unknown operation export lost original state")
	}
}
func TestKVParkedCompletionAndCloseCannotEnterReplacementDocument(t *testing.T) {
	s, d, l := testState(t)
	active := true
	s.deps.Active = func(doc *Document) bool { return active && doc == d }
	s.Run(d)
	job := d.job
	await(t, job.finished)
	active = false
	s.Consume()
	if d.job != job || d.last != nil {
		t.Fatal("parked result consumed")
	}
	active = true
	s.Consume()
	if d.last == nil {
		t.Fatal("original activated receipt missing")
	}
	s.Run(d)
	await(t, d.job.finished)
	replacement := &fakeLease{scope: testScope()}
	s.deps.ProviderFor = func(int64) (contract.KVRequestsProvider, error) { return &fakeProvider{lease: replacement}, nil }
	next := s.Open(d.ID(), 7, DefaultRequest())
	if !d.closed || d.Editor != nil || d.saved.JSON != "" || !l.closed.Load() || next.last != nil || replacement.closed.Load() {
		t.Fatal("close retained caches or damaged replacement")
	}
}
func TestKVLeaveDecisionDoesNotResolveUnknownCommit(t *testing.T) {
	s, d, _ := testState(t)
	d.unknown = &Receipt{Authored: contract.KVRequest{Method: "Put"}, Result: contract.KVResult{WriteOutcomeUnknown: true}}
	continued := 0
	finish := func(ui.Services) { continued++ }
	if !s.GuardLeave(d, finish) {
		t.Fatal("unknown close bypassed decision")
	}
	s.DecideLeave(ui.Services{}, false)
	if continued != 0 || d.unknown == nil {
		t.Fatal("cancel discarded original")
	}
	s.GuardLeave(d, finish)
	s.DecideLeave(ui.Services{}, true)
	if continued != 1 || d.unknown == nil || !d.unknown.Result.WriteOutcomeUnknown || s.GuardLeave(d, finish) {
		t.Fatal("close decision reclassified native outcome or repeated guard")
	}
	d.Editor.Buffer.Set("changed again")
	if !s.GuardLeave(d, finish) {
		t.Fatal("new input reused stale leave permission")
	}
}
func TestKVFileSnapshotLatePickerAndSaveDoNotOverwriteNewEdits(t *testing.T) {
	s, d, _ := testState(t)
	picked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var written string
	s.SetFiles(Files{SavePath: func(string) (string, error) { close(picked); <-release; return "original.nyakv", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var b strings.Builder
		if err := write(&b); err != nil {
			return err
		}
		written = b.String()
		return nil
	}})
	d.Editor.Buffer.Set("original unsent draft")
	saved := d.Request()
	s.SaveRequest(d, true)
	await(t, picked)
	d.Editor.Buffer.Set("new draft while picker waits")
	once.Do(func() { close(release) })
	consume(t, s, d)
	restored, err := DecodeRequest([]byte(written))
	if err != nil || restored != saved || !d.Dirty() || d.Request().JSON != "new draft while picker waits" {
		t.Fatal("save replaced current draft or lost capture", err)
	}
}

func TestKVConcurrentFileFailureDoesNotEnterNativeReceipt(t *testing.T) {
	s, d, l := testState(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	l.execute = func(ctx context.Context, p contract.KVPreparation) (contract.KVResult, error) {
		close(entered)
		select {
		case <-ctx.Done():
			return contract.KVResult{}, ctx.Err()
		case <-release:
			return contract.KVResult{Request: p.Snapshot(), Attempted: true, Completed: true, Code: "OK"}, nil
		}
	}
	s.Run(d)
	await(t, entered)
	s.SetFiles(Files{SavePath: func(string) (string, error) { return "request.nyakv", nil }, WriteStream: func(string, func(io.Writer) error) error { return errors.New("original file unavailable") }})
	s.SaveRequest(d, true)
	await(t, d.io.finished)
	s.Consume()
	if !d.Busy() || d.error == "" || d.last != nil {
		t.Fatal("file error did not stay separate while native read was pending")
	}
	once.Do(func() { close(release) })
	consume(t, s, d)
	if d.last == nil || !d.last.Result.Completed || d.last.Error != "" {
		t.Fatal("unrelated file failure entered acknowledged native receipt")
	}
}
func TestKVClosedExportRevokesOriginalPickerBeforeWrite(t *testing.T) {
	s, d, _ := testState(t)
	s.Run(d)
	consume(t, s, d)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var writes atomic.Int32
	s.SetFiles(Files{SavePath: func(string) (string, error) { close(entered); <-release; defer close(done); return "late.pb", nil }, WriteStream: func(string, func(io.Writer) error) error { writes.Add(1); return nil }})
	s.ExportReceipt(d, true)
	job := d.io
	await(t, entered)
	s.Close(d)
	once.Do(func() { close(release) })
	await(t, done)
	await(t, job.finished)
	if writes.Load() != 0 {
		t.Fatal("closed original picker wrote late file")
	}
}
func TestKVRequestFileStrictVersionAndInvalidDraftRoundTrip(t *testing.T) {
	original := contract.KVRequest{Method: "/etcdserverpb.KV/Put", JSON: "invalid unfinished draft {"}
	raw, err := EncodeRequest(original)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeRequest(raw)
	if err != nil || restored != original {
		t.Fatal("unsent draft changed")
	}
	for _, body := range []string{`{"version":1,"version":1,"method":"/etcdserverpb.KV/Put","text":"{}"}`, `{"version":1,"method":"/etcdserverpb.KV/Put","Text":"{}"}`, `{"version":2,"method":"/etcdserverpb.KV/Put","text":"{}"}`, `{"version":1.0,"method":"/etcdserverpb.KV/Put","text":"{}"}`, `{"version":1,"method":"/etcdserverpb.Auth/AuthDisable","text":"{}"}`, `{"version":1,"method":"/etcdserverpb.KV/Put","text":null}`, `{"version":1,"method":"/etcdserverpb.KV/Put","text":"\ud800"}`} {
		if _, err := DecodeRequest([]byte(body)); err == nil {
			t.Fatal("ambiguous/unsupported request file accepted")
		}
	}
	var parsed map[string]json.RawMessage
	if json.Unmarshal(raw, &parsed) != nil {
		t.Fatal("invalid persisted request envelope")
	}
}
