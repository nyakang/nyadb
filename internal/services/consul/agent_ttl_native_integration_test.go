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
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeTTLRequest(t *testing.T, action, id, output string) Request {
	t.Helper()
	var request Request
	var err error
	switch action {
	case "pass":
		request, err = AgentCheckPassRequest(id, &output)
	case "warn":
		request, err = AgentCheckWarnRequest(id, &output)
	case "fail":
		request, err = AgentCheckFailRequest(id, &output)
	case "update":
		raw, encodeErr := json.Marshal(map[string]string{"Status": "passing", "Output": output})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		request, err = AgentCheckUpdateRequest(id, append(append([]byte(" \n"), raw...), []byte("\n ")...))
	default:
		t.Fatal("unknown TTL action", action)
	}
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func nativeTTLCheck(t *testing.T, base *Client, id string) (string, string, string) {
	t.Helper()
	var check struct{ CheckID, Status, Output, Notes string }
	if json.Unmarshal(nativeAgentServiceEvidenceMaps(t, base, true)[id], &check) != nil || check.CheckID != id {
		t.Fatal("actual original TTL check missing", id)
	}
	return check.Status, check.Output, check.Notes
}

func TestConsulNativeAgentTTLOriginalStatesNotesOutputAndReset(t *testing.T) {
	s, base, _, _ := nativeDocumentSession(t, 8501)
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	serviceID := "owned-service-" + t.Name()
	nativeDo(t, base, nativeAgentRegister(t, nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": serviceID, "ID": serviceID}), nil))
	for _, binding := range []string{"", serviceID} {
		id := "owned-" + t.Name() + binding + "-雪%?#++"
		definition := map[string]any{"Name": "owned TTL", "ID": id, "TTL": "10m", "Notes": "original definition notes 中文"}
		if binding != "" {
			definition["ServiceID"] = binding
		}
		executeDocument(t, l, nativeCheckRegister(t, nativeAgentServiceEvidenceJSON(t, definition)))
		for _, action := range []string{"pass", "warn", "fail", "update"} {
			output := "original first\nsecond\r\nNUL\x00中文 %?#++ 18446744073709551615"
			r := nativeTTLRequest(t, action, id, output)
			original := r.Clone()
			plan := documentPlan(t, l, r)
			if len(r.Body) > 0 {
				r.Body[0] = 'x'
			}
			if r.Query != nil {
				r.Query.Set("note", "later edited note")
			}
			result, err := l.Execute(context.Background(), plan)
			if err != nil || !result.Completed || result.WriteOutcomeUnknown || result.HTTP == nil || !result.HTTP.Captured || !result.HTTP.EOF || !result.HTTP.Acknowledged || !result.HTTP.Succeeded || len(result.HTTP.Body) != 0 || !bytes.Equal(result.HTTP.Request.Body, original.Body) || url.Values(result.HTTP.Request.Query).Get("note") != original.Query.Get("note") {
				t.Fatal("original TTL request or empty ACK changed", err)
			}
			if _, err := l.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
				t.Fatal("original TTL request was replayed", err)
			}
			status, wantedOutput, notes := nativeTTLCheck(t, base, id)
			want := map[string]string{"pass": "passing", "warn": "warning", "fail": "critical", "update": "passing"}[action]
			if status != want || wantedOutput != output || notes != "original definition notes 中文" {
				t.Fatal("TTL state/output or original Notes changed", action, status, wantedOutput, notes)
			}
		}
		for _, field := range []string{"", `,"Output":""`, `,"Output":null`} {
			raw := []byte(" {\n\"Status\":\"passing\"" + field + "\n} ")
			r, err := AgentCheckUpdateRequest(id, raw)
			if err != nil {
				t.Fatal(err)
			}
			result := executeDocument(t, l, r)
			_, output, notes := nativeTTLCheck(t, base, id)
			if !result.HTTP.Succeeded || !bytes.Equal(result.HTTP.Request.Body, raw) || output != "" || notes != "original definition notes 中文" {
				t.Fatal("native Output defaults rewrote authored bytes or definition notes")
			}
		}
		for _, action := range []string{"pass", "warn", "fail"} {
			for _, note := range []*string{nil, ttlTestNote("")} {
				var r Request
				var err error
				switch action {
				case "pass":
					r, err = AgentCheckPassRequest(id, note)
				case "warn":
					r, err = AgentCheckWarnRequest(id, note)
				case "fail":
					r, err = AgentCheckFailRequest(id, note)
				}
				if err != nil {
					t.Fatal(err)
				}
				result := executeDocument(t, l, r)
				_, output, _ := nativeTTLCheck(t, base, id)
				if !result.HTTP.Succeeded || url.Values(result.HTTP.Request.Query).Has("note") != (note != nil) || output != "" {
					t.Fatal("native TTL note presence/default changed")
				}
			}
		}
		long := strings.Repeat("x", 8192)
		for _, action := range []string{"pass", "update"} {
			r := nativeTTLRequest(t, action, id, long)
			result := executeDocument(t, l, r)
			_, output, _ := nativeTTLCheck(t, base, id)
			if !result.HTTP.Succeeded || len(output) >= len(long) || !bytes.Equal(result.HTTP.Request.Body, r.Body) || url.Values(result.HTTP.Request.Query).Get("note") != r.Query.Get("note") {
				t.Fatal("native output truncation destroyed complete original TTL request", action)
			}
		}
	}
	id := "owned-reset-" + t.Name()
	executeDocument(t, l, nativeCheckRegister(t, nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": id, "ID": id, "TTL": "4s", "Notes": "original reset notes"})))
	executeDocument(t, l, nativeTTLRequest(t, "pass", id, "first original deadline"))
	first := time.Now()
	time.Sleep(2500 * time.Millisecond)
	executeDocument(t, l, nativeTTLRequest(t, "warn", id, "original deadline reset"))
	time.Sleep(1800 * time.Millisecond)
	status, output, notes := nativeTTLCheck(t, base, id)
	if time.Since(first) < 4*time.Second || status != "warning" || output != "original deadline reset" || notes != "original reset notes" {
		t.Fatal("TTL reset did not extend original deadline", status, output, notes)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, _, notes = nativeTTLCheck(t, base, id)
		if status == "critical" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("original finite TTL never expired")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if notes != "original reset notes" {
		t.Fatal("TTL expiry rewrote original definition notes")
	}
}

func TestConsulNativeAgentTTLOriginalDefinitionsKnownDenialsAndNativeErrors(t *testing.T) {
	s, base, cfg, _ := nativeDocumentSession(t, 8502)
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	id := "owned-" + t.Name()
	executeDocument(t, l, nativeCheckRegister(t, nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": id, "ID": id, "TTL": "10m", "Notes": "original notes"})))
	for _, action := range []string{"pass", "warn", "fail", "update"} {
		missing := nativeTTLRequest(t, action, "owned-no-such-ttl", "original denied output")
		result, err := l.Execute(context.Background(), documentPlan(t, l, missing))
		if err == nil || !result.Completed || result.HTTP.Status != 404 || !result.HTTP.Acknowledged || result.HTTP.Succeeded || result.WriteOutcomeUnknown || len(result.HTTP.Body) == 0 {
			t.Fatal("original missing TTL check lost complete known denial", err)
		}
		if limitedToken := os.Getenv("NYADB_CONSUL_READ_TOKEN"); limitedToken != "" {
			limited, err := New(cfg, limitedToken)
			if err != nil {
				t.Fatal(err)
			}
			denied, err := limited.Do(context.Background(), nativeTTLRequest(t, action, id, "denied original output"))
			limited.Close()
			if err == nil || denied.Status != 403 || !denied.Captured || !denied.EOF || !denied.Acknowledged || denied.Succeeded || denied.WriteOutcomeUnknown || len(denied.Body) == 0 {
				t.Fatal("original TTL ACL preflight lost complete denial", err)
			}
		}
	}
	if cfg.Auth == "token" {
		serviceID := "owned-acl-service-" + t.Name()
		nativeDo(t, base, nativeAgentRegister(t, nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": serviceID, "ID": serviceID}), nil))
		serviceCheckID := id + "-service-acl"
		nativeDo(t, base, nativeCheckRegister(t, nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": serviceCheckID, "ID": serviceCheckID, "ServiceID": serviceID, "TTL": "10m", "Notes": "original service ACL notes"})))
		for _, role := range []string{"node", "service"} {
			rules := `node_prefix "" { policy = "read" } service_prefix "" { policy = "write" } agent_prefix "" { policy = "read" }`
			if role == "node" {
				rules = `node_prefix "" { policy = "write" } service_prefix "" { policy = "read" } agent_prefix "" { policy = "read" }`
			}
			policy := nativeAgentServiceEvidence(t, base, "/v1/acl/policy", nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": "owned-ttl-" + role + "-" + t.Name(), "Rules": rules}))
			var policyID struct{ ID string }
			if policy.Status != 200 || json.Unmarshal(policy.Body, &policyID) != nil || policyID.ID == "" {
				t.Fatal("owned TTL scope policy creation failed")
			}
			token := nativeAgentServiceEvidence(t, base, "/v1/acl/token", nativeAgentServiceEvidenceJSON(t, map[string]any{"Description": "owned TTL " + role, "Policies": []map[string]string{{"ID": policyID.ID}}}))
			var credential struct{ SecretID string }
			if token.Status != 200 || json.Unmarshal(token.Body, &credential) != nil || credential.SecretID == "" {
				t.Fatal("owned TTL scope token creation failed")
			}
			limited, err := New(cfg, credential.SecretID)
			if err != nil {
				t.Fatal(err)
			}
			defer limited.Close()
			for _, binding := range []string{"node", "service"} {
				checkID := id
				if binding == "service" {
					checkID = serviceCheckID
				}
				for _, action := range []string{"pass", "warn", "fail", "update"} {
					beforeStatus, beforeOutput, beforeNotes := nativeTTLCheck(t, base, checkID)
					originalOutput := "owned " + role + " original " + binding + " " + action
					result, err := limited.Do(context.Background(), nativeTTLRequest(t, action, checkID, originalOutput))
					status, output, notes := nativeTTLCheck(t, base, checkID)
					if role == binding {
						if err != nil || result.Status != 200 || !result.Acknowledged || !result.Succeeded || len(result.Body) != 0 || output != originalOutput || notes != beforeNotes {
							t.Fatal("TTL ACL used foreign scope for original allowed check", role, binding, action)
						}
					} else if err == nil || result.Status != 403 || !result.Captured || !result.EOF || !result.Acknowledged || result.Succeeded || result.WriteOutcomeUnknown || len(result.Body) == 0 || status != beforeStatus || output != beforeOutput || notes != beforeNotes {
						t.Fatal("TTL ACL denial changed original check or lost complete refusal", role, binding, action)
					}
				}
			}
		}
	}
	for _, test := range []struct {
		name   string
		status int
	}{{"unknown-field", 400}, {"non-ttl", 500}} {
		t.Run(test.name, func(t *testing.T) {
			lease, err := s.ReserveKV(2)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			checkID := id + "-" + test.name
			definition := map[string]any{"Name": checkID, "ID": checkID, "TTL": "10m"}
			if test.name == "non-ttl" {
				delete(definition, "TTL")
				definition["TCP"] = "127.0.0.1:1"
				definition["Interval"] = "10m"
			}
			nativeDo(t, base, nativeCheckRegister(t, nativeAgentServiceEvidenceJSON(t, definition)))
			raw := []byte(` {"Status":"passing","Output":"original"} `)
			if test.name == "unknown-field" {
				raw = []byte(` {"Status":"passing","UnknownNative":18446744073709551615} `)
			}
			r, err := AgentCheckUpdateRequest(checkID, raw)
			if err != nil {
				t.Fatal(err)
			}
			original, err := lease.Execute(context.Background(), documentPlan(t, lease, r))
			if err == nil || original.Completed || !original.WriteOutcomeUnknown || original.HTTP.Status != test.status || !original.HTTP.Captured || !original.HTTP.EOF || original.HTTP.Acknowledged || !bytes.Equal(original.HTTP.Request.Body, raw) || len(original.HTTP.Body) == 0 {
				t.Fatal("original native TTL rejection was changed or guessed known", err)
			}
			read, _ := AgentChecksRequest(nil)
			executeDocument(t, lease, read)
			if !reflect.DeepEqual(*lease.(*documentLease).recoveryOwned().Unknown, original) {
				t.Fatal("later TTL state read confirmed unknown write")
			}
			for _, request := range []Request{r, nativeTTLRequest(t, "fail", checkID, "later"), putRequest("later/"+checkID, nil)} {
				doc, err := DocumentRequest(request)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := lease.Prepare(doc); !errors.Is(err, contract.ErrKVWriteUnknown) {
					t.Fatal("native TTL rejection removed cross-operation barrier", err)
				}
			}
		})
	}
}
func TestConsulNativeAgentTTLProcessedRepliesOriginalLeaseRecoveryAndNoReplay(t *testing.T) {
	base, cfg, token := nativeClient(t)
	for _, kind := range []string{"pass", "warn", "fail", "update"} {
		for _, mode := range []string{"lost", "partial", "http400", "http500", "http408", "invalid", "cancel", "close", "retire"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				id := "owned-" + strings.ReplaceAll(t.Name(), "/", "-")
				raw := nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": id, "ID": id, "TTL": "10m", "Notes": "original 中文"})
				nativeDo(t, base, nativeCheckRegister(t, raw))
				r := nativeTTLRequest(t, kind, id, "original processed output 中文")
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
				status, output, _ := nativeTTLCheck(t, base, id)
				if status != map[string]string{"pass": "passing", "warn": "warning", "fail": "critical", "update": "passing"}[kind] || output != "original processed output 中文" {
					t.Fatal("relay fault did not follow actual original TTL mutation")
				}
			})
		}
	}
}
