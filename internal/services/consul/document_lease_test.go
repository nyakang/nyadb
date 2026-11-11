package consul

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const testAgentID = "11111111-2222-4333-8444-555555555555"

func sessionHandler(calls *atomic.Int32, action http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("X-Consul-Index", "18446744073709551615")
		switch r.URL.Path {
		case "/v1/agent/self":
			w.Write([]byte(`{"Config":{"Version":"2.0.2","NodeID":"` + testAgentID + `","NodeName":"original","Datacenter":"dc1"}}`))
		case "/v1/status/leader":
			w.Write([]byte(`"127.0.0.1:8300"`))
		case "/v1/kv/":
			w.Write([]byte(`[]`))
		default:
			action(w, r)
		}
	}
}
func documentSession(t *testing.T, action http.HandlerFunc) (*Session, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(sessionHandler(calls, action))
	t.Cleanup(server.Close)
	s, err := OpenNativeSession(context.Background(), 1, Config{Endpoint: server.URL}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, calls
}
func documentPlan(t *testing.T, l contract.KVRequestLease, r Request) contract.KVPreparation {
	t.Helper()
	request, err := DocumentRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	p, err := l.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func executeDocument(t *testing.T, l contract.KVRequestLease, r Request) contract.KVResult {
	t.Helper()
	p := documentPlan(t, l, r)
	result, err := l.Execute(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestOriginalDocumentReservationPlansReceiptsAndUnsignedFields(t *testing.T) {
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			raw, _ := io.ReadAll(r.Body)
			if !bytes.Equal(raw, []byte{0, 255}) {
				t.Error("original request bytes changed")
			}
			w.Write([]byte("true"))
			return
		}
		w.Write([]byte(`[{"Key":"key","Value":"AP8=","Flags":18446744073709551615,"CreateIndex":9007199254740993,"ModifyIndex":18446744073709551615,"LockIndex":0,"Unknown":{"original":true}}]`))
	})
	before := calls.Load()
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if calls.Load() != before || l.Scope().Header != (contract.KVHeader{}) || l.Scope().HTTP.AgentID != testAgentID || l.Scope().Generation != 1 {
		t.Fatal("reserve performed I/O or invented identity")
	}
	p := documentPlan(t, l, putRequest("key", []byte{0, 255}))
	if calls.Load() != before {
		t.Fatal("prepare performed I/O")
	}
	foreign, _ := s.ReserveKV(2)
	defer foreign.Close()
	if _, err = foreign.Execute(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) || calls.Load() != before {
		t.Fatal("foreign plan dispatched")
	}
	result, err := l.Execute(context.Background(), p)
	if err != nil || result.HTTP == nil || !result.Completed || !result.HTTP.Ended || !result.HTTP.Captured || !result.HTTP.EOF || result.NativeCaptured || result.Native != nil || result.Reply != nil || result.Header != (contract.KVHeader{}) {
		t.Fatal("native HTTP ACK was reinterpreted", err)
	}
	if _, err = l.Execute(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("plan reused")
	}
	read := executeDocument(t, l, readRequest("key"))
	if len(read.HTTP.Entries) != 1 || read.HTTP.Entries[0].Flags != math.MaxUint64 || read.HTTP.Entries[0].ModifyIndex != math.MaxUint64 || *read.HTTP.Metadata.Index != math.MaxUint64 || !bytes.Contains(read.HTTP.Body, []byte(`"Unknown"`)) {
		t.Fatal("native precision or unknown fields lost")
	}
	read.HTTP.Body[0] = 'x'
	read.HTTP.Entries[0].Value[0] = 9
	*read.HTTP.Metadata.Index = 1
	if l.LastResult().HTTP.Body[0] != '[' || l.LastResult().HTTP.Entries[0].Value[0] != 0 || *l.LastResult().HTTP.Metadata.Index != math.MaxUint64 {
		t.Fatal("inspection changed original cache")
	}
	if _, err = s.ReserveKV(1); err == nil {
		t.Fatal("same document reserved twice")
	}
}

func TestDocumentDecoderRejectsAmbiguousOrForeignNativeAuthorityWithoutIO(t *testing.T) {
	s, calls := documentSession(t, func(http.ResponseWriter, *http.Request) {
		t.Error("decoder performed native I/O")
	})
	l, _ := s.ReserveKV(1)
	defer l.Close()
	before := calls.Load()
	for _, raw := range []string{
		`null`,
		`{"method":"GET","method":"PUT","path":"/v1/kv/key"}`,
		`{"method":"GET","path":"/v1/kv/key","headers":{"X-Consul-Token":"temporary"}}`,
		`{"method":"GET","path":"/v1/kv/key","query":null}`,
		`{"method":"GET","path":"/v1/kv/key","body_base64":null}`,
		`{"method":"GET","path":"/v1/kv/key","query":{"token":["temporary"]}}`,
		`{"method":"GET","path":"/v1/kv/key","query":{"dc":["foreign"]}}`,
		`{"method":"GET","path":"/v1/kv/key","query":{"keys":null}}`,
		`{"method":"GET","path":"/v1/kv/key","query":{"keys":[null]}}`,
		`{"method":"GET","path":"/v1/kv/key","query":{"keys":["","true"]}}`,
		`{"method":"GET","path":"/v1/kv/key","query":{"raw":[""],"raw":["true"]}}`,
		`{"method":"PUT","path":"/v1/kv/key","body_base64":"AP9="}`,
		`{"method":"PUT","path":"/v1/kv/key","body_base64":"AP8=\n"}`,
		`{"method":"PUT","path":"/v1/acl/token","body_base64":"e30="}`,
		`{"method":"GET","path":"/v1/kv/../key"}`,
		`{"method":"PUT","path":"/v1/txn","body_base64":"bnVsbA=="}`,
		strings.Repeat(" ", requestLimit+1),
	} {
		if _, err := l.Prepare(contract.KVRequest{Method: HTTPRequestMethod, JSON: raw}); err == nil {
			t.Fatal("ambiguous or foreign native document accepted", raw[:min(150, len(raw))])
		}
	}
	if calls.Load() != before {
		t.Fatal("rejected document dispatched native request")
	}
	request := Request{Method: "PUT", Path: "/v1/kv/key", Query: url.Values{"flags": {"18446744073709551615"}}, Body: []byte{0, 255}}
	document, err := DocumentRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	request.Body[0] = 9
	request.Query["flags"][0] = "0"
	native, canonical, write, err := decodeDocumentRequest(document)
	if err != nil || !write || canonical != document || native.Query.Get("flags") != "18446744073709551615" || !bytes.Equal(native.Body, []byte{0, 255}) {
		t.Fatal("canonical immutable document changed original native bytes", err)
	}
}

func TestDocumentUnknownWriteAndLaterReadRecoverIndependentReceiptsWithoutIO(t *testing.T) {
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			w.WriteHeader(500)
			w.Write([]byte("actually committed reply withheld"))
			return
		}
		w.Write([]byte(`[{"Key":"key","Value":null,"Flags":0,"CreateIndex":1,"ModifyIndex":2}]`))
	})
	l, _ := s.ReserveKV(1)
	defer l.Close()
	earlier := documentPlan(t, l, putRequest("later", nil))
	p := documentPlan(t, l, putRequest("key", []byte{0, 255}))
	original, err := l.Execute(context.Background(), p)
	if err == nil || !original.WriteOutcomeUnknown || original.HTTP.Acknowledged {
		t.Fatal("unknown write was confirmed")
	}
	if _, err = l.Execute(context.Background(), earlier); !errors.Is(err, contract.ErrKVWriteUnknown) {
		t.Fatal("earlier plan bypassed unknown")
	}
	request, _ := DocumentRequest(putRequest("key", nil))
	if _, err = l.Prepare(request); !errors.Is(err, contract.ErrKVWriteUnknown) {
		t.Fatal("unknown write prepared")
	}
	read := executeDocument(t, l, readRequest("key"))
	if read.WriteOutcomeUnknown || !read.HTTP.Entries[0].ValueNull {
		t.Fatal("later read inherited an invented result")
	}
	before := calls.Load()
	recovery, err := s.RecoverKV(l)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Lease.Close()
	if calls.Load() != before || recovery.Unknown == nil || !recovery.Unknown.WriteOutcomeUnknown || recovery.Last.WriteOutcomeUnknown || !bytes.Equal(recovery.Unknown.HTTP.Body, original.HTTP.Body) || !recovery.Last.HTTP.Entries[0].ValueNull {
		t.Fatal("recovery executed or replaced original unknown")
	}
	if _, err = recovery.Lease.Prepare(request); !errors.Is(err, contract.ErrKVWriteUnknown) {
		t.Fatal("recovery removed write barrier")
	}
	if _, err = recovery.Lease.Execute(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("recovery reused old authorization")
	}
	recovery.Unknown.HTTP.Body[0] = 'x'
	if !bytes.Equal(recovery.Lease.(*documentLease).recoveryOwned().Unknown.HTTP.Body, original.HTTP.Body) {
		t.Fatal("recovery observation aliases transferred cache")
	}
	old := l.(*documentLease)
	fresh := recovery.Lease.(*documentLease)
	if old.cacheBytes != 0 || fresh.cacheBytes == 0 {
		t.Fatal("receipt accounting was not transferred")
	}
}

