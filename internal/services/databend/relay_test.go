package databend

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

const firstQueryID = "11111111111111111111111111111111"
const secondQueryID = "22222222222222222222222222222222"

func testRelay(t *testing.T, endpoint string) *Relay {
	t.Helper()
	r, err := StartRelay(context.Background(), connections.DatabendConfig{Endpoint: endpoint, Auth: "basic", Username: "original"}, "upstream-secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}

func relayRequest(t *testing.T, r *Relay, method, path, id string, body []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, r.Endpoint()+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", r.localAuth)
	if id != "" {
		req.Header.Set(queryIDHeader, id)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return req
}

func relayReply(t *testing.T, req *http.Request) (int, []byte) {
	t.Helper()
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body
}

func TestRelayPreservesExactReceiptAndDistinctIdenticalSQL(t *testing.T) {
	var calls atomic.Int32
	native := []byte("{\"number\":9007199254740993.12345678901234567890,\"binary\":\"00FF80\",\"null\":null}\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		user, pass, ok := req.BasicAuth()
		if !ok || user != "original" || pass != "upstream-secret" || req.Header.Get("Idempotency-Key") != "" {
			t.Error("original authorization/transport boundaries changed")
		}
		if req.Header.Get("X-Databend-Warehouse") != "original-warehouse" || req.Header.Get("X-Databend-Tenant") != "original-tenant" {
			t.Error("original routing headers changed")
		}
		calls.Add(1)
		w.Header().Set("X-Databend-Route-Hint", "native-route")
		_, _ = w.Write(native)
	}))
	defer server.Close()
	r := testRelay(t, server.URL)
	for _, id := range []string{firstQueryID, firstQueryID, secondQueryID, secondQueryID} {
		req := relayRequest(t, r, "POST", "/v1/query", id, []byte("{\"sql\":\"INSERT INTO original VALUES (1)\"}"))
		req.Header.Set("X-Databend-Warehouse", "original-warehouse")
		req.Header.Set("X-Databend-Tenant", "original-tenant")
		req.Header.Set("Idempotency-Key", "do-not-authorize-Go-retry")
		code, body := relayReply(t, req)
		if code != 200 || !bytes.Equal(body, native) {
			t.Fatal("native result changed", code)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("duplicate replayed or legitimate identical SQL collapsed", calls.Load())
	}
	for _, id := range []string{firstQueryID, secondQueryID} {
		status, ok := r.QueryStatus(id)
		if !ok || !status.Complete || status.Uncertain || !status.Cached {
			t.Fatal("original completion was not retained", status)
		}
	}
}

func TestRelayRejectsChangedOriginalOwnership(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()
	r := testRelay(t, server.URL)
	relayReply(t, relayRequest(t, r, "POST", "/v1/query", firstQueryID, []byte("original")))
	for _, modify := range []func(*http.Request){
		func(req *http.Request) { req.Body = io.NopCloser(strings.NewReader("mutation")); req.ContentLength = 8 },
		func(req *http.Request) { req.Header.Set("X-Databend-Warehouse", "replacement") },
		func(req *http.Request) { req.Header.Set("X-Databend-Tenant", "replacement") },
		func(req *http.Request) { req.Header.Set("Accept", "application/vnd.apache.arrow.stream") },
	} {
		req := relayRequest(t, r, "POST", "/v1/query", firstQueryID, []byte("original"))
		modify(req)
		if code, _ := relayReply(t, req); code != http.StatusConflict {
			t.Fatal("original ID accepted new ownership", code)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("changed ownership reached upstream")
	}
}

func TestRelayLostCommittedReplyNeverResubmits(t *testing.T) {
	var commits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		commits.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()
	r := testRelay(t, server.URL)
	for i := 0; i < 3; i++ {
		code, _ := relayReply(t, relayRequest(t, r, "POST", "/v1/query", firstQueryID, []byte("write")))
		if code != http.StatusConflict {
			t.Fatal("lost reply became success", code)
		}
	}
	status, ok := r.QueryStatus(firstQueryID)
	if commits.Load() != 1 || !ok || status.Complete || !status.Uncertain || status.Cached {
		t.Fatal("uncertain original write replayed or became known", commits.Load(), status)
	}
}

func TestRelayPartialReceiptRemainsUncertain(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = buffered.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 20\r\n\r\npartial")
		_ = buffered.Flush()
		_ = conn.Close()
	}))
	defer server.Close()
	r := testRelay(t, server.URL)
	response, err := http.DefaultClient.Do(relayRequest(t, r, "POST", "/v1/query", firstQueryID, []byte("write")))
	if err == nil {
		_, err = io.ReadAll(response.Body)
		response.Body.Close()
	}
	if err == nil {
		t.Fatal("truncated upstream response became complete")
	}
	if code, _ := relayReply(t, relayRequest(t, r, "POST", "/v1/query", firstQueryID, []byte("write"))); code != http.StatusConflict {
		t.Fatal("truncated receipt replay became success")
	}
	status, _ := r.QueryStatus(firstQueryID)
	if calls.Load() != 1 || !status.Uncertain || status.Complete || status.Cached {
		t.Fatal("partial receipt lost original uncertainty", status)
	}
}

