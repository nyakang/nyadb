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

func nativeSave(t *testing.T, ctx context.Context, l *dataLease, edits ...contract.DataEdit) *contract.DataEditResult {
	t.Helper()
	r, err := l.Runtime().Saver(ctx, l.object.ProfileID, l.object.Catalog, l.object.Name, edits)
	if err != nil || r == nil || !r.CommitConfirmed || r.CommitUncertain || r.Incomplete || r.Applied != len(edits) || len(r.Outcomes) != len(edits) || !r.RefreshRequired {
		t.Fatal("native save did not acknowledge every original edit", r, err)
	}
	return r
}

func nativeEditSnapshot(t *testing.T, ctx context.Context, l *dataLease, id, mode string) *contract.RowSnapshot {
	t.Helper()
	p, err := l.page(ctx, l.object.ProfileID, l.object.Catalog, l.object.Name, contract.DataOptions{Object: l.object, Limit: 10, FilterColumn: "_id", FilterMode: "equal", FilterValue: id})
	if err != nil || len(p.RowSnapshots) != 1 {
		t.Fatal("native edit snapshot not unique", err)
	}
	s := p.RowSnapshots[0]
	if mode == "values" {
		row, e := l.row(ctx, l.object.ProfileID, l.object.Catalog, l.object.Name, s.Ref)
		if e != nil || row.Snapshot.Mode != "values" {
			t.Fatal("complete native values snapshot unavailable", e)
		}
		s = row.Snapshot
	}
	if s.Mode != mode {
		t.Fatal("wrong validation mode", s.Mode)
	}
	return s
}

func documentEdit(kind string, original *contract.RowSnapshot, source string) contract.DataEdit {
	e := contract.DataEdit{Kind: kind, Original: original}
	if kind != "delete" {
		e.Values = map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: source}}
	}
	return e
}

func nativeEditRelay(t *testing.T, ctx context.Context, direct *Client, hook func(http.ResponseWriter, *http.Request, *Response) bool) *Session {
	t.Helper()
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, e := io.ReadAll(io.LimitReader(r.Body, requestLimit+1))
		if e != nil {
			w.WriteHeader(502)
			return
		}
		response, e := direct.Do(r.Context(), Request{Method: r.Method, Path: r.URL.RequestURI(), ContentType: r.Header.Get("Content-Type"), Body: body})
		var native *HTTPError
		if e != nil && !errors.As(e, &native) {
			w.WriteHeader(502)
			return
		}
		if hook(w, r, &response) {
			return
		}
		w.Header().Set("Content-Type", response.ContentType)
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
	}))
	t.Cleanup(relay.Close)
	raw, err := OpenSession(ctx, sessionProfile(t, relayConfig(t, relay)), "")
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func isDocumentWrite(r *http.Request) bool {
	return (r.Method == "PUT" || r.Method == "DELETE") && (strings.Contains(r.URL.Path, "/_create/") || strings.Contains(r.URL.Path, "/_doc/"))
}

func dropNativeReply(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Error("native reply interception", err)
		return
	}
	_ = conn.Close()
}

