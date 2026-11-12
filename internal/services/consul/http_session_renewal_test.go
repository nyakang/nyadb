package consul

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const renewalTestID = "12345678-1234-4234-8234-123456789abc"
const renewalTestBody = `[{"ID":"` + renewalTestID + `","Name":"original","Node":"node","TTL":"10s","LockDelay":9007199254740993,"Behavior":"release","Extra":{"preserved":true}}]`

func renewalTestRequest() contract.KVHTTPSessionRenewalRequest {
	return contract.KVHTTPSessionRenewalRequest{SessionID: renewalTestID, Interval: 100 * time.Millisecond, MaxRenewals: 2, MaxBytes: 16 << 20}
}
func startSessionRenewal(t *testing.T, l contract.KVHTTPSessionRenewalLease, r contract.KVHTTPSessionRenewalRequest) contract.KVHTTPSessionRenewalRun {
	t.Helper()
	p, e := l.Prepare(r)
	if e != nil {
		t.Fatal(e)
	}
	w, e := l.Start(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	return w
}
func settledSessionRenewal(t *testing.T, w contract.KVHTTPSessionRenewalRun) contract.KVHTTPSessionRenewalSnapshot {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		s := w.Snapshot()
		if s.Ended {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatal("original renewal executor did not ACK")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestHTTPSessionRenewalOriginalBoundedAuthorizationAndCompleteEvidence(t *testing.T) {
	var puts atomic.Int32
	var previous time.Time
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			if r.URL.Path != "/v1/session/renew/"+renewalTestID {
				t.Error("foreign UUID")
			}
			if !previous.IsZero() && time.Since(previous) < 100*time.Millisecond {
				t.Error("interval was shortened")
			}
			previous = time.Now()
			puts.Add(1)
		} else if r.URL.Path != "/v1/session/info/"+renewalTestID {
			t.Error("foreign observation")
		}
		w.Write([]byte(renewalTestBody))
	})
	l, e := s.ReserveKVHTTPSessionRenewal(1)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	r := renewalTestRequest()
	before := calls.Load()
	p, e := l.Prepare(r)
	if e != nil || p.Snapshot() != r || calls.Load() != before {
		t.Fatal("preparation changed authorization or performed I/O", e)
	}
	other, _ := s.ReserveKVHTTPSessionRenewal(2)
	defer other.Close()
	if _, e = other.Start(context.Background(), p); !errors.Is(e, contract.ErrKVPlanUsed) {
		t.Fatal("foreign preparation allowed", e)
	}
	w, e := l.Start(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	defer w.Close()
	v := settledSessionRenewal(t, w)
	if puts.Load() != 2 || len(v.Observations) != 3 || v.Terminal.Reason != "renewal-limit" || v.Terminal.Attempts != 2 || v.Terminal.Acknowledged != 2 || v.Terminal.WriteOutcomeUnknown {
		t.Fatal("bounded run changed", v.Terminal)
	}
	for i, o := range v.Observations {
		if o.Sequence != i || !o.ProjectionComplete || o.Session.LockDelay != 9007199254740993 || !bytes.Contains(o.HTTP.Body, []byte(`"Extra"`)) {
			t.Fatal("complete native evidence lost")
		}
	}
	v.Observations[0].HTTP.Body[0] = 'x'
	v.Observations[0].HTTP.Header["X-Consul-Index"][0] = "1"
	got := w.Poll()
	if len(got.Observations) != 3 || got.Observations[0].HTTP.Body[0] != '[' || len(w.Poll().Observations) != 0 || w.Snapshot().Observations[0].HTTP.Header["X-Consul-Index"][0] == "1" {
		t.Fatal("original private archive mutated")
	}
	if _, e = l.Start(context.Background(), p); !errors.Is(e, contract.ErrKVBusy) {
		t.Fatal("archive overwritten")
	}
	if e = l.ClearReceipts(); e != nil {
		t.Fatal(e)
	}
	if _, e = l.Start(context.Background(), p); !errors.Is(e, contract.ErrKVPlanUsed) {
		t.Fatal("old authorization replayed")
	}
}
func TestHTTPSessionRenewalRejectsUnusableObservationBeforeWrites(t *testing.T) {
	for _, body := range []string{`[]`, `null`, strings.Replace(renewalTestBody, renewalTestID, testAgentID, 1), strings.Replace(renewalTestBody, `"10s"`, `"0s"`, 1), strings.Replace(renewalTestBody, `"10s"`, `"100ms"`, 1), strings.Replace(renewalTestBody, `9007199254740993`, `null`, 1), strings.Replace(renewalTestBody, `"TTL":"10s",`, "", 1), strings.Replace(renewalTestBody, `"Behavior":"release"`, `"Behavior":"delete","Behavior":"release"`, 1)} {
		t.Run(body, func(t *testing.T) {
			var puts atomic.Int32
			s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "PUT" {
					puts.Add(1)
				}
				w.Write([]byte(body))
			})
			l, _ := s.ReserveKVHTTPSessionRenewal(1)
			defer l.Close()
			w := startSessionRenewal(t, l, renewalTestRequest())
			defer w.Close()
			v := settledSessionRenewal(t, w)
			if puts.Load() != 0 || len(v.Observations) != 1 || v.Terminal.WriteOutcomeUnknown {
				t.Fatal("unusable observation authorized writes", v.Terminal)
			}
		})
	}
}
func TestHTTPSessionRenewalKnownChangedProjectionStopsWithoutRewritingACK(t *testing.T) {
	for _, mode := range []string{"ttl", "behavior", "missing", "denied"} {
		t.Run(mode, func(t *testing.T) {
			var puts atomic.Int32
			s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
				body := renewalTestBody
				if r.Method == "PUT" {
					puts.Add(1)
					switch mode {
					case "ttl":
						body = strings.Replace(body, `"10s"`, `"20s"`, 1)
					case "behavior":
						body = strings.Replace(body, `"release"`, `"delete"`, 1)
					case "missing":
						body = strings.Replace(body, `"TTL":"10s",`, "", 1)
					case "denied":
						w.WriteHeader(403)
						body = "denied"
					}
				}
				w.Write([]byte(body))
			})
			l, _ := s.ReserveKVHTTPSessionRenewal(1)
			defer l.Close()
			w := startSessionRenewal(t, l, renewalTestRequest())
			defer w.Close()
			v := settledSessionRenewal(t, w)
			if puts.Load() != 1 || len(v.Observations) != 2 || v.Terminal.WriteOutcomeUnknown || !v.Observations[1].HTTP.Acknowledged || v.Terminal.Reason == "renewal-limit" {
				t.Fatal("native ACK or projection changed", v.Terminal)
			}
		})
	}
}
func TestHTTPSessionRenewalBudgetsAndValidationPrecedeConnection(t *testing.T) {
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(renewalTestBody)) })
	l, _ := s.ReserveKVHTTPSessionRenewal(1)
	defer l.Close()
	base := renewalTestRequest()
	before := calls.Load()
	for _, change := range []func(*contract.KVHTTPSessionRenewalRequest){func(r *contract.KVHTTPSessionRenewalRequest) { r.SessionID = "1" }, func(r *contract.KVHTTPSessionRenewalRequest) { r.Interval = 0 }, func(r *contract.KVHTTPSessionRenewalRequest) { r.MaxRenewals = 257 }, func(r *contract.KVHTTPSessionRenewalRequest) { r.MaxBytes = 0 }} {
		r := base
		change(&r)
		if _, e := l.Prepare(r); !errors.Is(e, ErrInvalid) {
			t.Fatal("invalid authorization accepted")
		}
	}
	p, _ := l.Prepare(base)
	receiptBudgetMu.Lock()
	s.budget = s.retained + base.MaxBytes + pendingReceiptBudget - 1
	receiptBudgetMu.Unlock()
	if _, e := l.Start(context.Background(), p); !errors.Is(e, ErrBounds) || calls.Load() != before {
		t.Fatal("budget rejection performed I/O", e)
	}
	receiptBudgetMu.Lock()
	s.budget = sessionReceiptBudget
	receiptBudgetMu.Unlock()
	r := base
	r.MaxBytes = 1
	w := startSessionRenewal(t, l, r)
	defer w.Close()
	v := settledSessionRenewal(t, w)
	if !v.TerminalOverflow || v.Terminal.Reason != "byte-limit" || v.Terminal.Attempts != 0 || len(v.Observations) != 1 || string(v.Observations[0].HTTP.Body) != renewalTestBody {
		t.Fatal("terminal reserve lost preflight evidence", v.Terminal)
	}
}
func TestHTTPSessionRenewalUnknownSurvivesStopClearRecoveryAndLaterRead(t *testing.T) {
	for _, mode := range []string{"lost", "partial", "http500", "http408", "uuid"} {
		t.Run(mode, func(t *testing.T) {
			var puts atomic.Int32
			s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "PUT" {
					puts.Add(1)
					switch mode {
					case "lost":
						c, _, _ := w.(http.Hijacker).Hijack()
						c.Close()
						return
					case "partial":
						w.Header().Set("Content-Length", "1000")
						w.Write([]byte("[{"))
						return
					case "http500":
						w.WriteHeader(500)
					case "http408":
						w.WriteHeader(408)
					case "uuid":
						w.Write([]byte(strings.Replace(renewalTestBody, renewalTestID, testAgentID, 1)))
						return
					}
				}
				w.Write([]byte(renewalTestBody))
			})
			l, _ := s.ReserveKVHTTPSessionRenewal(1)
			defer l.Close()
			earlier, _ := l.Prepare(renewalTestRequest())
			w := startSessionRenewal(t, l, renewalTestRequest())
			v := settledSessionRenewal(t, w)
			if puts.Load() != 1 || v.Terminal.Reason != "write-unknown" || !v.Terminal.WriteOutcomeUnknown || !v.Observations[1].HTTP.WriteOutcomeUnknown {
				t.Fatal("unknown write missing", v.Terminal)
			}
			if e := w.Close(); !errors.Is(e, contract.ErrKVWriteUnknown) {
				t.Fatal("run close erased unknown", e)
			}
			before := calls.Load()
			recovery, e := s.RecoverKVHTTPSessionRenewal(l)
			if e != nil {
				t.Fatal(e)
			}
			defer recovery.Lease.Close()
			if calls.Load() != before || recovery.Renewal == nil || !recovery.Renewal.Terminal.WriteOutcomeUnknown {
				t.Fatal("recovery replayed/lost original")
			}
			if _, e = recovery.Lease.Start(context.Background(), earlier); !errors.Is(e, contract.ErrKVPlanUsed) {
				t.Fatal("old authorization transferred")
			}
			if _, e = recovery.Lease.Prepare(renewalTestRequest()); !errors.Is(e, contract.ErrKVWriteUnknown) {
				t.Fatal("recovery removed write barrier", e)
			}
			if e = recovery.Lease.ClearReceipts(); !errors.Is(e, contract.ErrKVWriteUnknown) {
				t.Fatal("clear removed unknown")
			}
			reader, _ := s.ReserveKV(2)
			defer reader.Close()
			executeDocument(t, reader, Request{Method: "GET", Path: "/v1/session/info/" + renewalTestID})
			if !bytes.Equal(recovery.Lease.LastRenewal().Observations[1].HTTP.Body, v.Observations[1].HTTP.Body) || !recovery.Lease.LastRenewal().Terminal.WriteOutcomeUnknown {
				t.Fatal("read promoted original unknown")
			}
		})
	}
}
func TestHTTPSessionRenewalStopAndCloseWaitForOriginalExecutor(t *testing.T) {
	s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(renewalTestBody)) })
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	s.opener = func(ctx context.Context) (*Client, OpenEvidence, error) {
		c, e, err := s.openCaptured(ctx)
		if err != nil {
			return c, e, err
		}
		original := c.http.Transport
		c.http.Transport = watchRoundTripper(func(r *http.Request) (*http.Response, error) {
			if r.Method == "PUT" {
				close(entered)
				<-release
				return nil, io.ErrUnexpectedEOF
			}
			return original.RoundTrip(r)
		})
		return c, e, nil
	}
	l, _ := s.ReserveKVHTTPSessionRenewal(1)
	defer func() { unblock(); l.Close() }()
	w := startSessionRenewal(t, l, renewalTestRequest())
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("PUT missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if e := w.Stop(ctx); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("stop invented ACK", e)
	}
	if w.Snapshot().Ended || w.(contract.KVHTTPSessionRenewalIndexedRun).PollStatus().Ended {
		t.Fatal("executor still pending")
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("session Close invented executor ACK")
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("session Close did not settle")
	}
	v := settledSessionRenewal(t, w)
	if !v.Terminal.WriteOutcomeUnknown || len(v.Observations) != 2 {
		t.Fatal("cancel lost original write evidence")
	}
}

