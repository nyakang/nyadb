package consul

import (
	"bytes"
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func testHTTPWatchRequest() contract.KVHTTPWatchRequest {
	return contract.KVHTTPWatchRequest{Target: "watch/key", ObservationPrefix: "watch", Wait: 100 * time.Millisecond, MaxObservations: 2, MaxBytes: 8 << 20}
}

func startHTTPWatch(t *testing.T, l contract.KVHTTPWatchLease, r contract.KVHTTPWatchRequest) contract.KVHTTPWatchRun {
	t.Helper()
	p, err := l.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	w, err := l.Start(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

func settledHTTPWatch(t *testing.T, w contract.KVHTTPWatchRun) contract.KVHTTPWatchSnapshot {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		s := w.Snapshot()
		if s.Ended {
			return s
		}
		select {
		case <-deadline.C:
			t.Fatal("original HTTP Watch did not settle")
		case <-ticker.C:
		}
	}
}

func TestHTTPWatchPurePreparationOriginalTargetAndIndependentWriteLease(t *testing.T) {
	var reads atomic.Int32
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v1/kv/watch" || r.URL.Query().Get("wait") != "100ms" || !r.URL.Query().Has("recurse") {
			t.Error("blocking request changed authored native range/wait")
		}
		w.Write([]byte(`[{"Key":"watch/key//猫\n?%","Value":"AP8=","Flags":18446744073709551615},{"Key":"watch/unrelated","Value":""}]`))
	})
	l, err := s.ReserveKVHTTPWatch(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	writer, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal("Watch consumed the independent write document ID", err)
	}
	defer writer.Close()
	writer.(*documentLease).writeUnknown.Store(true)
	before := calls.Load()
	r := testHTTPWatchRequest()
	r.Target = "watch/key//猫\n?%"
	r.MaxObservations = 1
	r.StartIndex = math.MaxUint64
	p, err := l.Prepare(r)
	if err != nil || p.Snapshot() != r || calls.Load() != before {
		t.Fatal("prepare performed I/O/normalized target", err)
	}
	foreign, _ := s.ReserveKVHTTPWatch(2)
	defer foreign.Close()
	if _, err = foreign.Start(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || calls.Load() != before {
		t.Fatal("foreign original preparation dispatched")
	}
	w, err := l.Start(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	got := settledHTTPWatch(t, w)
	if got.Request != r || got.Terminal.Reason != "observation-limit" || reads.Load() != 1 || len(got.Observations) != 1 {
		t.Fatal("bounded original run changed", got.Terminal, reads.Load())
	}
	o := got.Observations[0]
	if !o.ProjectionComplete || len(o.Matches) != 1 || o.Matches[0] != 0 || o.HTTP.Entries[0].Key != r.Target || !bytes.Equal(o.HTTP.Entries[0].Value, []byte{0, 255}) || o.HTTP.Entries[0].Flags != math.MaxUint64 || *o.HTTP.Metadata.Index != math.MaxUint64 || o.IndexRelation != "unchanged" {
		t.Fatal("original target/bytes/uint64 metadata changed")
	}
	got.Observations[0].HTTP.Body[0] = 'x'
	got.Observations[0].HTTP.Entries[0].Value[0] = 9
	*got.Observations[0].HTTP.Metadata.Index = 2
	got.Observations[0].Matches[0] = 1
	batch := w.Poll()
	if len(batch.Observations) != 1 || batch.Observations[0].HTTP.Body[0] != '[' || batch.Observations[0].HTTP.Entries[0].Value[0] != 0 || *batch.Observations[0].HTTP.Metadata.Index != math.MaxUint64 || batch.Observations[0].Matches[0] != 0 || len(w.Poll().Observations) != 0 {
		t.Fatal("snapshot mutation changed private archive/buffer")
	}
	if !writer.(*documentLease).writeUnknown.Load() {
		t.Fatal("successful Watch settled unrelated unknown write")
	}
}

func TestHTTPWatchInvalidAuthorityWaitAndBoundsRejectedBeforeIO(t *testing.T) {
	s, calls := documentSession(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid Watch performed I/O") })
	l, _ := s.ReserveKVHTTPWatch(1)
	defer l.Close()
	before := calls.Load()
	cases := []func(*contract.KVHTTPWatchRequest){
		func(r *contract.KVHTTPWatchRequest) { r.Target = "" },
		func(r *contract.KVHTTPWatchRequest) { r.Target = "other/key" },
		func(r *contract.KVHTTPWatchRequest) { r.ObservationPrefix = "watch//"; r.Target = "watch//key" },
		func(r *contract.KVHTTPWatchRequest) { r.ObservationPrefix = "watch/./"; r.Target = "watch/./key" },
		func(r *contract.KVHTTPWatchRequest) { r.Wait = 99 * time.Millisecond },
		func(r *contract.KVHTTPWatchRequest) { r.Wait = 10*time.Minute + 1 },
		func(r *contract.KVHTTPWatchRequest) { r.Wait = 20 * time.Second },
		func(r *contract.KVHTTPWatchRequest) { r.MaxObservations = 0 },
		func(r *contract.KVHTTPWatchRequest) { r.MaxObservations = 257 },
		func(r *contract.KVHTTPWatchRequest) { r.MaxBytes = 0 },
		func(r *contract.KVHTTPWatchRequest) { r.MaxBytes = httpWatchMaxBytes + 1 },
		func(r *contract.KVHTTPWatchRequest) { r.Target = "watch/a\x00b" },
	}
	for i, change := range cases {
		r := testHTTPWatchRequest()
		change(&r)
		if _, err := l.Prepare(r); err == nil {
			t.Fatalf("invalid authored case %d accepted", i)
		}
	}
	r := testHTTPWatchRequest()
	r.Target = ""
	r.Prefix = true
	r.ObservationPrefix = ""
	if _, err := l.Prepare(r); err != nil {
		t.Fatal("explicit root prefix rejected", err)
	}
	if calls.Load() != before {
		t.Fatal("validation read native state")
	}
	for i := uint64(2); i <= 32; i++ {
		q, err := s.ReserveKVHTTPWatch(i)
		if err != nil {
			t.Fatal(err)
		}
		defer q.Close()
	}
	if _, err := s.ReserveKV(99); !errors.Is(err, contract.ErrKVLeaseLimit) {
		t.Fatal("request lease ignored Watch limit", err)
	}
	if _, err := s.ReserveDataDocument(s.KVScope().Namespace.Object("keyspace", "keys")); !errors.Is(err, contract.ErrKVLeaseLimit) {
		t.Fatal("data lease ignored Watch limit", err)
	}
}

func TestHTTPWatchIndexFailuresPreserveOriginalResponseAndStop(t *testing.T) {
	for _, test := range []struct{ name, index, leader, reason string }{
		{"missing", "", "true", "index-unavailable"}, {"zero", "0", "true", "index-zero"}, {"backward", "8", "true", "index-backward"},
		{"invalid", "18446744073709551616", "true", "read-failed"}, {"bad-leader", "9", "maybe", "read-failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var reads atomic.Int32
			s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
				reads.Add(1)
				w.Header().Del("X-Consul-Index")
				if test.index != "" {
					w.Header().Set("X-Consul-Index", test.index)
				}
				w.Header().Set("X-Consul-Knownleader", test.leader)
				w.Write([]byte(`[{"Key":"watch/key","Value":""}]`))
			})
			l, _ := s.ReserveKVHTTPWatch(1)
			defer l.Close()
			r := testHTTPWatchRequest()
			r.StartIndex = 9
			r.MaxObservations = 20
			got := settledHTTPWatch(t, startHTTPWatch(t, l, r))
			if reads.Load() != 1 || len(got.Observations) != 1 || got.Terminal.Reason != test.reason || !got.Observations[0].HTTP.Captured || len(got.Observations[0].HTTP.Body) == 0 {
				t.Fatal("original invalid index discarded/repeated", got.Terminal)
			}
		})
	}
}

