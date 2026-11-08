package meilisearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type namespaceMock struct {
	mu                           sync.Mutex
	indexes                      map[string]contract.NamespaceRecord
	action, fault, task, request string
	writes, calls                atomic.Int32
}

func namespaceMockLease(t *testing.T, action, fault string) (*Session, *taskDocument, *namespaceMock) {
	t.Helper()
	m := &namespaceMock{action: action, fault: fault, indexes: map[string]contract.NamespaceRecord{}}
	for i, uid := range []string{"owned", "other", "sibling"} {
		pk := "id"
		if i == 1 {
			pk = "sku"
		}
		m.indexes[uid] = contract.NamespaceRecord{Exists: true, CreatedAt: fmt.Sprintf("2026-11-08T08:00:0%dZ", i), UpdatedAt: "2026-11-08T08:01:00Z", PrimaryKey: pk, HasPrimaryKey: true}
	}
	if action == "namespace_create" {
		delete(m.indexes, "owned")
	}
	if action == "namespace_rename" {
		delete(m.indexes, "other")
	}
	c, server := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
		m.calls.Add(1)
		m.mu.Lock()
		defer m.mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/proxy/meili")
		if r.Method == "GET" {
			if strings.HasPrefix(path, "/tasks/") {
				if m.fault == "hidden" {
					w.WriteHeader(404)
					io.WriteString(w, `{"code":"task_not_found","type":"invalid_request","message":"hidden"}`)
					return
				}
				io.WriteString(w, m.task)
				return
			}
			uid := strings.TrimSuffix(strings.TrimPrefix(path, "/indexes/"), "/stats")
			index, ok := m.indexes[uid]
			if !ok {
				w.WriteHeader(404)
				io.WriteString(w, `{"code":"index_not_found","type":"invalid_request","message":"missing"}`)
				return
			}
			if strings.HasSuffix(path, "/stats") {
				fmt.Fprintf(w, `{"numberOfDocuments":%d,"isIndexing":false,"fieldDistribution":{"future":9007199254740993}}`, index.Documents)
				return
			}
			var pk any
			if index.HasPrimaryKey {
				pk = index.PrimaryKey
			}
			json.NewEncoder(w).Encode(map[string]any{"uid": uid, "createdAt": index.CreatedAt, "updatedAt": index.UpdatedAt, "primaryKey": pk, "future": json.RawMessage(`9007199254740993`)})
			return
		}
		m.writes.Add(1)
		body, _ := io.ReadAll(r.Body)
		m.request = r.Method + " " + path + "\n" + string(body)
		if m.fault == "lost" {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		if m.fault == "rejected" {
			w.WriteHeader(403)
			io.WriteString(w, `{"code":"invalid_api_key","type":"auth","message":"denied"}`)
			return
		}
		draft := contract.NamespaceDraft{Ref: contract.NamespaceRef{Catalog: "owned"}, Destination: contract.NamespaceRef{Catalog: "other"}, PrimaryKey: "new_id", PrimaryKeySet: true}
		_, kind, index, _ := namespaceRequest(action, draft)
		var indexValue any
		if index != "" {
			indexValue = index
		}
		details := map[string]any{}
		old := m.indexes["owned"]
		if fault != "failed" {
			switch action {
			case "namespace_create":
				m.indexes["owned"] = contract.NamespaceRecord{Exists: true, CreatedAt: "2026-11-09T01:00:00Z", UpdatedAt: "2026-11-09T01:00:00Z", HasPrimaryKey: true, PrimaryKey: "new_id"}
				details["primaryKey"] = "new_id"
			case "namespace_primary_key":
				old.PrimaryKey = "new_id"
				m.indexes["owned"] = old
				details["primaryKey"] = "new_id"
			case "namespace_rename":
				delete(m.indexes, "owned")
				m.indexes["other"] = old
				details["primaryKey"], details["oldIndexUid"], details["newIndexUid"] = nil, "owned", "other"
			case "namespace_swap":
				m.indexes["owned"], m.indexes["other"] = m.indexes["other"], old
				details["swaps"] = []map[string]any{{"indexes": []string{"owned", "other"}, "rename": false}}
			case "namespace_clear":
				details["deletedDocuments"] = old.Documents
				old.Documents = 0
				m.indexes["owned"] = old
			case "namespace_delete":
				details["deletedDocuments"] = old.Documents
				delete(m.indexes, "owned")
			}
		}
		status := "succeeded"
		if fault == "failed" {
			status = "failed"
		}
		if fault == "unverified" {
			delete(m.indexes, "owned")
		}
		if fault == "wrong_kind" {
			kind = "dumpCreation"
		}
		if fault == "wrong_scope" {
			indexValue = "sibling"
		}
		receipt := map[string]any{"taskUid": 0, "indexUid": indexValue, "type": kind, "status": "enqueued", "enqueuedAt": "2026-11-09T01:00:00Z"}
		raw, _ := json.Marshal(map[string]any{"uid": 0, "indexUid": indexValue, "type": kind, "status": status, "enqueuedAt": "2026-11-09T01:00:00Z", "details": details})
		m.task = string(raw)
		w.WriteHeader(202)
		json.NewEncoder(w).Encode(receipt)
	})
	c.Close()
	s := testSession(t, connections.MeilisearchConfig{Endpoint: server.URL + "/proxy/meili"}, "")
	lease, err := s.ReserveTaskDocument(s.Namespace("").Object("tasks", "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	d := lease.(*taskDocument)
	t.Cleanup(func() { d.Close() })
	return s, d, m
}
func namespacePlan(t *testing.T, d *taskDocument, action string) contract.TaskMutationPlan {
	t.Helper()
	draft := contract.NamespaceDraft{Ref: d.bound.s.Namespace("owned")}
	if action == "namespace_create" || action == "namespace_primary_key" {
		draft.PrimaryKeySet, draft.PrimaryKey = true, "new_id"
	}
	if action == "namespace_rename" || action == "namespace_swap" {
		draft.Destination = d.bound.s.Namespace("other")
	}
	before := []contract.NamespaceRecord{}
	for _, ref := range namespaceMutationRefs(&contract.NamespaceMutation{Draft: draft}) {
		r, err := d.ReadNamespace(context.Background(), ref)
		if err != nil {
			t.Fatal(err)
		}
		before = append(before, r)
	}
	p, err := d.PrepareNamespaceMutation(action, draft, before)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestNamespaceManagementSixActionsOriginalProofAdmissionReadbackRecovery(t *testing.T) {
	for _, action := range []string{"namespace_create", "namespace_primary_key", "namespace_rename", "namespace_swap", "namespace_clear", "namespace_delete"} {
		t.Run(action, func(t *testing.T) {
			s, d, m := namespaceMockLease(t, action, "")
			p := namespacePlan(t, d, action)
			calls := m.calls.Load()
			forged := contract.CloneTaskMutationPlan(p)
			forged.Namespace.Before[0].Documents++
			if _, err := d.MutateTasks(context.Background(), forged); !errors.Is(err, contract.ErrTaskMutationUsed) || m.calls.Load() != calls {
				t.Fatal("forged plan dispatched", err)
			}
			if _, err := d.PrepareNamespaceMutation(action, p.Namespace.Draft, p.Namespace.Before); err != nil || m.calls.Load() != calls {
				t.Fatal("pure preview did I/O", err)
			}
			p = namespacePlan(t, d, action)
			admitted, err := d.MutateTasks(context.Background(), p)
			if err != nil || admitted.State != "enqueued" || admitted.NamespaceVerified || admitted.Receipt.Namespace == nil || m.writes.Load() != 1 {
				t.Fatal("admission lost", err, admitted)
			}
			if _, err = s.ReserveTaskDocument(s.Namespace("owned").Object("tasks", "tasks")); !errors.Is(err, contract.ErrNamespaceBusy) {
				t.Fatal("pending namespace task lease opened", err)
			}
			if _, err = s.ReserveDataDocument(s.Namespace("owned").Object("documents", "documents")); !errors.Is(err, contract.ErrNamespaceBusy) {
				t.Fatal("pending data lease opened", err)
			}
			calls = m.calls.Load()
			recovery, err := s.RecoverTaskDocument(d.object, d)
			if err != nil || recovery.Namespace == nil || m.calls.Load() != calls {
				t.Fatal("claim recovery failed", err)
			}
			defer recovery.Lease.Close()
			next := recovery.Lease.(*taskDocument)
			if _, err = next.ObserveTaskMutation(context.Background(), admitted.Receipt, time.Second); !errors.Is(err, ErrInvalid) {
				t.Fatal("old receipt accepted", err)
			}
			settled, err := next.ObserveTaskMutation(context.Background(), recovery.Receipt, time.Second)
			if err != nil || settled.State != "succeeded" || !settled.NamespaceVerified || m.writes.Load() != 1 {
				t.Fatal("completion readback failed", err, settled)
			}
			lease, err := s.ReserveTaskDocument(s.Namespace("owned").Object("tasks", "tasks"))
			if err != nil {
				t.Fatal("settled claim retained", err)
			}
			lease.Close()
		})
	}
}
func TestNamespaceManagementPreflightLeasesVersionsAndScope(t *testing.T) {
	for _, obstacle := range []string{"data", "settings", "tasks", "changed", "destination", "epoch"} {
		t.Run(obstacle, func(t *testing.T) {
			action := "namespace_rename"
			s, d, m := namespaceMockLease(t, action, "")
			p := namespacePlan(t, d, action)
			want := contract.ErrNamespaceInUse
			switch obstacle {
			case "data":
				l, e := s.ReserveDataDocument(s.Namespace("owned").Object("documents", "documents"))
				if e != nil {
					t.Fatal(e)
				}
				defer l.Close()
			case "settings":
				l, e := s.ReserveSettingsDocument(s.Namespace("owned").Object("settings", "settings"))
				if e != nil {
					t.Fatal(e)
				}
				defer l.Close()
			case "tasks":
				l, e := s.ReserveTaskDocument(s.Namespace("owned").Object("tasks", "tasks"))
				if e != nil {
					t.Fatal(e)
				}
				defer l.Close()
			case "changed":
				m.mu.Lock()
				a := m.indexes["owned"]
				a.Documents = 1
				m.indexes["owned"] = a
				m.mu.Unlock()
				want = contract.ErrNamespaceChanged
			case "destination":
				m.mu.Lock()
				m.indexes["other"] = m.indexes["sibling"]
				m.mu.Unlock()
				want = contract.ErrNamespaceChanged
			case "epoch":
				s.mu.Lock()
				s.namespaceScopes = map[string]namespaceScope{"owned": {version: 1}}
				s.mu.Unlock()
				want = contract.ErrNamespaceChanged
			}
			got, err := d.MutateTasks(context.Background(), p)
			if !errors.Is(err, want) || got.State != "not_submitted" || m.writes.Load() != 0 {
				t.Fatal("unsafe preflight dispatch", err, got)
			}
			if _, owner := s.namespaceVersion("owned"); owner != "" {
				t.Fatal("known preflight retains claim")
			}
		})
	}
	s, d, _ := namespaceMockLease(t, "namespace_primary_key", "")
	scoped, e := s.ReserveTaskDocument(s.Namespace("owned").Object("tasks", "tasks"))
	if e != nil {
		t.Fatal(e)
	}
	defer scoped.Close()
	l := scoped.(*taskDocument)
	for _, a := range []string{"namespace_create", "namespace_rename", "namespace_swap", "namespace_delete"} {
		if l.SupportsNamespaceManagement(a) {
			t.Fatal("global operation exposed on index scope")
		}
	}
	if _, e = l.ReadNamespace(context.Background(), s.Namespace("other")); !errors.Is(e, ErrInvalid) {
		t.Fatal("scope escaped", e)
	}
	p := namespacePlan(t, d, "namespace_primary_key")
	bad := p.Namespace.Before[0]
	bad.PrimaryKey = "forged"
	if _, e = d.PrepareNamespaceMutation(p.Action, p.Namespace.Draft, []contract.NamespaceRecord{bad}); e == nil {
		t.Fatal("forged namespace read")
	}
	draft := p.Namespace.Draft
	draft.PrimaryKey = ""
	draft.PrimaryKeySet = false
	if _, e = d.PrepareNamespaceMutation(p.Action, draft, p.Namespace.Before); e == nil {
		t.Fatal("null primary key removal offered")
	}
}
func TestNamespaceManagementUncertainUnverifiedFailedAndRejectedClaims(t *testing.T) {
	for _, fault := range []string{"lost", "wrong_kind", "wrong_scope", "hidden", "unverified", "failed", "rejected"} {
		t.Run(fault, func(t *testing.T) {
			s, d, m := namespaceMockLease(t, "namespace_create", fault)
			p := namespacePlan(t, d, "namespace_create")
			got, err := d.MutateTasks(context.Background(), p)
			uncertain := fault == "lost" || fault == "wrong_kind" || fault == "wrong_scope"
			if uncertain && (err == nil || got.State != "uncertain" || got.Receipt.Raw != "") {
				t.Fatal("unknown admission mislabeled", err, got)
			}
			if fault == "rejected" && (err == nil || got.State != "rejected") {
				t.Fatal("native rejection lost", err)
			}
			if got.Receipt.Raw != "" {
				observed, e := d.ObserveTaskMutation(context.Background(), got.Receipt, time.Second)
				if fault == "hidden" && !errors.Is(e, contract.ErrTaskMutationUnavailable) {
					t.Fatal(e)
				}
				if fault == "unverified" && (!errors.Is(e, contract.ErrNamespaceUnverified) || observed.NamespaceVerified) {
					t.Fatal("unverified success accepted", e)
				}
				if fault == "failed" && (e != nil || observed.State != "failed" || observed.NamespaceVerified) {
					t.Fatal("failure verified", e)
				}
			}
			pending := uncertain || fault == "hidden" || fault == "unverified"
			_, owner := s.namespaceVersion("owned")
			if (owner != "") != pending {
				t.Fatal("claim lifetime", fault, owner)
			}
			calls := m.calls.Load()
			r, e := s.RecoverTaskDocument(d.object, d)
			if e != nil || r.Uncertain != uncertain || m.calls.Load() != calls || (r.Namespace != nil) != pending {
				t.Fatal("recovery lost unresolved scope", e)
			}
			defer r.Lease.Close()
			if pending {
				if _, e = r.Lease.(*taskDocument).PrepareServerBackup("dump"); !errors.Is(e, contract.ErrTaskMutationUsed) {
					t.Fatal("unresolved operation permitted replay", e)
				}
			}
		})
	}
}
