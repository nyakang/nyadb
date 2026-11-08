package meilisearch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type taskManagementMock struct {
	hidden                    bool
	mu                        sync.Mutex
	status, response, request string
	calls, writes             atomic.Int32
	fault                     bool
}

func taskManagementMockLease(t *testing.T) (*Session, *taskDocument, *taskManagementMock) {
	t.Helper()
	m := &taskManagementMock{status: "processing"}
	c, server := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
		m.calls.Add(1)
		m.mu.Lock()
		defer m.mu.Unlock()
		if r.Method != "GET" {
			m.writes.Add(1)
			m.request = r.Method + " " + r.URL.RequestURI()
			if m.fault {
				conn, _, _ := w.(http.Hijacker).Hijack()
				_ = conn.Close()
				return
			}
			if m.response != "" {
				w.WriteHeader(403)
				_, _ = io.WriteString(w, m.response)
				return
			}
			raw := strings.ReplaceAll(mockReceipt, `"indexUid":"owned"`, `"indexUid":null`)
			kind := "taskCancelation"
			if r.Method == "DELETE" {
				kind = "taskDeletion"
			}
			_, _ = io.WriteString(w, strings.ReplaceAll(raw, "documentAdditionOrUpdate", kind))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/tasks/0") {
			if m.hidden {
				w.WriteHeader(404)
				_, _ = io.WriteString(w, `{"message":"Task not found","code":"task_not_found","type":"invalid_request"}`)
				return
			}
			raw := strings.ReplaceAll(taskDocumentFixtureRaw(0, "succeeded"), `"indexUid":"owned"`, `"indexUid":null`)
			kind := "taskCancelation"
			key := "canceledTasks"
			if strings.HasPrefix(m.request, "DELETE") {
				kind = "taskDeletion"
				key = "deletedTasks"
			}
			raw = strings.ReplaceAll(raw, "documentAdditionOrUpdate", kind)
			raw = strings.ReplaceAll(raw, `"details":{"future":9007199254740993,"text":"原生🐱"}`, `"details":{"matchedTasks":1,"`+key+`":1,"future":9007199254740993}`)
			_, _ = io.WriteString(w, raw)
			return
		}
		raw := taskDocumentFixtureRaw(1, m.status)
		body, _ := json.Marshal(map[string]any{"results": []json.RawMessage{json.RawMessage(raw)}, "total": 1, "limit": 1, "from": 1, "next": nil})
		_, _ = w.Write(body)
	})
	c.Close()
	s := testSession(t, connections.MeilisearchConfig{Endpoint: server.URL + "/proxy/meili"}, "")
	lease, err := s.ReserveTaskDocument(s.Namespace("owned").Object("tasks", "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	d := lease.(*taskDocument)
	t.Cleanup(func() { _ = d.Close() })
	return s, d, m
}
func taskManagementOriginal(t *testing.T, d *taskDocument) contract.TaskRecord {
	t.Helper()
	page, err := d.List(context.Background(), contract.TaskQuery{Limit: 1})
	if err != nil || len(page.Results) != 1 {
		t.Fatal(err)
	}
	return page.Results[0]
}
func TestMeilisearchTaskManagementProofsFiniteScopeOneUseAndRecovery(t *testing.T) {
	for _, action := range []string{"cancel", "delete"} {
		t.Run(action, func(t *testing.T) {
			s, d, m := taskManagementMockLease(t)
			if action == "delete" {
				m.mu.Lock()
				m.status = "succeeded"
				m.mu.Unlock()
			}
			record := taskManagementOriginal(t, d)
			before := m.calls.Load()
			for _, records := range [][]contract.TaskRecord{nil, {record, record}, make([]contract.TaskRecord, 257)} {
				if _, err := d.PrepareTaskMutation(action, records); err == nil {
					t.Fatal("unbounded or duplicate plan")
				}
			}
			forged := record
			forged.UID = "2"
			if _, err := d.PrepareTaskMutation(action, []contract.TaskRecord{forged}); err == nil {
				t.Fatal("forged task accepted")
			}
			plan, err := d.PrepareTaskMutation(action, []contract.TaskRecord{record})
			if err != nil || m.calls.Load() != before {
				t.Fatal("preview performed I/O", err)
			}
			other, err := s.ReserveTaskDocument(d.object)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			if _, err = other.(contract.TaskManagementLease).MutateTasks(context.Background(), plan); !errors.Is(err, contract.ErrTaskMutationUsed) {
				t.Fatal("foreign lease accepted plan", err)
			}
			changed := plan
			changed.Records = slices.Clone(plan.Records)
			changed.Records[0].UID = "2"
			if _, err = d.MutateTasks(context.Background(), changed); !errors.Is(err, contract.ErrTaskMutationUsed) || m.calls.Load() != before {
				t.Fatal("changed plan dispatched", err)
			}
			replacement, err := d.PrepareTaskMutation(action, []contract.TaskRecord{record})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.MutateTasks(context.Background(), plan); !errors.Is(err, contract.ErrTaskMutationUsed) {
				t.Fatal("replaced preview accepted", err)
			}
			result, err := d.MutateTasks(context.Background(), replacement)
			if err != nil || result.State != "enqueued" || result.CountsKnown || result.Receipt.UID != "0" || m.writes.Load() != 1 {
				t.Fatal("admission presented as completion", err, result)
			}
			if !strings.Contains(result.Request, "uids=1") || !strings.Contains(result.Request, "indexUids=owned") || len(result.Targets) != 1 {
				t.Fatal("finite preview lost", result)
			}
			if _, err = d.MutateTasks(context.Background(), replacement); !errors.Is(err, contract.ErrTaskMutationUsed) || m.writes.Load() != 1 {
				t.Fatal("preview replayed", err)
			}
			if _, err = d.PrepareTaskMutation(action, []contract.TaskRecord{record}); !errors.Is(err, contract.ErrTaskMutationUsed) {
				t.Fatal("new operation before observation", err)
			}
			before = m.calls.Load()
			recovered, err := s.RecoverTaskDocument(d.object, d)
			if err != nil || m.calls.Load() != before || recovered.Receipt.Raw != result.Receipt.Raw || recovered.Receipt.Token == result.Receipt.Token {
				t.Fatal("recovery lost evidence or performed I/O", err)
			}
			next := recovered.Lease.(contract.TaskManagementLease)
			defer recovered.Lease.Close()
			if _, err = next.ObserveTaskMutation(context.Background(), result.Receipt, time.Second); !errors.Is(err, ErrInvalid) || m.calls.Load() != before {
				t.Fatal("old proof followed recovered lease", err)
			}
			settled, err := next.ObserveTaskMutation(context.Background(), recovered.Receipt, time.Second)
			if err != nil || settled.State != "succeeded" || !settled.CountsKnown || settled.Matched != 1 || settled.Changed != 1 || !strings.Contains(settled.TaskRaw, "9007199254740993") || m.writes.Load() != 1 {
				t.Fatal("original result/counts lost", err, settled)
			}
		})
	}
}
func TestMeilisearchTaskManagementStateRecheckAndUnknownSubmission(t *testing.T) {
	for _, fault := range []string{"state", "rejected", "unknown", "foreign_error"} {
		t.Run(fault, func(t *testing.T) {
			s, d, m := taskManagementMockLease(t)
			record := taskManagementOriginal(t, d)
			plan, err := d.PrepareTaskMutation("cancel", []contract.TaskRecord{record})
			if err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			switch fault {
			case "state":
				m.status = "succeeded"
			case "rejected":
				m.response = `{"code":"invalid_api_key","type":"auth","message":"denied"}`
			case "unknown":
				m.fault = true
			case "foreign_error":
				m.response = `{"code":"proxy_failure","type":"auth","message":"unknown"}`
			}
			m.mu.Unlock()
			result, err := d.MutateTasks(context.Background(), plan)
			if err == nil {
				t.Fatal("fault did not surface")
			}
			if fault == "state" {
				if !errors.Is(err, contract.ErrTaskMutationState) || result.State != "not_submitted" || m.writes.Load() != 0 {
					t.Fatal("ineligible task submitted", err)
				}
				return
			}
			expected := "uncertain"
			if fault == "rejected" {
				expected = "rejected"
			}
			if result.State != expected || result.Request == "" || result.Targets[0] != "1" || m.writes.Load() != 1 {
				t.Fatal("submission evidence mislabeled", result, err)
			}
			before := m.calls.Load()
			recovered, err := s.RecoverTaskDocument(d.object, d)
			if err != nil || m.calls.Load() != before || recovered.Uncertain != (expected == "uncertain") {
				t.Fatal("recovery lost uncertainty", err)
			}
			defer recovered.Lease.Close()
			if expected == "uncertain" {
				if _, err = recovered.Lease.(contract.TaskManagementLease).PrepareTaskMutation("cancel", []contract.TaskRecord{record}); !errors.Is(err, contract.ErrTaskMutationUsed) || m.writes.Load() != 1 {
					t.Fatal("unknown submission became retry", err)
				}
			}
		})
	}
}

func TestMeilisearchTaskManagementHiddenOriginalTaskRetainsAdmission(t *testing.T) {
	s, d, m := taskManagementMockLease(t)
	record := taskManagementOriginal(t, d)
	plan, err := d.PrepareTaskMutation("cancel", []contract.TaskRecord{record})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := d.MutateTasks(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.hidden = true
	m.mu.Unlock()
	observed, err := d.ObserveTaskMutation(context.Background(), admitted.Receipt, time.Second)
	if !errors.Is(err, contract.ErrTaskMutationUnavailable) || observed.State != "enqueued" || observed.CountsKnown || observed.Receipt.Raw != admitted.Receipt.Raw || !strings.Contains(observed.TaskRaw, "task_not_found") {
		t.Fatal("hidden task became success or failure", err, observed)
	}
	if _, err = d.PrepareTaskMutation("cancel", []contract.TaskRecord{record}); !errors.Is(err, contract.ErrTaskMutationUsed) {
		t.Fatal("hidden original receipt permitted new mutation", err)
	}
	recovery, err := s.RecoverTaskDocument(d.object, d)
	if err != nil || recovery.Receipt.Raw != admitted.Receipt.Raw || m.writes.Load() != 1 {
		t.Fatal("recovery discarded hidden receipt or replayed", err)
	}
	defer recovery.Lease.Close()
}