func TestRecoveryCannotInstallOriginalReceiptsAfterDestinationClose(t *testing.T) {
	s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"Key":"key","Value":"AP8="}]`))
	})
	previous, _ := s.ReserveKV(1)
	defer previous.Close()
	original := executeDocument(t, previous, readRequest("key"))
	old := previous.(*documentLease)
	old.ops.Lock()
	defer old.ops.Unlock()
	for _, retired := range []bool{false, true} {
		fresh, _ := s.ReserveKV(2)
		next := fresh.(*documentLease)
		if retired {
			next.retire()
		} else {
			next.Close()
		}
		if err := transferOwnedReceipts(old, next); err == nil {
			t.Fatal("closed destination accepted original receipt ownership")
		}
		if next.cacheBytes != 0 || next.LastResult().HTTP != nil || old.cacheBytes == 0 || !bytes.Equal(old.LastResult().HTTP.Body, original.HTTP.Body) {
			t.Fatal("failed recovery installed or discarded original evidence")
		}
		next.Close()
	}
}

func TestDocumentCacheInspectionDoesNotWaitForOriginalExecutor(t *testing.T) {
	entered := make(chan struct{})
	s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			io.Copy(io.Discard, r.Body)
			close(entered)
			<-r.Context().Done()
			return
		}
		w.Write([]byte(`[{"Key":"key","Value":"AP8="}]`))
	})
	l, _ := s.ReserveKV(1)
	defer l.Close()
	executeDocument(t, l, readRequest("key"))
	p := documentPlan(t, l, putRequest("key", []byte{0, 255}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan contract.KVResult, 1)
	go func() { r, _ := l.Execute(ctx, p); done <- r }()
	wait(t, entered)
	inspection := make(chan contract.KVResult, 1)
	go func() { inspection <- l.LastResult() }()
	select {
	case r := <-inspection:
		if !bytes.Equal(r.HTTP.Entries[0].Value, []byte{0, 255}) {
			t.Fatal("cached observation changed")
		}
	case <-time.After(time.Second):
		t.Fatal("inspection waited for HTTP executor")
	}
	other := documentPlan(t, l, readRequest("key"))
	if _, err := l.Execute(ctx, other); !errors.Is(err, contract.ErrKVBusy) {
		t.Fatal("busy lease dispatched another executor")
	}
	cancel()
	r := <-done
	if !r.WriteOutcomeUnknown || !r.HTTP.Canceled || !r.HTTP.Ended || !s.Alive() {
		t.Fatal("cancel damaged original receipt or other session documents")
	}
}

func TestSessionRetirementPreservesParkedReceiptAndExplicitCloseReleasesIt(t *testing.T) {
	entered := make(chan struct{})
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
	})
	l, _ := s.ReserveKV(1)
	p := documentPlan(t, l, putRequest("key", []byte{0, 255}))
	done := make(chan contract.KVResult, 1)
	go func() { r, _ := l.Execute(context.Background(), p); done <- r }()
	wait(t, entered)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if !r.WriteOutcomeUnknown || !r.HTTP.Ended || !l.LastResult().WriteOutcomeUnknown {
		t.Fatal("retirement dropped parked original completion")
	}
	before := calls.Load()
	if _, err := l.Prepare(contract.KVRequest{Method: HTTPRequestMethod, JSON: `{}`}); err == nil || calls.Load() != before {
		t.Fatal("retired original replayed")
	}
	if s.retained <= 0 {
		t.Fatal("retained original receipt escaped accounting")
	}
	l.Close()
	if s.retained != 0 || l.LastResult().HTTP != nil {
		t.Fatal("explicit document close did not release caches")
	}
}

func TestDocumentBudgetRejectsBeforeDispatchAndRecoveryRetainsOwnershipOnFailure(t *testing.T) {
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500); w.Write([]byte("original unknown")) })
	l, _ := s.ReserveKV(1)
	defer l.Close()
	p := documentPlan(t, l, putRequest("key", []byte{0, 255}))
	receiptBudgetMu.Lock()
	s.budget = s.retained + pendingReceiptBudget - 1
	receiptBudgetMu.Unlock()
	before := calls.Load()
	r, err := l.Execute(context.Background(), p)
	if !errors.Is(err, ErrBounds) || r.Attempted || r.WriteOutcomeUnknown || calls.Load() != before || p.(*documentPreparation).used.Load() {
		t.Fatal("receipt limit dispatched a write")
	}
	receiptBudgetMu.Lock()
	s.budget = sessionReceiptBudget
	receiptBudgetMu.Unlock()
	l.Execute(context.Background(), p)
	old := l.(*documentLease)
	fresh, err := OpenNativeSession(context.Background(), 1, s.cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	receiptBudgetMu.Lock()
	fresh.budget = fresh.retained + old.cacheBytes - 1
	receiptBudgetMu.Unlock()
	before = calls.Load()
	recovery, err := fresh.RecoverKV(l)
	if !errors.Is(err, ErrBounds) || recovery.Lease != nil || recovery.Unknown == nil || !recovery.Unknown.WriteOutcomeUnknown || old.cacheBytes == 0 || calls.Load() != before {
		t.Fatal("failed recovery dropped evidence or dispatched")
	}
	receiptBudgetMu.Lock()
	fresh.budget = sessionReceiptBudget
	receiptBudgetMu.Unlock()
	recovery, err = fresh.RecoverKV(l)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Lease.Close()
	if old.cacheBytes != 0 || recovery.Unknown == nil || calls.Load() != before {
		t.Fatal("pure recovery did not transfer accounted original")
	}
}

func TestLateDocumentOpenClosesCapturedClientAndKeepsReplacementAlive(t *testing.T) {
	s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("true")) })
	entered, release := make(chan struct{}), make(chan struct{})
	var captured *Client
	s.opener = func(ctx context.Context) (*Client, OpenEvidence, error) {
		c, e, err := s.openCaptured(ctx)
		captured = c
		close(entered)
		<-release
		return c, e, err
	}
	l, _ := s.ReserveKV(1)
	defer l.Close()
	done := make(chan error, 1)
	go func() { done <- l.Preconnect(context.Background()) }()
	wait(t, entered)
	closing := make(chan struct{})
	go func() { s.Close(); close(closing) }()
	replacement, err := OpenNativeSession(context.Background(), 1, s.cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	select {
	case <-closing:
		t.Fatal("close did not wait for original opener")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	wait(t, closing)
	if err := <-done; err == nil || captured.Alive() || !replacement.Alive() {
		t.Fatal("late open harmed replacement")
	}
}

func TestDocumentClientsKeepCapturedTLSAfterSourceCAIsRemoved(t *testing.T) {
	server := httptest.NewTLSServer(sessionHandler(&atomic.Int32{}, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("true")) }))
	defer server.Close()
	certificate, err := x509.ParseCertificate(server.Certificate().Raw)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "original-ca.crt")
	if err = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenNativeSession(context.Background(), 1, Config{Endpoint: server.URL, CAFile: path}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	l, _ := s.ReserveKV(1)
	defer l.Close()
	if err = l.Preconnect(context.Background()); err != nil {
		t.Fatal("captured TLS files were reopened", err)
	}
	if !executeDocument(t, l, putRequest("key", nil)).HTTP.Acknowledged {
		t.Fatal("original TLS client lost authority")
	}
}
