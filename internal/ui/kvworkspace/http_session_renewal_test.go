package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type fakeHTTPSessionRenewalPlan struct {
	r contract.KVHTTPSessionRenewalRequest
}

func (p fakeHTTPSessionRenewalPlan) Snapshot() contract.KVHTTPSessionRenewalRequest { return p.r }

type fakeHTTPSessionRenewalRun struct {
	mu                      sync.Mutex
	r                       contract.KVHTTPSessionRenewalRequest
	batch                   contract.KVHTTPSessionRenewalBatch
	polls, reads, snapshots atomic.Int32
	ack                     chan struct{}
}

func (w *fakeHTTPSessionRenewalRun) Request() contract.KVHTTPSessionRenewalRequest { return w.r }
func (w *fakeHTTPSessionRenewalRun) Poll() contract.KVHTTPSessionRenewalBatch {
	panic("indexed presentation copied full pending bodies")
}
func (w *fakeHTTPSessionRenewalRun) Snapshot() contract.KVHTTPSessionRenewalSnapshot {
	w.snapshots.Add(1)
	w.mu.Lock()
	defer w.mu.Unlock()
	return contract.KVHTTPSessionRenewalSnapshot{Request: w.r, KVHTTPSessionRenewalBatch: w.batch.Clone()}
}
func (w *fakeHTTPSessionRenewalRun) PollStatus() contract.KVHTTPSessionRenewalStatus {
	w.polls.Add(1)
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.batch
	return contract.KVHTTPSessionRenewalStatus{Observations: len(b.Observations), Ended: b.Ended, RetainedBytes: b.RetainedBytes, TerminalOverflow: b.TerminalOverflow, Terminal: b.Terminal}
}
func (w *fakeHTTPSessionRenewalRun) Observation(i int) (contract.KVHTTPSessionRenewalObservation, bool) {
	w.reads.Add(1)
	w.mu.Lock()
	defer w.mu.Unlock()
	if i < 0 || i >= len(w.batch.Observations) {
		return contract.KVHTTPSessionRenewalObservation{}, false
	}
	return w.batch.Observations[i].Clone(), true
}
func (w *fakeHTTPSessionRenewalRun) Stop(ctx context.Context) error {
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
func (w *fakeHTTPSessionRenewalRun) Close() error { return w.Stop(context.Background()) }

type fakeHTTPSessionRenewalLease struct {
	scope          contract.KVScope
	run            *fakeHTTPSessionRenewalRun
	starts, clears atomic.Int32
	lost           atomic.Bool
	start          func(context.Context, contract.KVHTTPSessionRenewalPreparation) (contract.KVHTTPSessionRenewalRun, error)
}

func (l *fakeHTTPSessionRenewalLease) Scope() contract.KVScope { return l.scope.Clone() }
func (l *fakeHTTPSessionRenewalLease) CheckKVLease() error {
	if l.lost.Load() {
		return contract.ErrRevoked
	}
	return nil
}
func (l *fakeHTTPSessionRenewalLease) Limits() contract.KVHTTPSessionRenewalLimits {
	return contract.KVHTTPSessionRenewalLimits{MinInterval: 100 * time.Millisecond, MaxInterval: time.Hour, RequestTimeout: 30 * time.Second, MaxRenewals: 256, MaxBytes: 256 << 20, TerminalBytes: 128 << 20}
}
func (l *fakeHTTPSessionRenewalLease) Prepare(r contract.KVHTTPSessionRenewalRequest) (contract.KVHTTPSessionRenewalPreparation, error) {
	return &fakeHTTPSessionRenewalPlan{r}, nil
}
func (l *fakeHTTPSessionRenewalLease) Start(ctx context.Context, p contract.KVHTTPSessionRenewalPreparation) (contract.KVHTTPSessionRenewalRun, error) {
	l.starts.Add(1)
	if l.start != nil {
		return l.start(ctx, p)
	}
	l.run.r = p.Snapshot()
	return l.run, nil
}
func (l *fakeHTTPSessionRenewalLease) LastRenewal() *contract.KVHTTPSessionRenewalSnapshot {
	s := l.run.Snapshot()
	return &s
}
func (l *fakeHTTPSessionRenewalLease) RetainedRenewal() contract.KVHTTPSessionRenewalRun {
	return l.run
}
func (l *fakeHTTPSessionRenewalLease) ClearReceipts() error { l.clears.Add(1); return nil }
func (l *fakeHTTPSessionRenewalLease) Close() error         { return l.run.Close() }

type fakeHTTPSessionRenewalProvider struct {
	lease                *fakeHTTPSessionRenewalLease
	recovered            *fakeHTTPSessionRenewalLease
	reserves, recoveries atomic.Int32
	recover              func(contract.KVHTTPSessionRenewalLease) (contract.KVHTTPSessionRenewalRecovery, error)
}

func (p *fakeHTTPSessionRenewalProvider) KVScope() contract.KVScope { return p.lease.Scope() }
func (p *fakeHTTPSessionRenewalProvider) ReserveKVHTTPSessionRenewal(uint64) (contract.KVHTTPSessionRenewalLease, error) {
	p.reserves.Add(1)
	return p.lease, nil
}
func (p *fakeHTTPSessionRenewalProvider) RecoverKVHTTPSessionRenewal(old contract.KVHTTPSessionRenewalLease) (contract.KVHTTPSessionRenewalRecovery, error) {
	p.recoveries.Add(1)
	if p.recover != nil {
		return p.recover(old)
	}
	return contract.KVHTTPSessionRenewalRecovery{Lease: p.recovered, Renewal: p.recovered.LastRenewal()}, nil
}

const renewalUUID = "01234567-89ab-cdef-0123-456789abcdef"

func httpSessionRenewalTest(t *testing.T) (*State, *Document, *fakeHTTPSessionRenewalProvider) {
	t.Helper()
	s, d, _ := testState(t)
	w := &fakeHTTPSessionRenewalRun{ack: make(chan struct{})}
	l := &fakeHTTPSessionRenewalLease{scope: d.scope.Clone(), run: w}
	p := &fakeHTTPSessionRenewalProvider{lease: l}
	s.deps.HTTPSessionRenewalProviderFor = func(id int64) (contract.KVHTTPSessionRenewalProvider, error) {
		if id != d.profileID {
			t.Error("foreign original profile")
		}
		return p, nil
	}
	d.httpSessionRenewalAvailable = true
	s.SetMethod(d, HTTPSessionRenewalMethod)
	d.Editor.Buffer.Set(encodeHTTPSessionRenewalInput(HTTPSessionRenewalInput{renewalUUID, "100ms", "2", "16777216"}).JSON)
	t.Cleanup(func() {
		select {
		case <-w.ack:
		default:
			close(w.ack)
		}
	})
	return s, d, p
}
func startHTTPSessionRenewal(t *testing.T, s *State, d *Document) {
	t.Helper()
	s.PrepareHTTPSessionRenewal(d)
	if _, ok := d.HTTPSessionRenewalConfirmation(); !ok {
		t.Fatal("missing immutable authorization", d.Error())
	}
	s.ConfirmHTTPSessionRenewal(d, true)
	eventEventually(t, func() bool { s.Consume(); return d.httpSessionRenewal.run != nil })
}
func renewalObservation(method, body string, sequence int) contract.KVHTTPSessionRenewalObservation {
	return contract.KVHTTPSessionRenewalObservation{
		Sequence: sequence, ReceivedAt: time.Unix(100, 123), ProjectionComplete: true,
		Session: contract.KVHTTPSessionRenewalSession{ID: renewalUUID, TTL: "10s", LockDelay: math.MaxUint64, Behavior: "release", Name: "original", Node: "node"},
		HTTP:    contract.KVHTTPResult{Request: contract.KVHTTPRequest{Method: method, Path: "/v1/session/renew/" + renewalUUID}, Body: []byte(body), Status: 200, Captured: true, EOF: true},
	}
}
func TestHTTPSessionRenewalRequestExactUUIDBudgetsAndDraftRoundTrip(t *testing.T) {
	input := HTTPSessionRenewalInput{renewalUUID, "1h", "256", "268435456"}
	base := encodeHTTPSessionRenewalInput(input)
	r, err := ParseHTTPSessionRenewalRequest(base.JSON)
	if err != nil || r.SessionID != renewalUUID || r.Interval != time.Hour || r.MaxRenewals != 256 || r.MaxBytes != 256<<20 {
		t.Fatal(r, err)
	}
	fields := map[string][]any{
		"session_id":   {"", strings.ToUpper(renewalUUID), "123", nil, 1},
		"interval":     {"99ms", "1h1ms", "", nil, 100},
		"max_renewals": {"0", "257", "1.5", "-1", "18446744073709551616", nil},
		"max_bytes":    {"0", "268435457", "-1", "1e2", nil},
	}
	for key, values := range fields {
		for _, value := range values {
			var m map[string]any
			_ = json.Unmarshal([]byte(base.JSON), &m)
			m[key] = value
			raw, _ := json.Marshal(m)
			if _, err := ParseHTTPSessionRenewalRequest(string(raw)); err == nil {
				t.Fatalf("accepted %s=%v", key, value)
			}
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(base.JSON), &m)
		delete(m, key)
		raw, _ := json.Marshal(m)
		if _, err := ParseHTTPSessionRenewalRequest(string(raw)); err == nil {
			t.Fatal("missing", key)
		}
	}
	for _, bad := range []string{strings.Replace(base.JSON, "{", `{"session_id":"duplicate",`, 1), strings.Replace(base.JSON, "{", `{"unknown":1,`, 1)} {
		if _, err := ParseHTTPSessionRenewalRequest(bad); err == nil {
			t.Fatal("accepted duplicate/unknown")
		}
	}
	numeric := strings.ReplaceAll(strings.ReplaceAll(base.JSON, `"256"`, `256`), `"268435456"`, `268435456`)
	if got, err := ParseHTTPSessionRenewalRequest(numeric); err != nil || got != r {
		t.Fatal(got, err)
	}
	input.Interval = "1"
	input.SessionID = "partial-"
	draft := encodeHTTPSessionRenewalInput(input)
	raw, err := EncodeRequest(draft)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRequest(raw)
	if err != nil || got != draft {
		t.Fatal("partial authored draft changed", err)
	}
	s, d, p := httpSessionRenewalTest(t)
	s.SetHTTPSessionRenewalInput(d, input)
	if got, err := httpSessionRenewalInput(d.Request().JSON); err != nil || got != input || p.reserves.Load() != 0 {
		t.Fatal("form validation performed I/O or lost partial input", err)
	}
}
func TestHTTPSessionRenewalConfirmationRevokesChangedAuthority(t *testing.T) {
	for _, change := range []string{"text", "method", "plan", "lease", "scope", "revoked", "unknown"} {
		t.Run(change, func(t *testing.T) {
			s, d, p := httpSessionRenewalTest(t)
			s.PrepareHTTPSessionRenewal(d)
			if p.lease.starts.Load() != 0 || d.httpSessionRenewal.confirmation == nil {
				t.Fatal("prepare executed")
			}
			s.ConfirmHTTPSessionRenewal(d, false)
			if d.Busy() {
				t.Fatal("unchecked execution")
			}
			w := d.httpSessionRenewal
			switch change {
			case "text":
				d.Editor.Buffer.Set(strings.Replace(d.Request().JSON, "100ms", "200ms", 1))
			case "method":
				s.SetMethod(d, "/etcdserverpb.KV/Put")
			case "plan":
				w.confirmation.plan.(*fakeHTTPSessionRenewalPlan).r.MaxRenewals++
			case "lease":
				w.lease = &fakeHTTPSessionRenewalLease{scope: w.scope, run: p.lease.run}
			case "scope":
				w.scope.HTTP.Datacenter = "other"
			case "revoked":
				p.lease.lost.Store(true)
			case "unknown":
				d.unknown = &Receipt{Result: contract.KVResult{WriteOutcomeUnknown: true}}
			}
			s.Consume()
			if _, ok := d.HTTPSessionRenewalConfirmation(); ok {
				t.Fatal("stale confirmation remained visible")
			}
			s.ConfirmHTTPSessionRenewal(d, true)
			if d.Busy() || p.lease.starts.Load() != 0 {
				t.Fatal("stale authority dispatched")
			}
		})
	}
}
func TestHTTPSessionRenewalInitialLeaseIdentityAndNilRun(t *testing.T) {
	s, d, p := httpSessionRenewalTest(t)
	p.lease.scope.Generation++
	s.PrepareHTTPSessionRenewal(d)
	if p.reserves.Load() != 0 || !d.NeedsRecovery() {
		t.Fatal("replacement bound without explicit recovery")
	}
	s, d, p = httpSessionRenewalTest(t)
	p.lease.start = func(context.Context, contract.KVHTTPSessionRenewalPreparation) (contract.KVHTTPSessionRenewalRun, error) {
		return nil, nil
	}
	s.PrepareHTTPSessionRenewal(d)
	s.ConfirmHTTPSessionRenewal(d, true)
	consume(t, s, d)
	if d.Error() == "" || d.httpSessionRenewalContent() {
		t.Fatal("nil executor reported success")
	}
}
func TestHTTPSessionRenewalParkedIndexedEvidenceAndStopAcknowledgement(t *testing.T) {
	s, d, p := httpSessionRenewalTest(t)
	active := true
	s.deps.Active = func(*Document) bool { return active }
	w := p.lease.run
	body := strings.Repeat("original-", 20000)
	w.batch = contract.KVHTTPSessionRenewalBatch{Observations: []contract.KVHTTPSessionRenewalObservation{renewalObservation("GET", body, 0), renewalObservation("PUT", body, 1)}, RetainedBytes: int64(2 * len(body))}
	s.PrepareHTTPSessionRenewal(d)
	s.ConfirmHTTPSessionRenewal(d, true)
	op := d.httpSessionRenewal.job
	await(t, op.finished)
	active = false
	s.Consume()
	if d.httpSessionRenewal.job != op || w.polls.Load() != 0 || w.snapshots.Load() != 0 {
		t.Fatal("parked original consumed")
	}
	active = true
	s.Consume()
	if w.polls.Load() != 1 || w.snapshots.Load() != 0 || len(d.httpSessionRenewal.preview) > previewLimit+5 || !strings.Contains(d.httpSessionRenewal.preview, "18446744073709551615") {
		t.Fatal("preview copied full archive, unbounded or narrowed LockDelay")
	}
	s.SelectHTTPSessionRenewalObservation(d, 0)
	if w.reads.Load() != 2 || w.snapshots.Load() != 0 {
		t.Fatal("selection copied full history")
	}
	s.StopHTTPSessionRenewal(d)
	s.Consume()
	if !d.Busy() || !d.CancelRequested() {
		t.Fatal("stop implied original executor ACK")
	}
	s.ClearHTTPSessionRenewal(d)
	if p.lease.clears.Load() != 0 {
		t.Fatal("cleared before ACK")
	}
	close(w.ack)
	consume(t, s, d)
	if !d.HTTPSessionRenewalStatus().Ended {
		t.Fatal("missing ACK")
	}
	s.PrepareHTTPSessionRenewal(d)
	if _, ok := d.HTTPSessionRenewalConfirmation(); ok {
		t.Fatal("silently replaced original evidence")
	}
	s.ClearHTTPSessionRenewal(d)
	if d.httpSessionRenewalContent() || p.lease.clears.Load() != 1 {
		t.Fatal("explicit clear failed")
	}
}
func TestHTTPSessionRenewalUnknownBarrierBothDirectionsAndOldPlans(t *testing.T) {
	s, d, p := httpSessionRenewalTest(t)
	d.unknown = &Receipt{Result: contract.KVResult{WriteOutcomeUnknown: true}}
	s.PrepareHTTPSessionRenewal(d)
	if p.reserves.Load() != 0 {
		t.Fatal("existing unknown accepted")
	}
	d.unknown = nil
	s.PrepareHTTPSessionRenewal(d)
	d.renewalUnknown = &RenewalReceipt{}
	s.ConfirmHTTPSessionRenewal(d, true)
	if d.Busy() || p.lease.starts.Load() != 0 {
		t.Fatal("new unknown bypassed prepared authority")
	}
	d.renewalUnknown = nil
	p.lease.run.batch = contract.KVHTTPSessionRenewalBatch{Ended: true, Terminal: contract.KVHTTPSessionRenewalTerminal{WriteOutcomeUnknown: true}, Observations: []contract.KVHTTPSessionRenewalObservation{renewalObservation("PUT", "lost", 1)}}
	startHTTPSessionRenewal(t, s, d)
	if !d.hasUnknownWrites() {
		t.Fatal("unknown renewal not sticky")
	}
	s.ClearHTTPSessionRenewal(d)
	if !d.httpSessionRenewalContent() || p.lease.clears.Load() != 0 {
		t.Fatal("unknown cleared")
	}
	s.SetMethod(d, "/etcdserverpb.KV/Put")
	d.Editor.Buffer.Set(`{"key":"YQ=="}`)
	s.Run(d)
	if _, ok := d.Confirmation(); ok || d.Busy() {
		t.Fatal("generic Put passed unknown barrier")
	}
	// Even a generic authority prepared before the unknown result cannot execute.
	d.confirmation = &confirmation{lease: d.lease, editor: d.Request(), plan: &fakePlan{request: d.Request(), write: true}}
	s.Confirm(d, true)
	if d.Busy() {
		t.Fatal("old generic authority bypassed unknown renewal")
	}
	s.SetMethod(d, "/etcdserverpb.KV/Range")
	d.Editor.Buffer.Set(`{"key":"YQ=="}`)
	s.Run(d)
	consume(t, s, d)
	if d.last == nil || !d.hasUnknownWrites() {
		t.Fatal("later read cleared unknown renewal")
	}
	s.SetMethod(d, HTTPSessionRenewalMethod)
	s.PrepareHTTPSessionRenewal(d)
	if p.lease.starts.Load() != 1 || d.httpSessionRenewal.confirmation != nil {
		t.Fatal("read authorized replay")
	}
}

// A provider without optional indexed archives must still transfer complete evidence.
type renewalSnapshotLease struct {
	contract.KVHTTPSessionRenewalLease
}

func TestHTTPSessionRenewalRecoveryRetainsOriginalArchiveWithoutExecution(t *testing.T) {
	for _, mode := range []string{"archive", "cancelled", "snapshot", "snapshot-cancelled", "snapshot-error"} {
		t.Run(mode, func(t *testing.T) {
			s, d, p := httpSessionRenewalTest(t)
			w := p.lease.run
			w.batch = contract.KVHTTPSessionRenewalBatch{Ended: true, Terminal: contract.KVHTTPSessionRenewalTerminal{WriteOutcomeUnknown: true}, Observations: []contract.KVHTTPSessionRenewalObservation{renewalObservation("PUT", "original evidence", 1)}}
			close(w.ack)
			startHTTPSessionRenewal(t, s, d)
			want, _ := s.HTTPSessionRenewalRecord(d)
			original := d.Request()
			s.DisconnectProfile(d.profileID)
			next := &fakeHTTPSessionRenewalLease{scope: d.scope.Clone(), run: w}
			next.scope.Generation++
			p.lease = next
			entered, release := make(chan struct{}), make(chan struct{})
			p.recover = func(contract.KVHTTPSessionRenewalLease) (contract.KVHTTPSessionRenewalRecovery, error) {
				r := contract.KVHTTPSessionRenewalRecovery{Lease: next, Renewal: next.LastRenewal()}
				if strings.HasPrefix(mode, "snapshot") {
					r.Lease = renewalSnapshotLease{next}
				}
				close(entered)
				<-release
				if mode == "snapshot-error" {
					return r, errors.New("transfer failed")
				}
				return r, nil
			}
			s.RecoverHTTPSessionRenewal(d)
			await(t, entered)
			cancelled := strings.Contains(mode, "cancelled")
			if cancelled {
				s.RequestCancel(d)
				if !d.Busy() {
					t.Fatal("cancel implied transfer ACK")
				}
			}
			close(release)
			consume(t, s, d)
			got, err := s.HTTPSessionRenewalRecord(d)
			if err != nil || !bytes.Equal(want, got) || d.Request() != original || !d.hasUnknownWrites() || next.starts.Load() != 0 {
				t.Fatal("transfer changed evidence, replayed, or erased unknown", err)
			}
			if d.NeedsRecovery() != (cancelled || mode == "snapshot-error") {
				t.Fatal("wrong recovery state")
			}
			if mode != "snapshot-error" && d.Scope().Generation != next.scope.Generation {
				t.Fatal("new generation missing")
			}
			if d.httpSessionRenewal.evidenceScope.Generation == next.scope.Generation {
				t.Fatal("old evidence relabeled")
			}
		})
	}
}
func TestHTTPSessionRenewalExportCapturesCompleteOptionalProjectionBeforePicker(t *testing.T) {
	s, d, p := httpSessionRenewalTest(t)
	w := p.lease.run
	complete := renewalObservation("PUT", strings.Repeat("original-", 20000), 1)
	incomplete := renewalObservation("PUT", "incomplete", 2)
	incomplete.ProjectionComplete = false
	w.batch = contract.KVHTTPSessionRenewalBatch{Ended: true, Observations: []contract.KVHTTPSessionRenewalObservation{complete, incomplete}}
	startHTTPSessionRenewal(t, s, d)
	want, _ := s.HTTPSessionRenewalRecord(d)
	entered, release := make(chan struct{}), make(chan struct{})
	var written bytes.Buffer
	s.SetFiles(Files{SavePath: func(string) (string, error) { close(entered); <-release; return "original.json", nil }, WriteStream: func(_ string, fn func(io.Writer) error) error { return fn(&written) }})
	s.ExportHTTPSessionRenewalRecord(d)
	await(t, entered)
	w.mu.Lock()
	w.batch.Observations[0].HTTP.Body = []byte("changed after capture")
	w.mu.Unlock()
	s.ClearHTTPSessionRenewal(d)
	if p.lease.clears.Load() != 0 {
		t.Fatal("evidence released during export")
	}
	s.SetMethod(d, "/etcdserverpb.KV/Range")
	s.DisconnectProfile(d.profileID)
	close(release)
	consume(t, s, d)
	if !bytes.Equal(want, written.Bytes()) {
		t.Fatal("export substituted current evidence")
	}
	var record struct {
		Request      HTTPSessionRenewalInput `json:"captured_request"`
		Observations []struct {
			Original struct{ HTTP struct{ Body []byte } } `json:"original_http"`
			Session  map[string]string                    `json:"native_session"`
		}
	}
	if err := json.Unmarshal(want, &record); err != nil {
		t.Fatal(err)
	}
	if record.Request.SessionID != renewalUUID || len(record.Observations) != 2 || !bytes.Equal(record.Observations[0].Original.HTTP.Body, complete.HTTP.Body) || record.Observations[0].Session["lock_delay"] != "18446744073709551615" || record.Observations[1].Session != nil {
		t.Fatal("lost body, UUID, precise LockDelay or invented projection")
	}
}
func TestHTTPSessionRenewalLeaveRevisionAndCloseWaitForOriginalExecutor(t *testing.T) {
	s, d, p := httpSessionRenewalTest(t)
	w := p.lease.run
	w.batch.Ended = true
	startHTTPSessionRenewal(t, s, d)
	d.saved = d.Request()
	if !s.GuardLeave(d, nil) {
		t.Fatal("settled evidence omitted")
	}
	s.DecideLeave(ui.Services{}, false)
	if !d.httpSessionRenewalContent() {
		t.Fatal("leave cancellation discarded")
	}
	s.GuardLeave(d, nil)
	s.DecideLeave(ui.Services{}, true)
	if s.GuardLeave(d, nil) {
		t.Fatal("unchanged permit invalid")
	}
	w.mu.Lock()
	w.batch.RetainedBytes++
	w.mu.Unlock()
	s.Consume()
	if !s.GuardLeave(d, nil) {
		t.Fatal("new evidence retained old permit")
	}
	s.DecideLeave(ui.Services{}, false)
	done := make(chan struct{})
	go func() { s.Close(d); close(done) }()
	select {
	case <-done:
		t.Fatal("close did not await executor")
	case <-time.After(20 * time.Millisecond):
	}
	close(w.ack)
	await(t, done)
	if d.httpSessionRenewal != nil || d.Editor != nil || s.documents[d.id] != nil {
		t.Fatal("close retained caches")
	}
}
func TestHTTPSessionRenewalParkedCopyAndCancelledExportKeepCapturedEvidence(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancel), func(t *testing.T) {
			s, d, p := httpSessionRenewalTest(t)
			w := p.lease.run
			w.batch = contract.KVHTTPSessionRenewalBatch{Ended: true, Observations: []contract.KVHTTPSessionRenewalObservation{renewalObservation("GET", "original complete body", 0)}}
			startHTTPSessionRenewal(t, s, d)
			want, _ := s.HTTPSessionRenewalRecord(d)
			active := true
			s.deps.Active = func(*Document) bool { return active }
			var services ui.Services
			tt := ui.NewTester(func(c *ui.Context) { services = c.Services() }, 720, 520)
			tt.Frame()
			services.WriteClipboard("unchanged")
			s.CopyHTTPSessionRenewalRecord(services, d)
			op := d.io
			active = false
			await(t, op.finished)
			w.mu.Lock()
			w.batch.Observations[0].HTTP.Body = []byte("later native body")
			w.mu.Unlock()
			s.Consume()
			if d.io != op || tt.Clipboard() != "unchanged" {
				t.Fatal("parked copy admitted")
			}
			if cancel {
				s.CancelFile(d)
			}
			active = true
			s.Consume()
			expected := string(want)
			if cancel {
				expected = "unchanged"
			}
			if tt.Clipboard() != expected {
				t.Fatal("copy did not preserve capture/cancellation")
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var wrote atomic.Bool
			s.SetFiles(Files{SavePath: func(string) (string, error) { close(entered); <-release; return "original.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error { wrote.Store(true); return write(io.Discard) }})
			s.ExportHTTPSessionRenewalRecord(d)
			await(t, entered)
			s.CancelFile(d)
			close(release)
			consume(t, s, d)
			if wrote.Load() || !d.httpSessionRenewalContent() {
				t.Fatal("cancelled picker wrote or dropped evidence")
			}
		})
	}
}
func TestHTTPSessionRenewalCancelledStartKeepsCancelingUntilOriginalACK(t *testing.T) {
	s, d, p := httpSessionRenewalTest(t)
	s.PrepareHTTPSessionRenewal(d)
	s.ConfirmHTTPSessionRenewal(d, true)
	op := d.httpSessionRenewal.job
	await(t, op.finished)
	s.RequestCancel(d)
	s.Consume()
	if !d.Busy() || !d.CancelRequested() || d.httpSessionRenewal.run == nil {
		t.Fatal("cancelled start forgot pending original ACK")
	}
	close(p.lease.run.ack)
	p.lease.run.mu.Lock()
	p.lease.run.batch.Ended = true
	p.lease.run.mu.Unlock()
	s.Consume()
	if d.Busy() || d.CancelRequested() {
		t.Fatal("acknowledged cancelled start not settled")
	}
}
