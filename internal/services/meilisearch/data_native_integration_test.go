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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeData(t *testing.T) (*Session, *Client) {
	t.Helper()
	if os.Getenv("NYADB_MEILISEARCH_DATA_IT") != "1" {
		t.Skip("requires owned native Meilisearch readonly data fixture")
	}
	cfg, secret := nativeFixture(t)
	s := testSession(t, cfg, secret)
	return s, nativeOpen(t)
}
func TestMeilisearchNativeDataQualifiedOffsetPagingFingerprintsAndCompleteExport(t *testing.T) {
	s, admin := nativeData(t)
	uid := nativeIndex(t, admin)
	documents := make([]map[string]any, 0, 5015)
	for i := range 5015 {
		documents = append(documents, map[string]any{"id": fmt.Sprintf("row_%04d", i), "integer": json.Number("9007199254740993"), "future": map[string]string{"text": "原生🐱"}})
	}
	documents[0]["memo"] = strings.Repeat("猫🐱", 2000)
	documents[1]["memo"] = strings.Repeat("x", 1_400_000)
	raw, _ := json.Marshal(documents)
	nativeWait(t, admin, nativeSubmit(t, admin, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: raw}))
	object := s.Namespace(uid).Object("documents", "documents")
	first, err := s.ReserveDocumentReads(object)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := s.ReserveDocumentReads(object)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	runtime := first.Runtime()
	o := contract.DataOptions{Object: object, Limit: 2}
	page, err := runtime.AdvancedLoader(context.Background(), s.ProfileID(), uid, "documents", o)
	if err != nil || len(page.Rows) != 2 || page.Rows[0][0].Text != "row_0000" || !strings.Contains(page.Rows[0][1].Text, "9007199254740993") || page.KeysetAvailable || page.StableOrder || page.PagingMode != "offset" {
		t.Fatal("native readonly OFFSET page", err)
	}
	o.After = page.NextCursor
	o.Offset = 2
	next, err := runtime.AdvancedLoader(context.Background(), s.ProfileID(), uid, "documents", o)
	if err != nil || next.Rows[0][0].Text != "row_0002" {
		t.Fatal("original native continuation", err)
	}
	if _, err = second.Runtime().AdvancedLoader(context.Background(), s.ProfileID(), uid, "documents", o); !errors.Is(err, ErrInvalid) {
		t.Fatal("sibling native cursor accepted", err)
	}
	ref := *page.RowRefs[0]
	full, err := runtime.RowReader(context.Background(), s.ProfileID(), uid, "documents", ref)
	if err != nil || full.Snapshot.Mode != "values" || !strings.Contains(full.Values["document"].Text, "9007199254740993") {
		t.Fatal("full original row", err)
	}
	chunk, err := runtime.Chunk(context.Background(), s.ProfileID(), uid, "documents", "document", ref, 1024, 1024)
	if err != nil || len([]rune(chunk.Text)) != 1024 || chunk.Offset != 1024 || chunk.NextOffset != 2048 || !chunk.HasMore {
		t.Fatal("native bounded Unicode field continuation", err)
	}
	if _, err = runtime.Chunk(context.Background(), s.ProfileID(), uid, "documents", "document", ref, 0, 1025); !errors.Is(err, ErrBounds) {
		t.Fatal("native field limit accepted", err)
	}
	if !page.Rows[1][1].Truncated {
		t.Fatal("native large document not explicitly truncated")
	}
	if _, err = runtime.RowReader(context.Background(), s.ProfileID(), uid, "documents", *page.RowRefs[1]); !errors.Is(err, ErrBounds) {
		t.Fatal("native full row limit accepted", err)
	}
	count, err := runtime.Count(context.Background(), s.ProfileID(), uid, "documents")
	if err != nil || count != 5015 {
		t.Fatal("native documents total", count, err)
	}
	var exported bytes.Buffer
	err = runtime.JSONL(context.Background(), object, &exported)
	if err != nil {
		t.Fatal("native full JSONL", err)
	}
	lines := bytes.Split(bytes.TrimSpace(exported.Bytes()), []byte{'\n'})
	seen := map[string]bool{}
	for _, line := range lines {
		var doc struct {
			ID      string
			Integer json.Number
		}
		if json.Unmarshal(line, &doc) != nil || doc.Integer.String() != "9007199254740993" || seen[doc.ID] || doc.ID == "" {
			t.Fatal("full original JSON source missing/repeated/rounded")
		}
		seen[doc.ID] = true
		if doc.ID == "row_0001" && len(line) < 1_400_000 {
			t.Fatal("complete export silently used grid preview")
		}
	}
	if len(lines) != 5015 || !seen["row_5014"] {
		t.Fatal("complete export used search hits", len(lines))
	}
	var tabular int
	err = runtime.Exporter(context.Background(), s.ProfileID(), uid, "documents", contract.DataOptions{Object: object}, []string{"document", "id"}, func(v []contract.DataValue) error {
		if v[0].Kind != contract.JSON || !strings.Contains(v[0].Text, "9007199254740993") || v[1].Kind != contract.Text {
			t.Error("typed export source")
		}
		tabular++
		return nil
	})
	if err != nil || tabular != 5015 {
		t.Fatal("native complete tabular stream", tabular, err)
	}
	// Changing the source without changing its ID invalidates the captured fingerprint.
	nativeWait(t, admin, nativeSubmit(t, admin, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"row_0000","integer":2}]`)}))
	if _, err = runtime.RowReader(context.Background(), s.ProfileID(), uid, "documents", ref); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("stale original source accepted", err)
	}
	if runtime.Capabilities.EditableTables || runtime.Saver != nil || runtime.Importer != nil || runtime.Transactions.Begin != nil {
		t.Fatal("private native reads inferred write semantics")
	}
	t.Log("native readonly original leases, offset cursor/sibling isolation, bounded Unicode fields, full-row limits, typed fingerprint/value rows and exact5015document JSONL/tabular export verified beyond search1000 with1400000byte source intact")
}
func TestMeilisearchNativeDataReadCancelCloseIndependentSlotsAndSessionLifecycle(t *testing.T) {
	for _, mode := range []string{"cancel", "lease_close", "session_close"} {
		t.Run(mode, func(t *testing.T) {
			_, upstream := nativeData(t)
			uid := nativeIndex(t, upstream)
			nativeWait(t, upstream, nativeSubmit(t, upstream, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"one","n":9007199254740993}]`)}))
			entered, acked := make(chan struct{}), make(chan struct{})
			var held atomic.Bool
			var reads atomic.Int32
			relay := nativeRelay(t, upstream, func(w http.ResponseWriter, r *http.Request, response Response) bool {
				if !strings.HasSuffix(r.URL.Path, "/documents") || r.URL.Query().Get("limit") != "0" {
					return false
				}
				reads.Add(1)
				if !held.CompareAndSwap(false, true) {
					return false
				}
				close(entered)
				<-r.Context().Done()
				close(acked)
				return true
			})
			_, secret := nativeFixture(t)
			s := testSession(t, relay.config, secret)
			object := s.Namespace(uid).Object("documents", "documents")
			a, err := s.ReserveDocumentReads(object)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			b, err := s.ReserveDocumentReads(object)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			settled := make(chan error, 1)
			go func() { _, err := a.Runtime().Count(ctx, s.ProfileID(), uid, "documents"); settled <- err }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("owning native count did not enter")
			}
			if count, err := b.Runtime().Count(context.Background(), s.ProfileID(), uid, "documents"); err != nil || count != 1 {
				t.Fatal("blocked original count canceled sibling", err)
			}
			if mode == "cancel" {
				cancel()
			} else if mode == "lease_close" {
				_ = a.Close()
			} else {
				var work sync.WaitGroup
				for range 3 {
					work.Add(1)
					go func() { defer work.Done(); _ = s.Close() }()
				}
				work.Wait()
			}
			select {
			case err := <-settled:
				if err == nil {
					t.Fatal("canceled original count returned success")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("original data executor did not settle")
			}
			select {
			case <-acked:
			case <-time.After(5 * time.Second):
				t.Fatal("native relay did not acknowledge")
			}
			before := reads.Load()
			time.Sleep(5 * time.Millisecond)
			if reads.Load() != before {
				t.Fatal("original read replayed")
			}
			if mode == "session_close" {
				if _, err = b.Runtime().Count(context.Background(), s.ProfileID(), uid, "documents"); !errors.Is(err, contract.ErrRevoked) {
					t.Fatal("closed session rebound", err)
				}
			} else {
				if count, err := b.Runtime().Count(context.Background(), s.ProfileID(), uid, "documents"); err != nil || count != 1 {
					t.Fatal("old cancellation killed sibling", err)
				}
			}
			fresh := testSession(t, relay.config, secret)
			freshLease, err := fresh.ReserveDocumentReads(fresh.Namespace(uid).Object("documents", "documents"))
			if err != nil {
				t.Fatal(err)
			}
			defer freshLease.Close()
			if count, err := freshLease.Runtime().Count(context.Background(), fresh.ProfileID(), uid, "documents"); err != nil || count != 1 {
				t.Fatal("old close killed independent original session", err)
			}
		})
	}
}
func TestMeilisearchNativeDataScopedPermissionsAndIndexObservationChanges(t *testing.T) {
	_, master := nativeData(t)
	cfg, _ := nativeFixture(t)
	if cfg.Auth != "bearer" {
		t.Fatal("scoped data acceptance requires authenticated fixture")
	}
	uid := nativeIndex(t, master)
	nativeWait(t, master, nativeSubmit(t, master, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"one","n":9007199254740993}]`)}))
	body, _ := json.Marshal(map[string]any{"name": "owned-private-data", "actions": []string{"indexes.get", "documents.get"}, "indexes": []string{uid}, "expiresAt": nil})
	response := nativeRun(t, master, Request{Method: "POST", Path: "/keys", Body: body})
	var key struct{ UID, Key string }
	if json.Unmarshal(response.Body, &key) != nil || key.UID == "" || key.Key == "" {
		t.Fatal("owned scoped key unavailable")
	}
	cfg.IndexUID = uid
	s := testSession(t, cfg, key.Key)
	if s.Identity() != (Identity{}) {
		t.Fatal("scoped data fabricated global observation")
	}
	object := s.Namespace(uid).Object("documents", "documents")
	lease, err := s.ReserveDocumentReads(object)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if count, err := lease.Runtime().Count(context.Background(), s.ProfileID(), uid, "documents"); err != nil || count != 1 {
		t.Fatal("allowed original scoped total", err)
	}
	_, err = s.TableMetadata(context.Background(), s.Namespace(uid).Object("settings", "settings"))
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != 403 {
		t.Fatal("denied scoped settings became empty success", err)
	}
	if _, err = lease.Runtime().AdvancedLoader(context.Background(), s.ProfileID(), uid, "documents", contract.DataOptions{Object: object, Limit: 1}); err != nil {
		t.Fatal("denied metadata killed documents", err)
	}
	// Same UID is recreated while binary observation remains unchanged; the observed creation changes.
	originalVersion := master.Identity()
	nativeWait(t, master, nativeSubmit(t, master, Request{Method: "DELETE", Path: "/indexes/" + uid}))
	nativeWait(t, master, nativeSubmit(t, master, Request{Method: "POST", Path: "/indexes", Body: []byte(`{"uid":"` + uid + `","primaryKey":"id"}`)}))
	nativeWait(t, master, nativeSubmit(t, master, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"replacement"}]`)}))
	if master.Identity() != originalVersion {
		t.Fatal("binary observation unexpectedly changed")
	}
	if _, err = lease.Runtime().Count(context.Background(), s.ProfileID(), uid, "documents"); !errors.Is(err, ErrDataTargetChanged) {
		t.Fatal("observed original index recreation accepted", err)
	}
	fresh, err := s.ReserveDocumentReads(object)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if count, err := fresh.Runtime().Count(context.Background(), s.ProfileID(), uid, "documents"); err != nil || count != 1 {
		t.Fatal("explicit new original document failed", err)
	}
	nativeRun(t, master, Request{Method: "DELETE", Path: "/keys/" + key.UID})
	_, err = fresh.Runtime().Count(context.Background(), s.ProfileID(), uid, "documents")
	if !errors.As(err, &status) || status.Status != 403 {
		t.Fatal("revoked original data key rebound", err)
	}
	if _, err = s.ReserveDocumentReads((contract.NamespaceRef{ProfileID: s.ProfileID() + 1, Catalog: uid}).Object("documents", "documents")); err == nil {
		t.Fatal("foreign profile admitted")
	}
	t.Log("native scoped data/index observations, independent denied settings and key revocation verified; timestamp and task observations are not atomic incarnation/CAS")
}

