package meilisearch

import (
	"context"
	"encoding/json"
	"errors"
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

type settingsMock struct {
	mu                sync.Mutex
	raw, index, patch string
	calls, writes     atomic.Int32
	fault             bool
}

func settingsMockLease(t *testing.T) (*Session, contract.SettingsDocumentLease, *settingsMock) {
	t.Helper()
	m := &settingsMock{raw: `{"searchableAttributes":["*"],"typoTolerance":{"enabled":true,"minWordSizeForTypos":{"oneTypo":5,"twoTypos":9}},"embedders":{"owned":{"apiKey":null,"url":"original"}},"future":{"exact":9007199254740993,"text":"原生🐱"}}`, index: mockIndex}
	c, server := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
		m.calls.Add(1)
		m.mu.Lock()
		defer m.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/indexes/owned"):
			_, _ = io.WriteString(w, m.index)
		case strings.HasSuffix(r.URL.Path, "/settings"):
			if r.Method == "GET" {
				_, _ = io.WriteString(w, m.raw)
				return
			}
			m.writes.Add(1)
			body, _ := io.ReadAll(r.Body)
			m.patch = string(body)
			if m.fault {
				h := w.(http.Hijacker)
				conn, _, _ := h.Hijack()
				_ = conn.Close()
				return
			}
			w.WriteHeader(202)
			_, _ = io.WriteString(w, strings.Replace(mockReceipt, "documentAdditionOrUpdate", "settingsUpdate", 1))
		case strings.HasSuffix(r.URL.Path, "/tasks"):
			raw := strings.Replace(taskDocumentFixtureRaw(0, "succeeded"), "documentAdditionOrUpdate", "settingsUpdate", 1)
			body, _ := json.Marshal(map[string]any{"results": []json.RawMessage{json.RawMessage(raw)}, "total": 1, "limit": 1, "from": 0, "next": nil})
			_, _ = w.Write(body)
		default:
			t.Error("unexpected native settings route", r.Method, r.URL.Path)
			w.WriteHeader(400)
		}
	})
	c.Close()
	s := testSession(t, connections.MeilisearchConfig{Endpoint: server.URL + "/proxy/meili"}, "")
	lease, err := s.ReserveSettingsDocument(s.Namespace("owned").Object("settings", "settings"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	return s, lease, m
}
func TestMeilisearchSettingsDocumentDeltaProofAdmissionAndRecovery(t *testing.T) {
	s, lease, m := settingsMockLease(t)
	if m.calls.Load() != 0 {
		t.Fatal("settings reservation performed I/O")
	}
	source, err := lease.Read(context.Background())
	if err != nil || !strings.Contains(source.Raw, "9007199254740993") {
		t.Fatal("native settings source lost", err)
	}
	draft := strings.ReplaceAll(source.Raw, `"searchableAttributes":["*"]`, `"searchableAttributes":["title"]`)
	draft = strings.Replace(draft, `"url":"original"`, `"url":"changed"`, 1)
	plan, err := lease.Prepare(source, draft, false)
	before := m.calls.Load()
	if err != nil || len(plan.Fields) != 2 || !strings.Contains(plan.Patch, `"url":"changed"`) || strings.Contains(plan.Patch, "apiKey") || strings.Contains(plan.Patch, "future") {
		t.Fatal("delta rewrote untouched/redacted settings", plan.Patch, err)
	}
	if _, err = lease.Prepare(source, prettyForSettingsTest(source.Raw), false); !errors.Is(err, contract.ErrSettingsNoChanges) || m.calls.Load() != before {
		t.Fatal("format-only edit performed native I/O", err)
	}
	for _, modify := range []func(*contract.SettingsPlan){func(p *contract.SettingsPlan) { p.Patch = `{}` }, func(p *contract.SettingsPlan) { p.Original.Object.Catalog = "other" }, func(p *contract.SettingsPlan) { p.Fields = append([]string(nil), p.Fields...); p.Fields[0] = "other" }} {
		changed := plan
		modify(&changed)
		if _, err = lease.Apply(context.Background(), changed); !errors.Is(err, contract.ErrSettingsPlanUsed) || m.calls.Load() != before {
			t.Fatal("modified native preview dispatched", err)
		}
	}
	result, err := lease.Apply(context.Background(), plan)
	if err != nil || result.State != "enqueued" || result.Receipt.UID != "0" || m.writes.Load() != 1 {
		t.Fatal("settings admission mistaken for completion", err, result.State)
	}
	if _, err = lease.Apply(context.Background(), plan); !errors.Is(err, contract.ErrSettingsPlanUsed) || m.writes.Load() != 1 {
		t.Fatal("settings preview replayed", err)
	}
	recover, err := s.RecoverSettingsDocument(source.Object, lease)
	if err != nil || recover.Receipt.Raw != result.Receipt.Raw || recover.Receipt.Token == result.Receipt.Token {
		t.Fatal("original receipt recovery lost evidence", err)
	}
	defer recover.Lease.Close()
	if m.calls.Load() != before+4 {
		t.Fatal("receipt recovery performed I/O")
	}
	settled, err := recover.Lease.Observe(context.Background(), recover.Receipt, time.Second)
	if err != nil || settled.State != "succeeded" || settled.Snapshot.Raw == "" || settled.TaskRaw == "" {
		t.Fatal("explicit original settings observation failed", err)
	}
	if m.writes.Load() != 1 {
		t.Fatal("observation replayed settings")
	}
	foreign, err := s.ReserveSettingsDocument(source.Object)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	before = m.calls.Load()
	if _, err = foreign.Prepare(source, draft, false); !errors.Is(err, ErrInvalid) || m.calls.Load() != before {
		t.Fatal("foreign settings proof performed I/O", err)
	}
}
func prettyForSettingsTest(raw string) string {
	var v json.RawMessage
	_ = json.Unmarshal([]byte(raw), &v)
	b, _ := json.MarshalIndent(v, "", " ")
	return string(b)
}
func TestMeilisearchSettingsDocumentConflictAndUnknownWriteNeverReplay(t *testing.T) {
	for _, fault := range []string{"conflict", "replacement", "lost"} {
		t.Run(fault, func(t *testing.T) {
			s, lease, m := settingsMockLease(t)
			source, err := lease.Read(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			plan, err := lease.Prepare(source, strings.Replace(source.Raw, `"*"`, `"title"`, 1), false)
			if err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			switch fault {
			case "conflict":
				m.raw = strings.Replace(m.raw, `"*"`, `"concurrent"`, 1)
			case "replacement":
				m.index = strings.Replace(m.index, "2026-11-08", "2026-11-09", -1)
			case "lost":
				m.fault = true
			}
			m.mu.Unlock()
			result, err := lease.Apply(context.Background(), plan)
			if fault == "lost" {
				if !errors.Is(err, contract.ErrSettingsLeaseLost) || result.State != "uncertain" || m.writes.Load() != 1 {
					t.Fatal("lost settings outcome not retained", err)
				}
			} else {
				if !errors.Is(err, contract.ErrSettingsConflict) || m.writes.Load() != 0 {
					t.Fatal("changed settings/index was overwritten", err)
				}
			}
			count := m.writes.Load()
			if _, err = lease.Apply(context.Background(), plan); err == nil || m.writes.Load() != count {
				t.Fatal("original settings preview replayed")
			}
			recovered, err := s.RecoverSettingsDocument(source.Object, lease)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Lease.Close()
			if fault == "lost" {
				if !recovered.Uncertain {
					t.Fatal("recovery erased uncertain write")
				}
				m.mu.Lock()
				m.fault = false
				m.mu.Unlock()
				current, err := recovered.Lease.Read(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if _, err = recovered.Lease.Prepare(current, plan.Draft, false); !errors.Is(err, contract.ErrSettingsPlanUsed) {
					t.Fatal("fresh read enabled replay of unknown original settings write", err)
				}
			}
			if fault == "replacement" {
				if _, err = recovered.Lease.Read(context.Background()); !errors.Is(err, contract.ErrSettingsLeaseLost) {
					t.Fatal("recovery rebound original settings to recreated index", err)
				}
			}
		})
	}
}
func TestMeilisearchSettingsDocumentNullResetsAndJSONBounds(t *testing.T) {
	_, lease, m := settingsMockLease(t)
	source, err := lease.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var desired map[string]json.RawMessage
	_ = json.Unmarshal([]byte(source.Raw), &desired)
	delete(desired, "typoTolerance")
	raw, _ := json.Marshal(desired)
	plan, err := lease.Prepare(source, string(raw), false)
	if err != nil || plan.Patch != `{"typoTolerance":null}` {
		t.Fatal("removed field was not explicit reset", plan.Patch, err)
	}
	before := m.calls.Load()
	for _, bad := range []string{`[]`, `null`, `{"duplicate":1,"duplicate":2}`, `{"nested":{"x":1,"x":2}}`, `{"text":"\ud800"}`, strings.Repeat(" ", settingsLimit+1)} {
		if _, err = lease.Prepare(source, bad, false); err == nil || m.calls.Load() != before {
			t.Fatal("invalid settings object dispatched")
		}
	}
	reset, err := lease.Prepare(source, "", true)
	if err != nil || !reset.Reset || reset.Patch != "" {
		t.Fatal("reset preview invalid", err)
	}
	result, err := lease.Apply(context.Background(), reset)
	if err != nil || result.State != "enqueued" || m.patch != "" {
		t.Fatal("settings reset did not retain native admission", err)
	}
}
