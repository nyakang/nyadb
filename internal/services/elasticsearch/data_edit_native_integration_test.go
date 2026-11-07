package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeDistinctRouting(t *testing.T, ctx context.Context, c *Client, index string) (string, string) {
	t.Helper()
	first, shard := "", -1
	for i := 0; i < 32; i++ {
		route := "tenant-" + strconv.Itoa(i)
		r := nativeRun(t, c, ctx, "GET", "/"+index+"/_search_shards?routing="+url.QueryEscape(route), nil)
		var root struct {
			Shards [][]struct {
				Shard int `json:"shard"`
			} `json:"shards"`
		}
		if decodeJSON(r.Body, &root) != nil || len(root.Shards) != 1 || len(root.Shards[0]) < 1 {
			t.Fatal("routing shard unavailable")
		}
		n := root.Shards[0][0].Shard
		if first == "" {
			first, shard = route, n
		} else if n != shard {
			return first, route
		}
	}
	t.Fatal("fixture routing did not address distinct shards")
	return "", ""
}
func editDocument(source string) map[string]contract.DataValue {
	return map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: source}}
}
func TestElasticsearchNativeDataDocumentCRUDRoutingConflictTypedValuesAndPrefix(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 120*time.Second)
	defer stop()
	admin := nativeOpen(t, ctx)
	index := nativeIndex(t, admin, ctx)
	routeA, routeB := nativeDistinctRouting(t, ctx, admin, index)
	s := nativeDocumentSession(t, ctx)
	l := nativeDataLease(t, s, "index", index)
	r := l.Runtime()
	db := s.identity.ClusterUUID
	first := editDocument(`{"n":9007199254740993,"precise":12345678901234567890.123456789,"nested":{"text":"中文🐱","ok":true}}`)
	first["_id"] = contract.DataValue{Text: "same"}
	first["_routing"] = contract.DataValue{Text: routeA}
	second := editDocument(`{"n":2}`)
	second["_id"] = contract.DataValue{Text: "same"}
	second["_routing"] = contract.DataValue{Text: routeB}
	created, err := r.Saver(ctx, s.ProfileID(), db, index, []contract.DataEdit{{Kind: "insert", Values: first}, {Kind: "insert", Values: second}})
	if err != nil || !created.CommitConfirmed || created.Applied != 2 || len(created.Outcomes) != 2 {
		t.Fatal("native grid create failed", err)
	}
	for _, out := range created.Outcomes {
		if out.Saved == nil || out.Saved.Ref.Object != l.object || out.Saved.Mode != "fingerprints" || len(out.Previews) != 4 || out.GeneratedID != 0 {
			t.Fatal("native generated identity was lost or converted to SQL integer")
		}
	}
	idA := DocumentID{Index: index, ID: "same", Routing: routeA}
	idB := DocumentID{Index: index, ID: "same", Routing: routeB}
	a, err := admin.GetDocument(ctx, idA)
	if err != nil || !strings.Contains(string(a.Source), "12345678901234567890.123456789") {
		t.Fatal("exact create source lost", err)
	}
	b, err := admin.GetDocument(ctx, idB)
	if err != nil || string(b.Source) != `{"n":2}` {
		t.Fatal("equal ID on another shard was overwritten", err)
	}
	oldPage, err := l.page(ctx, s.ProfileID(), db, index, contract.DataOptions{Object: l.object, Limit: 1})
	if err != nil || oldPage.NextCursor == nil {
		t.Fatal("original page boundary missing", err)
	}
	updated, err := r.Saver(ctx, s.ProfileID(), db, index, []contract.DataEdit{{Kind: "update", Original: created.Outcomes[0].Saved, Values: editDocument(`{"n":9007199254740995,"nested":{"text":"更新🐱"}}`)}})
	if err != nil || !updated.CommitConfirmed || updated.Outcomes[0].Saved == nil {
		t.Fatal("native OCC replacement failed", err)
	}
	if !updated.RefreshRequired {
		t.Fatal("confirmed write did not revoke the old read snapshot")
	}
	if _, err = l.page(ctx, s.ProfileID(), db, index, contract.DataOptions{Object: l.object, Limit: 1, Offset: 1, After: oldPage.NextCursor}); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("post-write page silently reused old PIT", err)
	}
	if _, err = l.page(ctx, s.ProfileID(), db, index, contract.DataOptions{Object: l.object, Limit: 1}); err != nil {
		t.Fatal("post-write new read view failed", err)
	}
	after, err := admin.GetDocument(ctx, idA)
	if err != nil || after.Sequence <= a.Sequence || string(after.Source) != `{"n":9007199254740995,"nested":{"text":"更新🐱"}}` {
		t.Fatal("native replacement changed precise source or version", err)
	}
	conflict, err := r.Saver(ctx, s.ProfileID(), db, index, []contract.DataEdit{{Kind: "update", Original: created.Outcomes[0].Saved, Values: editDocument(`{"n":0}`)}})
	if !errors.Is(err, contract.ErrDataConflict) || conflict.Applied != 0 || conflict.CommitUncertain || conflict.Incomplete || l.pending != nil {
		t.Fatal("stale original was retried or reported uncertain", err)
	}
	// Preserve authoritative typed original values as an alternative signed payload.
	valuesOriginal := cloneDocumentSnapshot(updated.Outcomes[0].Saved)
	valuesOriginal.Mode = "values"
	valuesOriginal.Fingerprints = nil
	valuesOriginal.Original = documentCells(after)
	l.sealSnapshot(valuesOriginal, rowVersion{Sequence: after.Sequence, PrimaryTerm: after.PrimaryTerm})
	typed, err := r.Saver(ctx, s.ProfileID(), db, index, []contract.DataEdit{{Kind: "update", Original: valuesOriginal, Values: editDocument(`{"n":9007199254740997}`)}})
	if err != nil || !typed.CommitConfirmed || typed.Outcomes[0].Original.Mode != "values" || len(typed.Outcomes[0].Original.Original) != 4 {
		t.Fatal("typed original payload was discarded", err)
	}
	tampered := cloneDocumentSnapshot(typed.Outcomes[0].Saved)
	*tampered.Fingerprints[3] = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err = r.Saver(ctx, s.ProfileID(), db, index, []contract.DataEdit{{Kind: "delete", Original: tampered}}); err == nil {
		t.Fatal("tampered original authorized a write")
	}
	routingChange := editDocument(`{"n":0}`)
	routingChange["_routing"] = contract.DataValue{Text: routeB}
	if _, err = r.Saver(ctx, s.ProfileID(), db, index, []contract.DataEdit{{Kind: "update", Original: typed.Outcomes[0].Saved, Values: routingChange}}); err == nil {
		t.Fatal("routing identity was changed")
	}
	deleted, err := r.Saver(ctx, s.ProfileID(), db, index, []contract.DataEdit{{Kind: "delete", Original: typed.Outcomes[0].Saved}})
	if err != nil || !deleted.CommitConfirmed || deleted.Outcomes[0].Saved != nil {
		t.Fatal("native delete failed", err)
	}
	if _, err = admin.GetDocument(ctx, idA); !isNativeStatus(err, 404) {
		t.Fatal("original routed row remains", err)
	}
	if _, err = admin.GetDocument(ctx, idB); err != nil {
		t.Fatal("delete touched equal ID on another shard", err)
	}
	if _, err = admin.ReplaceDocument(ctx, b, json.RawMessage(`{"n":3}`)); err != nil {
		t.Fatal(err)
	}
	prefix := editDocument(`{"n":4}`)
	prefix["_id"] = contract.DataValue{Text: "prefix"}
	batch := []contract.DataEdit{{Kind: "insert", Values: prefix}, {Kind: "delete", Original: created.Outcomes[1].Saved}}
	incomplete, err := r.Saver(ctx, s.ProfileID(), db, index, batch)
	if !errors.Is(err, contract.ErrDataConflict) || incomplete.Applied != 1 || !incomplete.Incomplete || incomplete.CommitUncertain || len(incomplete.Outcomes) != 1 {
		t.Fatal("confirmed prefix was reported atomic or lost", err)
	}
	if _, err = r.Saver(ctx, s.ProfileID(), db, index, batch); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("incomplete batch replayed", err)
	}
	verification, err := r.Verify(ctx, s.ProfileID(), db, index, incomplete)
	if err != nil || verification.Status != "indeterminate" || len(verification.States) != 2 || verification.States[0] != "applied" || verification.States[1] != "not_processed" || l.pending == nil {
		t.Fatal("prefix verification certified the whole batch", err)
	}
	if _, err = admin.GetDocument(ctx, DocumentID{Index: index, ID: "prefix"}); err != nil {
		t.Fatal("confirmed prefix vanished", err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	nativeNoPITs(t, ctx, admin, index)
}

func TestElasticsearchNativeDataDocumentAliasAndStreamWritesPreserveTargets(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 120*time.Second)
	defer stop()
	admin := nativeOpen(t, ctx)
	index := nativeIndex(t, admin, ctx)
	alias := index + "_write"
	nativeRun(t, admin, ctx, "POST", "/_aliases", []byte(`{"actions":[{"add":{"index":"`+index+`","alias":"`+alias+`","is_write_index":true,"index_routing":"tenant-a","search_routing":"tenant-a","filter":{"term":{"n":1}}}}]}`))
	s := nativeDocumentSession(t, ctx)
	l := nativeDataLease(t, s, "alias", alias)
	db := s.identity.ClusterName
	created, err := l.save(ctx, s.ProfileID(), db, alias, []contract.DataEdit{{Kind: "insert", Values: editDocument(`{"n":1}`)}})
	if err != nil || !created.CommitConfirmed || created.Outcomes[0].Saved.Ref.Values[0] == nil {
		t.Fatal("alias native create failed", err)
	}
	ref := created.Outcomes[0].Saved.Ref
	if string(ref.Values[0]) != index || string(ref.Values[2]) != "tenant-a" || ref.Object != l.object {
		t.Fatal("alias write lost concrete index or write routing")
	}
	updated, err := l.save(ctx, s.ProfileID(), db, alias, []contract.DataEdit{{Kind: "update", Original: created.Outcomes[0].Saved, Values: editDocument(`{"n":1,"extra":"updated"}`)}})
	if err != nil || !updated.CommitConfirmed {
		t.Fatal("alias concrete OCC update failed", err)
	}
	if _, err = l.save(ctx, s.ProfileID(), db, alias, []contract.DataEdit{{Kind: "delete", Original: updated.Outcomes[0].Saved}}); err != nil {
		t.Fatal("alias concrete OCC delete failed", err)
	}
	badRoute := editDocument(`{"n":1}`)
	badRoute["_routing"] = contract.DataValue{Text: "other"}
	if _, err = l.save(ctx, s.ProfileID(), db, alias, []contract.DataEdit{{Kind: "insert", Values: badRoute}}); err == nil {
		t.Fatal("alias write routing was bypassed")
	}
	stream, template := index+"_stream_edit", index+"_stream_template"
	nativeRun(t, admin, ctx, "PUT", "/_index_template/"+template, []byte(`{"index_patterns":["`+stream+`"],"data_stream":{},"template":{"settings":{"number_of_shards":1,"number_of_replicas":0}}}`))
	nativeRun(t, admin, ctx, "PUT", "/_data_stream/"+stream, nil)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Do(cleanup, Request{Method: "DELETE", Path: "/_data_stream/" + stream})
		_, _ = admin.Do(cleanup, Request{Method: "DELETE", Path: "/_index_template/" + template})
	})
	ls := nativeDataLease(t, s, "data_stream", stream)
	document := editDocument(`{"@timestamp":"2026-11-08T00:00:00Z","exact":9007199254740993}`)
	inserted, err := ls.save(ctx, s.ProfileID(), db, stream, []contract.DataEdit{{Kind: "insert", Values: document}})
	if err != nil || !inserted.CommitConfirmed || !strings.HasPrefix(string(inserted.Outcomes[0].Saved.Ref.Values[0]), ".ds-"+stream) {
		t.Fatal("stream write did not bind original backing index", err)
	}
	streamUpdated, err := ls.save(ctx, s.ProfileID(), db, stream, []contract.DataEdit{{Kind: "update", Original: inserted.Outcomes[0].Saved, Values: editDocument(`{"@timestamp":"2026-11-08T00:00:00Z","exact":9007199254740995}`)}})
	if err != nil || !streamUpdated.CommitConfirmed {
		t.Fatal("stream backing OCC update failed", err)
	}
	if _, err = ls.save(ctx, s.ProfileID(), db, stream, []contract.DataEdit{{Kind: "delete", Original: streamUpdated.Outcomes[0].Saved}}); err != nil {
		t.Fatal("stream backing OCC delete failed", err)
	}
	document["_routing"] = contract.DataValue{Text: "unsupported"}
	if _, err = ls.save(ctx, s.ProfileID(), db, stream, []contract.DataEdit{{Kind: "insert", Values: document}}); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("stream silently accepted unsupported custom routing", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	nativeNoPITs(t, ctx, admin, index)
	nativeNoPITs(t, ctx, admin, stream)
}

