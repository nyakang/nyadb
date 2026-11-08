package meilisearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeTasks(t *testing.T, c *Client, q TaskQuery) TaskPage {
	t.Helper()
	p, response, err := c.ListTasks(context.Background(), q)
	if err != nil || response.Status != 200 || len(p.Raw) == 0 {
		t.Fatal("native task page", err)
	}
	return p
}
func nativeTaskDetails(t *testing.T, task Task) map[string]json.RawMessage {
	t.Helper()
	fields, err := nativeObject(task.Raw)
	details, e := nativeObject(fields["details"])
	if err != nil || e != nil {
		t.Fatal("native complete task details missing")
	}
	return details
}
func nativeTaskChange(t *testing.T, c *Client, action string, ids []uint32) Receipt {
	t.Helper()
	receipt, response, err := c.ChangeTasks(context.Background(), action, ids)
	if err != nil || response.Status != 200 || receipt.IndexUID != nil {
		t.Fatal("native HTTP200 asynchronous task admission", err)
	}
	return receipt
}

func TestMeilisearchNativeTaskCatalogPagingFiltersAndOriginalEvidence(t *testing.T) {
	c := nativeOpen(t)
	uid := nativeIndex(t, c)
	other := nativeIndex(t, c)
	receipts := []Receipt{}
	for i := 0; i < 7; i++ {
		r := nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(fmt.Sprintf(`[{"id":"row_%d","integer":9007199254740993,"text":"原生🐱"}]`, i))})
		nativeWait(t, c, r)
		receipts = append(receipts, r)
	}
	failure := nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"text":"private diagnostic 原生🐱"}]`)})
	task, err := c.WaitTask(context.Background(), failure, time.Minute)
	var failed *TaskFailure
	if !errors.As(err, &failed) || task.Status != "failed" {
		t.Fatal("native failed task unavailable", err)
	}
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes/" + other + "/documents", Body: []byte(`[{"id":"other"}]`)}))
	for _, reverse := range []bool{false, true} {
		q := TaskQuery{Limit: 2, IndexUIDs: []string{uid}, Reverse: reverse}
		seen := map[uint32]bool{}
		var previous *uint32
		for pages := 0; ; pages++ {
			if pages > 20 {
				t.Fatal("native cursor did not terminate")
			}
			page := nativeTasks(t, c, q)
			if page.Total != 9 || len(page.Results) == 0 {
				t.Fatal("native index-filtered live total or results changed")
			}
			for _, record := range page.Results {
				if seen[record.UID] || record.IndexUID == nil || *record.IndexUID != uid {
					t.Fatal("cursor repeated task or escaped index")
				}
				if previous != nil && (reverse && record.UID <= *previous || !reverse && record.UID >= *previous) {
					t.Fatal("native ordering changed")
				}
				id := record.UID
				previous = &id
				seen[id] = true
			}
			if page.Next == nil {
				break
			}
			q.From = page.Next
		}
		if len(seen) != 9 {
			t.Fatal("native multi-page task omitted", len(seen))
		}
	}
	filtered := nativeTasks(t, c, TaskQuery{Limit: 1000, IndexUIDs: []string{uid}, Statuses: []string{"failed"}, Types: []string{"documentAdditionOrUpdate"}, UIDs: []uint32{failure.UID}})
	if filtered.Total != 1 || len(filtered.Results) != 1 || !bytes.Equal(filtered.Results[0].Raw, task.Raw) {
		t.Fatal("complete original failure task changed")
	}
	original := filtered.Results[0]
	fields, e := nativeObject(original.Raw)
	diagnostic, e2 := nativeObject(fields["error"])
	code, e3 := nativeString(diagnostic["code"])
	details, e4 := nativeObject(fields["details"])
	received, e5 := nativeUint(details["receivedDocuments"], 64)
	if e != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || code != "missing_document_id" || received != 1 {
		t.Fatal("native task error/details lost")
	}
	batch, e := nativeUint(fields["batchUid"], 32)
	if e != nil {
		t.Fatal("native batch UID lost")
	}
	p := nativeTasks(t, c, TaskQuery{Limit: 10, IndexUIDs: []string{uid}, BatchUIDs: []uint32{uint32(batch)}})
	if len(p.Results) != 1 || p.Results[0].UID != failure.UID {
		t.Fatal("native batch filter changed")
	}
	now := time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)
	for _, q := range []TaskQuery{{Limit: 10, IndexUIDs: []string{uid}, AfterEnqueuedAt: now}, {Limit: 10, IndexUIDs: []string{uid}, AfterStartedAt: now}, {Limit: 10, IndexUIDs: []string{uid}, AfterFinishedAt: now}, {Limit: 10, UIDs: []uint32{4294967295}}, {Limit: 10, CanceledBy: []uint32{4294967295}}} {
		if p := nativeTasks(t, c, q); p.Total != 0 || len(p.Results) != 0 || p.From != nil || p.Next != nil {
			t.Fatal("native empty filter/cursor changed")
		}
	}
	p = nativeTasks(t, c, TaskQuery{Limit: 10, IndexUIDs: []string{uid}, BeforeEnqueuedAt: now, BeforeStartedAt: now, BeforeFinishedAt: now})
	if p.Total != 9 {
		t.Fatal("native timestamp filters changed")
	}
	looked, response, err := c.LookupTask(context.Background(), failure.UID)
	if err != nil || !bytes.Equal(looked.Raw, original.Raw) || !bytes.Equal(looked.Raw, response.Body) {
		t.Fatal("native single task evidence changed", err)
	}
	refreshed, _, err := c.ReadObservedTask(context.Background(), original)
	if err != nil || refreshed.Status != "failed" {
		t.Fatal("native original observation failed", err)
	}
	foreign := nativeOpen(t)
	if _, _, err := foreign.ReadObservedTask(context.Background(), original); !errors.Is(err, ErrInvalid) {
		t.Fatal("native cross-client observation admitted")
	}
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "PATCH", Path: "/indexes/" + uid + "/settings", Body: []byte(`{"stopWords":["owned"]}`)}))
	if p := nativeTasks(t, c, TaskQuery{Limit: 2, IndexUIDs: []string{uid}}); p.Total != 10 {
		t.Fatal("task list fabricated snapshot total")
	}
	t.Log("native descending/ascending inclusive next-UID pages; live totals, batch/UID/type/status/timestamp filters, full error/details and original-client evidence verified")
}

func TestMeilisearchNativeTaskManagement200AdmissionAndCompletedCounts(t *testing.T) {
	c := nativeOpen(t)
	uid := nativeIndex(t, c)
	other := nativeIndex(t, c)
	failed := nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"missing":"id"}]`)})
	if _, err := c.WaitTask(context.Background(), failed, time.Minute); err == nil {
		t.Fatal("fixture task should fail")
	}
	kept := nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes/" + other + "/documents", Body: []byte(`[{"id":"kept"}]`)})
	nativeWait(t, c, kept)
	// Occupy the executor before admitting the original target. Cancellation is
	// native and targets only the queued UID; the blocker must still succeed.
	docs := make([]map[string]any, 40000)
	for i := range docs {
		docs[i] = map[string]any{"id": "block_" + strconv.Itoa(i), "text": strings.Repeat("owned native search words ", 4)}
	}
	body, _ := json.Marshal(docs)
	blocker := nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: body})
	target := nativeSubmit(t, c, Request{Method: "PATCH", Path: "/indexes/" + uid + "/settings", Body: []byte(`{"stopWords":["queued"]}`)})
	cancellation := nativeTaskChange(t, c, "cancel", []uint32{target.UID})
	details := nativeTaskDetails(t, nativeWait(t, c, cancellation))
	matched, e := nativeUint(details["matchedTasks"], 64)
	canceled, e2 := nativeUint(details["canceledTasks"], 64)
	if e != nil || e2 != nil || matched != 1 || canceled != 1 {
		t.Fatal("native cancellation did not cancel the sole queued target")
	}
	record, _, err := c.LookupTask(context.Background(), target.UID)
	if err != nil || record.Status != "canceled" {
		t.Fatal("native target not canceled", err)
	}
	f, _ := nativeObject(record.Raw)
	by, e := nativeUint(f["canceledBy"], 32)
	if e != nil || uint32(by) != cancellation.UID {
		t.Fatal("native original cancellation UID lost")
	}
	if p := nativeTasks(t, c, TaskQuery{Limit: 10, CanceledBy: []uint32{cancellation.UID}}); len(p.Results) != 1 || p.Results[0].UID != target.UID {
		t.Fatal("native canceledBy filtering failed")
	}
	nativeWait(t, c, blocker)
	// A completed task matches cancellation but cannot be undone.
	noop := nativeTaskDetails(t, nativeWait(t, c, nativeTaskChange(t, c, "cancel", []uint32{blocker.UID})))
	n, e := nativeUint(noop["canceledTasks"], 64)
	if e != nil || n != 0 {
		t.Fatal("completed task cancellation confused with rollback")
	}
	deletion := nativeTaskChange(t, c, "delete", []uint32{failed.UID, target.UID, blocker.UID})
	details = nativeTaskDetails(t, nativeWait(t, c, deletion))
	matched, e = nativeUint(details["matchedTasks"], 64)
	deleted, e2 := nativeUint(details["deletedTasks"], 64)
	if e != nil || e2 != nil || matched != 3 || deleted != 3 {
		t.Fatal("native deletion counts changed")
	}
	_, response, err := c.ReadObservedTask(context.Background(), record)
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != 404 || !bytes.Contains(response.Body, []byte("task_not_found")) {
		t.Fatal("deleted original task reconstructed or replayed", err)
	}
	if observed, _, err := c.LookupTask(context.Background(), kept.UID); err != nil || observed.Status != "succeeded" {
		t.Fatal("explicit task IDs affected another index")
	}
	docsRead, _, err := c.ListDocuments(context.Background(), uid, DocumentsRequest{Limit: 1})
	if err != nil || docsRead.Total != 40000 {
		t.Fatal("deleting tasks deleted original documents", err)
	}
	t.Log("native HTTP200 admission; exactly one queued task canceled, completed-task cancellation is zero, exactly three records deleted while source documents and another index task remain")
}

