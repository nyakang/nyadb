package meilisearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeWriteLease(t *testing.T, s *Session, uid string) contract.DataDocumentLease {
	t.Helper()
	l, err := s.ReserveDataDocument(s.Namespace(uid).Object("documents", "documents"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}
func nativeWritePage(t *testing.T, s *Session, uid string, l contract.DataDocumentLease) *contract.DataPage {
	t.Helper()
	p, err := l.Runtime().AdvancedLoader(context.Background(), s.ProfileID(), uid, "documents", contract.DataOptions{Object: s.Namespace(uid).Object("documents", "documents"), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func nativeObserveDataWrite(t *testing.T, s *Session, uid string, l contract.DataDocumentLease, result *contract.DataEditResult) *contract.DataVerification {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		v, err := l.Runtime().Verify(context.Background(), s.ProfileID(), uid, "documents", result)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status != "pending" {
			return v
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("native task observation did not finish")
	return nil
}

func TestMeilisearchNativeDataWritesAdmissionExactReplacementAndDeletion(t *testing.T) {
	s, admin := nativeData(t)
	uid := nativeIndex(t, admin)
	nativeWait(t, admin, nativeSubmit(t, admin, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":9007199254740993,"old":true,"n":1}]`)}))
	l := nativeWriteLease(t, s, uid)
	p := nativeWritePage(t, s, uid, l)
	full, err := l.Runtime().RowReader(context.Background(), s.ProfileID(), uid, "documents", *p.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range []*contract.RowSnapshot{p.RowSnapshots[0], full.Snapshot} {
		forged := cloneDataValue(t, snapshot)
		if forged.Mode == "values" {
			forged.Original[1].Text = `{"id":9007199254740993,"forged":true}`
		} else {
			*forged.Fingerprints[1] = "forged"
		}
		if _, err = l.Runtime().Saver(context.Background(), s.ProfileID(), uid, "documents", []contract.DataEdit{{Kind: "delete", Original: forged}}); !errors.Is(err, ErrInvalid) {
			t.Fatal("native forged payload accepted", err)
		}
	}
	upsert := []contract.DataEdit{{Kind: "update", Original: full.Snapshot, Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: `{"id":9007199254740993,"n":9007199254740993,"猫":"🐱"}`}}}, {Kind: "insert", Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: `{"id":"inserted","n":9007199254740993}`}}}}
	result, err := l.Runtime().Saver(context.Background(), s.ProfileID(), uid, "documents", upsert)
	if err != nil || result.Async == nil || result.CommitConfirmed || result.CommitUncertain || len(result.Outcomes) != 0 || result.Applied != 0 {
		t.Fatal("native admission misrepresented completion", err)
	}
	if _, err = l.Runtime().Saver(context.Background(), s.ProfileID(), uid, "documents", upsert); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("admitted batch replayed", err)
	}
	v := nativeObserveDataWrite(t, s, uid, l, result)
	if v.Status != "applied" || !v.Recovered.CommitConfirmed || !v.Recovered.Verified || len(v.Recovered.Outcomes) != 2 || v.Recovered.Async.TaskJSON == "" {
		t.Fatal("native actual outcome missing")
	}
	raw, _, err := admin.ReadDocument(context.Background(), uid, "9007199254740993")
	if err != nil || strings.Contains(string(raw), `"old"`) || !strings.Contains(string(raw), "9007199254740993") {
		t.Fatal("POST did not completely replace exact source", err)
	}
	// PUT is a merge; grid writes deliberately select POST replacement.
	nativeWait(t, admin, nativeSubmit(t, admin, Request{Method: "PUT", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":9007199254740993,"merge":true}]`)}))
	raw, _, err = admin.ReadDocument(context.Background(), uid, "9007199254740993")
	if err != nil || !strings.Contains(string(raw), `"merge"`) || !strings.Contains(string(raw), `"n"`) {
		t.Fatal("native merge control", err)
	}
	if _, err = l.Runtime().Saver(context.Background(), s.ProfileID(), uid, "documents", []contract.DataEdit{{Kind: "delete", Original: v.Recovered.Outcomes[0].Saved}}); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("original row conflict was admitted", err)
	}
	p = nativeWritePage(t, s, uid, l)
	deletes := make([]contract.DataEdit, len(p.RowSnapshots))
	for i, snapshot := range p.RowSnapshots {
		deletes[i] = contract.DataEdit{Kind: "delete", Original: snapshot}
	}
	result, err = l.Runtime().Saver(context.Background(), s.ProfileID(), uid, "documents", deletes)
	if err != nil || result.Async == nil {
		t.Fatal(err)
	}
	v = nativeObserveDataWrite(t, s, uid, l, result)
	if v.Status != "applied" || len(v.Recovered.Outcomes) != 2 {
		t.Fatal("native delete batch unconfirmed")
	}
	if count, err := l.Runtime().Count(context.Background(), s.ProfileID(), uid, "documents"); err != nil || count != 0 {
		t.Fatal("native exact integer/string delete", count, err)
	}
	t.Log("native POST complete replacement, PUT merge control, exact integer/string identities, original values/fingerprints, one admission, explicit task observation and actual delete/readback verified")
}

func TestMeilisearchNativeDataWritesTaskFailureRetainsOriginalReceipt(t *testing.T) {
	s, admin := nativeData(t)
	uid := nativeIndex(t, admin)
	nativeWait(t, admin, nativeSubmit(t, admin, Request{Method: "PATCH", Path: "/indexes/" + uid + "/settings", Body: []byte(`{"filterableAttributes":["_geo"]}`)}))
	l := nativeWriteLease(t, s, uid)
	edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: `{"id":"bad_geo","_geo":{"lat":"invalid","lng":0}}`}}}}
	result, err := l.Runtime().Saver(context.Background(), s.ProfileID(), uid, "documents", edits)
	if err != nil || result.Async == nil {
		t.Fatal("failure was not natively admitted", err)
	}
	v := nativeObserveDataWrite(t, s, uid, l, result)
	if v.Status != "indeterminate" || v.Recovered.Async.Status != "failed" || !strings.Contains(v.Recovered.Async.TaskJSON, "invalid_document_geo_field") {
		t.Fatalf("native terminal task evidence absent: %s %s %s", v.Status, v.Recovered.Async.Status, v.Recovered.Async.TaskJSON)
	}
	if _, err = l.Runtime().Saver(context.Background(), s.ProfileID(), uid, "documents", edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("failed task replayed", err)
	}
	if count, err := l.Runtime().Count(context.Background(), s.ProfileID(), uid, "documents"); err != nil || count != 0 {
		t.Fatal("task failure reported row as saved", count, err)
	}
}

func TestMeilisearchNativeDataWritesLostCancelCloseNoReplay(t *testing.T) {
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
			go func() { r, e := l.Runtime().Saver(ctx, s.ProfileID(), uid, "documents", edits); out <- saved{r, e} }()
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
				if _, err := l.Runtime().Saver(context.Background(), s.ProfileID(), uid, "documents", edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
					t.Fatal("unknown admission replayed", err)
				}
			}
			if writes.Load() != 1 {
				t.Fatal("write replayed")
			}
		})
	}
}