func TestEasysearchNativeDataCRUDValidationModesPipelinesAndRefresh(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 120*time.Second)
	defer stop()
	admin := sessionNativeOpen(t, ctx)
	index := ownedIndex(t, admin, ctx)
	sessionNativeRun(t, admin, ctx, "PUT", "/"+index+"/_mapping", []byte(`{"dynamic":false}`))
	s := nativeDocumentSession(t, ctx)
	l := nativeDataLease(t, s, "index", index)
	first, err := l.page(ctx, s.ProfileID(), l.object.Catalog, index, contract.DataOptions{Object: l.object, Limit: 1})
	if err != nil || first.Editable == nil || !*first.Editable {
		t.Fatal("ordinary index not editable", err)
	}
	source := `{"n":9007199254740993,"decimal":12345678901234567890.123456789,"name":"original","nested":[1,null,"猫"]}`
	insert := documentEdit("insert", nil, source)
	insert.Values["_id"] = contract.DataValue{Text: "exact"}
	insert.Values["_routing"] = contract.DataValue{Text: "tenant-a"}
	result := nativeSave(t, ctx, l, insert)
	if result.Outcomes[0].Saved == nil || !strings.Contains(result.Outcomes[0].Previews[3].Text, "9007199254740993") {
		t.Fatal("insert did not retain actual exact saved source")
	}
	if _, err = l.page(ctx, s.ProfileID(), l.object.Catalog, index, contract.DataOptions{Object: l.object, Limit: 1, After: first.CurrentCursor}); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("old PIT boundary survived a live write", err)
	}
	for _, mode := range []string{"fingerprints", "values"} {
		original := nativeEditSnapshot(t, ctx, l, "exact", mode)
		updated := `{"n":9007199254740993,"name":"` + mode + `"}`
		result = nativeSave(t, ctx, l, documentEdit("update", original, updated))
		if result.Outcomes[0].Saved.Mode != "fingerprints" || result.Outcomes[0].Previews[2].Text != "tenant-a" || result.Outcomes[0].Previews[3].Text != updated {
			t.Fatal("save crossed routing identity or lost server source")
		}
		if _, err = l.save(ctx, s.ProfileID(), l.object.Catalog, index, []contract.DataEdit{documentEdit("update", original, `{"name":"stale"}`)}); !errors.Is(err, contract.ErrDataConflict) {
			t.Fatal("stale native tokens were replaced or replayed", err)
		}
	}
	pipeline := index + "_pipeline"
	sessionNativeRun(t, admin, ctx, "PUT", "/_ingest/pipeline/"+pipeline, []byte(`{"processors":[{"set":{"field":"native_pipeline","value":"saved-on-server"}}]}`))
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Do(cleanup, Request{Method: "DELETE", Path: "/_ingest/pipeline/" + pipeline})
	})
	sessionNativeRun(t, admin, ctx, "PUT", "/"+index+"/_settings", []byte(`{"index.default_pipeline":"`+pipeline+`"}`))
	original := nativeEditSnapshot(t, ctx, l, "exact", "values")
	result = nativeSave(t, ctx, l, documentEdit("update", original, `{"name":"submitted"}`))
	if !strings.Contains(result.Outcomes[0].Previews[3].Text, "saved-on-server") {
		t.Fatal("submitted JSON was presented as the actual pipeline source")
	}
	generated := nativeSave(t, ctx, l, documentEdit("insert", nil, `{"name":"generated"}`))
	identity, err := l.ref(l.object.Catalog, generated.Outcomes[0].Saved.Ref)
	if err != nil || !strings.HasPrefix(identity.ID.ID, "nya_") {
		t.Fatal("generated native ID not retained", err)
	}
	nativeSave(t, ctx, l, documentEdit("delete", nativeEditSnapshot(t, ctx, l, "exact", "values"), ""))
	if _, err = admin.GetDocument(ctx, DocumentID{Index: index, ID: "exact", Routing: "tenant-a"}); !isNativeStatus(err, 404) {
		t.Fatal("original routed document still present", err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	nativeNoPITs(t, ctx, admin, index)
}

func TestEasysearchNativeDataAliasAndStreamWriteIdentity(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 120*time.Second)
	defer stop()
	admin := sessionNativeOpen(t, ctx)
	index := ownedIndex(t, admin, ctx)
	alias := "NyaDB_" + index
	setAlias := func(actions ...any) {
		body, _ := json.Marshal(map[string]any{"actions": actions})
		sessionNativeRun(t, admin, ctx, "POST", "/_aliases", body)
	}
	setAlias(map[string]any{"add": map[string]any{"index": index, "alias": alias, "is_write_index": true, "index_routing": "original", "search_routing": "original", "filter": map[string]any{"term": map[string]string{"name": "original"}}}})
	s := nativeDocumentSession(t, ctx)
	l := nativeDataLease(t, s, "alias", alias)
	insert := documentEdit("insert", nil, `{"name":"original"}`)
	insert.Values["_id"] = contract.DataValue{Text: "alias"}
	result := nativeSave(t, ctx, l, insert)
	if result.Outcomes[0].Previews[0].Text != index || result.Outcomes[0].Previews[2].Text != "original" {
		t.Fatal("uppercase native alias lost write-index routing")
	}
	nativeSave(t, ctx, l, documentEdit("update", nativeEditSnapshot(t, ctx, l, "alias", "values"), `{"name":"original","updated":true}`))
	nativeSave(t, ctx, l, documentEdit("delete", nativeEditSnapshot(t, ctx, l, "alias", "fingerprints"), ""))
	other := ownedIndex(t, admin, ctx)
	ambiguous := index + "_ambiguous"
	setAlias(map[string]any{"add": map[string]any{"index": index, "alias": ambiguous}}, map[string]any{"add": map[string]any{"index": other, "alias": ambiguous}})
	amb := nativeDataLease(t, s, "alias", ambiguous)
	if _, err := amb.save(ctx, s.ProfileID(), amb.object.Catalog, ambiguous, []contract.DataEdit{documentEdit("insert", nil, `{"name":"ambiguous"}`)}); !errors.Is(err, contract.ErrDataReadOnly) {
		t.Fatal("ambiguous alias allowed writes", err)
	}
	stream, template := index+"_edit_stream", index+"_edit_template"
	sessionNativeRun(t, admin, ctx, "PUT", "/_index_template/"+template, []byte(`{"index_patterns":["`+stream+`"],"data_stream":{},"template":{"settings":{"number_of_shards":1,"number_of_replicas":0}}}`))
	sessionNativeRun(t, admin, ctx, "PUT", "/_data_stream/"+stream, nil)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Do(cleanup, Request{Method: "DELETE", Path: "/_data_stream/" + stream})
		_, _ = admin.Do(cleanup, Request{Method: "DELETE", Path: "/_index_template/" + template})
	})
	ls := nativeDataLease(t, s, "data_stream", stream)
	insert = documentEdit("insert", nil, `{"@timestamp":"2026-11-08T00:00:00Z","name":"original"}`)
	insert.Values["_id"] = contract.DataValue{Text: "stream"}
	result = nativeSave(t, ctx, ls, insert)
	if !strings.HasPrefix(result.Outcomes[0].Previews[0].Text, ".ds-"+stream) {
		t.Fatal("native stream receipt did not capture concrete backing")
	}
	nativeSave(t, ctx, ls, documentEdit("update", nativeEditSnapshot(t, ctx, ls, "stream", "values"), `{"@timestamp":"2026-11-08T00:00:00Z","name":"changed"}`))
	nativeSave(t, ctx, ls, documentEdit("delete", nativeEditSnapshot(t, ctx, ls, "stream", "fingerprints"), ""))
	insert.Values["_routing"] = contract.DataValue{Text: "unsupported"}
	if _, err := ls.save(ctx, s.ProfileID(), ls.object.Catalog, stream, []contract.DataEdit{insert}); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("stream custom routing permitted without native capability", err)
	}
	sessionNativeRun(t, admin, ctx, "POST", "/"+stream+"/_rollover", []byte(`{}`))
	delete(insert.Values, "_routing")
	if _, err := ls.save(ctx, s.ProfileID(), ls.object.Catalog, stream, []contract.DataEdit{insert}); !errors.Is(err, ErrTargetChanged) {
		t.Fatal("original stream document followed rollover", err)
	}
}

