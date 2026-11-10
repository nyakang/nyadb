package kvworkspace

import (
	"bytes"
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type compactionTestLease struct{ *fakeLease }

func (*compactionTestLease) MaintenanceMethods() []string { return []string{CompactMethod} }
func (l *compactionTestLease) Prepare(r contract.KVRequest) (contract.KVPreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	return &fakePlan{r, r.Method == CompactMethod || strings.HasSuffix(r.Method, "/Put")}, nil
}
func compactionTestState(t *testing.T) (*State, *Document, *compactionTestLease) {
	t.Helper()
	l := &compactionTestLease{&fakeLease{scope: testScope()}}
	l.execute = func(_ context.Context, p contract.KVPreparation) (contract.KVResult, error) {
		return compactionTestResult(l, p), nil
	}
	provider := &fakeProvider{lease: l.fakeLease, reserve: func(uint64) (contract.KVRequestLease, error) { return l, nil }}
	s := New(Dependencies{ProviderFor: func(int64) (contract.KVRequestsProvider, error) { return provider, nil }})
	d := s.Open(1, 7, DefaultCompactionRequest())
	t.Cleanup(s.CloseAll)
	return s, d, l
}
func compactionTestResult(l *compactionTestLease, p contract.KVPreparation) contract.KVResult {
	h := l.scope.Header
	h.Revision = 9007199254740995
	r := contract.KVResult{Request: p.Snapshot(), Attempted: true, Completed: true, Code: "OK", Header: h, Native: []byte{1, 2, 3}, JSON: `{"header":{"revision":"9007199254740995"}}`, Reply: &contract.KVReply{Kind: "compaction", Header: h}}
	if strings.HasSuffix(p.Snapshot().Method, "/Status") {
		r.Reply.Kind = "status"
		r.Reply.Status = &contract.KVStatus{Version: l.scope.Version}
	}
	if strings.HasSuffix(p.Snapshot().Method, "/Range") {
		r.Reply.Kind = "range"
	}
	return r
}

func TestCompactionStrictRevisionFilesAndOptionalCapabilityWithoutIO(t *testing.T) {
	s, d, l := compactionTestState(t)
	s.SetCompactionInputs(d, "9223372036854775807", true)
	if n, p, err := compactionRequestValues(d.Request()); err != nil || n != math.MaxInt64 || !p {
		t.Fatal("exact positive signed revision lost", err)
	}
	raw, err := EncodeRequest(d.Request())
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRequest(raw)
	if err != nil || decoded != d.Request() {
		t.Fatal("native request file changed original text")
	}
	if l.executes.Load() != 0 || l.connects.Load() != 0 {
		t.Fatal("form/file performed IO")
	}
	for _, body := range []string{`{}`, `{"revision":"0"}`, `{"revision":"-1"}`, `{"revision":"9223372036854775808"}`, `{"revision":9007199254740993.1}`, `{"revision":1e2}`, `{"revision":null}`, `{"revision":"1","revision":"2"}`, `{"revision":"1","physical":null}`, `{"revision":"1","physical":1}`, `{"revision":"1","key":"AA=="}`, `{"revision":"1","range_end":"AA=="}`} {
		r := contract.KVRequest{Method: CompactMethod, JSON: body}
		if _, _, err := compactionRequestValues(r); err == nil {
			t.Fatal("ambiguous or scoped compaction accepted", body)
		}
		if _, err := EncodeRequest(r); err == nil {
			t.Fatal("invalid Compact file accepted", body)
		}
	}
	plain, pd, _ := testState(t)
	for _, method := range plain.MethodsFor(pd) {
		if method.Path == CompactMethod {
			t.Fatal("absence of facet fabricated Compact")
		}
	}
	plain.SetMethod(pd, CompactMethod)
	pd.Editor.Buffer.Set(DefaultCompactionRequest().JSON)
	plain.Run(pd)
	if pd.confirmation != nil {
		t.Fatal("unsupported native maintenance published")
	}
	s.Run(d)
	if d.confirmation != nil || l.executes.Load() != 0 {
		t.Fatal("preview substituted preparation for original observation")
	}
}

func TestCompactionObservationConfirmationOriginalLeaseAndIndependentUnknown(t *testing.T) {
	s, d, l := compactionTestState(t)
	s.SetCompactionInputs(d, "9007199254740993", true)
	s.ObserveCompaction(d)
	consume(t, s, d)
	if l.executes.Load() != 2 || d.last != nil || !compactionObservationReady(d, d.compactionObservation) {
		t.Fatal("separate read sequence or original scope lost", d.Error())
	}
	copy, _ := d.CompactionObservation()
	copy.Scope.Prefix[0] ^= 1
	copy.Steps[0].Result.Native[0] ^= 1
	if d.compactionObservation.Scope.Prefix[0] != 0 || d.compactionObservation.Steps[0].Result.Native[0] != 1 {
		t.Fatal("observation aliases caller")
	}
	s.PrepareCompaction(d)
	s.Confirm(d, false)
	if d.confirmation == nil || l.executes.Load() != 2 {
		t.Fatal("unchecked preview dispatched")
	}
	if !s.GuardLeave(d, nil) {
		t.Fatal("original Compact confirmation missing leave guard")
	}
	s.DecideLeave(ui.Services{}, false)
	s.SetCompactionInputs(d, "9007199254740994", true)
	s.Confirm(d, true)
	s.PrepareCompaction(d)
	if d.confirmation != nil || l.executes.Load() != 2 {
		t.Fatal("changed revision reused observation")
	}
	s.SetCompactionInputs(d, "9007199254740993", true)
	s.ObserveCompaction(d)
	consume(t, s, d)
	s.PrepareCompaction(d)
	l.execute = func(_ context.Context, p contract.KVPreparation) (contract.KVResult, error) {
		if p.Writes() {
			l.lost.Store(true)
			return contract.KVResult{Request: p.Snapshot(), Attempted: true, WriteOutcomeUnknown: true, Code: "Unavailable"}, errors.New("original reply lost")
		}
		return compactionTestResult(l, p), nil
	}
	s.Confirm(d, true)
	consume(t, s, d)
	if d.unknown == nil || d.unknown.Compaction == nil || len(d.unknown.Compaction.Observation.Steps) != 2 || !d.NeedsRecovery() {
		t.Fatal("original unknown Compact lost full observation")
	}
	original := d.unknown.Clone()
	fresh := &compactionTestLease{&fakeLease{scope: testScope()}}
	fresh.execute = func(_ context.Context, p contract.KVPreparation) (contract.KVResult, error) {
		return compactionTestResult(fresh, p), nil
	}
	provider := &fakeProvider{lease: fresh.fakeLease, recover: func(old contract.KVRequestLease) (contract.KVRecovery, error) {
		old.Close()
		return contract.KVRecovery{Lease: fresh}, nil
	}}
	s.deps.ProviderFor = func(int64) (contract.KVRequestsProvider, error) { return provider, nil }
	s.Recover(d)
	consume(t, s, d)
	if fresh.executes.Load() != 0 || fresh.connects.Load() != 0 || compactionObservationReady(d, d.compactionObservation) {
		t.Fatal("recovery reused old physical observation or executed request")
	}
	s.ObserveCompaction(d)
	consume(t, s, d)
	s.PrepareCompaction(d)
	if fresh.executes.Load() != 2 || d.confirmation != nil || d.last != d.unknown || d.unknown.Authored != original.Authored {
		t.Fatal("later read cleared unknown Compact barrier")
	}
	d.selectedUnknown = true
	record, err := s.OperationRecord(d)
	if err != nil || !strings.Contains(string(record), `"write_outcome_unknown": true`) || !strings.Contains(string(record), `"original_read_receipts"`) || !strings.Contains(string(record), `"9007199254740993"`) {
		t.Fatal("complete unknown operation record changed", err)
	}
}

func TestCompactionSavedObservationRequiresOriginalLeaveDecision(t *testing.T) {
	s, d, _ := compactionTestState(t)
	s.ObserveCompaction(d)
	consume(t, s, d)
	d.saved = d.Request()
	if !s.GuardLeave(d, nil) {
		t.Fatal("saved request bypassed original observation leave guard")
	}
	s.DecideLeave(ui.Services{}, true)
	if s.GuardLeave(d, nil) {
		t.Fatal("unchanged observation invalidated explicit leave decision")
	}
	s.ObserveCompaction(d)
	consume(t, s, d)
	if !s.GuardLeave(d, nil) {
		t.Fatal("new observation reused earlier leave permission")
	}
}

func TestCompactionReadCancellationParkingAndSameGenerationRecovery(t *testing.T) {
	s, d, l := compactionTestState(t)
	entered, released, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(released) }) })
	l.execute = func(ctx context.Context, p contract.KVPreparation) (contract.KVResult, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-released
		return contract.KVResult{Request: p.Snapshot(), Attempted: true, Code: "Canceled"}, ctx.Err()
	}
	s.ObserveCompaction(d)
	await(t, entered)
	s.RequestCancel(d)
	await(t, canceled)
	s.Consume()
	if !d.Busy() || !d.CancelRequested() {
		t.Fatal("cancellation settled before original executor acknowledgement")
	}
	release.Do(func() { close(released) })
	consume(t, s, d)
	if d.compactionObservation.Captured || len(d.compactionObservation.Steps) != 1 || d.unknown != nil || d.last != nil {
		t.Fatal("partial original read became complete or a write receipt")
	}
	active := false
	s.deps.Active = func(*Document) bool { return active }
	l.execute = func(_ context.Context, p contract.KVPreparation) (contract.KVResult, error) {
		return compactionTestResult(l, p), nil
	}
	active = true
	s.ObserveCompaction(d)
	job := d.job
	active = false
	await(t, job.finished)
	s.Consume()
	if d.job != job || len(d.compactionObservation.Steps) != 1 {
		t.Fatal("parked completion admitted on another document")
	}
	active = true
	s.Consume()
	if !compactionObservationReady(d, d.compactionObservation) {
		t.Fatal("original parked read did not return")
	}
	old := d.compactionObservation.Clone()
	s.Close(d)
	if d.compactionObservation != nil || d.lease != nil || d.Editor != nil || !bytes.Equal(old.Steps[0].Result.Native, []byte{1, 2, 3}) {
		t.Fatal("close leaked caches or erased caller-owned receipts")
	}
}