func TestMeilisearchNativeDataWritesScopedPermissionsAndObservation(t *testing.T) {
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
	r, err := l.Runtime().Saver(context.Background(), s.ProfileID(), uid, "documents", edits)
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != 403 || r != nil {
		t.Fatal("denied write inferred admission", err)
	}
	if count, err := l.Runtime().Count(context.Background(), s.ProfileID(), uid, "documents"); err != nil || count != 0 {
		t.Fatal("denied write killed allowed reads", err)
	}
	s2 := testSession(t, cfg, makeKey([]string{"indexes.get", "documents.get", "documents.add"}))
	l2 := nativeWriteLease(t, s2, uid)
	r, err = l2.Runtime().Saver(context.Background(), s2.ProfileID(), uid, "documents", edits)
	if err != nil || r.Async == nil {
		t.Fatal("allowed scoped addition rejected", err)
	}
	if _, err = l2.Runtime().Verify(context.Background(), s2.ProfileID(), uid, "documents", r); !errors.As(err, &status) || status.Status != 403 {
		t.Fatal("denied task observation became success", err)
	}
	if _, err = l2.Runtime().Saver(context.Background(), s2.ProfileID(), uid, "documents", edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("missing tasks.get permitted replay", err)
	}
}

func TestMeilisearchNativeDataWritesObservationCancelAndOriginalClose(t *testing.T) {
	for _, mode := range []string{"cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			_, admin := nativeData(t)
			uid := nativeIndex(t, admin)
			entered, acked := make(chan struct{}), make(chan struct{})
			var held atomic.Bool
			var writes atomic.Int32
			relay := nativeRelay(t, admin, func(w http.ResponseWriter, r *http.Request, response Response) bool {
				path := strings.TrimPrefix(r.URL.Path, "/proxy/meili")
				if r.Method == "POST" && path == "/indexes/"+uid+"/documents" {
					writes.Add(1)
				}
				if !strings.HasPrefix(path, "/tasks/") || !held.CompareAndSwap(false, true) {
					return false
				}
				close(entered)
				<-r.Context().Done()
				close(acked)
				return true
			})
			_, secret := nativeFixture(t)
			s := testSession(t, relay.config, secret)
			l := nativeWriteLease(t, s, uid)
			edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: `{"id":"observe","n":9007199254740993}`}}}}
			result, err := l.Runtime().Saver(context.Background(), s.ProfileID(), uid, "documents", edits)
			if err != nil || result.Async == nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			out := make(chan error, 1)
			go func() { _, e := l.Runtime().Verify(ctx, s.ProfileID(), uid, "documents", result); out <- e }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("original task read not entered")
			}
			if mode == "close" {
				_ = l.Close()
			} else {
				cancel()
			}
			select {
			case err := <-out:
				if err == nil {
					t.Fatal("interrupted observation returned completion")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("observation executor not acknowledged")
			}
			select {
			case <-acked:
			case <-time.After(5 * time.Second):
				t.Fatal("relay not acknowledged")
			}
			if writes.Load() != 1 || result.Async.TaskJSON != "" {
				t.Fatal("observation wrote or modified caller receipt")
			}
			if mode == "cancel" {
				if _, err = l.Runtime().Saver(context.Background(), s.ProfileID(), uid, "documents", edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
					t.Fatal("canceled observation permitted replay", err)
				}
				v := nativeObserveDataWrite(t, s, uid, l, result)
				if v.Status != "applied" || writes.Load() != 1 {
					t.Fatal("explicit observation resubmitted original write")
				}
			} else if _, err = l.Runtime().Verify(context.Background(), s.ProfileID(), uid, "documents", result); !errors.Is(err, contract.ErrRevoked) {
				t.Fatal("closed original observation rebound", err)
			}
		})
	}
}