func TestEasysearchNativeDataLostAndMalformedWriteVerificationNeverReplays(t *testing.T) {
	for _, fault := range []string{"lost_insert", "lost_update", "lost_delete", "malformed", "pipeline_lost", "pipeline_known", "source_metadata_changed"} {
		t.Run(fault, func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
			defer stop()
			admin := sessionNativeOpen(t, ctx)
			index := ownedIndex(t, admin, ctx)
			kind := "insert"
			if fault == "lost_update" || fault == "lost_delete" {
				kind = strings.TrimPrefix(fault, "lost_")
				if _, err := admin.CreateDocument(ctx, DocumentID{Index: index, ID: "original"}, []byte(`{"name":"before"}`)); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(fault, "pipeline_") {
				pipeline := index + "_pipeline"
				sessionNativeRun(t, admin, ctx, "PUT", "/_ingest/pipeline/"+pipeline, []byte(`{"processors":[{"set":{"field":"transformed","value":true}}]}`))
				t.Cleanup(func() {
					cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					_, _ = admin.Do(cleanup, Request{Method: "DELETE", Path: "/_ingest/pipeline/" + pipeline})
				})
				sessionNativeRun(t, admin, ctx, "PUT", "/"+index+"/_settings", []byte(`{"index.default_pipeline":"`+pipeline+`"}`))
			}
			var writes atomic.Int32
			var failSavedRead atomic.Bool
			var sourceMetadataFault atomic.Bool
			s := nativeEditRelay(t, ctx, admin, func(w http.ResponseWriter, r *http.Request, response *Response) bool {
				if isDocumentWrite(r) {
					writes.Add(1)
					if fault == "pipeline_known" || fault == "source_metadata_changed" {
						failSavedRead.Store(true)
						return false
					}
					if fault == "malformed" {
						response.Body = []byte(`{"result":"created"}`)
						return false
					}
					dropNativeReply(t, w)
					return true
				}
				if r.Method == "GET" && strings.Contains(r.URL.Path, "/_doc/") && failSavedRead.CompareAndSwap(true, false) {
					if fault == "source_metadata_changed" {
						_, e := admin.Do(ctx, Request{Method: "PUT", Path: "/" + index + "/_mapping", Body: []byte(`{"_source":{"excludes":["name"]}}`)})
						if !isNativeStatus(e, 400) {
							t.Error("native 2.5.1 source-rule update should be rejected", e)
						}
						// Native rules are immutable. Independently inject a changed
						// metadata receipt to exercise the retained-rule guard; this
						// is not evidence of a real online mapping change.
						sourceMetadataFault.Store(true)
					}
					w.WriteHeader(403)
					_, _ = w.Write([]byte(`{"error":"temporary fixture read denial"}`))
					return true
				}
				if r.Method == "GET" && r.URL.Path == "/"+index+"/_mapping" && sourceMetadataFault.Load() {
					var metadata map[string]map[string]any
					if decodeJSON(response.Body, &metadata) != nil {
						t.Error("native mapping receipt unavailable for fault injection")
						w.WriteHeader(502)
						return true
					}
					mapping, ok := metadata[index]["mappings"].(map[string]any)
					if !ok {
						t.Error("native mapping shape unavailable")
						w.WriteHeader(502)
						return true
					}
					mapping["_source"] = map[string]any{"excludes": []string{"name"}}
					response.Body, _ = json.Marshal(metadata)
				}
				return false
			})
			l := nativeDataLease(t, s, "index", index)
			edit := documentEdit(kind, nil, `{"name":"desired"}`)
			if kind != "insert" {
				edit.Original = nativeEditSnapshot(t, ctx, l, "original", "values")
			} else {
				edit.Values["_id"] = contract.DataValue{Text: "original"}
			}
			result, err := l.save(ctx, s.ProfileID(), l.object.Catalog, index, []contract.DataEdit{edit})
			if !errors.Is(err, contract.ErrDataCommitUncertain) || result == nil || !result.CommitUncertain || result.CommitConfirmed || l.pending == nil || writes.Load() != 1 {
				t.Fatal("unknown native write not retained safely", result, err)
			}
			if _, err = l.save(ctx, s.ProfileID(), l.object.Catalog, index, []contract.DataEdit{edit}); !errors.Is(err, contract.ErrDataCommitUncertain) || writes.Load() != 1 {
				t.Fatal("pending write was replayed", err)
			}
			wrong := cloneDocumentResult(result)
			wrong.Applied++
			if _, err = l.verify(ctx, s.ProfileID(), l.object.Catalog, index, wrong); !errors.Is(err, ErrInvalid) {
				t.Fatal("foreign verification envelope accepted", err)
			}
			v, err := l.verify(ctx, s.ProfileID(), l.object.Catalog, index, result)
			if err != nil || v == nil || writes.Load() != 1 {
				t.Fatal("verification wrote or failed original reads", err)
			}
			if fault == "pipeline_lost" || fault == "source_metadata_changed" {
				if v.Status != "indeterminate" || v.Recovered != nil || l.pending == nil {
					t.Fatal("unassociated pipeline output falsely confirmed")
				}
			} else {
				if v.Status != "applied" || v.Recovered == nil || !v.Recovered.Verified || !v.Recovered.CommitConfirmed || l.pending != nil {
					t.Fatal("original native write not recovered", v)
				}
				if fault == "pipeline_known" && !strings.Contains(v.Recovered.Outcomes[0].Previews[3].Text, "transformed") {
					t.Fatal("known receipt did not recover actual pipeline output")
				}
			}
			if err = l.Close(); err != nil {
				t.Fatal(err)
			}
			nativeNoPITs(t, ctx, admin, index)
		})
	}
}