func TestRelayConcurrentRetriesWaitForOriginalExecutor(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		close(started)
		<-finish
		_, _ = w.Write([]byte("original"))
	}))
	defer server.Close()
	r := testRelay(t, server.URL)
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		req := relayRequest(t, r, "POST", "/v1/query", firstQueryID, []byte("write"))
		wg.Add(1)
		go func() {
			defer wg.Done()
			reply, err := http.DefaultClient.Do(req)
			if err == nil {
				body, readErr := io.ReadAll(reply.Body)
				reply.Body.Close()
				err = readErr
				if err == nil && (reply.StatusCode != 200 || string(body) != "original") {
					err = io.ErrUnexpectedEOF
				}
			}
			errors <- err
		}()
	}
	<-started
	close(finish)
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("concurrent original retries submitted multiple writes", calls.Load())
	}
}

func TestRelayLocalDisconnectStillConsumesOriginalReceipt(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		close(started)
		<-finish
		_, _ = w.Write([]byte("committed"))
	}))
	defer server.Close()
	r := testRelay(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	req := relayRequest(t, r, "POST", "/v1/query", firstQueryID, []byte("write")).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		reply, _ := http.DefaultClient.Do(req)
		if reply != nil {
			reply.Body.Close()
		}
	}()
	<-started
	cancel()
	<-done
	close(finish)
	if code, body := relayReply(t, relayRequest(t, r, "POST", "/v1/query", firstQueryID, []byte("write"))); code != 200 || string(body) != "committed" || calls.Load() != 1 {
		t.Fatal("local disconnect abandoned/replayed original receipt", code, calls.Load())
	}
}

func TestRelayLargeOriginalResponseKeepsTombstone(t *testing.T) {
	var calls atomic.Int32
	native := bytes.Repeat([]byte("x"), maxCachedReceipt+1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		_, _ = w.Write(native)
	}))
	defer server.Close()
	r := testRelay(t, server.URL)
	if code, body := relayReply(t, relayRequest(t, r, "POST", "/v1/query", firstQueryID, nil)); code != 200 || !bytes.Equal(body, native) {
		t.Fatal("large original response was truncated", code, len(body))
	}
	if code, _ := relayReply(t, relayRequest(t, r, "POST", "/v1/query", firstQueryID, nil)); code != http.StatusConflict {
		t.Fatal("missing cache authorized repeat execution", code)
	}
	status, _ := r.QueryStatus(firstQueryID)
	if calls.Load() != 1 || !status.Complete || status.Cached || status.Uncertain {
		t.Fatal("cache size became execution outcome", status)
	}
}

