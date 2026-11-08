package meilisearch

import (
	"bytes"
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

const mockVersion = `{"pkgVersion":"1.56.1","commitSha":"421053d3f573efb3c1e5d857efb4e0408e599fd3","commitDate":"unknown"}`
const mockReceipt = `{"taskUid":0,"indexUid":"owned","status":"enqueued","type":"documentAdditionOrUpdate","enqueuedAt":"2026-11-08T08:05:30.372437829Z","future":{"n":9007199254740993}}`

func mockOpen(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/proxy/meili/health":
			_, _ = io.WriteString(w, `{"status":"available"}`)
		case "/proxy/meili/version":
			_, _ = io.WriteString(w, mockVersion)
		case "/proxy/meili/indexes":
			if r.Method == "GET" {
				_, _ = io.WriteString(w, `{"results":[],"offset":0,"limit":1,"total":0}`)
				return
			}
			handler(w, r)
		default:
			handler(w, r)
		}
	}))
	t.Cleanup(server.Close)
	c, err := Open(context.Background(), connections.MeilisearchConfig{Endpoint: server.URL + "/proxy/meili"}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c, server
}

func TestMeilisearchRequestJSONPreservesExactBytes(t *testing.T) {
	body := " {\n \"id\":\"owned\", \"n\":9007199254740993, \"decimal\":1.2300e-20, \"text\":\"原生🐱\"\n}\n"
	r, err := ParseRequest("\n# explicit native request\npatch /indexes/owned/settings\n" + body)
	if err != nil || r.Method != "PATCH" || string(r.Body) != body {
		t.Fatal("native request bytes changed", err)
	}
	var doc map[string]any
	if decodeJSON(r.Body, &doc) != nil || doc["n"] != json.Number("9007199254740993") || doc["decimal"] != json.Number("1.2300e-20") {
		t.Fatal("exact number spellings lost")
	}
	for _, source := range []string{
		"GET https://other.invalid/db", "GET //other.invalid/db", "GET /../other", "GET /%2e%2e/other", "GET /%252e%252e/other", "GET /%2fversion", "GET /db/%00", "GET /db/%ff", "GET /db/\\other", "GET /db#fragment", "GET /db?x=%xx", "GET /db\nGET /other", "POST /db\nAuthorization: secret", "POST /db\n{\"id\":1,\"\\u0069d\":2}", "POST /db\n{\"a\":\"\\ud800\"}", "POST /db\n{\"a\":\"\\udc00\"}", "POST /db\n{} {}", "OPTIONS /db", "GET /db\n{}",
	} {
		if _, err := ParseRequest(source); !errors.Is(err, ErrInvalid) {
			t.Fatal("ambiguous request accepted", source)
		}
	}
	for _, raw := range [][]byte{[]byte(strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66)), []byte(`{"x":[{"key":1,"key":2}]}`), []byte("{\"x\":\"\xff\"}")} {
		var v any
		if decodeJSON(raw, &v) == nil {
			t.Fatal("invalid bounded JSON accepted")
		}
	}
}

func TestMeilisearchHealthDoesNotBypassVersionAndPermissions(t *testing.T) {
	for _, mode := range []string{"bad_key", "forbidden_indexes", "wrong_version", "missing_version", "missing_indexes", "null_health"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer private-key" {
					t.Error("Bearer bytes changed")
				}
				switch r.URL.Path {
				case "/health":
					if mode == "null_health" {
						_, _ = io.WriteString(w, `{"status":null}`)
					} else {
						_, _ = io.WriteString(w, `{"status":"available"}`)
					}
				case "/version":
					if mode == "bad_key" {
						w.WriteHeader(403)
					} else if mode == "wrong_version" {
						_, _ = io.WriteString(w, strings.Replace(mockVersion, "1.56.1", "2.0.0", 1))
					} else if mode == "missing_version" {
						_, _ = io.WriteString(w, `{}`)
					} else {
						_, _ = io.WriteString(w, mockVersion)
					}
				default:
					if mode == "forbidden_indexes" {
						w.WriteHeader(403)
					} else {
						_, _ = io.WriteString(w, `{"results":[],"offset":0,"limit":1}`)
					}
				}
			}))
			client, err := Open(context.Background(), connections.MeilisearchConfig{Endpoint: server.URL, Auth: "bearer"}, "private-key")
			server.Close()
			if err == nil {
				client.Close()
				t.Fatal("unverified native connection accepted")
			}
			if strings.Contains(err.Error(), "private-key") {
				t.Fatal("secret exposed")
			}
		})
	}
}

func TestMeilisearchScopedHandshakeRequiresExactAuthorizedIndex(t *testing.T) {
	for _, mode := range []string{"valid", "wrong_uid", "denied", "missing_time"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					_, _ = io.WriteString(w, `{"status":"available"}`)
					return
				}
				if r.URL.Path != "/indexes/owned" || r.Header.Get("Authorization") != "Bearer owned-key" {
					t.Error("scoped handshake escaped explicit index")
				}
				switch mode {
				case "denied":
					w.WriteHeader(403)
				case "missing_time":
					_, _ = io.WriteString(w, `{"uid":"owned"}`)
				default:
					uid := "owned"
					if mode == "wrong_uid" {
						uid = "replacement"
					}
					_, _ = io.WriteString(w, `{"uid":"`+uid+`","createdAt":"2026-11-08T08:00:00Z","updatedAt":"2026-11-08T08:00:00Z","primaryKey":null}`)
				}
			}))
			defer server.Close()
			c, err := Open(context.Background(), connections.MeilisearchConfig{Endpoint: server.URL, Auth: "bearer", IndexUID: "owned"}, "owned-key")
			if mode == "valid" {
				if err != nil {
					t.Fatal("valid scoped index rejected", err)
				}
				defer c.Close()
				if c.Identity() != (Identity{}) {
					t.Fatal("scoped handshake fabricated version observation")
				}
			} else if err == nil {
				c.Close()
				t.Fatal("invalid scoped index authenticated")
			}
		})
	}
}