func TestEasysearchNativeDataConflictPrefixAndFinalScopeGuard(t *testing.T) {
	for _, fault := range []string{"native_conflict", "partial_prefix", "alias_after_saved_read", "uuid_after_saved_read", "stream_wrong_backing", "stream_after_write"} {
		t.Run(fault, func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
			defer stop()
			admin := sessionNativeOpen(t, ctx)
			index := ownedIndex(t, admin, ctx)
			id := DocumentID{Index: index, ID: "original"}
			if _, err := admin.CreateDocument(ctx, id, []byte(`{"name":"original"}`)); err != nil {
				t.Fatal(err)
			}
			name, kind := index, "index"
			if fault == "alias_after_saved_read" {
				name, kind = "NyaDB_"+index, "alias"
				sessionNativeRun(t, admin, ctx, "POST", "/_aliases", []byte(`{"actions":[{"add":{"index":"`+index+`","alias":"`+name+`","is_write_index":true,"filter":{"term":{"name":"original"}}}}]}`))
			}
			if strings.HasPrefix(fault, "stream_") {
				name, kind = index+"_race_stream", "data_stream"
				template := index + "_race_template"
				sessionNativeRun(t, admin, ctx, "PUT", "/_index_template/"+template, []byte(`{"index_patterns":["`+name+`"],"data_stream":{},"template":{"settings":{"number_of_shards":1,"number_of_replicas":0}}}`))
				sessionNativeRun(t, admin, ctx, "PUT", "/_data_stream/"+name, nil)
				t.Cleanup(func() {
					cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					_, _ = admin.Do(cleanup, Request{Method: "DELETE", Path: "/_data_stream/" + name})
					_, _ = admin.Do(cleanup, Request{Method: "DELETE", Path: "/_index_template/" + template})
				})
			}
			var armed, changed atomic.Bool
			var writes atomic.Int32
			s := nativeEditRelay(t, ctx, admin, func(w http.ResponseWriter, r *http.Request, response *Response) bool {
				if isDocumentWrite(r) {
					writes.Add(1)
				}
				trigger := r.Method == "GET" && r.URL.Path == "/"+index+"/_doc/original" && armed.Load()
				if fault == "alias_after_saved_read" || fault == "uuid_after_saved_read" {
					trigger = trigger && writes.Load() > 0
				}
				if fault == "stream_wrong_backing" {
					// Rollover after capturing the preflight GET 404. The next
					// full guard must revoke the frozen stream before submission.
					trigger = r.Method == "GET" && strings.Contains(r.URL.Path, "/_doc/new") && armed.Load()
				}
				if fault == "stream_after_write" {
					trigger = isDocumentWrite(r) && armed.Load()
				}
				if trigger && changed.CompareAndSwap(false, true) {
					var e error
					switch fault {
					case "native_conflict":
						current, readErr := admin.GetDocument(ctx, id)
						e = readErr
						if e == nil {
							_, e = admin.ReplaceDocument(ctx, current, []byte(`{"name":"external"}`))
						}
					case "alias_after_saved_read":
						_, e = admin.Do(ctx, Request{Method: "POST", Path: "/_aliases", Body: []byte(`{"actions":[{"add":{"index":"` + index + `","alias":"` + name + `","is_write_index":true,"filter":{"term":{"name":"changed"}}}}]}`)})
					case "uuid_after_saved_read":
						_, e = admin.Do(ctx, Request{Method: "DELETE", Path: "/" + index})
						if e == nil {
							_, e = admin.Do(ctx, Request{Method: "PUT", Path: "/" + index, Body: []byte(`{"settings":{"number_of_replicas":0}}`)})
						}
					case "stream_wrong_backing", "stream_after_write":
						_, e = admin.Do(ctx, Request{Method: "POST", Path: "/" + name + "/_rollover", Body: []byte(`{}`)})
					}
					if e != nil {
						t.Error("native concurrent fixture change", e)
						w.WriteHeader(502)
						return true
					}
				}
				return false
			})
			l := nativeDataLease(t, s, kind, name)
			edits := []contract.DataEdit{}
			if strings.HasPrefix(fault, "stream_") {
				e := documentEdit("insert", nil, `{"@timestamp":"2026-11-08T00:00:00Z","name":"original"}`)
				e.Values["_id"] = contract.DataValue{Text: "new"}
				edits = append(edits, e)
			} else {
				original := nativeEditSnapshot(t, ctx, l, "original", "fingerprints")
				if fault == "partial_prefix" {
					e := documentEdit("insert", nil, `{"name":"first"}`)
					e.Values["_id"] = contract.DataValue{Text: "new"}
					edits = append(edits, e)
					current, err := admin.GetDocument(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = admin.ReplaceDocument(ctx, current, []byte(`{"name":"external"}`)); err != nil {
						t.Fatal(err)
					}
				}
				edits = append(edits, documentEdit("update", original, `{"name":"original","changed":true}`))
			}
			armed.Store(true)
			result, err := l.save(ctx, s.ProfileID(), l.object.Catalog, name, edits)
			switch fault {
			case "native_conflict":
				if !errors.Is(err, contract.ErrDataConflict) || writes.Load() != 1 || result.Applied != 0 || result.CommitUncertain || l.pending != nil {
					t.Fatal("native 409 replayed or confused with an unknown write", result, err)
				}
			case "partial_prefix":
				if !errors.Is(err, contract.ErrDataConflict) || result.Applied != 1 || !result.Incomplete || result.CommitConfirmed || l.pending == nil || writes.Load() != 1 {
					t.Fatal("successful prefix discarded or whole batch confirmed", result, err)
				}
				v, e := l.verify(ctx, s.ProfileID(), l.object.Catalog, name, result)
				if e != nil || v.Status != "indeterminate" || len(v.States) != 2 || v.States[0] != "applied" || v.States[1] != "not_processed" || l.pending == nil || writes.Load() != 1 {
					t.Fatal("prefix verification replayed suffix or cleared drafts", v, e)
				}
			case "alias_after_saved_read", "uuid_after_saved_read", "stream_after_write":
				if !errors.Is(err, contract.ErrDataCommitUncertain) || !result.CommitUncertain || writes.Load() != 1 || !changed.Load() {
					t.Fatal("final source read bypassed alias identity guard", result, err)
				}
				v, e := l.verify(ctx, s.ProfileID(), l.object.Catalog, name, result)
				if e != nil || v.Status != "indeterminate" || v.Recovered != nil || writes.Load() != 1 {
					t.Fatal("verification rebound to changed alias", v, e)
				}
			case "stream_wrong_backing":
				if !errors.Is(err, ErrTargetChanged) || writes.Load() != 0 || !changed.Load() || l.pending != nil {
					t.Fatal("stream rollover during preflight was followed", result, err)
				}
			}
		})
	}
}

