package couchdb

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCouchDBNativeDocumentAllDocsMangoExactJSONAndDeletedBoundary(t *testing.T) {
	admin := nativeOpen(t, "NYADB_COUCHDB_ADMIN_CONFIG")
	c := nativeOpen(t, "NYADB_COUCHDB_CONFIG")
	db := nativeID()
	nativeRun(t, admin, Request{Method: "PUT", Path: "/" + db})
	t.Cleanup(func() { nativeRun(t, admin, Request{Method: "DELETE", Path: "/" + db}) })
	nativeRun(t, admin, Request{Method: "PUT", Path: "/" + db + "/_security", Body: []byte(`{"admins":{"names":[],"roles":[]},"members":{"names":[],"roles":[]}}`)})
	large := strings.Repeat("原生🧪", 140000)
	expected := map[string]bool{}
	for i := range 7 {
		id := "row_" + strconv.Itoa(i)
		expected[id] = true
		value := map[string]any{"_id": id, "n": json.Number("9007199254740993"), "nested": map[string]any{"null": nil, "empty": "", "array": []any{false, "", 42}}}
		if i == 0 {
			value["large"] = large
		}
		body, _ := json.Marshal(value)
		nativeRun(t, admin, Request{Method: "PUT", Path: "/" + db + "/" + id, Body: body})
	}
	special := "文档/空 格?\"#🐱"
	expected[special] = true
	body, _ := json.Marshal(map[string]any{"_id": special, "n": json.Number("9007199254740993")})
	nativeRun(t, admin, Request{Method: "PUT", Path: "/" + db + "/" + url.PathEscape(special), Body: body})
	expected["_design/sample"] = true
	nativeRun(t, admin, Request{Method: "PUT", Path: "/" + db + "/_design/sample", Body: []byte(`{"views":{"v":{"map":"function(doc) { emit(doc._id, doc.n); }"}}}`)})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// A complete server rejection is a known read error. The caller can
	// explicitly correct its query on this same original client afterward.
	for _, identities := range []bool{false, true} {
		request := FindRequest{Selector: json.RawMessage(`{"n":{"$eq":9007199254740993}}`), Sort: json.RawMessage(`["_id"]`), Limit: 2}
		var err error
		if identities {
			_, err = c.FindDocumentIdentities(ctx, db, request)
		} else {
			_, err = c.Find(ctx, db, request)
		}
		var status *HTTPError
		if !errors.As(err, &status) || status.Status != 400 || errors.Is(err, ErrRetired) {
			t.Fatal("native Mango rejected sort claimed an interrupted write", err)
		}
	}
	seen := map[string]bool{}
	start := ""
	var first *AllDocumentsPage
	for pages := 0; pages < 8; pages++ {
		page, err := c.AllDocuments(ctx, db, start, 3)
		if err != nil || page.TotalRows != 9 || len(page.UpdateSequence) == 0 {
			t.Fatal("native all-docs read", err)
		}
		if first == nil {
			first = page
		}
		for _, doc := range page.Documents {
			if !expected[doc.ID] || seen[doc.ID] {
				t.Fatal("native all-docs duplicate or changed ID")
			}
			seen[doc.ID] = true
			if doc.ID == "row_0" {
				var fields map[string]json.RawMessage
				var full string
				if decodeJSON(doc.JSON, &fields) != nil || string(fields["n"]) != "9007199254740993" || json.Unmarshal(fields["large"], &full) != nil || full != large || len(full) <= 1<<20 {
					t.Fatal("complete native source or exact number lost")
				}
			}
		}
		if !page.HasMore {
			break
		}
		if page.NextKey == "" || page.NextKey == start {
			t.Fatal("native all-docs made no progress")
		}
		start = page.NextKey
	}
	if len(seen) != len(expected) {
		t.Fatal("native all-docs incomplete", len(seen))
	}
	bookmark := ""
	mangoSeen := map[string]bool{}
	for pages := 0; pages < 8; pages++ {
		page, err := c.Find(ctx, db, FindRequest{Selector: json.RawMessage(`{"_id":{"$gt":null}}`), Sort: json.RawMessage(`["_id"]`), Bookmark: bookmark, Limit: 3})
		if err != nil {
			t.Fatal("native Mango read", err)
		}
		for _, doc := range page.Documents {
			if !expected[doc.ID] || mangoSeen[doc.ID] || strings.HasPrefix(doc.ID, "_design/") {
				t.Fatal("native Mango range misunderstood")
			}
			mangoSeen[doc.ID] = true
		}
		bookmark = page.Bookmark
		if !page.HasMore {
			break
		}
	}
	// Native _all_docs/doc_count includes design documents; Mango excludes them.
	if len(mangoSeen) != 8 {
		t.Fatal("native Mango/document count range changed", len(mangoSeen))
	}
	if first == nil || !first.HasMore || first.NextKey != "row_1" {
		t.Fatal("unexpected native initial boundary")
	}
	boundary := first.Documents[len(first.Documents)-1]
	nativeRun(t, admin, Request{Method: "DELETE", Path: "/" + db + "/" + url.PathEscape(boundary.ID) + "?rev=" + url.QueryEscape(boundary.Revision)})
	after, err := c.AllDocuments(ctx, db, boundary.ID, 3)
	if err != nil || len(after.Documents) != 3 || after.Documents[0].ID != "row_2" {
		t.Fatal("deleted original boundary skipped successor", err)
	}
}

