package meilisearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func mockCatalog(tasks []string, limit int, from, next any) string {
	results := make([]any, len(tasks))
	for i, raw := range tasks {
		var f any
		if decodeJSON([]byte(raw), &f) != nil {
			panic("invalid fixture")
		}
		results[i] = f
	}
	return string(mustJSON(map[string]any{"results": results, "total": len(tasks), "limit": limit, "from": from, "next": next, "future": map[string]any{"integer": 9007199254740993}}))
}
func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

func TestMeilisearchTaskQueryBoundsEncodingAndExplicitScope(t *testing.T) {
	c, _ := mockOpen(t, func(http.ResponseWriter, *http.Request) { t.Fatal("pure validation dispatched") })
	zero := uint32(0)
	q := TaskQuery{Limit: 3, From: &zero, Reverse: true, UIDs: []uint32{0, 4294967295}, BatchUIDs: []uint32{1}, CanceledBy: []uint32{2}, IndexUIDs: []string{"owned"}, Statuses: []string{"canceled", "failed"}, Types: []string{"documentAdditionOrUpdate", "futureType"}, AfterEnqueuedAt: "2026-11-08T08:05:30.123+08:00"}
	path, err := c.taskQuery(q)
	u, _ := url.Parse(path)
	v := u.Query()
	if err != nil || v.Get("from") != "0" || v.Get("uids") != "0,4294967295" || v.Get("afterEnqueuedAt") != q.AfterEnqueuedAt || v.Get("reverse") != "true" {
		t.Fatal("native query precision or encoding lost", err)
	}
	for _, invalid := range []TaskQuery{{}, {Limit: 1001}, {Limit: -1}, {Limit: 1, UIDs: []uint32{0, 0}}, {Limit: 1, IndexUIDs: []string{"*"}}, {Limit: 1, IndexUIDs: []string{"../escape"}}, {Limit: 1, Statuses: []string{"succeeded,failed"}}, {Limit: 1, Types: []string{"bad&type"}}, {Limit: 1, AfterFinishedAt: "yesterday"}, {Limit: 1, BeforeStartedAt: "2026-11-08"}, {Limit: 1, UIDs: make([]uint32, 257)}} {
		if _, _, err := c.ListTasks(context.Background(), invalid); err == nil {
			t.Fatal("invalid/unbounded query accepted")
		}
	}
	c.config.IndexUID = "owned"
	path, err = c.taskQuery(TaskQuery{Limit: 1})
	u, _ = url.Parse(path)
	if err != nil || u.Query().Get("indexUids") != "owned" {
		t.Fatal("original index scope omitted")
	}
	for _, indexes := range [][]string{{"other"}, {"owned", "other"}} {
		if _, err := c.taskQuery(TaskQuery{Limit: 1, IndexUIDs: indexes}); !errors.Is(err, ErrInvalid) {
			t.Fatal("scope escaped")
		}
	}
}

func TestMeilisearchTaskCatalogRejectsMalformedPagesAndPreservesEvidence(t *testing.T) {
	raw := mockTask("failed")
	valid := mockCatalog([]string{raw}, 1, 0, nil)
	for _, change := range []func(string) string{
		func(s string) string { return strings.Replace(s, `"from":0`, `"from":null`, 1) },
		func(s string) string { return strings.Replace(s, `"next":null`, `"next":0`, 1) },
		func(s string) string { return strings.Replace(s, `"limit":1`, `"limit":2`, 1) },
		func(s string) string { return strings.Replace(s, `"total":1`, `"total":0`, 1) },
		func(s string) string { return strings.Replace(s, `"uid":0`, `"uid":1.0`, 1) },
		func(s string) string { return strings.Replace(s, `"from":0`, `"from":4294967296`, 1) },
		func(s string) string { return strings.Replace(s, `"status":"failed"`, `"status":"future"`, 1) },
		func(s string) string { return strings.Replace(s, `"total":1`, `"total":1,"total":1`, 1) },
		func(s string) string { return strings.Replace(s, `"results":[`, `"results":[`+raw+`,`, 1) },
	} {
		responseRaw := change(valid)
		c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, responseRaw) })
		page, response, err := c.ListTasks(context.Background(), TaskQuery{Limit: 1})
		if !errors.Is(err, ErrProtocol) || len(page.Results) != 0 || string(page.Raw) != responseRaw || string(response.Body) != responseRaw {
			t.Fatal("malformed task evidence lost or published", err)
		}
	}
	for _, query := range []TaskQuery{{Limit: 1, UIDs: []uint32{1}}, {Limit: 1, IndexUIDs: []string{"other"}}, {Limit: 1, Statuses: []string{"succeeded"}}, {Limit: 1, Types: []string{"indexCreation"}}} {
		c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, valid) })
		if _, _, err := c.ListTasks(context.Background(), query); !errors.Is(err, ErrProtocol) {
			t.Fatal("out-of-filter record published")
		}
	}
}