func TestHTTPSessionRenewalSharesOriginalLeaseCapacityAndReleasesAccounting(t *testing.T) {
	s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(renewalTestBody)) })
	for i := uint64(1); i <= 30; i++ {
		l, e := s.ReserveKV(i)
		if e != nil {
			t.Fatal(e)
		}
		defer l.Close()
	}
	watch, e := s.ReserveKVHTTPWatch(1)
	if e != nil {
		t.Fatal(e)
	}
	defer watch.Close()
	l, e := s.ReserveKVHTTPSessionRenewal(1)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	if _, e = s.ReserveKVHTTPSessionRenewal(2); !errors.Is(e, contract.ErrKVLeaseLimit) {
		t.Fatal("renewal bypassed mixed capacity", e)
	}
	if _, e = s.ReserveKV(31); !errors.Is(e, contract.ErrKVLeaseLimit) {
		t.Fatal("request bypassed mixed capacity", e)
	}
	receiptBudgetMu.Lock()
	before := s.retained
	receiptBudgetMu.Unlock()
	w := startSessionRenewal(t, l, renewalTestRequest())
	settledSessionRenewal(t, w)
	if e = w.Close(); e != nil {
		t.Fatal(e)
	}
	receiptBudgetMu.Lock()
	retained, reserved := s.retained, s.reserved
	receiptBudgetMu.Unlock()
	if retained != before || reserved != 0 {
		t.Fatal("settled renewal budget leaked", retained, before, reserved)
	}
	l.Close()
	extra, e := s.ReserveKV(31)
	if e != nil {
		t.Fatal("closed renewal slot leaked", e)
	}
	extra.Close()
}

