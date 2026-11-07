package easysearch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestEasysearchNativeScrollLostResponseDoesNotReplayAdvancedResource(t *testing.T) {
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
	for _, id := range []string{"a", "b", "c"} {
		if _, err = direct.CreateDocument(ctx, DocumentID{Index: index, ID: id}, []byte(`{"name":"original"}`)); err != nil {
			t.Fatal(err)
		}
	}
	var advances atomic.Int32
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(request.Body, requestLimit+1))
		response, err := direct.Do(request.Context(), Request{Method: request.Method, Path: request.URL.RequestURI(), ContentType: request.Header.Get("Content-Type"), Body: body})
		if err != nil {
			w.WriteHeader(502)
			return
		}
		if request.Method == "POST" && request.URL.Path == "/_search/scroll" {
			advances.Add(1)
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
	client, err := Open(ctx, relayConfig(t, relay), "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	scroll, err := client.OpenScroll(ctx, index, []byte(`{"match_all":{}}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	if first, err := scroll.Next(ctx); err != nil || len(first.Documents) != 1 || !first.HasMore {
		t.Fatal("original scroll first page", err)
	}
	_, err = scroll.Next(ctx)
	var interrupted *Interrupted
	if !errors.As(err, &interrupted) || interrupted.WriteOutcomeUnknown || advances.Load() != 1 {
		t.Fatal("native advanced scroll was replayed or lost its read semantics", err)
	}
	if _, err = scroll.Next(ctx); !errors.Is(err, ErrRetired) || advances.Load() != 1 {
		t.Fatal("interrupted original scroll advanced again", err)
	}
	cleanup, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()
	if err = scroll.Close(cleanup); err != nil {
		t.Fatal("captured original scroll cleanup after acknowledgement loss", err)
	}
}
