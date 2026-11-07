package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

const nativeIdentity = `{"name":"fixture","cluster_name":"nyadb","cluster_uuid":"own-cluster","version":{"number":"9.2.0","build_flavor":"default","build_hash":"native"},"tagline":"You Know, for Search"}`

func fakeClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		if r.URL.Path == "/" {
			_, _ = io.WriteString(w, nativeIdentity)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := Open(context.Background(), connections.ElasticsearchConfig{Endpoint: server.URL}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestElasticsearchIdentityRejectsOtherProductsAndVersions(t *testing.T) {
	for _, raw := range []string{
		strings.Replace(nativeIdentity, `"9.2.0"`, `"8.0.0"`, 1),
		strings.Replace(nativeIdentity, `"default"`, `"oss"`, 1),
		strings.Replace(nativeIdentity, `"own-cluster"`, `"_na_"`, 1),
		strings.Replace(nativeIdentity, `"number":"9.2.0"`, `"number":"9.2.0","number":"9.2.0"`, 1),
		`{"version":{"number":"9.2.0"}}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			_, _ = io.WriteString(w, raw)
		}))
		client, err := Open(context.Background(), connections.ElasticsearchConfig{Endpoint: server.URL}, "")
		server.Close()
		if err == nil || client != nil {
			t.Fatal("foreign or incomplete identity admitted")
		}
	}
}

func TestElasticsearchPathsCredentialsRedirectsAndNativeDiagnostics(t *testing.T) {
	var calls atomic.Int32
	client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", "/leak")
			w.WriteHeader(307)
			return
		}
		w.WriteHeader(403)
		_, _ = io.WriteString(w, `{"error":{"type":"security_exception","reason":"secret input must not appear"}}`)
	})
	for _, path := range []string{"https://other/x", "//other/x", "/../x", "/%2E%2E/x", "/x#secret", "/x\n", "/x\\y"} {
		if _, err := client.Do(context.Background(), Request{Method: "GET", Path: path}); !errors.Is(err, ErrInvalid) {
			t.Fatal("foreign/ambiguous path admitted", path, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid path reached network")
	}
	response, err := client.Do(context.Background(), Request{Method: "GET", Path: "/redirect"})
	if err == nil || response.Status != 307 || calls.Load() != 1 {
		t.Fatal("redirect followed")
	}
	response, err = client.Do(context.Background(), Request{Method: "GET", Path: "/forbidden"})
	if err == nil || strings.Contains(err.Error(), "secret") || !strings.Contains(string(response.Body), "secret input") || calls.Load() != 2 {
		t.Fatal("native diagnostic leaked literal or original response lost")
	}
	if _, err = Open(context.Background(), connections.ElasticsearchConfig{Endpoint: client.config.Endpoint}, "secret"); err == nil {
		t.Fatal("secret accepted in unauthenticated HTTP mode")
	}
}

func TestElasticsearchCommittedWriteLostReceiptRetiresWithoutReplay(t *testing.T) {
	var calls atomic.Int32
	client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		hijack, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = hijack.Close()
		}
	})
	_, err := client.CreateDocument(context.Background(), DocumentID{Index: "own", ID: "one"}, json.RawMessage(`{"n":9007199254740993}`))
	var interrupted *Interrupted
	if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown || !errors.Is(err, ErrRetired) || calls.Load() != 1 {
		t.Fatal("uncertain write was replayed or acknowledged", err)
	}
	_, err = client.Do(context.Background(), Request{Method: "GET", Path: "/own/_doc/one"})
	if !errors.Is(err, ErrRetired) || calls.Load() != 1 {
		t.Fatal("retired connection implicitly recovered", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestElasticsearchCloseWaitsOriginalHTTPWorker(t *testing.T) {
	client := fakeClient(t, func(http.ResponseWriter, *http.Request) {})
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	client.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		close(cancelled)
		<-release
		return nil, request.Context().Err()
	})
	requestDone := make(chan error, 1)
	go func() {
		_, err := client.Do(context.Background(), Request{Method: "POST", Path: "/own/_doc/one", Body: []byte(`{"n":1}`)})
		requestDone <- err
	}()
	<-started
	if _, err := client.Do(context.Background(), Request{Method: "GET", Path: "/own"}); !errors.Is(err, ErrBusy) {
		t.Fatal("overlapping native operations allowed")
	}
	closed := make(chan error, 1)
	go func() { closed <- client.Close() }()
	<-cancelled
	select {
	case <-closed:
		t.Fatal("Close returned before original worker acknowledged cancellation")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if err := <-requestDone; !errors.Is(err, ErrRetired) || !errors.Is(err, context.Canceled) {
		t.Fatal("original interruption lost", err)
	}
}

func TestElasticsearchTypedDocumentsKeepExactJSONAndBothVersionTokens(t *testing.T) {
	source := `{"n":9007199254740993,"decimal":12345678901234567890.123456789,"nested":[null,"",false]}`
	client := fakeClient(t, func(w http.ResponseWriter, request *http.Request) {
		if request.Method == "GET" {
			_, _ = io.WriteString(w, `{"_index":"own","_id":"a/b","_seq_no":9007199254740993,"_primary_term":7,"found":true,"_source":`+source+`}`)
			return
		}
		if request.URL.Query().Get("if_seq_no") != "9007199254740993" || request.URL.Query().Get("if_primary_term") != "7" || request.URL.Query().Get("routing") != "tenant & /" || request.URL.EscapedPath() != "/own/_doc/a%2Fb" {
			t.Error("qualified identity/concurrency changed", request.URL.EscapedPath())
		}
		w.WriteHeader(409)
		_, _ = io.WriteString(w, `{"error":{"type":"version_conflict_engine_exception"}}`)
	})
	original, err := client.GetDocument(context.Background(), DocumentID{Index: "own", ID: "a/b", Routing: "tenant & /"})
	if err != nil || string(original.Source) != source || original.Sequence != 9007199254740993 {
		t.Fatal("native JSON/token precision lost", err)
	}
	copy := original.Clone()
	copy.Source[0] = ' '
	if original.Source[0] != '{' {
		t.Fatal("snapshot aliases source bytes")
	}
	_, err = client.ReplaceDocument(context.Background(), original, json.RawMessage(`{"n":2}`))
	var native *HTTPError
	if !errors.As(err, &native) || native.Status != 409 || client.available() != nil {
		t.Fatal("conflict silently retried or healthy client lost", err)
	}
	_, err = client.DeleteDocument(context.Background(), original)
	if !errors.As(err, &native) || native.Status != 409 {
		t.Fatal("delete did not preserve original tokens")
	}
	for _, raw := range []string{`{"n":1,"n":2}`, `null`, `[]`, `{"n":1} {}`, `{"n":1e}`, strings.Repeat(" ", 1<<20) + `{}`} {
		if validSource([]byte(raw)) {
			t.Fatal("ambiguous or out-of-bounds source accepted")
		}
	}
}
