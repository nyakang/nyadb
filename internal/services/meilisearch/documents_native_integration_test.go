package meilisearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func nativeDocuments(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("NYADB_MEILISEARCH_DOCUMENT_IT") != "1" {
		t.Skip("requires owned native document read fixture")
	}
	return nativeOpen(t)
}
func TestMeilisearchNativeDocumentsEnumerationBeyondSearchLimitAndExactIDs(t *testing.T) {
	c := nativeDocuments(t)
	uid := nativeIndex(t, c)
	rows := make([]map[string]any, 0, 1017)
	for i := range 1017 {
		rows = append(rows, map[string]any{"id": fmt.Sprintf("row_%04d", i), "n": json.Number("9007199254740993"), "future": map[string]string{"text": "原生🐱"}})
	}
	body, _ := json.Marshal(rows)
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: body}))
	seen := map[string]bool{}
	offset := uint64(0)
	for {
		page, response, err := c.ListDocuments(context.Background(), uid, DocumentsRequest{Offset: offset, Limit: 256})
		if err != nil || response.Status != 200 || page.Offset != offset || page.Limit != 256 || page.Total != 1017 || !bytes.Equal(page.Raw, response.Body) {
			t.Fatal("native enumeration page", err)
		}
		for _, raw := range page.Documents {
			var doc struct{ ID string }
			if json.Unmarshal(raw, &doc) != nil || doc.ID == "" || seen[doc.ID] || !bytes.Contains(raw, []byte("9007199254740993")) || !bytes.Contains(raw, []byte("原生🐱")) {
				t.Fatal("document missing, repeated or rounded")
			}
			seen[doc.ID] = true
		}
		offset += uint64(len(page.Documents))
		if offset == page.Total {
			break
		}
		if len(page.Documents) == 0 {
			t.Fatal("enumeration silently truncated")
		}
	}
	if len(seen) != 1017 || !seen["row_1016"] {
		t.Fatal("enumeration used bounded search hits")
	}
	search, _, err := c.Search(context.Background(), uid, []byte(`{"q":"","limit":2000}`))
	if err != nil || len(search.Hits) != 1000 {
		t.Fatal("native default search boundary changed", err)
	}
	empty, _, err := c.ListDocuments(context.Background(), uid, DocumentsRequest{Offset: 2000, Limit: 2})
	if err != nil || empty.Total != 1017 || len(empty.Documents) != 0 {
		t.Fatal("native beyond-total page", err)
	}
	count, _, err := c.ListDocuments(context.Background(), uid, DocumentsRequest{Limit: 0})
	if err != nil || count.Total != 1017 || len(count.Documents) != 0 {
		t.Fatal("native explicit count page", err)
	}
	// Numeric IDs keep their original native JSON type and exact spelling.
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":9007199254740993,"n":9007199254740993},{"id":-1,"n":9007199254740993},{"id":"a-b_c","n":9007199254740993}]`)}))
	for _, id := range []string{"9007199254740993", "-1", "a-b_c"} {
		raw, _, err := c.ReadDocument(context.Background(), uid, id)
		if err != nil || !bytes.Contains(raw, []byte("9007199254740993")) {
			t.Fatal("native ID source", err)
		}
	}
	_, response, err := c.ReadDocument(context.Background(), uid, "missing")
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != 404 || errors.Is(err, ErrRetired) || !bytes.Contains(response.Body, []byte("document_not_found")) {
		t.Fatal("native missing document became empty success", err)
	}
	if _, _, err = c.ReadDocument(context.Background(), uid, "row_1016"); err != nil {
		t.Fatal("complete readonly error retired client", err)
	}
	receipt := nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":1.0}]`)})
	task, err := c.WaitTask(context.Background(), receipt, 30*time.Second)
	var failure *TaskFailure
	if !errors.As(err, &failure) || !bytes.Contains(task.Raw, []byte("invalid_document_id")) {
		t.Fatal("native decimal ID accepted", err)
	}
	t.Log("native documents enumerated1017 while search stopped1000; exact integer/negative IDs, original JSON and native missing/invalid ID evidence retained")
}
func TestMeilisearchNativeDocumentsReadInterruptionsCancelCloseAndNoReplay(t *testing.T) {
	for _, mode := range []string{"lost", "cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			upstream := nativeDocuments(t)
			uid := nativeIndex(t, upstream)
			nativeWait(t, upstream, nativeSubmit(t, upstream, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"one","n":9007199254740993}]`)}))
			entered, acked := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			c := nativeRelay(t, upstream, func(w http.ResponseWriter, r *http.Request, response Response) bool {
				if !strings.HasSuffix(r.URL.Path, "/documents/one") {
					return false
				}
				if response.Status != 200 || !bytes.Contains(response.Body, []byte("9007199254740993")) {
					t.Error("original native read failed")
				}
				calls.Add(1)
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
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			settled := make(chan error, 1)
			go func() { _, _, err := c.ReadDocument(ctx, uid, "one"); settled <- err }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("original read never entered")
			}
			if mode == "cancel" {
				cancel()
			} else if mode == "close" {
				c.Close()
			}
			select {
			case err := <-settled:
				var interrupted *Interrupted
				if !errors.As(err, &interrupted) || interrupted.WriteOutcomeUnknown {
					t.Fatal("read interruption became unknown write", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("owning read did not settle")
			}
			if _, _, err := c.ReadDocument(context.Background(), uid, "one"); !errors.Is(err, ErrRetired) {
				t.Fatal("retired read automatically recovered", err)
			}
			select {
			case <-acked:
			case <-time.After(5 * time.Second):
				t.Fatal("owning relay did not acknowledge")
			}
			fresh := nativeOpen(t)
			raw, _, err := fresh.ReadDocument(context.Background(), uid, "one")
			if err != nil || !bytes.Contains(raw, []byte("9007199254740993")) || calls.Load() != 1 {
				t.Fatal("old read replayed or damaged sibling", err)
			}
		})
	}
}
func TestMeilisearchNativeDocumentsScopedKeyPermissions(t *testing.T) {
	master := nativeDocuments(t)
	cfg, _ := nativeFixture(t)
	if cfg.Auth != "bearer" {
		t.Fatal("scoped acceptance requires authenticated fixture")
	}
	uid := nativeIndex(t, master)
	nativeWait(t, master, nativeSubmit(t, master, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"one","n":9007199254740993}]`)}))
	body, _ := json.Marshal(map[string]any{"name": "owned-documents-codec", "actions": []string{"indexes.get", "documents.get"}, "indexes": []string{uid}, "expiresAt": nil})
	response := nativeRun(t, master, Request{Method: "POST", Path: "/keys", Body: body})
	var key struct{ UID, Key string }
	if json.Unmarshal(response.Body, &key) != nil || key.UID == "" || key.Key == "" {
		t.Fatal("scoped key unavailable")
	}
	cfg.IndexUID = uid
	c, err := Open(context.Background(), cfg, key.Key)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Identity() != (Identity{}) {
		t.Fatal("scoped read inferred global version")
	}
	page, _, err := c.ListDocuments(context.Background(), uid, DocumentsRequest{Limit: 2})
	if err != nil || len(page.Documents) != 1 {
		t.Fatal("allowed scoped enumeration", err)
	}
	for _, path := range []string{"/version", "/indexes/" + uid + "/settings", "/indexes/" + uid + "/stats"} {
		_, err := c.Do(context.Background(), Request{Method: "GET", Path: path})
		var status *HTTPError
		if !errors.As(err, &status) || status.Status != 403 || errors.Is(err, ErrRetired) {
			t.Fatal("native scoped permission became empty success", err)
		}
	}
	raw, _, err := c.ReadDocument(context.Background(), uid, "one")
	if err != nil || !bytes.Contains(raw, []byte("9007199254740993")) {
		t.Fatal("denied read damaged allowed documents", err)
	}
	if _, _, err = c.ListDocuments(context.Background(), "other", DocumentsRequest{Limit: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatal("foreign typed scope admitted", err)
	}
	nativeRun(t, master, Request{Method: "DELETE", Path: "/keys/" + key.UID})
	_, _, err = c.ReadDocument(context.Background(), uid, "one")
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != 403 {
		t.Fatal("revoked original key recovered with another secret", err)
	}
}
