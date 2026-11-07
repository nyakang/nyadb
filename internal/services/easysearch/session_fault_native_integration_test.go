package easysearch

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

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestEasysearchNativeMetadataRejectsCapturedIndexReplacementAndAliasChange(t *testing.T) {
	for _, change := range []string{"index_uuid", "alias_definition", "stream_rollover"} {
		t.Run(change, func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
			defer stop()
			direct := sessionNativeOpen(t, ctx)
			index := ownedIndex(t, direct, ctx)
			name, kind := index, "index"
			if change == "alias_definition" {
				name, kind = index+"_alias", "alias"
				body, _ := json.Marshal(map[string]any{"actions": []any{map[string]any{"add": map[string]any{"index": index, "alias": name, "search_routing": "original", "filter": map[string]any{"term": map[string]string{"name": "original"}}}}}})
				sessionNativeRun(t, direct, ctx, "POST", "/_aliases", body)
			} else if change == "stream_rollover" {
				name, kind = index+"_stream", "data_stream"
				template := index + "_template"
				body, _ := json.Marshal(map[string]any{"index_patterns": []string{name}, "data_stream": map[string]any{}, "template": map[string]any{"settings": map[string]int{"number_of_shards": 1, "number_of_replicas": 0}, "mappings": map[string]any{"properties": map[string]any{"@timestamp": map[string]string{"type": "date"}}}}})
				sessionNativeRun(t, direct, ctx, "PUT", "/_index_template/"+template, body)
				sessionNativeRun(t, direct, ctx, "PUT", "/_data_stream/"+name, nil)
				t.Cleanup(func() {
					cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					if _, err := direct.Do(cleanup, Request{Method: "DELETE", Path: "/_data_stream/" + name}); err != nil {
						t.Error("owned stream cleanup", err)
					}
					if _, err := direct.Do(cleanup, Request{Method: "DELETE", Path: "/_index_template/" + template}); err != nil {
						t.Error("owned template cleanup", err)
					}
				})
			}
			original, err := direct.ReadTargetMetadata(ctx, name, kind)
			if err != nil {
				t.Fatal("native original target identities", err)
			}
			var altered atomic.Bool
			relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(io.LimitReader(r.Body, requestLimit+1))
				response, err := direct.Do(r.Context(), Request{Method: r.Method, Path: r.URL.RequestURI(), ContentType: r.Header.Get("Content-Type"), Body: body})
				if err != nil {
					w.WriteHeader(502)
					return
				}
				trigger := r.URL.Path == "/"+name+"/_mapping"
				if change == "alias_definition" {
					trigger = r.URL.Path == "/_alias/"+name
				}
				if trigger && altered.CompareAndSwap(false, true) {
					switch change {
					case "index_uuid":
						_, err = direct.Do(ctx, Request{Method: "DELETE", Path: "/" + index})
						if err == nil {
							_, err = direct.Do(ctx, Request{Method: "PUT", Path: "/" + index, Body: []byte(`{"settings":{"number_of_replicas":0},"mappings":{"properties":{"replacement":{"type":"keyword"}}}}`)})
						}
					case "alias_definition":
						body, _ := json.Marshal(map[string]any{"actions": []any{map[string]any{"add": map[string]any{"index": index, "alias": name, "search_routing": "changed", "filter": map[string]any{"term": map[string]string{"name": "changed"}}}}}})
						_, err = direct.Do(ctx, Request{Method: "POST", Path: "/_aliases", Body: body})
					case "stream_rollover":
						_, err = direct.Do(ctx, Request{Method: "POST", Path: "/" + name + "/_rollover", Body: []byte(`{}`)})
					}
					if err != nil {
						w.WriteHeader(502)
						return
					}
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
			object := s.Namespace("").Object(kind, name)
			metadata, err := s.TableMetadata(ctx, object)
			if metadata != nil || !errors.Is(err, ErrTargetChanged) || !altered.Load() {
				t.Fatal("changed real target published as original metadata", err)
			}
			current, err := direct.ReadTargetMetadata(ctx, name, kind)
			if err != nil {
				t.Fatal("explicit current metadata read", err)
			}
			if change == "index_uuid" && current.IndexUUIDs[index] == original.IndexUUIDs[index] {
				t.Fatal("fixture did not replace native index UUID")
			}
			if change == "alias_definition" && sameNativeJSON(current.Aliases, original.Aliases) {
				t.Fatal("fixture did not replace native alias definition")
			}
			if change == "stream_rollover" && len(current.IndexUUIDs) <= len(original.IndexUUIDs) {
				t.Fatal("fixture did not roll over real data stream")
			}
			metadata, err = s.TableMetadata(ctx, object)
			if err != nil || metadata == nil {
				t.Fatal("explicit metadata refresh could not capture current target", err)
			}
		})
	}
}

func TestEasysearchNativeQueryLostCommittedReplyRequiresExplicitRecoveryWithoutReplay(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	direct := sessionNativeOpen(t, ctx)
	index := ownedIndex(t, direct, ctx)
	var writes atomic.Int32
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, requestLimit+1))
		response, err := direct.Do(r.Context(), Request{Method: r.Method, Path: r.URL.RequestURI(), ContentType: r.Header.Get("Content-Type"), Body: body})
		if err != nil {
			w.WriteHeader(502)
			return
		}
		if r.Method == "PUT" && r.URL.Path == "/"+index+"/_create/lease" {
			writes.Add(1)
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
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
	lease, err := s.ReserveQuery(40)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if err = lease.Preconnect(ctx); err != nil {
		t.Fatal(err)
	}
	request := "PUT /" + index + "/_create/lease\n{\"exact\":9007199254740993}"
	result, err := lease.Execute(ctx, s.identity.ClusterName, request)
	if !errors.Is(err, contract.ErrSQLSessionLost) || len(result.Steps) != 1 || result.Steps[0].State != "uncertain" || writes.Load() != 1 {
		t.Fatal("lost committed REST write was reported as normal completion", err)
	}
	if _, err = lease.Execute(ctx, s.identity.ClusterName, request); !errors.Is(err, contract.ErrSQLSessionLost) || writes.Load() != 1 {
		t.Fatal("lost original lease replayed a native write", err)
	}
	fresh, err := lease.Replace()
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Release()
	if writes.Load() != 1 {
		t.Fatal("explicit replacement replayed original request")
	}
	result, err = fresh.Execute(ctx, s.identity.ClusterName, "GET /"+index+"/_doc/lease")
	if err != nil || len(result.Sets) != 1 || result.Sets[0].Rows[0][0].Text != "200" || result.Sets[0].Rows[0][1].Kind != contract.JSON || !strings.Contains(result.Sets[0].Rows[0][1].Text, "9007199254740993") || writes.Load() != 1 {
		t.Fatal("explicit read did not retain exact committed write without replay", err)
	}
}