func TestCouchDBNativeDocumentMangoLostReadCancellationNeverReplays(t *testing.T) {
	for _, mode := range []string{"lost", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			backend := nativeOpen(t, "NYADB_COUCHDB_ADMIN_CONFIG")
			var reads atomic.Int32
			completed := make(chan struct{})
			relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(io.LimitReader(r.Body, requestLimit+1))
				if err != nil {
					t.Error("relay read failed")
					return
				}
				response, err := backend.Do(r.Context(), Request{Method: r.Method, Path: r.URL.RequestURI(), Body: raw})
				if err != nil {
					t.Error("original native read failed", err)
					return
				}
				if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/_find") {
					reads.Add(1)
					close(completed)
					if mode == "lost" {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = conn.Close()
					} else {
						<-r.Context().Done()
					}
					return
				}
				w.Header().Set("Content-Type", response.ContentType)
				w.WriteHeader(response.Status)
				_, _ = w.Write(response.Body)
			}))
			defer relay.Close()
			ca := filepath.Join(t.TempDir(), "relay-ca.pem")
			if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: relay.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, secret := nativeFixture(t, "NYADB_COUCHDB_ADMIN_CONFIG")
			if len(relay.Certificate().DNSNames) == 0 {
				t.Fatal("native read relay lacks a verified hostname")
			}
			cfg.Endpoint, cfg.CAFile, cfg.ServerName = relay.URL, ca, relay.Certificate().DNSNames[0]
			c, err := Open(context.Background(), cfg, secret)
			if err != nil {
				t.Fatal("verified native read relay", err)
			}
			defer c.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			done := make(chan error, 1)
			request := FindRequest{Selector: json.RawMessage(`{"_id":{"$gt":null}}`), Limit: 2}
			go func() { _, err := c.Find(ctx, "nyadb_fixture", request); done <- err }()
			select {
			case <-completed:
			case <-ctx.Done():
				t.Fatal("original native Mango did not finish")
			}
			if mode == "canceled" {
				cancel()
			}
			var outcome error
			select {
			case outcome = <-done:
			case <-time.After(25 * time.Second):
				t.Fatal("original Mango worker did not settle")
			}
			var interrupted *Interrupted
			if !errors.As(outcome, &interrupted) || interrupted.WriteOutcomeUnknown || !errors.Is(outcome, ErrRetired) {
				t.Fatal("certified Mango read outcome changed", outcome)
			}
			if _, err := c.Find(context.Background(), "nyadb_fixture", request); !errors.Is(err, ErrRetired) || reads.Load() != 1 {
				t.Fatal("lost native read replayed", err)
			}
			response := nativeRun(t, backend, Request{Method: "GET", Path: "/nyadb_fixture"})
			if response.Status != 200 || !bytes.Contains(response.Body, []byte(`"db_name":"nyadb_fixture"`)) {
				t.Fatal("independent native client retired")
			}
		})
	}
}
