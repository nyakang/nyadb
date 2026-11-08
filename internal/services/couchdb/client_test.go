package couchdb

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

const mockIdentity = `{"couchdb":"Welcome","uuid":"0123456789abcdef0123456789abcdef","version":"3.5.3","vendor":{"name":"The Apache Software Foundation"}}`
const mockAnonymous = `{"ok":true,"userCtx":{"name":null,"roles":[]},"info":{}}`

func mockOpen(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/proxy/couchdb/":
			_, _ = io.WriteString(w, mockIdentity)
		case "/proxy/couchdb/_session":
			_, _ = io.WriteString(w, mockAnonymous)
		default:
			handler(w, r)
		}
	}))
	t.Cleanup(server.Close)
	c, err := Open(context.Background(), connections.CouchDBConfig{Endpoint: server.URL + "/proxy/couchdb"}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c, server
}

func TestCouchDBRequestAndJSONPreserveNativeBytes(t *testing.T) {
	body := " {\n \"_id\":\"原生🐱\", \"n\":9007199254740993, \"decimal\":1.2300e-20, \"nested\":[null,false,\"\\ud83d\\udc31\"]\n}\n"
	r, err := ParseRequest("\n# native request\nput /db/doc%20name?rev=1-test\n" + body)
	if err != nil || r.Method != "PUT" || r.Path != "/db/doc%20name?rev=1-test" || string(r.Body) != body {
		t.Fatal("native request changed", err)
	}
	var doc map[string]any
	if decodeJSON(r.Body, &doc) != nil || doc["n"] != json.Number("9007199254740993") || doc["decimal"] != json.Number("1.2300e-20") {
		t.Fatal("native number spellings lost")
	}
	for _, source := range []string{
		"GET https://other.invalid/db", "GET //other.invalid/db", "GET /../other", "GET /%2e%2e/other", "GET /%252e%252e/other", "GET /db/%00", "GET /db/%ff", "GET /db/\\other", "GET /db#fragment", "GET /db?x=%xx", "GET /db\nGET /other", "POST /db\nAuthorization: secret", "POST /db\n{\"id\":1,\"\\u0069d\":2}", "POST /db\n{\"a\":\"\\ud800\"}", "POST /db\n{\"a\":\"\\udc00\"}", "POST /db\n{\"a\":\"\\ud800\\u0041\"}", "POST /db\n{\"a\":\"\\ud800\\ud800\"}", "POST /db\n{} {}", "PATCH /db\n{}",
	} {
		if _, err := ParseRequest(source); !errors.Is(err, ErrInvalid) {
			t.Fatal("ambiguous native request accepted", source)
		}
	}
	for _, raw := range [][]byte{[]byte(strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66)), []byte(`{"x":[{"key":1,"key":2}]}`), []byte("{\"x\":\"\xff\"}")} {
		var v any
		if decodeJSON(raw, &v) == nil {
			t.Fatal("invalid bounded JSON accepted")
		}
	}
	for _, raw := range []string{`{"x":"\\ud800"}`, `{"x":"\ud83d\udc31"}`, `{"x":"\uFFFD"}`} {
		var v any
		if decodeJSON([]byte(raw), &v) != nil {
			t.Fatal("valid Unicode escape rejected")
		}
	}
}