func TestElasticsearchNativeDataDocumentDisabledRefreshRetainsUncertainWriteAndVerifiesRealtime(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	admin := nativeOpen(t, ctx)
	index := nativeIndex(t, admin, ctx)
	nativeRun(t, admin, ctx, "PUT", "/"+index+"/_settings", []byte(`{"index.refresh_interval":"-1"}`))
	s := nativeDocumentSession(t, ctx)
	l := nativeDataLease(t, s, "index", index)
	db := s.identity.ClusterUUID
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	result, err := l.save(writeCtx, s.ProfileID(), db, index, []contract.DataEdit{{Kind: "insert", Values: editDocument(`{"n":9007199254740993}`)}})
	cancel()
	if !errors.Is(err, contract.ErrDataCommitUncertain) || result == nil || !result.CommitUncertain || result.Incomplete || len(result.Outcomes) != 1 {
		t.Fatal("refresh cancellation was certified as rollback", err)
	}
	identity, err := l.ref(db, result.Outcomes[0].Saved.Ref)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := admin.GetDocument(ctx, identity.ID)
	if err != nil || string(actual.Source) != `{"n":9007199254740993}` {
		t.Fatal("native write had not actually committed before refresh wait", err)
	}
	verification, err := l.verify(ctx, s.ProfileID(), db, index, result)
	if err != nil || verification.Status != "applied" || verification.Recovered == nil || !verification.Recovered.Verified || !verification.Recovered.CommitConfirmed || verification.Recovered.Outcomes[0].Saved.Mode != "fingerprints" {
		t.Fatal("independent realtime verification failed", err)
	}
	nativeRun(t, admin, ctx, "POST", "/"+index+"/_refresh", nil)
}

