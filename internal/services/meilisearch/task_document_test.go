package meilisearch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func taskDocumentFixtureRaw(uid uint32, status string) string {
	raw := strings.Replace(mockTask(status), `"uid":0`, `"uid":`+strconv.FormatUint(uint64(uid), 10), 1)
	return strings.TrimSuffix(raw, "}") + `,"startedAt":"2026-11-08T08:05:30.4Z","finishedAt":null,"duration":null,"details":{"future":9007199254740993,"text":"原生🐱"},"error":null}`
}
func taskDocumentMock(t *testing.T) (*Session, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	c, server := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.Contains(r.URL.Path, "/tasks/") {
			_, _ = io.WriteString(w, taskDocumentFixtureRaw(1, "processing"))
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/tasks") {
			t.Error("unexpected task request")
			w.WriteHeader(400)
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		uid := uint32(2)
		if from := r.URL.Query().Get("from"); from != "" {
			n, _ := strconv.ParseUint(from, 10, 32)
			uid = uint32(n)
		}
		if ids := r.URL.Query().Get("uids"); ids != "" {
			n, _ := strconv.ParseUint(ids, 10, 32)
			uid = uint32(n)
		}
		var next any
		if uid > 0 {
			next = uid - 1
		}
		raw := json.RawMessage(taskDocumentFixtureRaw(uid, "processing"))
		body, _ := json.Marshal(map[string]any{"results": []json.RawMessage{raw}, "total": 3, "limit": limit, "from": uid, "next": next})
		if r.URL.Query().Get("uids") != "" {
			body, _ = json.Marshal(map[string]any{"results": []json.RawMessage{raw}, "total": 1, "limit": limit, "from": uid, "next": nil})
		}
		_, _ = w.Write(body)
	})
	c.Close()
	s := testSession(t, connections.MeilisearchConfig{Endpoint: server.URL + "/proxy/meili"}, "")
	return s, calls
}
func TestMeilisearchTaskDocumentReservationCursorsAndOriginalProofs(t *testing.T) {
	s, calls := taskDocumentMock(t)
	scope := s.Namespace("owned").Object("tasks", "tasks")
	lease, err := s.ReserveTaskDocument(scope)
	if err != nil || calls.Load() != 0 {
		t.Fatal("reservation performed I/O", err)
	}
	defer lease.Close()
	first, err := lease.List(context.Background(), contract.TaskQuery{Limit: 1})
	if err != nil || len(first.Results) != 1 || first.Next == "" || first.Results[0].UID != "2" || first.Results[0].Index != "owned" || !strings.Contains(first.Results[0].Details, "9007199254740993") {
		t.Fatal("task lease source/precision changed", err)
	}
	before := calls.Load()
	for _, changed := range []contract.TaskQuery{{Limit: 2, From: first.Next}, {Limit: 1, From: first.Next, Reverse: true}, {Limit: 1, From: first.Next, Statuses: []string{"failed"}}, {Limit: 1, From: first.Next, Indexes: []string{"other"}}, {Limit: 1, From: "forged"}, {Limit: 1, UIDs: []string{"01"}}, {Limit: 1, UIDs: []string{"4294967296"}}} {
		if _, err := lease.List(context.Background(), changed); err == nil || calls.Load() != before {
			t.Fatal("changed/foreign query dispatched", err)
		}
	}
	second, err := lease.List(context.Background(), contract.TaskQuery{Limit: 1, From: first.Next})
	if err != nil || second.Results[0].UID != "1" {
		t.Fatal("opaque original next cursor lost", err)
	}
	record, err := lease.Read(context.Background(), second.Results[0])
	if err != nil || record.UID != "1" || record.Raw != second.Results[0].Raw {
		t.Fatal("original signed task read changed", err)
	}
	other, err := s.ReserveTaskDocument(scope)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	before = calls.Load()
	if _, err := other.Read(context.Background(), record); !errors.Is(err, ErrInvalid) || calls.Load() != before {
		t.Fatal("foreign task evidence opened a client", err)
	}
	for _, mutate := range []func(*contract.TaskRecord){func(r *contract.TaskRecord) { r.UID = "99" }, func(r *contract.TaskRecord) { r.Index = "other" }, func(r *contract.TaskRecord) { r.Status = "succeeded" }, func(r *contract.TaskRecord) {
		r.Raw = strings.Replace(r.Raw, "9007199254740993", "9007199254740992", 1)
	}, func(r *contract.TaskRecord) { r.Details = "{}" }, func(r *contract.TaskRecord) { r.Token += "A" }} {
		changed := record
		mutate(&changed)
		if _, err := lease.Read(context.Background(), changed); !errors.Is(err, ErrInvalid) || calls.Load() != before {
			t.Fatal("edited original task dispatched", err)
		}
	}
	if _, err := other.List(context.Background(), contract.TaskQuery{Limit: 1, From: first.Next}); !errors.Is(err, ErrInvalid) || calls.Load() != before {
		t.Fatal("cursor escaped original lease")
	}
	last, err := lease.Observe(context.Background(), record, 250*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || last.UID != "1" {
		t.Fatal("bounded observation lost last native record", err)
	}
	if _, err := lease.Read(context.Background(), record); err != nil {
		t.Fatal("idle observer timeout retired healthy task client", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := lease.List(context.Background(), contract.TaskQuery{Limit: 1}); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed original session reused", err)
	}
}

func TestMeilisearchTaskDocumentChangedNativeIdentityRetiresWithoutReplay(t *testing.T) {
	for _, index := range []string{"", "owned"} {
		t.Run("scope="+index, func(t *testing.T) {
			var changed atomic.Bool
			var calls atomic.Int32
			original := taskDocumentFixtureRaw(1, "processing")
			replacement := strings.ReplaceAll(original, "2026-11-08", "2026-11-09")
			c, server := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				raw := original
				if changed.Load() {
					raw = replacement
				}
				if strings.HasSuffix(r.URL.Path, "/tasks") {
					body, _ := json.Marshal(map[string]any{"results": []json.RawMessage{json.RawMessage(raw)}, "total": 1, "limit": 1, "from": 1, "next": nil})
					_, _ = w.Write(body)
				} else {
					_, _ = io.WriteString(w, raw)
				}
			})
			c.Close()
			s := testSession(t, connections.MeilisearchConfig{Endpoint: server.URL + "/proxy/meili"}, "")
			lease, err := s.ReserveTaskDocument(s.Namespace(index).Object("tasks", "tasks"))
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			page, err := lease.List(context.Background(), contract.TaskQuery{Limit: 1})
			if err != nil {
				t.Fatal(err)
			}
			changed.Store(true)
			_, err = lease.Read(context.Background(), page.Results[0])
			var diagnostic *contract.TaskRequestError
			if !errors.Is(err, contract.ErrTaskLeaseLost) || !errors.Is(err, ErrTaskChanged) || !errors.As(err, &diagnostic) || !strings.Contains(diagnostic.Raw, "2026-11-09") {
				t.Fatal("changed native task did not retain evidence and retire original lease", err)
			}
			before := calls.Load()
			if _, err = lease.List(context.Background(), contract.TaskQuery{Limit: 1}); !errors.Is(err, contract.ErrTaskLeaseLost) || calls.Load() != before {
				t.Fatal("changed task identity replayed native read", err)
			}
		})
	}
}