func TestCouchDBOpenVerifiesPrincipalRatherThanRootSuccess(t *testing.T) {
	for _, session := range []string{mockAnonymous, `{"ok":true,"userCtx":{"name":"different","roles":[]},"info":{"authenticated":"default"}}`, `{"ok":true,"userCtx":{"name":"native","roles":[]},"info":{"authenticated":"cookie"}}`, `{"ok":true,"userCtx":{"name":"native"},"info":{"authenticated":"default"}}`, `{"ok":true,"ok":false,"userCtx":{"name":"native","roles":[]},"info":{"authenticated":"default"}}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				_, _ = io.WriteString(w, mockIdentity)
			} else {
				_, _ = io.WriteString(w, session)
			}
		}))
		client, err := Open(context.Background(), connections.CouchDBConfig{Endpoint: server.URL, Auth: "basic", Username: "native"}, "private-password")
		server.Close()
		if err == nil {
			client.Close()
			t.Fatal("root HTTP 200 authenticated an unverified principal")
		}
		if strings.Contains(err.Error(), "private-password") {
			t.Fatal("secret exposed")
		}
	}
}

func TestCouchDBRedirectsAndEscapesNeverLeaveEndpointContext(t *testing.T) {
	var received atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { received.Add(1) }))
	t.Cleanup(other.Close)
	var original atomic.Int32
	c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
		original.Add(1)
		if r.URL.Path != "/proxy/couchdb/db" {
			t.Error("original context changed")
		}
		w.Header().Set("Location", other.URL+"/capture")
		w.WriteHeader(307)
		_, _ = io.WriteString(w, `{"error":"redirect"}`)
	})
	r, err := c.Do(context.Background(), Request{Method: "GET", Path: "/db"})
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != 307 || r.Status != 307 || string(r.Body) != `{"error":"redirect"}` || received.Load() != 0 || original.Load() != 1 {
		t.Fatal("redirect followed or original receipt lost")
	}
	for _, path := range []string{"/%2e%2e/db", "//other/db", "/../db"} {
		if _, err := c.Do(context.Background(), Request{Method: "PUT", Path: path}); !errors.Is(err, ErrInvalid) {
			t.Fatal("endpoint escape submitted")
		}
	}
	if original.Load() != 1 {
		t.Fatal("invalid path reached transport")
	}
}

func TestCouchDBCompleteWriteErrorKeepsReceiptWithoutInferringRollback(t *testing.T) {
	var writes atomic.Int32
	c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
		writes.Add(1)
		w.WriteHeader(500)
		_, _ = io.WriteString(w, `{"error":"after_commit","reason":"private-response"}`)
	})
	r, err := c.Do(context.Background(), Request{Method: "PUT", Path: "/db/doc", Body: []byte(`{"value":1}`)})
	var status *HTTPError
	var uncertain *Interrupted
	if !errors.As(err, &status) || status.Status != 500 || !errors.As(err, &uncertain) || !uncertain.WriteOutcomeUnknown || !bytes.Contains(r.Body, []byte("after_commit")) || strings.Contains(err.Error(), "private-response") {
		t.Fatal("generic error inferred rollback or leaked receipt in diagnostic")
	}
	if _, err := c.Do(context.Background(), Request{Method: "PUT", Path: "/db/doc"}); !errors.Is(err, ErrRetired) || writes.Load() != 1 {
		t.Fatal("unknown write replayed")
	}
}

func TestCouchDBResponseBoundsAndTruncationRetireOriginalClient(t *testing.T) {
	for _, mode := range []string{"bounds", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
				if mode == "bounds" {
					_, _ = io.CopyN(w, strings.NewReader(strings.Repeat("x", responseLimit+1)), responseLimit+1)
				} else {
					w.Header().Set("Content-Length", "1024")
					_, _ = io.WriteString(w, "incomplete")
				}
			})
			r, err := c.Do(context.Background(), Request{Method: "GET", Path: "/db"})
			var interrupted *Interrupted
			if !errors.As(err, &interrupted) || interrupted.WriteOutcomeUnknown || len(r.Body) != 0 || (mode == "bounds" && !errors.Is(err, ErrBounds)) {
				t.Fatal("incomplete response accepted as cached data")
			}
		})
	}
}

func TestCouchDBCloseWaitsForOriginalWorkerAndKeepsOtherClient(t *testing.T) {
	entered := make(chan struct{})
	c, server := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/proxy/couchdb/wait" {
			close(entered)
			<-r.Context().Done()
		} else {
			_, _ = io.WriteString(w, `{"ok":true}`)
		}
	})
	other, err := Open(context.Background(), connections.CouchDBConfig{Endpoint: server.URL + "/proxy/couchdb"}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	settled := make(chan error, 1)
	go func() { _, err := c.Do(ctx, Request{Method: "GET", Path: "/wait"}); settled <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("original worker did not enter")
	}
	c.Close()
	select {
	case err := <-settled:
		if !errors.Is(err, ErrRetired) {
			t.Fatal("original Close did not settle worker")
		}
	case <-time.After(time.Second):
		t.Fatal("original worker did not acknowledge Close")
	}
	if _, err := other.Do(context.Background(), Request{Method: "GET", Path: "/alive"}); err != nil {
		t.Fatal("old Close affected independent client", err)
	}
}
