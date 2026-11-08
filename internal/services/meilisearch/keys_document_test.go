package meilisearch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const testKeyUID = "be602e9b-f9d9-4fa7-844a-4372640abf83"
const testOtherKeyUID = "b6de1f51-efbf-4bb9-986f-ff688b313498"

type keyMock struct {
	mu                                                             sync.Mutex
	name, description, created, updated, secret, request, response string
	deleted, fault                                                 bool
	responseStatus                                                 int
	calls, writes                                                  atomic.Int32
}

func (m *keyMock) raw(uid string) []byte {
	raw, _ := json.Marshal(map[string]any{"uid": uid, "key": m.secret, "name": m.name, "description": m.description, "createdAt": m.created, "updatedAt": m.updated, "expiresAt": nil, "actions": []string{"search"}, "indexes": []string{"owned*"}, "future": map[string]any{"exact": json.Number("9007199254740993"), "text": "原生🐱", "echo": m.secret}})
	return raw
}
func keyMockLease(t *testing.T) (*Session, *keysDocument, *keyMock) {
	t.Helper()
	m := &keyMock{name: "owned", description: "description", created: "2026-11-08T08:05:30.372437829Z", updated: "2026-11-08T08:05:30.372437829Z", secret: strings.Repeat("a", 64)}
	c, server := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
		m.calls.Add(1)
		m.mu.Lock()
		defer m.mu.Unlock()
		if r.Method != "GET" {
			m.writes.Add(1)
			body, _ := io.ReadAll(r.Body)
			m.request = r.Method + " " + r.URL.RequestURI() + "\n" + string(body)
			if m.fault {
				conn, _, _ := w.(http.Hijacker).Hijack()
				_ = conn.Close()
				return
			}
			if m.response != "" {
				status := m.responseStatus
				if status == 0 {
					status = 409
				}
				w.WriteHeader(status)
				_, _ = io.WriteString(w, m.response)
				return
			}
			if r.Method == "DELETE" {
				m.deleted = true
				w.WriteHeader(204)
				return
			}
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(body, &fields)
			_ = json.Unmarshal(fields["name"], &m.name)
			_ = json.Unmarshal(fields["description"], &m.description)
			m.updated = "2026-11-08T08:06:30.372437829Z"
			if r.Method == "POST" {
				w.WriteHeader(201)
				_, _ = w.Write(m.raw(testOtherKeyUID))
				return
			}
			_, _ = w.Write(m.raw(testKeyUID))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/keys") {
			rows := []json.RawMessage{}
			if !m.deleted {
				rows = append(rows, json.RawMessage(m.raw(testKeyUID)))
			}
			offset, _ := strconv.ParseUint(r.URL.Query().Get("offset"), 10, 64)
			limit, _ := strconv.ParseUint(r.URL.Query().Get("limit"), 10, 64)
			raw, _ := json.Marshal(map[string]any{"results": rows, "offset": offset, "limit": limit, "total": len(rows)})
			_, _ = w.Write(raw)
			return
		}
		if m.deleted {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"code":"api_key_not_found","type":"invalid_request","message":"gone"}`)
			return
		}
		_, _ = w.Write(m.raw(testKeyUID))
	})
	c.Close()
	s := testSession(t, connections.MeilisearchConfig{Endpoint: server.URL + "/proxy/meili"}, "")
	lease, err := s.ReserveKeysDocument(contract.ObjectRef{ProfileID: s.ProfileID(), Kind: "keys", Name: "keys"})
	if err != nil {
		t.Fatal(err)
	}
	d := lease.(*keysDocument)
	t.Cleanup(func() { _ = d.Close() })
	return s, d, m
}

func TestMeilisearchKeysMalformedReceiptsAndNativeRefusalsKeepOriginalOutcome(t *testing.T) {
	for _, status := range []int{201, 200, 202, 403, 409, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			s, d, m := keyMockLease(t)
			plan, err := d.Prepare("create", contract.KeyRecord{}, contract.KeyDraft{Name: "owned", Actions: []string{"search"}, Indexes: []string{"owned*"}})
			if err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			m.responseStatus = status
			m.response = `{"code":"invalid_api_key","type":"auth","message":"refused","key":"` + m.secret + `","echo":"` + m.secret + `","exact":9007199254740993}`
			m.mu.Unlock()
			result, err := d.Apply(context.Background(), plan)
			if err == nil || m.writes.Load() != 1 || strings.Contains(result.Response, m.secret) || !strings.Contains(result.Response, "9007199254740993") {
				t.Fatal("malformed or failed synchronous key response lost or leaked evidence")
			}
			definite := status == 403 || status == 409
			expected := "uncertain"
			if definite {
				expected = "rejected"
			}
			if result.State != expected || d.uncertain == definite {
				t.Fatal("native refusal and malformed success had the same outcome", result.State)
			}
			var diagnostic *contract.KeyRequestError
			if !errors.As(err, &diagnostic) || diagnostic.Raw != result.Response || strings.Contains(err.Error(), m.secret) {
				t.Fatal("original public diagnostic lost or leaked")
			}
			before := m.calls.Load()
			recovery, err := s.RecoverKeysDocument(d.object, d)
			if err != nil || recovery.Uncertain == definite || recovery.Last.Response != result.Response || m.calls.Load() != before {
				t.Fatal("malformed/failed receipt recovery performed I/O or changed outcome", err)
			}
			defer recovery.Lease.Close()
			if !definite {
				if _, err = recovery.Lease.Prepare("create", contract.KeyRecord{}, plan.Draft); !errors.Is(err, contract.ErrKeyPlanUsed) {
					t.Fatal("malformed receipt recovered into a retry")
				}
			}
		})
	}
}

func TestMeilisearchKeysBoundsPageReplacementAndChangedMetadataDoNotSend(t *testing.T) {
	_, d, m := keyMockLease(t)
	for _, query := range [][2]uint64{{0, 0}, {0, 101}, {1 << 33, 20}} {
		if _, err := d.List(context.Background(), query[0], query[1]); !errors.Is(err, ErrBounds) || m.calls.Load() != 0 {
			t.Fatal("invalid key pagination used native I/O")
		}
	}
	r := originalKey(t, d)
	before := m.calls.Load()
	if _, err := d.Prepare("update", r, contract.KeyDraft{Name: r.Name, Description: r.Description}); !errors.Is(err, contract.ErrKeyNoChanges) || m.calls.Load() != before {
		t.Fatal("unchanged metadata made a network request", err)
	}
	forged := cloneKey(r)
	forged.Raw = `{"key":"exposed"}`
	if _, err := d.Read(context.Background(), forged); !errors.Is(err, contract.ErrKeyConflict) || m.calls.Load() != before {
		t.Fatal("forged key read used native I/O", err)
	}
	plan, err := d.Prepare("update", r, contract.KeyDraft{Name: "desired", Description: "description"})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.name = "external metadata"
	m.mu.Unlock()
	current, err := d.Read(context.Background(), r)
	if err != nil || current.Name != "external metadata" || current.Token == r.Token {
		t.Fatal("explicit current native metadata read lost", err)
	}
	result, err := d.Apply(context.Background(), plan)
	if !errors.Is(err, contract.ErrKeyConflict) || result.State != "not_submitted" || m.writes.Load() != 0 {
		t.Fatal("external metadata overwritten", err)
	}
	plan, err = d.Prepare("update", current, contract.KeyDraft{Name: "desired", Description: "description"})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.name = "new page metadata"
	m.mu.Unlock()
	_ = originalKey(t, d)
	before = m.calls.Load()
	if _, err = d.Apply(context.Background(), plan); !errors.Is(err, contract.ErrKeyConflict) || m.calls.Load() != before {
		t.Fatal("replaced original page's preview made a native request", err)
	}
}
func originalKey(t *testing.T, d *keysDocument) contract.KeyRecord {
	t.Helper()
	p, err := d.List(context.Background(), 0, 20)
	if err != nil || len(p.Results) != 1 {
		t.Fatal("original key missing", err)
	}
	return p.Results[0]
}
func TestMeilisearchKeysNativeRedactionProofsRevealAndUpdate(t *testing.T) {
	s, d, m := keyMockLease(t)
	if m.calls.Load() != 0 {
		t.Fatal("reservation performed I/O")
	}
	r := originalKey(t, d)
	if strings.Contains(r.Raw, m.secret) || !strings.Contains(r.Raw, "9007199254740993") || !strings.Contains(r.Raw, "原生🐱") || !strings.Contains(r.Raw, keyMask) {
		t.Fatal("original public key evidence leaked or changed")
	}
	before := m.calls.Load()
	secret, err := d.Reveal(r)
	if err != nil || secret != m.secret || m.calls.Load() != before {
		t.Fatal("explicit original secret reveal changed or performed I/O", err)
	}
	foreign, err := s.ReserveKeysDocument(d.object)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if _, err = foreign.Reveal(r); !errors.Is(err, contract.ErrKeyConflict) || m.calls.Load() != before {
		t.Fatal("foreign secret revealed", err)
	}
	forged := cloneKey(r)
	forged.Actions[0] = "*"
	if _, err = d.Reveal(forged); !errors.Is(err, contract.ErrKeyConflict) {
		t.Fatal("forged policy revealed secret")
	}
	draft := contract.KeyDraft{Name: "updated", Description: "updated description", Actions: []string{"*"}, Indexes: []string{"*"}, ExpiresAt: "2030-01-01T00:00:00Z"}
	plan, err := d.Prepare("update", r, draft)
	if err != nil || strings.Contains(plan.Body, "actions") || strings.Contains(plan.Body, "indexes") || strings.Contains(plan.Body, "expiresAt") || m.calls.Load() != before {
		t.Fatal("immutable update fields or pure preview changed", err, plan.Body)
	}
	changed := plan
	changed.Body = `{"name":"forged"}`
	if _, err = d.Apply(context.Background(), changed); !errors.Is(err, contract.ErrKeyPlanUsed) || m.calls.Load() != before {
		t.Fatal("forged preview transmitted", err)
	}
	result, err := d.Apply(context.Background(), plan)
	if err != nil || result.State != "completed" || result.Status != 200 || result.Record.Name != "updated" || m.writes.Load() != 1 || strings.Contains(result.Response, m.secret) {
		t.Fatal("synchronous native update lost or leaked evidence", err, result.State)
	}
	if _, err = d.Apply(context.Background(), plan); !errors.Is(err, contract.ErrKeyPlanUsed) || m.writes.Load() != 1 {
		t.Fatal("synchronous update replayed", err)
	}
	before = m.calls.Load()
	recovered, err := s.RecoverKeysDocument(d.object, d)
	if err != nil || m.calls.Load() != before || recovered.Last.Record.Token == result.Record.Token {
		t.Fatal("recovery performed I/O or lost original record", err)
	}
	defer recovered.Lease.Close()
	secret, err = recovered.Lease.Reveal(recovered.Last.Record)
	if err != nil || secret != m.secret {
		t.Fatal("explicit recovered original secret unavailable", err)
	}
	if _, err = recovered.Lease.Reveal(result.Record); !errors.Is(err, contract.ErrKeyConflict) {
		t.Fatal("old proof followed recovery")
	}
	deletion, err := recovered.Lease.Prepare("delete", recovered.Last.Record, contract.KeyDraft{})
	if err != nil {
		t.Fatal(err)
	}
	done, err := recovered.Lease.Apply(context.Background(), deletion)
	if err != nil || done.State != "completed" || done.Status != 204 || done.Response != "" {
		t.Fatal("synchronous deletion became task admission", err)
	}
	if _, err = recovered.Lease.Reveal(recovered.Last.Record); !errors.Is(err, contract.ErrKeyConflict) {
		t.Fatal("deleted secret retained in registry")
	}
}
func TestMeilisearchKeysCreateBoundsReplacementAndSynchronousReceipt(t *testing.T) {
	_, d, m := keyMockLease(t)
	draft := contract.KeyDraft{Name: "created", Description: "description", Actions: []string{"search"}, Indexes: []string{"owned*"}}
	for _, mutate := range []func(*contract.KeyDraft){func(v *contract.KeyDraft) { v.Actions = nil }, func(v *contract.KeyDraft) { v.Indexes = []string{"bad space"} }, func(v *contract.KeyDraft) { v.Actions = []string{"search", "search"} }, func(v *contract.KeyDraft) { v.UID = m.secret }, func(v *contract.KeyDraft) { v.ExpiresAt = "not a native date" }, func(v *contract.KeyDraft) { v.Name = strings.Repeat("x", keyDraftLimit+1) }} {
		bad := cloneKeyDraft(draft)
		mutate(&bad)
		if _, err := d.Prepare("create", contract.KeyRecord{}, bad); err == nil || m.calls.Load() != 0 {
			t.Fatal("invalid create preview used native I/O")
		}
	}
	plan, err := d.Prepare("create", contract.KeyRecord{}, draft)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := d.Prepare("create", contract.KeyRecord{}, draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Apply(context.Background(), plan); !errors.Is(err, contract.ErrKeyPlanUsed) || m.writes.Load() != 0 {
		t.Fatal("replaced creation preview sent")
	}
	result, err := d.Apply(context.Background(), replacement)
	if err != nil || result.Status != 201 || result.State != "completed" || result.UID != testOtherKeyUID || strings.Contains(result.Response, m.secret) {
		t.Fatal("creation response lost or exposed secret", err, result.State)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), m.secret) {
		t.Fatal("public result contained credential")
	}
	revealed, err := d.Reveal(result.Record)
	if err != nil || revealed != m.secret {
		t.Fatal("created credential not explicitly available", err)
	}
	for _, e := range d.entries {
		saved := e.secret
		_ = d.Close()
		if !slices.Equal(saved, make([]byte, len(saved))) {
			t.Fatal("close retained secret bytes")
		}
		break
	}
}
func TestMeilisearchKeysConflictRejectedAndUnknownDoNotReplay(t *testing.T) {
	for _, fault := range []string{"conflict", "rejected", "unknown"} {
		t.Run(fault, func(t *testing.T) {
			s, d, m := keyMockLease(t)
			r := originalKey(t, d)
			plan, err := d.Prepare("update", r, contract.KeyDraft{Name: "new", Description: "description"})
			if err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			switch fault {
			case "conflict":
				m.name = "external"
			case "rejected":
				m.response = `{"code":"api_key_already_exists","type":"invalid_request","message":"duplicate"}`
			case "unknown":
				m.fault = true
			}
			m.mu.Unlock()
			result, err := d.Apply(context.Background(), plan)
			if err == nil {
				t.Fatal("fault did not surface")
			}
			if fault == "conflict" {
				if !errors.Is(err, contract.ErrKeyConflict) || result.State != "not_submitted" || m.writes.Load() != 0 {
					t.Fatal("changed original key overwritten", err)
				}
				return
			}
			expected := "rejected"
			if fault == "unknown" {
				expected = "uncertain"
			}
			if result.State != expected || m.writes.Load() != 1 || result.Request == "" {
				t.Fatal("write outcome misclassified", err, result.State)
			}
			before := m.calls.Load()
			recovered, err := s.RecoverKeysDocument(d.object, d)
			if err != nil || m.calls.Load() != before || recovered.Uncertain != (fault == "unknown") {
				t.Fatal("evidence recovery changed unknown state or used I/O", err)
			}
			defer recovered.Lease.Close()
			if fault == "unknown" {
				if _, err = recovered.Lease.Prepare("create", contract.KeyRecord{}, contract.KeyDraft{Actions: []string{"search"}, Indexes: []string{"*"}}); !errors.Is(err, contract.ErrKeyPlanUsed) || m.writes.Load() != 1 {
					t.Fatal("unknown outcome became retry", err)
				}
			}
		})
	}
}