func TestElasticsearchNativeDataDocumentReadsActualPipelineSourceAndRejectsHiddenWrites(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	admin := nativeOpen(t, ctx)
	index := nativeIndex(t, admin, ctx)
	pipeline := index + "_pipeline"
	nativeRun(t, admin, ctx, "PUT", "/_ingest/pipeline/"+pipeline, []byte(`{"processors":[{"set":{"field":"normalized","value":"actual server source"}}]}`))
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Do(cleanup, Request{Method: "DELETE", Path: "/_ingest/pipeline/" + pipeline})
	})
	nativeRun(t, admin, ctx, "PUT", "/"+index+"/_settings", []byte(`{"index.default_pipeline":"`+pipeline+`"}`))
	s := nativeDocumentSession(t, ctx)
	l := nativeDataLease(t, s, "index", index)
	db := s.identity.ClusterUUID
	result, err := l.save(ctx, s.ProfileID(), db, index, []contract.DataEdit{{Kind: "insert", Values: editDocument(`{"n":9007199254740993}`)}})
	if err != nil || !result.CommitConfirmed || result.Outcomes[0].Saved == nil || !strings.Contains(result.Outcomes[0].Previews[3].Text, "actual server source") {
		t.Fatal("saved grid snapshot invented untransformed source", err)
	}
	identity, err := l.ref(db, result.Outcomes[0].Saved.Ref)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := admin.GetDocument(ctx, identity.ID)
	if err != nil || !matchesDocumentSnapshot(actual, result.Outcomes[0].Saved) || !strings.Contains(string(actual.Source), "9007199254740993") {
		t.Fatal("saved source fingerprint did not describe actual pipeline result", err)
	}
	hidden := "." + index + "_hidden"
	nativeRun(t, admin, ctx, "PUT", "/"+hidden, []byte(`{"settings":{"index.hidden":true,"number_of_replicas":0}}`))
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Do(cleanup, Request{Method: "DELETE", Path: "/" + hidden})
	})
	h := nativeDataLease(t, s, "index", hidden)
	page, err := h.page(ctx, s.ProfileID(), db, hidden, contract.DataOptions{Object: h.object, Limit: 1})
	if err != nil || page.Editable == nil || *page.Editable {
		t.Fatal("hidden target became editable", err)
	}
	if _, err = h.save(ctx, s.ProfileID(), db, hidden, []contract.DataEdit{{Kind: "insert", Values: editDocument(`{"n":1}`)}}); !errors.Is(err, contract.ErrDataReadOnly) {
		t.Fatal("hidden/system target accepted grid writes", err)
	}
	if count, err := h.count(ctx, s.ProfileID(), db, hidden); err != nil || count != 0 {
		t.Fatal("rejected hidden write had effects", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	nativeNoPITs(t, ctx, admin, hidden)
}

func TestElasticsearchNativeDataDocumentEncodedIDRoundtripDoesNotChangeQualifiedIdentity(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	admin := nativeOpen(t, ctx)
	index := nativeIndex(t, admin, ctx)
	s := nativeDocumentSession(t, ctx)
	l := nativeDataLease(t, s, "index", index)
	db := s.identity.ClusterUUID
	id := "路径/../精确?猫#%2F"
	values := editDocument(`{"n":9007199254740993}`)
	values["_id"] = contract.DataValue{Text: id}
	values["_routing"] = contract.DataValue{Text: "租户/猫?"}
	result, err := l.save(ctx, s.ProfileID(), db, index, []contract.DataEdit{{Kind: "insert", Values: values}})
	if err != nil || !result.CommitConfirmed || string(result.Outcomes[0].Saved.Ref.Values[1]) != id {
		t.Fatal("encoded native ID did not roundtrip", err)
	}
	actual, err := admin.GetDocument(ctx, DocumentID{Index: index, ID: id, Routing: "租户/猫?"})
	if err != nil || actual.Identity.ID != id || string(actual.Source) != `{"n":9007199254740993}` {
		t.Fatal("typed read treated encoded ID as another path", err)
	}
	if _, err = l.save(ctx, s.ProfileID(), db, index, []contract.DataEdit{{Kind: "update", Original: result.Outcomes[0].Saved, Values: editDocument(`{"n":9007199254740995}`)}}); err != nil {
		t.Fatal("encoded ID update failed", err)
	}
}
