package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

const unitIdentity = `{"title":"qdrant - vector search engine","version":"1.20.0","commit":"0123456789abcdef"}`
const emptyCollections = `{"status":"ok","time":0.01,"result":{"collections":[]}}`

func unitClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	c, err := Open(context.Background(), connections.QdrantConfig{Endpoint: s.URL}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func unitHandshake(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/":
		_, _ = w.Write([]byte(unitIdentity))
	case "/collections":
		_, _ = w.Write([]byte(emptyCollections))
	default:
		return false
	}
	return true
}

func TestQdrantRequestExactJSONAndNativeEnvelope(t *testing.T) {
	body := `{"id":18446744073709551615,"n":1.2300e-20,"text":"原生🐱","nested":{"nil":null}}`
	r, err := ParseRequest("# explicit request\npost /collections/a/points/query\n" + body)
	if err != nil || r.Method != "POST" || !bytes.Equal(r.Body, []byte(body)) {
		t.Fatal("exact native body", err)
	}
	for _, source := range []string{"SELECT * FROM vectors", "GET https://localhost/collections", "GET //elsewhere/", "GET /a/../b", "GET /a/%2e%2e/b", "GET /a#fragment", "POST /a\n{\"id\":1,\"id\":2}", "POST /a\n{\"v\":\"\\ud800\"}", "POST /a\n{}\nPOST /b", "GET /a\n{}", "GET /%00", "PUT /a\n{\"n\":NaN}"} {
		if _, err := ParseRequest(source); err == nil {
			t.Fatal("invalid native request accepted")
		}
	}
	result, err := NativeResult([]byte(`{"status":"ok","result":` + body + `,"time":0.1}`))
	if err != nil || !bytes.Equal(result, []byte(body)) {
		t.Fatal("native result spelling changed")
	}
	for _, raw := range []string{`{"status":{"error":"failed"},"result":false}`, `{"status":"ok"}`, `{"status":"ok","result":{"n":1,"n":2}}`, `{"status":"ok","result":"\ud800"}`} {
		if _, err := NativeResult([]byte(raw)); err == nil {
			t.Fatal("invalid native envelope accepted")
		}
	}
}

func TestQdrantHandshakeRequiresProductAndBrowsePermission(t *testing.T) {
	for _, identity := range []string{`{"title":"other","version":"1.20.0","commit":"0123456"}`, `{"title":"qdrant - vector search engine","version":"1.19.0","commit":"0123456"}`, `{"title":"qdrant - vector search engine","version":"1.20.0","commit":"unknown"}`, unitIdentity} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				_, _ = w.Write([]byte(identity))
			} else {
				w.WriteHeader(http.StatusForbidden)
			}
		}))
		c, err := Open(context.Background(), connections.QdrantConfig{Endpoint: s.URL}, "")
		if err == nil {
			c.Close()
			t.Fatal("product or permission failure accepted")
		}
		s.Close()
	}
}

func TestQdrantPrefixExactHeaderAndNoRedirect(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls.Add(1) }))
	defer destination.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api-key") != "original-header-key" || r.Header.Get("Authorization") != "" {
			t.Error("original native credential changed")
		}
		switch r.URL.Path {
		case "/proxy/":
			_, _ = w.Write([]byte(unitIdentity))
		case "/proxy/collections":
			_, _ = w.Write([]byte(emptyCollections))
		default:
			http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
		}
	}))
	defer s.Close()
	c, err := Open(context.Background(), connections.QdrantConfig{Endpoint: s.URL + "/proxy/", Auth: "api_key"}, "original-header-key")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r, err := c.Do(context.Background(), Request{Method: "PUT", Path: "/collections/a", Body: []byte(`{}`)})
	var interrupted *Interrupted
	if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown || r.Status != 307 || destinationCalls.Load() != 0 {
		t.Fatal("redirect followed or mutation uncertainty lost", err)
	}
	if strings.Contains(err.Error(), "original-header-key") {
		t.Fatal("credential exposed")
	}
}