func TestMeilisearchCanceledBeforeDispatchDoesNotRetireOrSubmit(t *testing.T) {
	var calls atomic.Int32
	c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, `{}`) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Do(ctx, Request{Method: "POST", Path: "/indexes", Body: []byte(`{"uid":"owned"}`)}); !errors.Is(err, context.Canceled) || errors.Is(err, ErrRetired) || calls.Load() != 0 {
		t.Fatal("canceled pre-dispatch became unknown write")
	}
	if _, err := c.Do(context.Background(), Request{Method: "GET", Path: "/alive"}); err != nil || calls.Load() != 1 {
		t.Fatal("canceled pre-dispatch poisoned client")
	}
}

func TestMeilisearchRedirectAndProxyNeverLeaveOriginalContext(t *testing.T) {
	var leaked, requests atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
	t.Cleanup(other.Close)
	t.Setenv("HTTP_PROXY", other.URL)
	c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/proxy/meili/redirect" {
			t.Error("original context changed")
		}
		w.Header().Set("Location", other.URL+"/capture")
		w.WriteHeader(307)
		_, _ = io.WriteString(w, `{"error":"redirect"}`)
	})
	r, err := c.Do(context.Background(), Request{Method: "GET", Path: "/redirect"})
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != 307 || r.Status != 307 || leaked.Load() != 0 || requests.Load() != 1 {
		t.Fatal("redirect/proxy followed")
	}
	if c.http.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("environment proxy enabled")
	}
}

func TestMeilisearchNativeSearchErrorAndRawWriteOutcomeDiffer(t *testing.T) {
	var calls atomic.Int32
	c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"code":"invalid_search_filter","message":"private-response"}`)
	})
	_, response, err := c.Search(context.Background(), "owned", []byte(`{"q":"原生","filter":"broken"}`))
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != 400 || errors.Is(err, ErrRetired) || !bytes.Contains(response.Body, []byte("invalid_search_filter")) {
		t.Fatal("complete read error retired healthy client")
	}
	_, err = c.Do(context.Background(), Request{Method: "POST", Path: "/indexes/owned/search", Body: []byte(`{}`)})
	var interrupted *Interrupted
	if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown || strings.Contains(err.Error(), "private-response") {
		t.Fatal("raw POST inferred rollback or leaked response")
	}
	if _, err := c.Do(context.Background(), Request{Method: "POST", Path: "/indexes"}); !errors.Is(err, ErrRetired) || calls.Load() != 2 {
		t.Fatal("uncertain write replayed")
	}
}

func TestMeilisearchSearchPreservesNativeHitsAndRejectsAmbiguity(t *testing.T) {
	body := []byte(` {"q":"原生","limit":2} `)
	c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !bytes.Equal(raw, body) || r.URL.Path != "/proxy/meili/indexes/owned/search" || r.Method != "POST" {
			t.Error("constructed search changed")
		}
		_, _ = io.WriteString(w, `{"hits":[{"id":"one","n":9007199254740993}],"query":"原生","processingTimeMs":1,"limit":2,"offset":0,"estimatedTotalHits":1,"future":true}`)
	})
	result, _, err := c.Search(context.Background(), "owned", body)
	if err != nil || len(result.Hits) != 1 || !bytes.Contains(result.Hits[0], []byte("9007199254740993")) || !bytes.Contains(result.Raw, []byte(`"future":true`)) {
		t.Fatal("native hit/evidence lost", err)
	}
	for _, invalid := range []struct{ uid, body string }{{"../owned", `{}`}, {"owned/search", `{}`}, {"owned", `null`}, {"owned", `[]`}, {"owned", `{"q":"a","q":"b"}`}} {
		if _, _, err := c.Search(context.Background(), invalid.uid, []byte(invalid.body)); !errors.Is(err, ErrInvalid) {
			t.Fatal("ambiguous search accepted")
		}
	}
}

func TestMeilisearchResponseBoundsTruncationAndCloseAcknowledgement(t *testing.T) {
	for _, mode := range []string{"bounds", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
				if mode == "bounds" {
					_, _ = io.WriteString(w, strings.Repeat("x", responseLimit+1))
				} else {
					w.Header().Set("Content-Length", "1024")
					_, _ = io.WriteString(w, "incomplete")
				}
			})
			r, err := c.Do(context.Background(), Request{Method: "GET", Path: "/wait"})
			var interrupted *Interrupted
			if !errors.As(err, &interrupted) || interrupted.WriteOutcomeUnknown || len(r.Body) != 0 || mode == "bounds" && !errors.Is(err, ErrBounds) {
				t.Fatal("incomplete response accepted")
			}
		})
	}
	entered := make(chan struct{})
	c, server := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/wait") {
			close(entered)
			<-r.Context().Done()
		} else {
			_, _ = io.WriteString(w, `{}`)
		}
	})
	other, err := Open(context.Background(), connections.MeilisearchConfig{Endpoint: server.URL + "/proxy/meili"}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Close)
	settled := make(chan error, 1)
	go func() { _, err := c.Do(context.Background(), Request{Method: "GET", Path: "/wait"}); settled <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("original HTTP worker did not enter")
	}
	c.Close()
	c.Close()
	select {
	case err := <-settled:
		if !errors.Is(err, ErrRetired) {
			t.Fatal("Close did not revoke original worker")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not settle worker")
	}
	if _, err := other.Do(context.Background(), Request{Method: "GET", Path: "/alive"}); err != nil {
		t.Fatal("Close affected independent client", err)
	}
}