func TestRelayVerifiesOriginalTLSHostAndTrust(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte("verified"))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "original-ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		ca, name string
		accepted bool
	}{
		{path, "", true}, {"", "", false}, {path, "wrong.invalid", false},
	} {
		r, err := StartRelay(context.Background(), connections.DatabendConfig{Endpoint: server.URL, Auth: "basic", Username: "original", CAFile: c.ca, ServerName: c.name}, "upstream-secret")
		if err != nil {
			t.Fatal(err)
		}
		code, _ := relayReply(t, relayRequest(t, r, "POST", "/v1/query", firstQueryID, []byte("write")))
		r.Close()
		if (code == 200) != c.accepted {
			t.Fatal("certificate trust/host verification changed", code, c.accepted)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("untrusted TLS request reached product", calls.Load())
	}
	// The positive certificate was the actual original server certificate.
	if _, err := x509.ParseCertificate(server.Certificate().Raw); err != nil {
		t.Fatal(err)
	}
}

func TestRelayRejectsEscapesAndUnauthenticatedLocalClients(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { calls.Add(1) }))
	defer server.Close()
	r := testRelay(t, server.URL)
	for _, path := range []string{"/", "/v1/query/../../write", "/v1/query/" + firstQueryID + "/other", "/v1/query?redirect=x", "/v1/query/" + firstQueryID + "/page/1?x=y", "/v1/streaming_load"} {
		req := relayRequest(t, r, "POST", path, firstQueryID, nil)
		if code, _ := relayReply(t, req); code != http.StatusConflict {
			t.Fatal("request escaped captured protocol", path, code)
		}
	}
	req := relayRequest(t, r, "POST", "/v1/query", firstQueryID, nil)
	req.Header.Del("Authorization")
	if code, _ := relayReply(t, req); code != http.StatusUnauthorized {
		t.Fatal("unauthenticated local client submitted SQL")
	}
	req = relayRequest(t, r, "POST", "/v1/query", firstQueryID, nil)
	req.Host = "replacement.invalid"
	if code, _ := relayReply(t, req); code != http.StatusConflict {
		t.Fatal("replacement local authority accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid local request reached upstream", calls.Load())
	}
}

func TestRelayDoesNotFollowOriginalRedirect(t *testing.T) {
	var escaped atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { escaped.Add(1) }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, other.URL+"/v1/query", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	r := testRelay(t, server.URL)
	for i := 0; i < 2; i++ {
		if code, _ := relayReply(t, relayRequest(t, r, "POST", "/v1/query", firstQueryID, []byte("write"))); code != http.StatusConflict {
			t.Fatal("upstream redirect escaped verification", code)
		}
	}
	if escaped.Load() != 0 {
		t.Fatal("redirect target received original credentials or write")
	}
}

func TestRelayCloseWaitsForCapturedHandlersAndKeepsOtherRelayAlive(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get(queryIDHeader) == firstQueryID {
			close(started)
			<-req.Context().Done()
			return
		}
		_, _ = w.Write([]byte("independent"))
	}))
	defer server.Close()
	r := testRelay(t, server.URL)
	other := testRelay(t, server.URL)
	done := make(chan struct{})
	go func() {
		defer close(done)
		response, _ := http.DefaultClient.Do(relayRequest(t, r, "POST", "/v1/query", firstQueryID, nil))
		if response != nil {
			response.Body.Close()
		}
	}()
	<-started
	r.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("local request remained after original relay close")
	}
	r.mu.Lock()
	remaining, cached := len(r.queries), r.cacheSize
	r.mu.Unlock()
	if remaining != 0 || cached != 0 {
		t.Fatal("closed transport retained receipts")
	}
	if code, body := relayReply(t, relayRequest(t, other, "POST", "/v1/query", secondQueryID, nil)); code != 200 || string(body) != "independent" {
		t.Fatal("closing original transport retired independent owner")
	}
	r.Close()
}