func TestHTTPWatchNativeFailurePartialDeniedAndTimeoutNeverReconnect(t *testing.T) {
	for _, mode := range []string{"lost", "partial", "denied", "http408", "http500", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			var reads atomic.Int32
			s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
				reads.Add(1)
				switch mode {
				case "lost", "partial":
					c, buf, _ := w.(http.Hijacker).Hijack()
					if mode == "partial" {
						buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\nX-Consul-Index: 9\r\n\r\n[{\"Key\":\"watch/key\"")
						buf.Flush()
					}
					c.Close()
				case "denied":
					w.WriteHeader(403)
					w.Write([]byte("Permission denied"))
				case "http408":
					w.WriteHeader(408)
					w.Write([]byte("original timeout"))
				case "http500":
					w.WriteHeader(500)
					w.Write([]byte("original error"))
				case "timeout":
					<-r.Context().Done()
				}
			})

			if mode == "timeout" {
				cfg := s.cfg
				cfg.ConnectTimeout, cfg.RequestTimeout = time.Second, 1200*time.Millisecond
				timed, err := OpenNativeSession(context.Background(), 1, cfg, "")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { timed.Close() })
				s = timed
			}
			var opens atomic.Int32
			s.opener = func(ctx context.Context) (*Client, OpenEvidence, error) { opens.Add(1); return s.openCaptured(ctx) }
			l, _ := s.ReserveKVHTTPWatch(1)
			defer l.Close()
			r := testHTTPWatchRequest()
			r.MaxObservations = 30
			p, _ := l.Prepare(r)
			requestDeadline := 300 * time.Millisecond
			if mode == "timeout" {
				requestDeadline = 2 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), requestDeadline)
			defer cancel()
			w, err := l.Start(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			got := settledHTTPWatch(t, w)
			if reads.Load() != 1 || opens.Load() != 1 || got.Terminal.Reason != "read-failed" || len(got.Observations) != 1 {
				t.Fatal("original failed read reconnected or replayed", got.Terminal, reads.Load(), opens.Load())
			}
			o := got.Observations[0]
			if !o.HTTP.Attempted || !o.HTTP.Ended || o.HTTP.WriteOutcomeUnknown || o.ProjectionComplete {
				t.Fatal("read failure fabricated write/result status")
			}
			if mode == "partial" && (!bytes.HasPrefix(o.HTTP.Body, []byte(`[{"Key"`)) || o.HTTP.Captured || o.HTTP.EOF) {
				t.Fatal("partial original body lost")
			}
			if _, err = l.Start(context.Background(), p); err == nil || reads.Load() != 1 || opens.Load() != 1 {
				t.Fatal("failed preparation implicitly replayed")
			}
		})
	}
}