func TestHTTPSessionRenewalRecoveryWaitsForOriginalCompletionNotification(t *testing.T) {
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) { t.Error("recovery performed HTTP") })
	lease, e := s.ReserveKVHTTPSessionRenewal(1)
	if e != nil {
		t.Fatal(e)
	}
	old := lease.(*httpSessionRenewalLease)
	done := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(done) }) }
	defer func() { unblock(); old.Close() }()
	// Model the small interval after executor unlock and before its done signal.
	old.run = &httpSessionRenewalRun{lease: old, request: renewalTestRequest(), batch: contract.KVHTTPSessionRenewalBatch{Ended: true}, done: done, cancel: func() {}, pending: make(chan contract.KVHTTPSessionRenewalObservation, 3)}
	type outcome struct {
		recovery contract.KVHTTPSessionRenewalRecovery
		err      error
	}
	out := make(chan outcome, 1)
	before := calls.Load()
	go func() { r, e := s.RecoverKVHTTPSessionRenewal(old); out <- outcome{r, e} }()
	select {
	case result := <-out:
		if result.recovery.Lease != nil {
			result.recovery.Lease.Close()
		}
		t.Fatal("recovery returned before original done", result.err)
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	select {
	case result := <-out:
		if result.err != nil {
			t.Fatal(result.err)
		}
		defer result.recovery.Lease.Close()
		if result.recovery.Renewal == nil || !result.recovery.Renewal.Ended || calls.Load() != before {
			t.Fatal("recovery published active evidence or executed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recovery failed to ACK")
	}
}
