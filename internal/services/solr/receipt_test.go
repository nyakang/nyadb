package solr

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSolrPartialAndOversizedWriteReceiptsRetireWithoutReplay(t *testing.T) {
	for _, fault := range []string{"partial", "oversized"} {
		t.Run(fault, func(t *testing.T) {
			var writes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/solr/admin/info/system" {
					_, _ = w.Write([]byte(testIdentity))
					return
				}
				writes.Add(1)
				if fault == "partial" {
					w.Header().Set("Content-Length", "1024")
					w.WriteHeader(200)
					_, _ = w.Write([]byte(`{"responseHeader":`))
					w.(http.Flusher).Flush()
					conn, _, e := w.(http.Hijacker).Hijack()
					if e != nil {
						t.Error(e)
						return
					}
					_ = conn.Close()
					return
				}
				_, _ = w.Write(bytes.Repeat([]byte("x"), responseLimit+1))
			}))
			defer server.Close()
			c, e := Open(context.Background(), connections.SolrConfig{Endpoint: server.URL}, "")
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			response, e := c.Do(context.Background(), Request{Method: "POST", Path: "/owned/update", Body: []byte(`[]`)})
			var lost *Interrupted
			if response.Status != 200 || len(response.Body) != 0 || !errors.As(e, &lost) || !lost.WriteOutcomeUnknown || writes.Load() != 1 {
				t.Fatal("incomplete HTTP 200 was treated as a complete write receipt", e)
			}
			if fault == "oversized" && !errors.Is(e, ErrBounds) {
				t.Fatal("response limit was lost", e)
			}
			if _, e = c.Do(context.Background(), Request{Method: "POST", Path: "/owned/update", Body: []byte(`[]`)}); !errors.Is(e, ErrRetired) || writes.Load() != 1 {
				t.Fatal("uncertain receipt was replayed")
			}
		})
	}
}
func TestSolrRequestBodyIsCapturedBeforeCallerChangesIt(t *testing.T) {
	entered, read := make(chan struct{}), make(chan struct{})
	actual := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/solr/admin/info/system" {
			_, _ = w.Write([]byte(testIdentity))
			return
		}
		close(entered)
		<-read
		body, e := io.ReadAll(r.Body)
		if e != nil {
			t.Error(e)
		}
		actual <- body
		_, _ = w.Write([]byte(`{"responseHeader":{"status":0}}`))
	}))
	defer server.Close()
	c, e := Open(context.Background(), connections.SolrConfig{Endpoint: server.URL}, "")
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	body := bytes.Repeat([]byte("x"), 1<<16)
	original := bytes.Clone(body)
	done := make(chan error, 1)
	go func() {
		_, e := c.Do(context.Background(), Request{Method: "POST", Path: "/owned/update", ContentType: "text/csv", Body: body})
		done <- e
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(read)
		t.Fatal("worker did not enter")
	}
	for i := range body {
		body[i] = 'y'
	}
	close(read)
	select {
	case e = <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original worker did not settle")
	}
	if !bytes.Equal(<-actual, original) {
		t.Fatal("worker submitted caller's changed body")
	}
}