func TestMeilisearchObservedTaskOriginalClientTupleAndEvidence(t *testing.T) {
	var calls atomic.Int32
	current := mockTask("processing")
	c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, current) })
	record, response, err := c.LookupTask(context.Background(), 0)
	if err != nil || record.Status != "processing" || !bytes.Equal(record.Raw, response.Body) {
		t.Fatal("lookup evidence lost", err)
	}
	response.Body[0] = ' '
	if record.Raw[0] != '{' {
		t.Fatal("task shares response bytes")
	}
	for _, edit := range []func(*TaskRecord){func(r *TaskRecord) { r.UID++ }, func(r *TaskRecord) { r.Type = "indexCreation" }, func(r *TaskRecord) { r.EnqueuedAt = "2026-11-08T08:05:31Z" }, func(r *TaskRecord) { r.Status = "succeeded" }, func(r *TaskRecord) { r.Raw = bytes.Clone(r.Raw); r.Raw[0] = ' ' }, func(r *TaskRecord) { s := "replacement"; r.IndexUID = &s }} {
		changed := record
		edit(&changed)
		if _, _, err := c.ReadObservedTask(context.Background(), changed); !errors.Is(err, ErrInvalid) || calls.Load() != 1 {
			t.Fatal("edited original evidence dispatched", err)
		}
	}
	other, _ := mockOpen(t, func(http.ResponseWriter, *http.Request) { t.Fatal("foreign observation dispatched") })
	if _, _, err := other.ReadObservedTask(context.Background(), record); !errors.Is(err, ErrInvalid) {
		t.Fatal("cross-client observation accepted")
	}
	current = mockTask("succeeded")
	refreshed, _, err := c.ReadObservedTask(context.Background(), record)
	if err != nil || refreshed.Status != "succeeded" || record.Status != "processing" {
		t.Fatal("legitimate status transition rejected", err)
	}
	current = strings.Replace(current, "08:05:30", "08:05:31", 1)
	changed, raw, err := c.ReadObservedTask(context.Background(), refreshed)
	if !errors.Is(err, ErrTaskChanged) || string(changed.Raw) != current || string(raw.Body) != current {
		t.Fatal("replacement task observation accepted", err)
	}
	if _, _, err := c.LookupTask(context.Background(), 0); !errors.Is(err, ErrRetired) || calls.Load() != 3 {
		t.Fatal("replacement connection reused")
	}
}

func TestMeilisearchTaskChangesExactUIDsNative200AndNoReplay(t *testing.T) {
	for _, action := range []string{"cancel", "delete"} {
		kind, method, path := "taskCancelation", "POST", "/tasks/cancel"
		if action == "delete" {
			kind, method, path = "taskDeletion", "DELETE", "/tasks"
		}
		for _, mode := range []string{"valid", "wrong_status", "wrong_type", "wrong_index", "wrong_body"} {
			var calls atomic.Int32
			c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != method || r.URL.Path != "/proxy/meili"+path || r.URL.Query().Get("uids") != "0,4294967295" || r.URL.Query().Get("indexUids") != "owned" {
					t.Fatal("mutation target widened")
				}
				raw := strings.Replace(mockReceipt, "documentAdditionOrUpdate", kind, 1)
				raw = strings.Replace(raw, `"indexUid":"owned"`, `"indexUid":null`, 1)
				switch mode {
				case "wrong_type":
					raw = strings.Replace(raw, kind, "indexCreation", 1)
				case "wrong_index":
					raw = strings.Replace(raw, `"indexUid":null`, `"indexUid":"owned"`, 1)
				case "wrong_body":
					raw = `{"status":"enqueued"}`
				}
				status := 200
				if mode == "wrong_status" {
					status = 202
				}
				w.WriteHeader(status)
				_, _ = io.WriteString(w, raw)
			})
			c.config.IndexUID = "owned"
			receipt, response, err := c.ChangeTasks(context.Background(), action, []uint32{0, 4294967295})
			if mode == "valid" {
				if err != nil || receipt.Type != kind || response.Status != 200 {
					t.Fatal("native HTTP200 admission rejected", err)
				}
				continue
			}
			var interrupted *Interrupted
			if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown || len(response.Body) == 0 {
				t.Fatal("unverified task admission accepted", err)
			}
			if _, _, err := c.ChangeTasks(context.Background(), action, []uint32{0, 4294967295}); !errors.Is(err, ErrRetired) || calls.Load() != 1 {
				t.Fatal("uncertain task operation replayed")
			}
		}
	}
	c, _ := mockOpen(t, func(http.ResponseWriter, *http.Request) { t.Fatal("invalid mutation dispatched") })
	for _, ids := range [][]uint32{nil, {}, {1, 1}, make([]uint32, 257)} {
		if _, _, err := c.ChangeTasks(context.Background(), "delete", ids); err == nil {
			t.Fatal("unbounded/duplicate task deletion accepted")
		}
	}
	if _, _, err := c.ChangeTasks(context.Background(), "all", []uint32{1}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown action accepted")
	}
}