func TestMeilisearchNativeDataExportInterruptionsAndObservedConcurrentChanges(t *testing.T) {
	for _, mode := range []string{"total_change", "lost_response", "cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			_, upstream := nativeData(t)
			uid := nativeIndex(t, upstream)
			rows := make([]map[string]string, 513)
			for i := range rows {
				rows[i] = map[string]string{"id": fmt.Sprintf("row_%04d", i)}
			}
			raw, _ := json.Marshal(rows)
			nativeWait(t, upstream, nativeSubmit(t, upstream, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: raw}))
			entered, acked := make(chan struct{}), make(chan struct{})
			var held atomic.Bool
			var attempts atomic.Int32
			relay := nativeRelay(t, upstream, func(w http.ResponseWriter, r *http.Request, response Response) bool {
				if !strings.HasSuffix(r.URL.Path, "/documents") || r.URL.Query().Get("limit") != "256" || r.URL.Query().Get("offset") != "256" {
					return false
				}
				attempts.Add(1)
				if !held.CompareAndSwap(false, true) {
					return false
				}
				if mode == "total_change" {
					// This response still has the original total; the next native
					// page observes an actual server mutation between pages.
					receipt, _, err := upstream.Submit(r.Context(), Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"added_between_pages"}]`)})
					if err == nil {
						_, err = upstream.WaitTask(r.Context(), receipt, 30*time.Second)
					}
					if err != nil {
						t.Error("owned concurrent insertion failed", err)
					}
					return false
				}
				close(entered)
				if mode == "lost_response" {
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
			lease, err := s.ReserveDocumentReads(s.Namespace(uid).Object("documents", "documents"))
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			directory := t.TempDir()
			t.Setenv("TMPDIR", directory)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var output bytes.Buffer
			settled := make(chan error, 1)
			go func() {
				settled <- lease.Runtime().JSONL(ctx, s.Namespace(uid).Object("documents", "documents"), &output)
			}()
			if mode == "cancel" || mode == "close" {
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("original export did not reach its second native page")
				}
				if mode == "cancel" {
					cancel()
				} else {
					_ = lease.Close()
				}
			}
			select {
			case err := <-settled:
				if err == nil || mode == "total_change" && !errors.Is(err, contract.ErrDataConflict) {
					t.Fatal("incomplete export returned success", err)
				}
				var interrupted *Interrupted
				if errors.As(err, &interrupted) && interrupted.WriteOutcomeUnknown {
					t.Fatal("readonly export fabricated uncertain write")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("original export did not acknowledge")
			}
			if output.Len() != 0 || attempts.Load() != 1 {
				t.Fatal("incomplete export emitted rows or replayed native page")
			}
			files, err := os.ReadDir(directory)
			if err != nil || len(files) != 0 {
				t.Fatal("original export spool leaked", err)
			}
			if mode != "total_change" {
				select {
				case <-acked:
				case <-time.After(5 * time.Second):
					t.Fatal("original export relay not acknowledged")
				}
			}
			fresh := testSession(t, relay.config, secret)
			newLease, err := fresh.ReserveDocumentReads(fresh.Namespace(uid).Object("documents", "documents"))
			if err != nil {
				t.Fatal(err)
			}
			defer newLease.Close()
			expected := int64(513)
			if mode == "total_change" {
				expected++
			}
			if count, err := newLease.Runtime().Count(context.Background(), fresh.ProfileID(), uid, "documents"); err != nil || count != expected {
				t.Fatal("original export interruption affected independent session", err)
			}
		})
	}
}
