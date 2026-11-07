package easysearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestEasysearchNativeDataExportLostAdvanceFailsWithoutReplayAndReleasesScroll(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	direct := sessionNativeOpen(t, ctx)
	index := ownedIndex(t, direct, ctx)
	var bulk bytes.Buffer
	for i := 0; i < 201; i++ {
		fmt.Fprintf(&bulk, "{\"create\":{\"_index\":%q,\"_id\":\"row-%03d\"}}\n{\"name\":\"original\"}\n", index, i)
	}
	r := sessionNativeRun(t, direct, ctx, "POST", "/_bulk?refresh=wait_for", bulk.Bytes())
	var seeded struct {
		Errors *bool             `json:"errors"`
		Items  []json.RawMessage `json:"items"`
	}
	if decodeJSON(r.Body, &seeded) != nil || seeded.Errors == nil || *seeded.Errors || len(seeded.Items) != 201 {
		t.Fatal("native seed incomplete")
	}
	var advances atomic.Int32
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(request.Body, requestLimit+1))
		response, e := direct.Do(request.Context(), Request{Method: request.Method, Path: request.URL.RequestURI(), ContentType: request.Header.Get("Content-Type"), Body: body})
		if e != nil {
			w.WriteHeader(502)
			return
		}
		if request.Method == "POST" && request.URL.Path == "/_search/scroll" {
			advances.Add(1)
			connection, _, e := w.(http.Hijacker).Hijack()
			if e == nil {
				_ = connection.Close()
			}
			return
		}
		w.Header().Set("Content-Type", response.ContentType)
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
	}))
	defer relay.Close()
	raw, err := OpenSession(ctx, sessionProfile(t, relayConfig(t, relay)), "")
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	defer s.Close()
	l := nativeDataLease(t, s, "index", index)
	emitted := 0
	err = l.exportRows(ctx, s.ProfileID(), s.identity.ClusterUUID, index, contract.DataOptions{Object: l.object}, nil, func([]contract.DataValue) error { emitted++; return nil })
	var interrupted *Interrupted
	if !errors.As(err, &interrupted) || interrupted.WriteOutcomeUnknown || advances.Load() != 1 || emitted != 100 {
		t.Fatal("advanced export replayed or falsely reported complete", err, emitted)
	}
	nativeNoPITs(t, ctx, direct, index)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEasysearchNativeDataCloseCancelsUnpublishedPageAndKeepsOtherDocument(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	direct := sessionNativeOpen(t, ctx)
	index := ownedIndex(t, direct, ctx)
	if _, err := direct.CreateDocument(ctx, DocumentID{Index: index, ID: "one"}, []byte(`{"name":"original"}`)); err != nil {
		t.Fatal(err)
	}
	entered, settled := make(chan struct{}), make(chan struct{})
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(request.Body, requestLimit+1))
		response, e := direct.Do(request.Context(), Request{Method: request.Method, Path: request.URL.RequestURI(), ContentType: request.Header.Get("Content-Type"), Body: body})
		if e != nil {
			w.WriteHeader(502)
			return
		}
		if request.URL.Path == "/_search" {
			close(entered)
			<-request.Context().Done()
			close(settled)
			return
		}
		w.Header().Set("Content-Type", response.ContentType)
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
	}))
	defer relay.Close()
	raw, err := OpenSession(ctx, sessionProfile(t, relayConfig(t, relay)), "")
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	defer s.Close()
	l := nativeDataLease(t, s, "index", index)
	other := nativeDataLease(t, s, "index", index)
	completed := make(chan error, 1)
	go func() {
		p, e := l.page(ctx, s.ProfileID(), s.identity.ClusterUUID, index, contract.DataOptions{Object: l.object, Limit: 1})
		if p != nil {
			e = errors.New("canceled page was published")
		}
		completed <- e
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("original search did not start")
	}
	if n, e := other.count(ctx, s.ProfileID(), s.identity.ClusterUUID, index); e != nil || n != 1 {
		t.Fatal("parked native request blocked other document", e)
	}
	if err = l.Close(); err != nil {
		t.Fatal("canceled document resource cleanup", err)
	}
	select {
	case e := <-completed:
		if !errors.Is(e, context.Canceled) {
			t.Fatal("original page did not settle as canceled", e)
		}
	case <-ctx.Done():
		t.Fatal("original worker did not settle")
	}
	select {
	case <-settled:
	case <-ctx.Done():
		t.Fatal("native relay did not observe cancellation")
	}
	if l.client != nil || l.cursor != nil || l.binding != nil || l.key != [32]byte{} {
		t.Fatal("unpublished document retained resource or credentials")
	}
	if n, e := other.count(ctx, s.ProfileID(), s.identity.ClusterUUID, index); e != nil || n != 1 {
		t.Fatal("closing one document revoked another", e)
	}
	nativeNoPITs(t, ctx, direct, index)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEasysearchNativeDataRejectsScopeChangeAfterReadReceipt(t *testing.T) {
	for _, operation := range []string{"page", "count", "export", "export_callback"} {
		t.Run(operation, func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
			defer stop()
			direct := sessionNativeOpen(t, ctx)
			index := ownedIndex(t, direct, ctx)
			alias := "NyaDB_" + index
			for _, id := range []string{"one", "two"} {
				if _, e := direct.CreateDocument(ctx, DocumentID{Index: index, ID: id, Routing: "original"}, []byte(`{"name":"original"}`)); e != nil {
					t.Fatal(e)
				}
			}
			setAlias := func(route string) error {
				body, _ := json.Marshal(map[string]any{"actions": []any{map[string]any{"add": map[string]any{"index": index, "alias": alias, "search_routing": route, "filter": map[string]any{"term": map[string]string{"name": route}}}}}})
				_, e := direct.Do(ctx, Request{Method: "POST", Path: "/_aliases", Body: body})
				return e
			}
			if e := setAlias("original"); e != nil {
				t.Fatal(e)
			}
			var changed atomic.Bool
			relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(io.LimitReader(r.Body, requestLimit+1))
				response, e := direct.Do(r.Context(), Request{Method: r.Method, Path: r.URL.RequestURI(), ContentType: r.Header.Get("Content-Type"), Body: body})
				if e != nil {
					w.WriteHeader(502)
					return
				}
				trigger := r.URL.Path == "/_search"
				if operation == "export_callback" {
					trigger = false
				}
				if operation == "count" {
					trigger = r.URL.Path == "/"+alias+"/_count"
				}
				if operation == "export" {
					trigger = r.URL.Path == "/"+alias+"/_search"
				}
				if trigger && changed.CompareAndSwap(false, true) {
					if e = setAlias("replacement"); e != nil {
						w.WriteHeader(502)
						return
					}
				}
				w.Header().Set("Content-Type", response.ContentType)
				w.WriteHeader(response.Status)
				_, _ = w.Write(response.Body)
			}))
			defer relay.Close()
			raw, e := OpenSession(ctx, sessionProfile(t, relayConfig(t, relay)), "")
			if e != nil {
				t.Fatal(e)
			}
			s := raw.(*Session)
			defer s.Close()
			l := nativeDataLease(t, s, "alias", alias)
			emitted := 0
			switch operation {
			case "page":
				p, err := l.page(ctx, s.ProfileID(), s.identity.ClusterUUID, alias, contract.DataOptions{Object: l.object, Limit: 1})
				e = err
				if p != nil {
					t.Fatal("changed page was published")
				}
			case "count":
				n, err := l.count(ctx, s.ProfileID(), s.identity.ClusterUUID, alias)
				e = err
				if n != 0 {
					t.Fatal("changed count was published")
				}
			case "export", "export_callback":
				e = l.exportRows(ctx, s.ProfileID(), s.identity.ClusterUUID, alias, contract.DataOptions{Object: l.object}, nil, func([]contract.DataValue) error {
					emitted++
					if operation == "export_callback" && changed.CompareAndSwap(false, true) {
						return setAlias("replacement")
					}
					return nil
				})
			}
			want := 0
			if operation == "export_callback" {
				want = 2
			}
			if !errors.Is(e, ErrTargetChanged) || !changed.Load() || emitted != want {
				t.Fatal("late native scope change was accepted", e, emitted)
			}
			if e = l.Close(); e != nil {
				t.Fatal("changed scope retained original resource", e)
			}
			nativeNoPITs(t, ctx, direct, index)
		})
	}
}