func TestMeilisearchNativeTaskREST200AdmissionAndSynchronous(t *testing.T) {
	cfg, secret := nativeFixture(t)
	s := testSession(t, cfg, secret)
	c := nativeOpen(t)
	uid := nativeIndex(t, c)
	target := nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"one"}]`)})
	nativeWait(t, c, target)
	lease, err := s.ReserveQuery(87)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	for _, text := range []string{"POST /tasks/cancel?uids=" + strconv.FormatUint(uint64(target.UID), 10), "DELETE /tasks?uids=" + strconv.FormatUint(uint64(target.UID), 10)} {
		result, err := lease.Execute(context.Background(), "", text)
		if err != nil || len(result.Steps) != 1 || result.Steps[0].State != contract.StatementEnqueued || len(result.Sets) != 1 || result.Sets[0].Rows[0][0].Text != "200" {
			t.Fatal("REST native HTTP200 task admission reported as completion", err)
		}
		receipt, err := parseReceipt([]byte(result.Sets[0].Rows[0][1].Text))
		if err != nil {
			t.Fatal("REST original task receipt lost")
		}
		nativeWait(t, c, receipt)
	}
	result, err := lease.Execute(context.Background(), "", "GET /tasks?limit=1")
	if err != nil || result.Steps[0].State != "completed" {
		t.Fatal("synchronous GET mislabeled as admission", err)
	}
	t.Log("public REST lease preserves HTTP200 cancel/delete original admission; synchronous task GET remains completed without task replay")
}

func TestMeilisearchNativeTaskCatalogReadCancelCloseAndMutationNoReplay(t *testing.T) {
	upstream := nativeOpen(t)
	uid := nativeIndex(t, upstream)
	target := nativeSubmit(t, upstream, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"one"}]`)})
	nativeWait(t, upstream, target)
	for _, closeClient := range []bool{false, true} {
		entered := make(chan struct{})
		c := nativeRelay(t, upstream, func(w http.ResponseWriter, r *http.Request, response Response) bool {
			if strings.HasSuffix(r.URL.Path, "/tasks") {
				close(entered)
				<-r.Context().Done()
				return true
			}
			return false
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, _, err := c.ListTasks(ctx, TaskQuery{Limit: 1}); done <- err }()
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("original task list not started")
		}
		if closeClient {
			c.Close()
		} else {
			cancel()
		}
		select {
		case err := <-done:
			var interrupted *Interrupted
			if !errors.As(err, &interrupted) || interrupted.WriteOutcomeUnknown || !errors.Is(err, context.Canceled) {
				t.Fatal("readonly cancellation invented write outcome", err)
			}
		case <-time.After(time.Second):
			t.Fatal("original task executor not acknowledged")
		}
		cancel()
	}
	for _, action := range []string{"cancel", "delete"} {
		for _, fault := range []string{"lost", "canceled", "closed"} {
			var calls atomic.Int32
			admitted := make(chan Receipt, 1)
			release := make(chan struct{})
			var released sync.Once
			unblock := func() { released.Do(func() { close(release) }) }
			c := nativeRelay(t, upstream, func(w http.ResponseWriter, r *http.Request, response Response) bool {
				if r.Method != "POST" && r.Method != "DELETE" {
					return false
				}
				calls.Add(1)
				receipt, err := parseReceipt(response.Body)
				if err != nil || response.Status != 200 {
					t.Error("native mutation not admitted")
					return false
				}
				admitted <- receipt
				if fault != "lost" {
					<-r.Context().Done()
					// Keep net/http from synthesizing a successful response
					// until the original canceled executor is acknowledged.
					<-release
					return true
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
				return true
			})
			t.Cleanup(unblock)
			ctx, cancel := context.WithCancel(context.Background())
			type completion struct {
				response Response
				err      error
			}
			done := make(chan completion, 1)
			go func() {
				_, response, err := c.ChangeTasks(ctx, action, []uint32{target.UID})
				done <- completion{response, err}
			}()
			var receipt Receipt
			select {
			case receipt = <-admitted:
			case <-time.After(10 * time.Second):
				cancel()
				t.Fatal("original native admission missing")
			}
			if fault == "canceled" {
				cancel()
			}
			if fault == "closed" {
				c.Close()
			}
			select {
			case result := <-done:
				var interrupted *Interrupted
				if !errors.As(result.err, &interrupted) || !interrupted.WriteOutcomeUnknown || result.response.Status != 0 {
					t.Fatal("lost/canceled native task admission not uncertain", result.err)
				}
				if fault != "lost" && !errors.Is(result.err, context.Canceled) {
					t.Fatal("mutation cancellation not acknowledged", result.err)
				}
			case <-time.After(10 * time.Second):
				cancel()
				t.Fatal("original native mutation executor not acknowledged")
			}
			unblock()
			cancel()
			if _, _, err := c.ChangeTasks(context.Background(), action, []uint32{target.UID}); !errors.Is(err, ErrRetired) || calls.Load() != 1 {
				t.Fatal("task cancellation/deletion replayed")
			}
			nativeWait(t, upstream, receipt)
		}
	}

	t.Log("verified TLS front-end read cancel/close waits for original executor; native cancel/delete admitted behind lost/canceled/closed replies, each submitted once with no replay")
}