type watchRoundTripper func(*http.Request) (*http.Response, error)

func (f watchRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTTPWatchStopCloseAndRetirementWaitOriginalExecutorACK(t *testing.T) {
	for _, mode := range []string{"stop", "lease-close", "session-close"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := documentSession(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected native dispatch") })
			entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			s.opener = func(ctx context.Context) (*Client, OpenEvidence, error) {
				c := newCapturedClient(s.cfg, nil, "")
				c.cache = false
				c.http.Transport = watchRoundTripper(func(r *http.Request) (*http.Response, error) {
					close(entered)
					<-r.Context().Done()
					close(canceled)
					<-release
					return nil, r.Context().Err()
				})
				return c, s.evidence, nil
			}
			l, _ := s.ReserveKVHTTPWatch(1)
			defer l.Close()
			w := startHTTPWatch(t, l, testHTTPWatchRequest())
			<-entered
			ended := make(chan error, 1)
			go func() {
				switch mode {
				case "stop":
					ended <- w.Stop(context.Background())
				case "lease-close":
					ended <- l.Close()
				case "session-close":
					ended <- s.Close()
				}
			}()
			<-canceled
			select {
			case <-ended:
				t.Fatal("cancellation claimed original ACK before executor returned")
			case <-time.After(20 * time.Millisecond):
			}
			close(release)
			if err := <-ended; err != nil {
				t.Fatal(err)
			}
			got := settledHTTPWatch(t, w)
			if mode != "lease-close" && (len(got.Observations) != 1 || !got.Observations[0].HTTP.Ended || !got.Observations[0].HTTP.Canceled) {
				t.Fatal("retirement erased original canceled receipt")
			}
			if mode == "lease-close" && len(got.Observations) != 0 {
				t.Fatal("document close retained caches")
			}
		})
	}
}