func TestCompactionBilingualWideNarrowFormAndExplicitConfirmation(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			s, d, l := compactionTestState(t)
			s.SetCompactionInputs(d, "9007199254740993", false)
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			workspaceClick(t, tt, "等待物理压缩完成")
			if _, physical, err := compactionRequestValues(d.Request()); err != nil || !physical {
				t.Fatal("physical control did not preserve native request")
			}
			workspaceClick(t, tt, "检查 revision")
			consume(t, s, d)
			tt.Frame()
			workspaceClick(t, tt, "复制完整观察")
			want, _ := compactionObservationJSON(d.compactionObservation.Clone())
			if tt.Clipboard() != string(want) {
				t.Fatal("observation copy truncated")
			}
			workspaceClick(t, tt, "预览 Compact")
			if d.confirmation == nil || l.executes.Load() != 2 {
				t.Fatal("preview missing or auto-dispatched", d.Error())
			}
			workspaceClick(t, tt, "复制完整 Compact 预览")
			want, _ = compactionPreviewJSON(d.confirmation.compaction)
			if tt.Clipboard() != string(want) {
				t.Fatal("complete preview changed")
			}
			workspaceClick(t, tt, "我确认压缩此原集群的历史")
			workspaceClick(t, tt, "确认执行 Compact")
			consume(t, s, d)
			if l.executes.Load() != 3 || d.last == nil || d.last.Compaction == nil || !d.last.Result.Completed {
				t.Fatal("confirmed native Compact missing")
			}
			workspaceClick(t, tt, "复制完整 JSON")
			want, err := s.OperationRecord(d)
			if err != nil || tt.Clipboard() != string(want) {
				t.Fatal("operation copy lost original observation")
			}
		}
	}
}