func TestMeilisearchNativeTaskCatalogScopedPermissionsAndRevocation(t *testing.T) {
	master := nativeOpen(t)
	cfg, _ := nativeFixture(t)
	if cfg.Auth != "bearer" {
		t.Fatal("requires selected authenticated fixture")
	}
	uid := nativeIndex(t, master)
	other := nativeIndex(t, master)
	own := nativeSubmit(t, master, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"own"}]`)})
	nativeWait(t, master, own)
	foreign := nativeSubmit(t, master, Request{Method: "POST", Path: "/indexes/" + other + "/documents", Body: []byte(`[{"id":"foreign"}]`)})
	nativeWait(t, master, foreign)
	body, _ := json.Marshal(map[string]any{"name": "owned-task-read-scope", "indexes": []string{uid}, "actions": []string{"indexes.get", "tasks.get"}, "expiresAt": nil})
	fields, e := nativeObject(nativeRun(t, master, Request{Method: "POST", Path: "/keys", Body: body}).Body)
	key, e2 := nativeString(fields["key"])
	keyUID, e3 := nativeString(fields["uid"])
	if e != nil || e2 != nil || e3 != nil {
		t.Fatal("native task scoped key unavailable")
	}
	cfg.IndexUID = uid
	open := func() *Client {
		c, err := Open(context.Background(), cfg, key)
		if err != nil {
			t.Fatal("native scoped task handshake", err)
		}
		t.Cleanup(c.Close)
		return c
	}
	c := open()
	page := nativeTasks(t, c, TaskQuery{Limit: 10})
	if page.Total != 2 {
		t.Fatal("scoped tasks include global/foreign records")
	}
	for _, record := range page.Results {
		if record.IndexUID == nil || *record.IndexUID != uid {
			t.Fatal("foreign task exposed")
		}
	}
	if _, _, err := c.LookupTask(context.Background(), foreign.UID); !errors.Is(err, ErrTaskUnavailable) {
		t.Fatal("foreign task escaped configured index", err)
	}
	if _, _, err := c.ListTasks(context.Background(), TaskQuery{Limit: 1, IndexUIDs: []string{other}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("explicit original scope changed")
	}
	original, _, err := c.LookupTask(context.Background(), own.UID)
	if err != nil {
		t.Fatal("own native task unreadable", err)
	}
	for _, action := range []string{"cancel", "delete"} {
		client := open()
		_, response, err := client.ChangeTasks(context.Background(), action, []uint32{own.UID})
		var status *HTTPError
		var interrupted *Interrupted
		if !errors.As(err, &status) || status.Status != 403 || !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown || !bytes.Contains(response.Body, []byte("invalid_api_key")) {
			t.Fatal("task readonly permission bypassed", err)
		}
	}
	nativeRun(t, master, Request{Method: "DELETE", Path: "/keys/" + keyUID})
	_, response, err := c.ReadObservedTask(context.Background(), original)
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != 403 || errors.Is(err, ErrRetired) || len(response.Body) == 0 {
		t.Fatal("revoked task key used different credentials", err)
	}
	_, masterSecret := nativeFixture(t)
	bounded, err := Open(context.Background(), cfg, masterSecret)
	if err != nil {
		t.Fatal("native master original index binding", err)
	}
	defer bounded.Close()
	deletion := nativeTaskChange(t, bounded, "delete", []uint32{own.UID, foreign.UID})
	details := nativeTaskDetails(t, nativeWait(t, bounded, deletion))
	matched, e := nativeUint(details["matchedTasks"], 64)
	deleted, e2 := nativeUint(details["deletedTasks"], 64)
	if e != nil || e2 != nil || matched != 1 || deleted != 1 {
		t.Fatal("configured index mutation widened task targets")
	}
	if record, _, err := master.LookupTask(context.Background(), foreign.UID); err != nil || record.Status != "succeeded" {
		t.Fatal("index-filtered task deletion affected foreign index", err)
	}

	t.Log("original index-scoped task list/lookup, hidden foreign task, forbidden native cancel/delete and revoked key verified without master fallback")
}
