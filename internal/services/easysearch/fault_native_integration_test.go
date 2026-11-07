package easysearch

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func relayConfig(t *testing.T, s *httptest.Server) connections.EasysearchConfig {
	t.Helper()
	ca := filepath.Join(t.TempDir(), "relay-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return connections.EasysearchConfig{Endpoint: s.URL, Auth: "none", CAFile: ca, ServerName: "example.com"}
}

func TestEasysearchNativeIncompletePITReceiptRetiresAndCleansCapturedResource(t *testing.T) {
	cfg, secret := nativeFixture(t)
	ctx, stop := context.WithTimeout(context.Background(), 60*time.Second)
	defer stop()
	direct, err := Open(ctx, cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := direct.Close(); err != nil {
			t.Error(err)
		}
	})
	index := ownedIndex(t, direct, ctx)
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, requestLimit+1))
		response, err := direct.Do(r.Context(), Request{Method: r.Method, Path: r.URL.RequestURI(), ContentType: r.Header.Get("Content-Type"), Body: body})
		if err != nil {
			w.WriteHeader(502)
			return
		}
		if r.Method == "POST" && r.URL.Path == "/"+index+"/_pit" {
			var object map[string]json.RawMessage
			if decodeJSON(response.Body, &object) != nil {
				w.WriteHeader(502)
				return
			}
			object["_shards"] = json.RawMessage(`{"total":2,"successful":1,"failed":1}`)
			response.Body, _ = json.Marshal(object)
		}
		w.Header().Set("Content-Type", response.ContentType)
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
	}))
	defer relay.Close()
	client, err := Open(ctx, relayConfig(t, relay), "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	cursor, err := client.OpenCursor(ctx, index, []byte(`{"match_all":{}}`), 1)
	var interrupted *Interrupted
	if cursor != nil || !errors.As(err, &interrupted) || interrupted.WriteOutcomeUnknown {
		t.Fatal("partial native PIT was advertised as complete", err)
	}
	if err = client.Close(); err != nil {
		t.Fatal("failed PIT retained its original resource", err)
	}
	reply, err := direct.Do(ctx, Request{Method: "GET", Path: "/_pit/_all"})
	var listed struct {
		Pits []json.RawMessage `json:"pits"`
	}
	if err != nil || decodeJSON(reply.Body, &listed) != nil || len(listed.Pits) != 0 {
		t.Fatal("captured native PIT was not freed", err)
	}
}

func TestEasysearchNativeLostWriteReceiptRetiresOriginalWithoutReplay(t *testing.T) {
	cfg, secret := nativeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	direct, err := Open(ctx, cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := direct.Close(); err != nil {
			t.Error(err)
		}
	})
	index := ownedIndex(t, direct, ctx)
	path := "/" + index + "/_create/lost-receipt?refresh=wait_for"
	var writes atomic.Int32
	var committed atomic.Bool
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(io.LimitReader(request.Body, requestLimit+1))
		if err != nil {
			w.WriteHeader(400)
			return
		}
		if request.URL.RequestURI() == path {
			writes.Add(1)
		}
		response, err := direct.Do(request.Context(), Request{Method: request.Method, Path: request.URL.RequestURI(), ContentType: request.Header.Get("Content-Type"), Body: body})
		if err != nil {
			w.WriteHeader(502)
			return
		}
		if request.URL.RequestURI() == path {
			committed.Store(response.Status == 201)
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = connection.Close()
			}
			return
		}
		w.Header().Set("Content-Type", response.ContentType)
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
	}))
	defer relay.Close()
	proxy := relayConfig(t, relay)
	original, err := Open(ctx, proxy, "")
	if err != nil {
		t.Fatal("verified relay root", err)
	}
	defer original.Close()
	_, err = original.Do(ctx, Request{Method: "PUT", Path: path, Body: []byte(`{"exact":9007199254740993,"outcome":"native committed"}`)})
	var unknown *Interrupted
	if !errors.As(err, &unknown) || !unknown.WriteOutcomeUnknown || !committed.Load() || writes.Load() != 1 {
		t.Fatal("native lost receipt was replayed or misclassified", err)
	}
	if _, err = original.Do(ctx, Request{Method: "GET", Path: "/"}); !errors.Is(err, ErrRetired) {
		t.Fatal("original interrupted client silently recovered", err)
	}
	read, err := direct.GetDocument(ctx, DocumentID{Index: index, ID: "lost-receipt"})
	if err != nil || !validSource(read.Source) {
		t.Fatal("native committed write not present", err)
	}
	recovered, err := Open(ctx, proxy, "")
	if err != nil {
		t.Fatal("explicit fresh native client", err)
	}
	defer recovered.Close()
	if writes.Load() != 1 {
		t.Fatal("explicit recovery replayed the original write")
	}
	if _, err = recovered.Do(ctx, Request{Method: "GET", Path: "/"}); err != nil {
		t.Fatal(err)
	}
}

func TestEasysearchNativeCancellationSettlesOriginalHTTPWorkerAndReleasesPIT(t *testing.T) {
	cfg, secret := nativeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	direct, err := Open(ctx, cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := direct.Close(); err != nil {
			t.Error(err)
		}
	})
	index := ownedIndex(t, direct, ctx)
	if _, err = direct.CreateDocument(ctx, DocumentID{Index: index, ID: "original"}, []byte(`{"name":"original"}`)); err != nil {
		t.Fatal(err)
	}
	started, settled := make(chan struct{}), make(chan struct{})
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(request.Body, requestLimit+1))
		response, err := direct.Do(request.Context(), Request{Method: request.Method, Path: request.URL.RequestURI(), ContentType: request.Header.Get("Content-Type"), Body: body})
		if err != nil {
			w.WriteHeader(502)
			return
		}
		if request.URL.Path == "/_search" {
			close(started)
			<-request.Context().Done()
			close(settled)
			return
		}
		w.Header().Set("Content-Type", response.ContentType)
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
	}))
	defer relay.Close()
	client, err := Open(ctx, relayConfig(t, relay), "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	cursor, err := client.OpenCursor(ctx, index, []byte(`{"match_all":{}}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	requestCtx, stop := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() { _, err := cursor.Next(requestCtx); result <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("native search not acknowledged by relay")
	}
	stop()
	select {
	case err = <-result:
	case <-ctx.Done():
		t.Fatal("cancelled worker did not settle")
	}
	var interrupted *Interrupted
	if !errors.As(err, &interrupted) || interrupted.WriteOutcomeUnknown {
		t.Fatal("native read cancellation semantics", err)
	}
	select {
	case <-settled:
	case <-ctx.Done():
		t.Fatal("original relay worker did not settle")
	}
	// Cleanup is a native DELETE for this original PIT, even after retirement.
	cleanup, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()
	if err = cursor.Close(cleanup); err != nil {
		t.Fatal("original PIT cleanup after cancellation", err)
	}
	if _, err = client.Do(ctx, Request{Method: "GET", Path: "/"}); !errors.Is(err, ErrRetired) {
		t.Fatal("cancelled client silently recovered", err)
	}
}
