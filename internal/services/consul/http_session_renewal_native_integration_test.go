package consul

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeRenewalRequest(id string) contract.KVHTTPSessionRenewalRequest {
	r := renewalTestRequest()
	r.SessionID = id
	return r
}
func nativeRenewalLease(t *testing.T, cfg Config, token string) (*Session, contract.KVHTTPSessionRenewalLease) {
	t.Helper()
	s, e := OpenNativeSession(context.Background(), 1901, cfg, token)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	l, e := s.ReserveKVHTTPSessionRenewal(1)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { l.Close() })
	return s, l
}
func TestConsulNativeHTTPSessionRenewalOriginalUUIDScopeTTLAndBoundedReceipts(t *testing.T) {
	base, cfg, token := nativeClient(t)
	cfg.Datacenter = "dc1"
	cfg.Consistency = "consistent"
	for _, behavior := range []string{"release", "delete"} {
		t.Run(behavior, func(t *testing.T) {
			id := createSession(t, base, behavior)
			defer nativeDo(t, base, Request{Method: "PUT", Path: "/v1/session/destroy/" + id})
			s, l := nativeRenewalLease(t, cfg, token)
			var opens atomic.Int32
			s.opener = func(ctx context.Context) (*Client, OpenEvidence, error) { opens.Add(1); return s.openCaptured(ctx) }
			r := nativeRenewalRequest(id)
			r.MaxRenewals = 3
			if behavior == "release" {
				r.Interval = 2 * time.Second
				r.MaxRenewals = 7
			}
			began := time.Now()
			w := startSessionRenewal(t, l, r)
			defer w.Close()
			v := settledSessionRenewal(t, w)
			if v.Terminal.Reason != "renewal-limit" || v.Terminal.Attempts != r.MaxRenewals || v.Terminal.Acknowledged != r.MaxRenewals || v.Terminal.WriteOutcomeUnknown || len(v.Observations) != r.MaxRenewals+1 || opens.Load() != 1 {
				t.Fatal("original bounded renewal failed", v.Terminal)
			}
			for i, o := range v.Observations {
				u, e := url.Parse(o.HTTP.URL)
				if e != nil || u.Query().Get("dc") != "dc1" || o.Sequence != i || !o.ProjectionComplete || o.Session.ID != id || o.Session.TTL != "10s" || o.Session.Behavior != behavior || o.Session.LockDelay != 0 || !o.HTTP.EOF || !o.HTTP.Succeeded || !bytes.Contains(o.HTTP.Body, []byte(id)) {
					t.Fatal("native UUID/TTL/HTTP scope lost", i, o.Session)
				}
				if i > 1 && o.ReceivedAt.Sub(v.Observations[i-1].ReceivedAt) < r.Interval {
					t.Fatal("authored interval shortened")
				}
				if token != "" && (strings.Contains(o.HTTP.URL, token) || bytes.Contains(o.HTTP.Body, []byte(token))) {
					t.Fatal("secret in evidence")
				}
			}
			if behavior == "release" && time.Since(began) < 12*time.Second {
				t.Fatal("renewal did not span original TTL")
			}
			observed := nativeDo(t, base, Request{Method: "GET", Path: "/v1/session/info/" + id})
			if !bytes.Contains(observed.Body, []byte(id)) {
				t.Fatal("stopping destroyed original session")
			}
		})
	}
}
func TestConsulNativeHTTPSessionRenewalProcessedReplyFaultsNeverReplay(t *testing.T) {
	base, cfg, token := nativeClient(t)
	for _, mode := range []string{"lost", "partial", "http500", "http408", "cancel", "close", "timeout", "uuid"} {
		t.Run(mode, func(t *testing.T) {
			id := createSession(t, base, "release")
			defer nativeDo(t, base, Request{Method: "PUT", Path: "/v1/session/destroy/" + id})
			faultMode := mode
			if mode == "timeout" {
				faultMode = "cancel"
			}
			relay, calls, processed := nativeFault(t, cfg, token, faultMode)
			if mode == "timeout" {
				relay.RequestTimeout = time.Second
			}
			s, l := nativeRenewalLease(t, relay, token)
			if mode == "uuid" {
				s.opener = func(ctx context.Context) (*Client, OpenEvidence, error) {
					c, e, err := s.openCaptured(ctx)
					if err != nil {
						return c, e, err
					}
					original := c.http.Transport
					c.http.Transport = watchRoundTripper(func(request *http.Request) (*http.Response, error) {
						response, err := original.RoundTrip(request)
						if err == nil && request.Method == "PUT" {
							body, readErr := io.ReadAll(response.Body)
							response.Body.Close()
							if readErr != nil {
								return nil, readErr
							}
							body = bytes.ReplaceAll(body, []byte(id), []byte(renewalTestID))
							response.Body = io.NopCloser(bytes.NewReader(body))
						}
						return response, err
					})
					return c, e, nil
				}
			}

			r := nativeRenewalRequest(id)
			w := startSessionRenewal(t, l, r)
			wait(t, processed)
			if mode == "cancel" {
				if e := w.Stop(context.Background()); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "close" {
				if e := s.Close(); e != nil {
					t.Fatal(e)
				}
			}
			v := settledSessionRenewal(t, w)
			if v.Terminal.Reason != "write-unknown" || v.Terminal.Attempts != 1 || v.Terminal.Acknowledged != 0 || !v.Terminal.WriteOutcomeUnknown || len(v.Observations) != 2 || !v.Observations[1].HTTP.WriteOutcomeUnknown {
				t.Fatal("actual processed PUT confirmed/replayed", v.Terminal)
			}
			if mode == "partial" && (v.Observations[1].HTTP.EOF || len(v.Observations[1].HTTP.Body) == 0) {
				t.Fatal("partial native evidence lost")
			}
			before := calls.Load()
			time.Sleep(2 * r.Interval)
			if calls.Load() != before {
				t.Fatal("unknown PUT auto-replayed")
			}
			target := s
			if mode == "close" {
				var e error
				target, e = OpenNativeSession(context.Background(), 1901, relay, token)
				if e != nil {
					t.Fatal(e)
				}
				defer target.Close()
			}
			before = calls.Load()
			recovery, e := target.RecoverKVHTTPSessionRenewal(l)
			if e != nil {
				t.Fatal(e)
			}
			defer recovery.Lease.Close()
			if calls.Load() != before || recovery.Renewal == nil || !recovery.Renewal.Terminal.WriteOutcomeUnknown || !bytes.Equal(recovery.Renewal.Observations[1].HTTP.Body, v.Observations[1].HTTP.Body) {
				t.Fatal("recovery changed original evidence")
			}
			if e = recovery.Lease.ClearReceipts(); !errors.Is(e, contract.ErrKVWriteUnknown) {
				t.Fatal("unknown clear allowed")
			}
			if _, e = recovery.Lease.Prepare(r); !errors.Is(e, contract.ErrKVWriteUnknown) {
				t.Fatal("recovery reauthorized write", e)
			}
			read := nativeDo(t, base, Request{Method: "GET", Path: "/v1/session/info/" + id})
			if !bytes.Contains(read.Body, []byte(id)) || !recovery.Lease.LastRenewal().Terminal.WriteOutcomeUnknown {
				t.Fatal("later read confirmed unknown renewal")
			}
		})
	}
}
func TestConsulNativeHTTPSessionRenewalMissingSessionAndOriginalACLDenials(t *testing.T) {
	base, cfg, token := nativeClient(t)
	id := createSession(t, base, "release")
	nativeDo(t, base, Request{Method: "PUT", Path: "/v1/session/destroy/" + id})
	_, l := nativeRenewalLease(t, cfg, token)
	w := startSessionRenewal(t, l, nativeRenewalRequest(id))
	defer w.Close()
	v := settledSessionRenewal(t, w)
	if len(v.Observations) != 1 || v.Terminal.Attempts != 0 || v.Terminal.WriteOutcomeUnknown {
		t.Fatal("missing session dispatched PUT", v.Terminal)
	}
	if cfg.Auth == "none" {
		return
	}
	id = createSession(t, base, "release")
	defer nativeDo(t, base, Request{Method: "PUT", Path: "/v1/session/destroy/" + id})
	for _, mode := range []string{"read-denied", "write-denied"} {
		t.Run(mode, func(t *testing.T) {
			secret := os.Getenv("NYADB_CONSUL_READ_TOKEN")
			if mode == "write-denied" {
				secret = os.Getenv("NYADB_CONSUL_SESSION_READ_TOKEN")
			}
			if secret == "" {
				t.Fatal("missing isolated original ACL token")
			}
			_, l := nativeRenewalLease(t, cfg, secret)
			w := startSessionRenewal(t, l, nativeRenewalRequest(id))
			defer w.Close()
			v := settledSessionRenewal(t, w)
			if v.Terminal.WriteOutcomeUnknown || v.Terminal.Reason == "renewal-limit" {
				t.Fatal("ACL denial changed native result", v.Terminal)
			}
			if mode == "read-denied" && v.Terminal.Attempts != 0 {
				t.Fatal("unreadable session renewed")
			}
			if mode == "write-denied" && (len(v.Observations) != 2 || v.Observations[1].HTTP.Status != 403 || !v.Observations[1].HTTP.Acknowledged || v.Terminal.Attempts != 1) {
				t.Fatal("native PUT ACL denial missing", v.Terminal)
			}
		})
	}
}
func TestConsulNativeHTTPSessionRenewalBudgetParkingRetirementAndEvidenceTransfer(t *testing.T) {
	base, cfg, token := nativeClient(t)
	id := createSession(t, base, "release")
	defer nativeDo(t, base, Request{Method: "PUT", Path: "/v1/session/destroy/" + id})
	s, l := nativeRenewalLease(t, cfg, token)
	r := nativeRenewalRequest(id)
	r.Interval = time.Second
	r.MaxRenewals = 3
	w := startSessionRenewal(t, l, r)
	deadline := time.Now().Add(10 * time.Second)
	for len(w.Snapshot().Observations) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("first original PUT missing")
		}
		time.Sleep(time.Millisecond)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	v := settledSessionRenewal(t, w)
	if len(v.Observations) != 2 || v.Terminal.Attempts != 1 || v.Terminal.Acknowledged != 1 || v.Terminal.WriteOutcomeUnknown {
		t.Fatal("retirement during interval lost known receipt", v.Terminal)
	}
	fresh, e := OpenNativeSession(context.Background(), 1901, cfg, token)
	if e != nil {
		t.Fatal(e)
	}
	defer fresh.Close()
	var opens atomic.Int32
	fresh.opener = func(ctx context.Context) (*Client, OpenEvidence, error) { opens.Add(1); return fresh.openCaptured(ctx) }
	receiptBudgetMu.Lock()
	fresh.budget = fresh.retained + l.(*httpSessionRenewalLease).base.cacheBytes - 1
	receiptBudgetMu.Unlock()
	if _, e = fresh.RecoverKVHTTPSessionRenewal(l); !errors.Is(e, ErrBounds) {
		t.Fatal("transfer ignored budget", e)
	}
	if len(l.LastRenewal().Observations) != 2 {
		t.Fatal("failed transfer lost archive")
	}
	receiptBudgetMu.Lock()
	fresh.budget = sessionReceiptBudget
	receiptBudgetMu.Unlock()
	recovery, e := fresh.RecoverKVHTTPSessionRenewal(l)
	if e != nil {
		t.Fatal(e)
	}
	defer recovery.Lease.Close()
	if opens.Load() != 0 || len(recovery.Renewal.Observations) != 2 {
		t.Fatal("recovery reopened original")
	}
	archive := recovery.Lease.(contract.KVHTTPSessionRenewalArchiveLease).RetainedRenewal()
	w.Close()
	if len(archive.Snapshot().Observations) != 2 {
		t.Fatal("late old close deleted transferred archive")
	}
	if e = recovery.Lease.ClearReceipts(); e != nil {
		t.Fatal(e)
	}
	r.MaxBytes = 1
	w = startSessionRenewal(t, recovery.Lease, r)
	defer w.Close()
	v = settledSessionRenewal(t, w)
	if v.Terminal.Reason != "byte-limit" || v.Terminal.Attempts != 0 || !v.TerminalOverflow || len(v.Observations) != 1 {
		t.Fatal("terminal budget dispatched PUT", v.Terminal)
	}
}
func TestConsulNativeHTTPSessionRenewalGoneBetweenObservationAndPUT(t *testing.T) {
	base, cfg, token := nativeClient(t)
	id := createSession(t, base, "release")
	s, l := nativeRenewalLease(t, cfg, token)
	s.opener = func(ctx context.Context) (*Client, OpenEvidence, error) {
		c, e, err := s.openCaptured(ctx)
		if err != nil {
			return c, e, err
		}
		original := c.http.Transport
		c.http.Transport = watchRoundTripper(func(r *http.Request) (*http.Response, error) {
			if r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/v1/session/renew/") {
				_, err := base.Do(context.Background(), Request{Method: "PUT", Path: "/v1/session/destroy/" + id})
				if err != nil {
					t.Error(err)
				}
			}
			return original.RoundTrip(r)
		})
		return c, e, nil
	}
	w := startSessionRenewal(t, l, nativeRenewalRequest(id))
	defer w.Close()
	v := settledSessionRenewal(t, w)
	if len(v.Observations) != 2 || v.Terminal.Attempts != 1 || v.Terminal.WriteOutcomeUnknown || v.Observations[1].HTTP.Status != 404 || !v.Observations[1].HTTP.Acknowledged {
		t.Fatal("native missing UUID PUT rejection changed", v.Terminal)
	}
}