func TestEasysearchNativeDataWriteReadOnlyTargetsAndRoutingIsolation(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 120*time.Second)
	defer stop()
	admin := sessionNativeOpen(t, ctx)
	index := ownedIndex(t, admin, ctx)
	s := nativeDocumentSession(t, ctx)
	for _, setup := range []struct{ name, config string }{
		{index + "_disabled", `{"mappings":{"_source":{"enabled":false}}}`},
		{index + "_filtered", `{"mappings":{"_source":{"excludes":["secret"]}}}`},
		{index + "_hidden", `{"settings":{"index.hidden":true}}`},
	} {
		name := setup.name
		sessionNativeRun(t, admin, ctx, "PUT", "/"+name, []byte(setup.config))
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = admin.Do(cleanup, Request{Method: "DELETE", Path: "/" + name})
		})
		l := nativeDataLease(t, s, "index", name)
		if _, err := l.save(ctx, s.ProfileID(), l.object.Catalog, name, []contract.DataEdit{documentEdit("insert", nil, `{"name":"rejected"}`)}); !errors.Is(err, contract.ErrDataReadOnly) {
			t.Fatal("unsupported source/visibility allowed replacement", name, err)
		}
	}
	// Search routing cannot silently become the alias's write routing.
	alias := "NyaDB_" + index + "_search_only"
	sessionNativeRun(t, admin, ctx, "POST", "/_aliases", []byte(`{"actions":[{"add":{"index":"`+index+`","alias":"`+alias+`","is_write_index":true,"search_routing":"search-only"}}]}`))
	al := nativeDataLease(t, s, "alias", alias)
	result := nativeSave(t, ctx, al, documentEdit("insert", nil, `{"name":"unrouted"}`))
	if result.Outcomes[0].Previews[2].Text != "" {
		t.Fatal("search routing was substituted for write routing")
	}
	routes := map[int]string{}
	for i := 0; len(routes) < 2 && i < 40; i++ {
		route := string(rune('a' + i))
		r := sessionNativeRun(t, admin, ctx, "GET", "/"+index+"/_search_shards?routing="+route, nil)
		var shards struct {
			Shards [][]struct{ Shard int } `json:"shards"`
		}
		if decodeJSON(r.Body, &shards) != nil || len(shards.Shards) != 1 || len(shards.Shards[0]) == 0 {
			t.Fatal("routing shard metadata unavailable")
		}
		routes[shards.Shards[0][0].Shard] = route
	}
	if len(routes) != 2 {
		t.Fatal("fixture did not find two routing shards")
	}
	l := nativeDataLease(t, s, "index", index)
	for _, route := range routes {
		e := documentEdit("insert", nil, `{"name":"original"}`)
		e.Values["_id"], e.Values["_routing"] = contract.DataValue{Text: "same-id"}, contract.DataValue{Text: route}
		nativeSave(t, ctx, l, e)
	}
	p, err := l.page(ctx, s.ProfileID(), l.object.Catalog, index, contract.DataOptions{Object: l.object, Limit: 10, FilterColumn: "_id", FilterMode: "equal", FilterValue: "same-id"})
	if err != nil || len(p.RowSnapshots) != 2 {
		t.Fatal("routed write identities collapsed", err)
	}
	a, b := p.RowSnapshots[0], p.RowSnapshots[1]
	identity, err := l.ref(l.object.Catalog, b.Ref)
	if err != nil {
		t.Fatal(err)
	}
	nativeSave(t, ctx, l, documentEdit("update", a, `{"name":"only-one"}`))
	doc, err := admin.GetDocument(ctx, identity.ID)
	if err != nil || string(doc.Source) != `{"name":"original"}` {
		t.Fatal("updating one routing identity changed the other", err)
	}
	nativeSave(t, ctx, l, documentEdit("delete", b, ""))
}