func TestQdrantCancelWaitsOriginalExecutorAndRetires(t *testing.T) {
	started := make(chan struct{})
	c := unitClient(t, func(w http.ResponseWriter, r *http.Request) {
		if unitHandshake(w, r) {
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, _, err := c.Scroll(ctx, "a", nil, 10, nil); result <- err }()
	<-started
	if _, err := c.Do(context.Background(), Request{Method: "GET", Path: "/busy"}); !errors.Is(err, ErrBusy) {
		t.Fatal("concurrent executor accepted")
	}
	cancel()
	select {
	case err := <-result:
		var interrupted *Interrupted
		if !errors.As(err, &interrupted) || interrupted.WriteOutcomeUnknown || !errors.Is(err, context.Canceled) {
			t.Fatal("original read cancellation semantics", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original executor did not acknowledge cancellation")
	}
	if _, err := c.Do(context.Background(), Request{Method: "GET", Path: "/after"}); !errors.Is(err, ErrRetired) {
		t.Fatal("retired executor reused")
	}
	c.Close()
	c.Close()
}

func TestQdrantLostMutationProtocolKeepsOriginalEvidence(t *testing.T) {
	var mutationCalls atomic.Int32
	c := unitClient(t, func(w http.ResponseWriter, r *http.Request) {
		if unitHandshake(w, r) {
			return
		}
		mutationCalls.Add(1)
		_, _ = w.Write([]byte(`{"status":"ok","result":{"operation_id":18446744073709551615,"status":"acknowledged"}}`))
	})
	receipt, r, err := c.SubmitPoints(context.Background(), "a", "upsert", []byte(`{"points":[]}`), true)
	var interrupted *Interrupted
	if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown || receipt.Raw != nil || len(r.Body) == 0 {
		t.Fatal("incomplete mutation evidence lost", err)
	}
	if _, _, err := c.SubmitPoints(context.Background(), "a", "upsert", []byte(`{"points":[]}`), true); !errors.Is(err, ErrRetired) || mutationCalls.Load() != 1 {
		t.Fatal("mutation was replayed")
	}
}

func TestQdrantPointIDsAndPayloadStayExact(t *testing.T) {
	raw := []byte(`[{"id":18446744073709551615,"version":9007199254740993,"payload":{"n":9007199254740995},"vector":{"sparse":{"indices":[1],"values":[0.25]},"multi":[[1,0],[0,1]]},"score":0.99999994}]`)
	points, err := parsePoints(raw, 1)
	if err != nil || len(points) != 1 || string(points[0].ID) != "18446744073709551615" || !bytes.Contains(points[0].JSON, []byte("9007199254740993")) || !bytes.Contains(points[0].Payload, []byte("9007199254740995")) {
		t.Fatal("native point precision changed", err)
	}
	for _, raw := range []string{`-1`, `18446744073709551616`, `1.0`, `1e2`, `"not-uuid"`, `"00000000-0000-0000-0000-00000000000g"`} {
		if validPointID([]byte(raw)) {
			t.Fatal("invalid native point ID accepted")
		}
	}
	for _, name := range []string{"", "..", "a/b", "a%2fb", "a\x00"} {
		if _, err := collectionPath(name); err == nil {
			t.Fatal("ambiguous native collection accepted")
		}
	}
	if path, err := collectionPath("原生 集合"); err != nil || !strings.Contains(path, "%") {
		t.Fatal("qualified encoded collection rejected")
	}
	var value map[string]json.RawMessage
	if decodeJSON(points[0].Payload, &value) != nil {
		t.Fatal("original payload corrupted")
	}
}

func TestQdrantTruncatedResponseRetainsOriginalPartialBytes(t *testing.T) {
	c := unitClient(t, func(w http.ResponseWriter, r *http.Request) {
		if unitHandshake(w, r) {
			return
		}
		conn, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error("controlled response truncation", err)
			return
		}
		_, _ = fmt.Fprint(buffer, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"status\":\"ok\"")
		_ = buffer.Flush()
		_ = conn.Close()
	})
	r, err := c.Do(context.Background(), Request{Method: "GET", Path: "/partial"})
	var interrupted *Interrupted
	if !errors.As(err, &interrupted) || interrupted.WriteOutcomeUnknown || r.Status != 200 || string(r.Body) != `{"status":"ok"` {
		t.Fatal("original partial read evidence lost", err)
	}
	if _, err := NativeResult(r.Body); err == nil {
		t.Fatal("partial JSON treated as completed result")
	}
}

func TestQdrantCanceledBeforeDispatchLeavesClientUsable(t *testing.T) {
	var calls atomic.Int32
	c := unitClient(t, func(w http.ResponseWriter, r *http.Request) {
		if unitHandshake(w, r) {
			return
		}
		calls.Add(1)
		_, _ = w.Write([]byte(`{"status":"ok","result":true}`))
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Do(ctx, Request{Method: "PUT", Path: "/collections/a", Body: []byte(`{}`)}); !errors.Is(err, context.Canceled) || errors.Is(err, ErrRetired) || calls.Load() != 0 {
		t.Fatal("canceled-before-dispatch request submitted or claimed uncertain", err)
	}
	if _, err := c.Do(context.Background(), Request{Method: "GET", Path: "/still-usable"}); err != nil || calls.Load() != 1 {
		t.Fatal("client retired without dispatch", err)
	}
}
