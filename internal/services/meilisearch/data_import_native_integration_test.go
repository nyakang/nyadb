package meilisearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func nativeImportSave(l contract.DataDocumentLease) func(context.Context, int64, string, string, []contract.DataEdit) (*contract.DataEditResult, error) {
	return func(ctx context.Context, id int64, db, name string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
		r, e := l.Runtime().Importer(ctx, id, db, name, contract.DataImportRequest{Mode: "document_replace", Edits: edits})
		if r == nil {
			return nil, e
		}
		return r.Saved, e
	}
}
func TestMeilisearchNativeDocumentImportLargeReplacementMergeAndExactJSON(t *testing.T) {
	s, admin := nativeData(t)
	uid := nativeIndex(t, admin)
	l := nativeWriteLease(t, s, uid)
	ctx := context.Background()
	nativeWait(t, admin, nativeSubmit(t, admin, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"row_0","old":true},{"id":"untouched","keep":true}]`)}))
	var edits []contract.DataEdit
	for i := 0; i < 1001; i++ {
		edits = append(edits, documentImportEdits(fmt.Sprintf(`{"id":"row_%d","n":9007199254740993,"nested":{"array":[null,true,7],"猫":"🐱"}}`, i))...)
	}
	r, e := l.Runtime().Importer(ctx, s.ProfileID(), uid, "documents", contract.DataImportRequest{Mode: "document_replace", Edits: edits})
	if e != nil || r.Committed || r.Saved == nil || r.Saved.Async == nil || r.Saved.Applied != 0 {
		t.Fatal("admission", e)
	}
	if _, e = l.Runtime().Importer(ctx, s.ProfileID(), uid, "documents", contract.DataImportRequest{Mode: "document_replace", Edits: edits}); !errors.Is(e, contract.ErrDataCommitUncertain) {
		t.Fatal("batch replayed", e)
	}
	v := nativeObserveDataWrite(t, s, uid, l, r.Saved)
	if v.Status != "applied" || v.Recovered.Applied != 1001 || !v.Recovered.Verified || len(v.Recovered.Outcomes) != 0 {
		t.Fatal("large complete readback unconfirmed", v.Status)
	}
	raw, _, e := admin.ReadDocument(ctx, uid, "row_0")
	if e != nil || strings.Contains(string(raw), `"old"`) || !strings.Contains(string(raw), "9007199254740993") {
		t.Fatal("replacement", e)
	}
	if _, _, e = admin.ReadDocument(ctx, uid, "untouched"); e != nil {
		t.Fatal("import cleared index", e)
	}
	merge := documentImportEdits(`{"id":"row_0","nested":{"changed":true},"nullable":null}`, `{"id":"new_merge","n":9007199254740993}`)
	r, e = l.Runtime().Importer(ctx, s.ProfileID(), uid, "documents", contract.DataImportRequest{Mode: "document_merge", Edits: merge})
	if e != nil {
		t.Fatal(e)
	}
	v = nativeObserveDataWrite(t, s, uid, l, r.Saved)
	if v.Status != "applied" || v.Recovered.Applied != 2 {
		t.Fatal("merge readback", v.Status)
	}
	raw, _, e = admin.ReadDocument(ctx, uid, "row_0")
	if e != nil || !strings.Contains(string(raw), `"n":9007199254740993`) || strings.Contains(string(raw), `"array"`) || !strings.Contains(string(raw), `"nullable":null`) {
		t.Fatal("top-level merge semantics", e, string(raw))
	}
	for _, mode := range []string{"append", "replace", "upsert"} {
		if _, e = l.Runtime().Importer(ctx, s.ProfileID(), uid, "documents", contract.DataImportRequest{Mode: mode, Edits: merge}); !errors.Is(e, contract.ErrUnsupported) {
			t.Fatal("table mode accepted", e)
		}
	}
	if _, e = l.Runtime().Importer(ctx, s.ProfileID(), uid, "documents", contract.DataImportRequest{Mode: "document_merge", Edits: documentImportEdits(`{"id":"row_0"}`, `{"id":"row_0"}`)}); e == nil {
		t.Fatal("duplicate accepted")
	}
	t.Log("one original native task for 1001 heterogeneous exact JSON documents; full readback; replacement, shallow merge, new IDs and untouched-index retention verified")
}
func TestMeilisearchNativeDocumentImportTaskFailureRetainsOriginalReceipt(t *testing.T) {
	s, admin := nativeData(t)
	uid := nativeIndex(t, admin)
	nativeWait(t, admin, nativeSubmit(t, admin, Request{Method: "PATCH", Path: "/indexes/" + uid + "/settings", Body: []byte(`{"filterableAttributes":["_geo"]}`)}))
	l := nativeWriteLease(t, s, uid)
	edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: `{"id":"bad_geo","_geo":{"lat":"invalid","lng":0}}`}}}}
	result, err := nativeImportSave(l)(context.Background(), s.ProfileID(), uid, "documents", edits)
	if err != nil || result.Async == nil {
		t.Fatal("failure was not natively admitted", err)
	}
	v := nativeObserveDataWrite(t, s, uid, l, result)
	if v.Status != "indeterminate" || v.Recovered.Async.Status != "failed" || !strings.Contains(v.Recovered.Async.TaskJSON, "invalid_document_geo_field") {
		t.Fatalf("native terminal task evidence absent: %s %s %s", v.Status, v.Recovered.Async.Status, v.Recovered.Async.TaskJSON)
	}
	if _, err = nativeImportSave(l)(context.Background(), s.ProfileID(), uid, "documents", edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("failed task replayed", err)
	}
	if count, err := l.Runtime().Count(context.Background(), s.ProfileID(), uid, "documents"); err != nil || count != 0 {
		t.Fatal("task failure reported row as saved", count, err)
	}
}

func TestMeilisearchNativeDocumentImportLostCancelCloseNoReplay(t *testing.T) {
	for _, mode := range []string{"lost", "cancel", "close", "session_close"} {
		t.Run(mode, func(t *testing.T) {
			_, admin := nativeData(t)
			uid := nativeIndex(t, admin)
			entered, acked := make(chan struct{}), make(chan struct{})
			var writes atomic.Int32
			relay := nativeRelay(t, admin, func(w http.ResponseWriter, r *http.Request, response Response) bool {
				if r.Method != "POST" || strings.TrimPrefix(r.URL.Path, "/proxy/meili") != "/indexes/"+uid+"/documents" {
					return false
				}
				writes.Add(1)
				close(entered)
				if mode == "lost" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
					} else {
						_ = conn.Close()
					}
				} else {
					<-r.Context().Done()
				}
				close(acked)
				return true
			})
			_, secret := nativeFixture(t)
			s := testSession(t, relay.config, secret)
			l := nativeWriteLease(t, s, uid)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: `{"id":"admitted","n":9007199254740993}`}}}}
			type saved struct {
				result *contract.DataEditResult
				err    error
			}
			out := make(chan saved, 1)
			go func() { r, e := nativeImportSave(l)(ctx, s.ProfileID(), uid, "documents", edits); out <- saved{r, e} }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("native write not entered")
			}
			switch mode {
			case "cancel":
				cancel()
			case "close":
				_ = l.Close()
			case "session_close":
				_ = s.Close()
			}
			select {
			case got := <-out:
				if got.err == nil || got.result == nil || !got.result.CommitUncertain || got.result.Async != nil {
					t.Fatal("lost admission fabricated receipt or success", got.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("executor not acknowledged")
			}
			select {
			case <-acked:
			case <-time.After(5 * time.Second):
				t.Fatal("relay not acknowledged")
			}
			if mode == "lost" || mode == "cancel" {
				if _, err := nativeImportSave(l)(context.Background(), s.ProfileID(), uid, "documents", edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
					t.Fatal("unknown admission replayed", err)
				}
			}
			if writes.Load() != 1 {
				t.Fatal("write replayed")
			}
		})
	}
}

func TestMeilisearchNativeDocumentImportScopedPermissionsAndObservation(t *testing.T) {
	_, admin := nativeData(t)
	cfg, _ := nativeFixture(t)
	if cfg.Auth != "bearer" {
		t.Fatal("authenticated fixture required")
	}
	uid := nativeIndex(t, admin)
	makeKey := func(actions []string) string {
		body, _ := json.Marshal(map[string]any{"actions": actions, "indexes": []string{uid}, "expiresAt": nil})
		r := nativeRun(t, admin, Request{Method: "POST", Path: "/keys", Body: body})
		var key struct{ UID, Key string }
		if json.Unmarshal(r.Body, &key) != nil || key.Key == "" {
			t.Fatal("scoped key missing")
		}
		t.Cleanup(func() { nativeRun(t, admin, Request{Method: "DELETE", Path: "/keys/" + key.UID}) })
		return key.Key
	}
	cfg.IndexUID = uid
	s := testSession(t, cfg, makeKey([]string{"indexes.get", "documents.get"}))
	l := nativeWriteLease(t, s, uid)
	edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: `{"id":"scoped"}`}}}}
	r, err := nativeImportSave(l)(context.Background(), s.ProfileID(), uid, "documents", edits)
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != 403 || r != nil {
		t.Fatal("denied write inferred admission", err)
	}
	if count, err := l.Runtime().Count(context.Background(), s.ProfileID(), uid, "documents"); err != nil || count != 0 {
		t.Fatal("denied write killed allowed reads", err)
	}
	s2 := testSession(t, cfg, makeKey([]string{"indexes.get", "documents.get", "documents.add"}))
	l2 := nativeWriteLease(t, s2, uid)
	r, err = nativeImportSave(l2)(context.Background(), s2.ProfileID(), uid, "documents", edits)
	if err != nil || r.Async == nil {
		t.Fatal("allowed scoped addition rejected", err)
	}
	if _, err = l2.Runtime().Verify(context.Background(), s2.ProfileID(), uid, "documents", r); !errors.As(err, &status) || status.Status != 403 {
		t.Fatal("denied task observation became success", err)
	}
	if _, err = nativeImportSave(l2)(context.Background(), s2.ProfileID(), uid, "documents", edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("missing tasks.get permitted replay", err)
	}
}