func TestHTTPWatchBoundsReserveBeforeIOAndRetainCompleteTerminalOverflow(t *testing.T) {
	var reads atomic.Int32
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		w.Write([]byte(`[{"Key":"watch/key","Value":"AP8="}]`))
	})
	l, _ := s.ReserveKVHTTPWatch(1)
	defer l.Close()
	r := testHTTPWatchRequest()
	r.MaxBytes = 1
	r.MaxObservations = 200
	p, _ := l.Prepare(r)
	before := calls.Load()
	s.budget = s.rootBytes + pendingReceiptBudget
	if _, err := l.Start(context.Background(), p); !errors.Is(err, ErrBounds) || calls.Load() != before {
		t.Fatal("bounds were checked after opening", err)
	}
	s.budget = sessionReceiptBudget
	w, err := l.Start(context.Background(), p)
	if err != nil {
		t.Fatal("rejected reservation consumed pure preparation", err)
	}
	defer w.Close()
	got := settledHTTPWatch(t, w)
	if got.Terminal.Reason != "byte-limit" || !got.TerminalOverflow || len(got.Observations) != 1 || reads.Load() != 1 || !got.Observations[0].HTTP.Captured || !bytes.Equal(got.Observations[0].HTTP.Entries[0].Value, []byte{0, 255}) || got.RetainedBytes <= 1 || got.RetainedBytes > pendingReceiptBudget+1 {
		t.Fatal("complete overflow was dropped/truncated/repeated", got.Terminal)
	}
	if err = w.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	receiptBudgetMu.Lock()
	retained, reserved := s.retained, s.reserved
	receiptBudgetMu.Unlock()
	if reserved != 0 || retained != s.rootBytes+got.RetainedBytes {
		t.Fatal("settled reservation/accounting leaked", reserved, retained)
	}
	if err = l.ClearObservations(); err != nil {
		t.Fatal(err)
	}
	receiptBudgetMu.Lock()
	retained = s.retained
	receiptBudgetMu.Unlock()
	if retained != s.rootBytes || l.LastWatch() != nil || len(w.Poll().Observations) != 0 {
		t.Fatal("explicit release retained original bytes")
	}
	if _, err = l.Start(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("old preparation replayed after clear", err)
	}
}

func TestHTTPWatchSequentialOriginalIndicesACLFilteredAndFiniteBuffer(t *testing.T) {
	var reads atomic.Int32
	s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		n := reads.Add(1)
		if got := r.URL.Query().Get("index"); got != strconv.Itoa(int(n-1)) {
			t.Errorf("native index changed: %s", got)
		}
		w.Header().Set("X-Consul-Index", strconv.Itoa(int(n)))
		w.Header().Set("X-Consul-Results-Filtered-By-Acls", "true")
		w.Write([]byte(`[{"Key":"watch/key","Value":null},{"Key":"watch/key2","Value":""},{"Key":"watch/unrelated","Value":"eA=="}]`))
	})
	l, _ := s.ReserveKVHTTPWatch(1)
	defer l.Close()
	r := testHTTPWatchRequest()
	r.Prefix = true
	r.MaxObservations = 3
	w := startHTTPWatch(t, l, r)
	got := settledHTTPWatch(t, w)
	if reads.Load() != 3 || len(got.Observations) != 3 || got.Terminal.Reason != "observation-limit" {
		t.Fatal("finite buffer changed/lost reads")
	}
	for i, o := range got.Observations {
		if !o.ProjectionComplete || o.HTTP.Metadata.ACLFiltered == nil || !*o.HTTP.Metadata.ACLFiltered || len(o.Matches) != 2 || !o.HTTP.Entries[0].ValueNull || !o.HTTP.Entries[1].ValuePresent || o.HTTP.Entries[1].ValueNull || o.IndexRelation != []string{"initial", "advanced", "advanced"}[i] {
			t.Fatal("ACL gaps/empty/null/native index fabricated")
		}
	}
	if len(w.Poll().Observations) != 3 || len(w.Snapshot().Observations) != 3 {
		t.Fatal("Poll discarded archived complete evidence")
	}
}

