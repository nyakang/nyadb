package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeCheckRegister(t *testing.T, raw []byte) Request {
	t.Helper()
	r, err := AgentCheckRegisterRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func nativeCheckDeregister(t *testing.T, id string) Request {
	t.Helper()
	r, err := AgentCheckDeregisterRequest(id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestConsulNativeAgentCheckOriginalDefinitionsDefaultsUpdatesAndKnownDenials(t *testing.T) {
	s, base, cfg, _ := nativeDocumentSession(t, 8401)
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	id := "owned-" + t.Name() + "-Unicode 雪%?#++"
	for index, status := range []string{"critical", "warning"} {
		notes := "original 中文 18446744073709551615"
		if index == 1 {
			notes = "updated original notes 中文"
		}
		raw := nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": "owned-" + t.Name(), "ID": id, "TTL": "10m", "Status": status, "Notes": notes})
		original := bytes.Clone(raw)
		request := nativeCheckRegister(t, raw)
		plan := documentPlan(t, l, request)
		raw[0], request.Body[0] = 'x', 'x'
		result, err := l.Execute(context.Background(), plan)
		if err != nil || !result.Completed || result.HTTP == nil || !result.HTTP.Captured || !result.HTTP.EOF || !result.HTTP.Acknowledged || !result.HTTP.Succeeded || result.WriteOutcomeUnknown || len(result.HTTP.Body) != 0 || !bytes.Equal(result.HTTP.Request.Body, original) || result.Native != nil || result.NativeCaptured {
			t.Fatal("original check empty ACK or complete definition lost", err)
		}
		if _, err := l.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
			t.Fatal("original check registration replayed", err)
		}
		u, err := url.Parse(result.HTTP.URL)
		if err != nil || u.Query().Has("dc") || u.Query().Has("consistent") || u.Query().Has("stale") || u.Query().Has("token") {
			t.Fatal("local check write inherited unsupported catalog routing or authentication")
		}
		entries := nativeAgentServiceEvidenceMaps(t, base, true)
		var check struct{ CheckID, Status, Notes string }
		if json.Unmarshal(entries[id], &check) != nil || check.CheckID != id || check.Notes != notes || check.Status != "critical" {
			t.Fatal("original check identity/notes or retained TTL state changed")
		}
	}
	if token := os.Getenv("NYADB_CONSUL_READ_TOKEN"); token != "" {
		cfg.Auth = "token"
		limited, err := New(cfg, token)
		if err != nil {
			t.Fatal(err)
		}
		defer limited.Close()
		for _, request := range []Request{nativeCheckRegister(t, nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": id, "ID": id, "TTL": "10m"})), nativeCheckDeregister(t, id)} {
			denied, err := limited.Do(context.Background(), request)
			if err == nil || denied.Status != 403 || !denied.Acknowledged || denied.Succeeded || denied.WriteOutcomeUnknown || !denied.Captured || !denied.EOF || len(denied.Body) == 0 {
				t.Fatal("actual check ACL refusal lost complete denial", err)
			}
			if _, err := limited.Prepare(request); err != nil {
				t.Fatal("actual check ACL refusal invented unknown-write barrier", err)
			}
		}
		if nativeAgentServiceEvidenceMaps(t, base, true)[id] == nil {
			t.Fatal("denied check removal changed original Agent map")
		}
	}
	removed := executeDocument(t, l, nativeCheckDeregister(t, id))
	if !removed.Completed || !removed.HTTP.Succeeded || len(removed.HTTP.Body) != 0 || nativeAgentServiceEvidenceMaps(t, base, true)[id] != nil {
		t.Fatal("original check deregistration retained its native ID")
	}
	missing, err := l.Execute(context.Background(), documentPlan(t, l, nativeCheckDeregister(t, id)))
	if err == nil || !missing.Completed || missing.HTTP.Status != 404 || !missing.HTTP.Acknowledged || missing.HTTP.Succeeded || missing.WriteOutcomeUnknown || len(missing.HTTP.Body) == 0 {
		t.Fatal("actual missing check ID invented an unknown write", err)
	}
	for _, field := range []string{"", `,"ID":""`, `,"ID":null`} {
		name := "owned-check-default-" + t.Name()
		raw := []byte(`{"Name":"` + name + `","TTL":"10m"` + field + `}`)
		result := executeDocument(t, l, nativeCheckRegister(t, raw))
		if !result.HTTP.Succeeded || !bytes.Equal(result.HTTP.Request.Body, raw) || nativeAgentServiceEvidenceMaps(t, base, true)[name] == nil {
			t.Fatal("native check ID default was guessed or original body rewritten")
		}
	}
}

func TestConsulNativeAgentCheckServiceBindingAndRejectedOriginalDefinitions(t *testing.T) {
	s, base, _, _ := nativeDocumentSession(t, 8402)
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	serviceID := "owned-service-" + t.Name()
	nativeDo(t, base, nativeAgentRegister(t, nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": serviceID, "ID": serviceID}), nil))
	checkID := serviceID + "-check"
	raw := nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": "owned service check", "ID": checkID, "ServiceID": serviceID, "TTL": "10m", "Notes": "original service binding 中文"})
	result := executeDocument(t, l, nativeCheckRegister(t, raw))
	var check struct{ CheckID, ServiceID string }
	if !result.HTTP.Succeeded || !bytes.Equal(result.HTTP.Request.Body, raw) || json.Unmarshal(nativeAgentServiceEvidenceMaps(t, base, true)[checkID], &check) != nil || check.CheckID != checkID || check.ServiceID != serviceID {
		t.Fatal("actual check registration changed its original service binding")
	}
	removed := executeDocument(t, l, nativeCheckDeregister(t, checkID))
	if !removed.HTTP.Succeeded || nativeAgentServiceEvidenceMaps(t, base, true)[checkID] != nil || nativeAgentServiceEvidenceMaps(t, base, false)[serviceID] == nil {
		t.Fatal("check removal changed its original service")
	}
	for _, test := range []struct {
		name    string
		status  int
		payload map[string]any
	}{
		{"unknown-field", 400, map[string]any{"Name": "owned unknown", "TTL": "10m", "UnknownNative": json.RawMessage("18446744073709551615")}},
		{"disabled-script", 500, map[string]any{"Name": "owned disabled script", "Args": []string{"/bin/true"}, "Interval": "10s"}},
		{"missing-service", 404, map[string]any{"Name": "owned missing service", "TTL": "10m", "ServiceID": "owned-no-such-service"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			lease, err := s.ReserveKV(2)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			id := "owned-" + strings.ReplaceAll(t.Name(), "/", "-")
			test.payload["ID"] = id
			raw := nativeAgentServiceEvidenceJSON(t, test.payload)
			original, err := lease.Execute(context.Background(), documentPlan(t, lease, nativeCheckRegister(t, raw)))
			if err == nil || original.Completed || !original.WriteOutcomeUnknown || original.HTTP == nil || original.HTTP.Status != test.status || original.HTTP.Acknowledged || !original.HTTP.Captured || !original.HTTP.EOF || !bytes.Equal(original.HTTP.Request.Body, raw) || len(original.HTTP.Body) == 0 {
				t.Fatal("original server rejection was rewritten or guessed as a known failure", err)
			}
			if nativeAgentServiceEvidenceMaps(t, base, true)[id] != nil {
				t.Fatal("rejected original definition unexpectedly registered a check")
			}
			before := original.Clone()
			read, _ := AgentChecksRequest(nil)
			executeDocument(t, lease, read)
			if !reflect.DeepEqual(lease.(*documentLease).recoveryOwned().Unknown, &before) {
				t.Fatal("later check map read confirmed the original rejected write")
			}
			for _, request := range []Request{nativeCheckRegister(t, raw), putRequest("later/"+id, nil)} {
				document, err := DocumentRequest(request)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := lease.Prepare(document); !errors.Is(err, contract.ErrKVWriteUnknown) {
					t.Fatal("rejected original check definition allowed implicit replay", err)
				}
			}
		})
	}
}

func TestConsulNativeAgentCheckProcessedRepliesOriginalLeaseRecoveryAndNoReplay(t *testing.T) {
	base, cfg, token := nativeClient(t)
	for _, kind := range []string{"register", "deregister"} {
		for _, mode := range []string{"lost", "partial", "http400", "http500", "http408", "invalid", "cancel", "close", "retire"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				id := "owned-" + strings.ReplaceAll(t.Name(), "/", "-")
				raw := nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": id, "ID": id, "TTL": "10m", "Notes": "original 中文"})
				r := nativeCheckRegister(t, raw)
				if kind == "deregister" {
					nativeDo(t, base, r)
					r = nativeCheckDeregister(t, id)
				}
				relay, calls, writes, processed := nativeAgentMutationFault(t, cfg, token, mode, "/v1/agent/check/")
				s, err := OpenNativeSession(context.Background(), 8302, relay, token)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				l, err := s.ReserveKV(1)
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
				if err := l.Preconnect(context.Background()); err != nil {
					t.Fatal(err)
				}
				plan := documentPlan(t, l, r)
				later := documentPlan(t, l, putRequest("later/"+id, nil))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan struct{})
				var original contract.KVResult
				go func() { original, err = l.Execute(ctx, plan); close(done) }()
				wait(t, processed)
				if mode == "cancel" {
					cancel()
				} else if mode == "close" {
					s.Close()
				} else if mode == "retire" {
					l.(*documentLease).retire()
				}
				wait(t, done)
				if err == nil || original.HTTP == nil || original.Completed || original.HTTP.Acknowledged || !original.WriteOutcomeUnknown || !original.HTTP.Ended || writes.Load() != 1 {
					t.Fatal("processed original Agent mutation confirmed or replayed", err)
				}
				if mode == "partial" && (original.HTTP.Captured || original.HTTP.EOF || len(original.HTTP.Body) != 0 || http.Header(original.HTTP.Header).Get("X-Original") != "retained") {
					t.Fatal("empty partial body invented complete original ACK")
				}
				if mode == "http400" || mode == "http500" || mode == "http408" || mode == "invalid" {
					if _, err := l.Execute(context.Background(), later); !errors.Is(err, contract.ErrKVWriteUnknown) {
						t.Fatal("earlier write plan bypassed Agent unknown barrier", err)
					}
					executeDocument(t, l, AgentSelfRequest())
				}
				target := s
				if mode == "close" {
					target, err = OpenNativeSession(context.Background(), 8302, relay, token)
					if err != nil {
						t.Fatal(err)
					}
					defer target.Close()
				}
				before := calls.Load()
				recovery, err := target.RecoverKV(l)
				if err != nil {
					t.Fatal(err)
				}
				defer recovery.Lease.Close()
				if calls.Load() != before || recovery.Unknown == nil || !reflect.DeepEqual(*recovery.Unknown, original) || writes.Load() != 1 {
					t.Fatal("Agent evidence-only recovery changed original receipt or dispatched")
				}
				for _, request := range []Request{r, putRequest("later/"+id, nil)} {
					doc, err := DocumentRequest(request)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := recovery.Lease.Prepare(doc); !errors.Is(err, contract.ErrKVWriteUnknown) {
						t.Fatal("recovery removed cross-operation unknown barrier", err)
					}
				}
				if _, err := recovery.Lease.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
					t.Fatal("recovery granted old original authority", err)
				}
				executeDocument(t, recovery.Lease, AgentSelfRequest())
				if !reflect.DeepEqual(*recovery.Lease.(*documentLease).recoveryOwned().Unknown, original) || writes.Load() != 1 {
					t.Fatal("later explicit original read cleared Agent unknown receipt")
				}
				_, present := nativeAgentServiceEvidenceMaps(t, base, true)[id]
				if present != (kind == "register") {
					t.Fatal("relay fault did not follow actual original mutation")
				}
			})
		}
	}
}