func TestEasysearchNativeDataSaveCancellationKeepsUnknownAndOtherLease(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	admin := sessionNativeOpen(t, ctx)
	index := ownedIndex(t, admin, ctx)
	entered, settled := make(chan struct{}), make(chan struct{})
	var writes atomic.Int32
	s := nativeEditRelay(t, ctx, admin, func(w http.ResponseWriter, r *http.Request, _ *Response) bool {
		if isDocumentWrite(r) {
			writes.Add(1)
			close(entered)
			<-r.Context().Done()
			close(settled)
			return true
		}
		return false
	})
	l := nativeDataLease(t, s, "index", index)
	other := nativeDataLease(t, s, "index", index)
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type completed struct {
		result *contract.DataEditResult
		err    error
	}
	done := make(chan completed, 1)
	go func() {
		r, e := l.save(requestCtx, s.ProfileID(), l.object.Catalog, index, []contract.DataEdit{documentEdit("insert", nil, `{"name":"committed"}`)})
		done <- completed{r, e}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("save did not reach original server")
	}
	if n, err := other.count(ctx, s.ProfileID(), other.object.Catalog, index); err != nil || n != 1 {
		t.Fatal("parked write blocked another lease", n, err)
	}
	cancel()
	select {
	case r := <-done:
		if !errors.Is(r.err, contract.ErrDataCommitUncertain) || r.result == nil || !r.result.CommitUncertain {
			t.Fatal("canceled write discarded unknown result", r.err)
		}
		select {
		case <-settled:
		case <-ctx.Done():
			t.Fatal("original relay did not settle cancellation")
		}
		v, err := l.verify(ctx, s.ProfileID(), l.object.Catalog, index, r.result)
		if err != nil || v.Status != "applied" || writes.Load() != 1 {
			t.Fatal("canceled write verification replayed", v, err)
		}
	case <-ctx.Done():
		t.Fatal("original save executor did not settle")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := other.count(ctx, s.ProfileID(), other.object.Catalog, index); err != nil || n != 1 {
		t.Fatal("closing writer revoked another document", err)
	}
}