func TestHTTPWatchEvidenceOnlyRecoveryTransfersBudgetsWithoutIO(t *testing.T) {
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[{"Key":"watch/key","Value":"AP8="}]`)) })
	l, _ := s.ReserveKVHTTPWatch(1)
	defer l.Close()
	r := testHTTPWatchRequest()
	r.MaxObservations = 1
	w := startHTTPWatch(t, l, r)
	original := settledHTTPWatch(t, w)
	w.Stop(context.Background())
	before := calls.Load()
	fresh, err := OpenNativeSession(context.Background(), 1, s.cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	before = calls.Load()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	recovery, err := fresh.RecoverKVHTTPWatch(l)
	if err != nil || recovery.Lease == nil || recovery.Watch == nil || calls.Load() != before || len(recovery.Watch.Observations) != 1 {
		t.Fatal("recovery executed original/new Watch", err)
	}
	defer recovery.Lease.Close()
	if l.LastWatch() != nil || recovery.Lease.LastWatch() == nil || recovery.Watch.Request != r || !bytes.Equal(recovery.Watch.Observations[0].HTTP.Body, original.Observations[0].HTTP.Body) {
		t.Fatal("original recovery evidence lost")
	}
	if s.retained != 0 || fresh.retained != fresh.rootBytes+original.RetainedBytes {
		t.Fatal("original session transfer accounting incorrect")
	}
	p, err := recovery.Lease.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recovery.Lease.Start(context.Background(), p); !errors.Is(err, contract.ErrKVBusy) || calls.Load() != before {
		t.Fatal("recovery resumed without explicit archive release")
	}
	if _, err = fresh.RecoverKVHTTPWatch(l); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("original evidence transfer replayed")
	}
	if err = recovery.Lease.ClearObservations(); err != nil {
		t.Fatal(err)
	}
	if fresh.retained != fresh.rootBytes {
		t.Fatal("fresh explicit clear failed")
	}
}

func TestHTTPWatchStopDeadlineDoesNotClaimOriginalAcknowledgement(t *testing.T) {
	s, _ := documentSession(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected dispatch") })
	entered, release := make(chan struct{}), make(chan struct{})
	s.opener = func(context.Context) (*Client, OpenEvidence, error) {
		c := newCapturedClient(s.cfg, nil, "")
		c.cache = false
		c.http.Transport = watchRoundTripper(func(r *http.Request) (*http.Response, error) {
			close(entered)
			<-r.Context().Done()
			<-release
			return nil, r.Context().Err()
		})
		return c, s.evidence, nil
	}
	l, _ := s.ReserveKVHTTPWatch(1)
	defer l.Close()
	w := startHTTPWatch(t, l, testHTTPWatchRequest())
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := w.Stop(ctx)
	ended := w.Snapshot().Ended
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) || ended {
		t.Fatal("caller deadline became original executor acknowledgement", err)
	}
	if err = w.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !w.Snapshot().Ended {
		t.Fatal("original acknowledgement missing")
	}
}

func TestHTTPWatchLaterByteLimitRetainsEveryOriginalObservation(t *testing.T) {
	var reads atomic.Int32
	s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		w.Write([]byte(`[{"Key":"watch/key","Value":"AP8="}]`))
	})
	l, _ := s.ReserveKVHTTPWatch(1)
	defer l.Close()
	r := testHTTPWatchRequest()
	r.MaxBytes, r.MaxObservations = 2<<20, 100
	w := startHTTPWatch(t, l, r)
	got := settledHTTPWatch(t, w)
	if got.Terminal.Reason != "byte-limit" || !got.TerminalOverflow || reads.Load() != 2 || len(got.Observations) != 2 || len(w.Poll().Observations) != 2 {
		t.Fatal("later limit evicted or continued original observations", got.Terminal)
	}
	for _, o := range got.Observations {
		if !o.HTTP.Captured || !bytes.Equal(o.HTTP.Entries[0].Value, []byte{0, 255}) {
			t.Fatal("later byte limit silently truncated evidence")
		}
	}
}

func TestHTTPWatchLateOldRunCloseDoesNotWaitOrCancelNewOriginalRead(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		if reads.Add(1) == 1 {
			w.Write([]byte(`[{"Key":"watch/key","Value":"AP8="}]`))
			return
		}
		close(entered)
		<-r.Context().Done()
		close(canceled)
	})
	l, _ := s.ReserveKVHTTPWatch(1)
	defer l.Close()
	r := testHTTPWatchRequest()
	r.MaxObservations = 1
	old := startHTTPWatch(t, l, r)
	settledHTTPWatch(t, old)
	old.Stop(context.Background())
	if err := l.ClearObservations(); err != nil {
		t.Fatal(err)
	}
	next := startHTTPWatch(t, l, r)
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- old.Close() }()
	var closeErr error
	late := false
	select {
	case closeErr = <-closed:
	case <-time.After(50 * time.Millisecond):
		late = true
	}
	select {
	case <-canceled:
		t.Error("old Close canceled the new original read")
	default:
	}
	if err := next.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if late {
		closeErr = <-closed
	}
	if closeErr != nil || late {
		t.Fatal("late old Close waited on a newer executor", closeErr)
	}
	if len(next.Snapshot().Observations) != 1 || !next.Snapshot().Observations[0].HTTP.Ended {
		t.Fatal("new original cancellation receipt lost")
	}
}

func TestHTTPWatchRecoveryAndOriginalRunCloseKeepOneEvidenceOwner(t *testing.T) {
	s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[{"Key":"watch/key","Value":"AP8="}]`)) })
	for i := 0; i < 12; i++ {
		l, err := s.ReserveKVHTTPWatch(1)
		if err != nil {
			t.Fatal(err)
		}
		r := testHTTPWatchRequest()
		r.MaxObservations = 1
		w := startHTTPWatch(t, l, r)
		settledHTTPWatch(t, w)
		w.Stop(context.Background())
		closed := make(chan error, 1)
		go func() { closed <- w.Close() }()
		recovery, err := s.RecoverKVHTTPWatch(l)
		if err != nil || recovery.Lease == nil {
			t.Fatal("concurrent original evidence recovery failed", err)
		}
		if err = <-closed; err != nil {
			t.Fatal(err)
		}
		l.Close()
		last := recovery.Lease.LastWatch()
		expected := s.rootBytes
		if last != nil {
			expected += last.RetainedBytes
		}
		receiptBudgetMu.Lock()
		retained, reserved := s.retained, s.reserved
		receiptBudgetMu.Unlock()
		if retained != expected || reserved != 0 {
			t.Fatal("concurrent Close/transfer double-charged or lost ownership", retained, expected, reserved)
		}
		recovery.Lease.Close()
		receiptBudgetMu.Lock()
		retained = s.retained
		receiptBudgetMu.Unlock()
		if retained != s.rootBytes {
			t.Fatal("settled evidence owner leaked budget")
		}
	}
}
