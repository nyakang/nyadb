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

func nativeMaintenanceRequest(t *testing.T, kind, id string, enable bool, reason *string) Request {
	t.Helper()
	var r Request
	var err error
	if kind == "node" {
		r, err = AgentNodeMaintenanceRequest(enable, reason)
	} else if kind == "service" {
		r, err = AgentServiceMaintenanceRequest(id, enable, reason)
	} else {
		t.Fatal("unknown maintenance target", kind)
	}
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func nativeMaintenanceCheckID(kind, id string) string {
	if kind == "node" {
		return "_node_maintenance"
	}
	return "_service_maintenance:" + id
}

func nativeMaintenanceObserve(t *testing.T, base *Client, kind, id string, present bool, notes string) json.RawMessage {
	t.Helper()
	raw := nativeAgentServiceEvidenceMaps(t, base, true)[nativeMaintenanceCheckID(kind, id)]
	if (raw != nil) != present {
		t.Fatal("original local maintenance presence differs", kind, present)
	}
	if present {
		var check struct{ CheckID, Status, Type, Notes, Output, ServiceID string }
		if json.Unmarshal(raw, &check) != nil || check.CheckID != nativeMaintenanceCheckID(kind, id) || check.Status != "critical" || check.Type != "maintenance" || check.Notes != notes || check.Output != "" || check.ServiceID != map[string]string{"node": "", "service": id}[kind] {
			t.Fatal("original maintenance identity/state/complete Notes changed", kind)
		}
	}
	return raw
}

func TestConsulNativeAgentMaintenanceOriginalReasonsDefaultsNoopsAndPreservation(t *testing.T) {
	s, base, _, _ := nativeDocumentSession(t, 8601)
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	id := "owned-" + t.Name() + "-雪%?#++"
	checkID := id + "-ordinary"
	nativeDo(t, base, nativeAgentRegister(t, nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": "owned maintenance", "ID": id, "Tags": []string{"original", "中文"}, "Meta": map[string]string{"exact": "18446744073709551615"}}), nil))
	nativeDo(t, base, nativeCheckRegister(t, nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": "owned ordinary", "ID": checkID, "ServiceID": id, "TTL": "10m", "Status": "passing", "Notes": "original ordinary notes 中文"})))
	originalCheck := nativeAgentServiceEvidenceMaps(t, base, true)[checkID]
	originalService := nativeAgentServiceEvidenceMaps(t, base, false)[id]
	for _, kind := range []string{"node", "service"} {
		defer nativeDo(t, base, nativeMaintenanceRequest(t, kind, id, false, nil))
		for _, reason := range []*string{nil, new(string), ttlTestNote("  original first\nsecond\r\nNUL\x00雪 %?#++  "), ttlTestNote(strings.Repeat("x", 8192))} {
			executeDocument(t, l, nativeMaintenanceRequest(t, kind, id, false, nil))
			request := nativeMaintenanceRequest(t, kind, id, true, reason)
			original := request.Clone()
			plan := documentPlan(t, l, request)
			want := "Maintenance mode is enabled for this " + kind + ", but no reason was provided. This is a default message."
			if reason != nil && *reason != "" {
				want = *reason
			}
			request.Query.Set("reason", "later edited reason")
			result, err := l.Execute(context.Background(), plan)
			if err != nil || !result.Completed || result.WriteOutcomeUnknown || result.HTTP == nil || !result.HTTP.Captured || !result.HTTP.EOF || !result.HTTP.Acknowledged || !result.HTTP.Succeeded || len(result.HTTP.Body) != 0 || !reflect.DeepEqual(url.Values(result.HTTP.Request.Query), original.Query) || result.HTTP.Request.Path != original.Path || result.Native != nil || result.Header != (contract.KVHeader{}) {
				t.Fatal("maintenance lost original request or empty local ACK", kind, err)
			}
			if _, err := l.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
				t.Fatal("original maintenance plan replayed", err)
			}
			first := nativeMaintenanceObserve(t, base, kind, id, true, want)
			executeDocument(t, l, nativeMaintenanceRequest(t, kind, id, true, ttlTestNote("later enable must preserve first reason")))
			if !bytes.Equal(first, nativeMaintenanceObserve(t, base, kind, id, true, want)) {
				t.Fatal("native repeated enable replaced original maintenance check")
			}
			for _, value := range []string{"0", "f", "F", "FALSE", "false", "False"} {
				r := nativeMaintenanceRequest(t, kind, id, false, ttlTestNote("disable reason is ignored natively"))
				r.Query.Set("enable", value)
				result := executeDocument(t, l, r)
				if !result.HTTP.Succeeded || url.Values(result.HTTP.Request.Query).Get("enable") != value {
					t.Fatal("native boolean text changed in complete receipt", value)
				}
				nativeMaintenanceObserve(t, base, kind, id, false, "")
			}
			if !bytes.Equal(originalCheck, nativeAgentServiceEvidenceMaps(t, base, true)[checkID]) || !bytes.Equal(originalService, nativeAgentServiceEvidenceMaps(t, base, false)[id]) {
				t.Fatal("maintenance changed original service or ordinary check")
			}
		}
		for _, value := range []string{"1", "t", "T", "TRUE", "true", "True"} {
			executeDocument(t, l, nativeMaintenanceRequest(t, kind, id, false, nil))
			r := nativeMaintenanceRequest(t, kind, id, true, ttlTestNote("original lexical "+value))
			r.Query.Set("enable", value)
			result := executeDocument(t, l, r)
			if !result.HTTP.Succeeded || url.Values(result.HTTP.Request.Query).Get("enable") != value {
				t.Fatal("native enable lexical text changed")
			}
			nativeMaintenanceObserve(t, base, kind, id, true, "original lexical "+value)
		}
		executeDocument(t, l, nativeMaintenanceRequest(t, kind, id, false, nil))
	}
}

func TestConsulNativeAgentMaintenanceOriginalScopesAndKnownDenials(t *testing.T) {
	s, base, cfg, _ := nativeDocumentSession(t, 8602)
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	id := "owned-" + t.Name()
	nativeDo(t, base, nativeAgentRegister(t, nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": id, "ID": id}), nil))
	for _, kind := range []string{"node", "service"} {
		defer nativeDo(t, base, nativeMaintenanceRequest(t, kind, id, false, nil))
	}
	for _, enable := range []bool{true, false} {
		r := nativeMaintenanceRequest(t, "service", "owned-no-such-original-service", enable, ttlTestNote("original missing-service reason"))
		result, err := l.Execute(context.Background(), documentPlan(t, l, r))
		if err == nil || !result.Completed || result.WriteOutcomeUnknown || result.HTTP == nil || result.HTTP.Status != 404 || !result.HTTP.Captured || !result.HTTP.EOF || !result.HTTP.Acknowledged || result.HTTP.Succeeded || len(result.HTTP.Body) == 0 {
			t.Fatal("missing original service lost proved denial", err)
		}
	}
	if cfg.Auth != "token" {
		return
	}
	credentials := map[string]string{"readonly": os.Getenv("NYADB_CONSUL_READ_TOKEN")}
	if credentials["readonly"] == "" {
		t.Fatal("token maintenance acceptance requires original read-only credential")
	}
	for _, role := range []string{"node", "service"} {
		rules := `node_prefix "" {policy="read"} service_prefix "" {policy="write"} agent_prefix "" {policy="read"}`
		if role == "node" {
			rules = `node_prefix "" {policy="write"} service_prefix "" {policy="read"} agent_prefix "" {policy="read"}`
		}
		policy := nativeAgentServiceEvidence(t, base, "/v1/acl/policy", nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": "owned-maintenance-" + role + "-" + t.Name(), "Rules": rules}))
		var identity struct{ ID string }
		if policy.Status != 200 || json.Unmarshal(policy.Body, &identity) != nil || identity.ID == "" {
			t.Fatal("owned maintenance policy creation failed")
		}
		token := nativeAgentServiceEvidence(t, base, "/v1/acl/token", nativeAgentServiceEvidenceJSON(t, map[string]any{"Policies": []map[string]string{{"ID": identity.ID}}}))
		var credential struct{ SecretID string }
		if token.Status != 200 || json.Unmarshal(token.Body, &credential) != nil || credential.SecretID == "" {
			t.Fatal("owned maintenance token creation failed")
		}
		credentials[role] = credential.SecretID
	}
	for role, credential := range credentials {
		limited, err := New(cfg, credential)
		if err != nil {
			t.Fatal(err)
		}
		defer limited.Close()
		for _, kind := range []string{"node", "service"} {
			for _, enable := range []bool{true, false} {
				// A denied disable must preserve an existing original check.
				nativeDo(t, base, nativeMaintenanceRequest(t, kind, id, !enable, ttlTestNote("original before ACL")))
				before := nativeAgentServiceEvidenceMaps(t, base, true)[nativeMaintenanceCheckID(kind, id)]
				result, err := limited.Do(context.Background(), nativeMaintenanceRequest(t, kind, id, enable, ttlTestNote("original ACL "+role)))
				if role == kind {
					if err != nil || !result.Captured || !result.EOF || !result.Acknowledged || !result.Succeeded || result.WriteOutcomeUnknown || len(result.Body) != 0 {
						t.Fatal("original maintenance ACL used foreign authority", role, kind, err)
					}
					nativeMaintenanceObserve(t, base, kind, id, enable, "original ACL "+role)
				} else if err == nil || result.Status != 403 || !result.Captured || !result.EOF || !result.Acknowledged || result.Succeeded || result.WriteOutcomeUnknown || len(result.Body) == 0 || !bytes.Equal(before, nativeAgentServiceEvidenceMaps(t, base, true)[nativeMaintenanceCheckID(kind, id)]) {
					t.Fatal("denied maintenance changed original check or lost complete denial", role, kind, err)
				}
			}
			nativeDo(t, base, nativeMaintenanceRequest(t, kind, id, false, nil))
		}
	}
}

func TestConsulNativeAgentMaintenanceCatalogConvergenceUsesOriginalAgentToken(t *testing.T) {
	base, cfg, _ := nativeClient(t)
	if cfg.Auth == "token" && os.Getenv("NYADB_CONSUL_AGENT_TOKEN_CONFIGURED") != "1" {
		t.Fatal("maintenance catalog-convergence acceptance requires an explicit owned Agent token")
	}
	id := "owned-" + t.Name()
	nativeDo(t, base, nativeAgentRegister(t, nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": id, "ID": id}), nil))
	nativeDo(t, base, nativeCheckRegister(t, nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": id + "-ordinary", "ID": id + "-ordinary", "ServiceID": id, "TTL": "10m", "Status": "passing"})))
	observe := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			r := Request{Method: "GET", Path: "/v1/health/service/" + url.PathEscape(id), Query: url.Values{"passing": {"true"}, "consistent": {""}}}
			result := nativeDo(t, base, r)
			var entries []struct{ Service struct{ ID string } }
			if json.Unmarshal(result.Body, &entries) != nil {
				t.Fatal("original catalog health response malformed")
			}
			present := false
			for _, entry := range entries {
				present = present || entry.Service.ID == id
			}
			if present == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("original catalog health did not converge; local ACK is separate", want)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	observe(true)
	for _, kind := range []string{"node", "service"} {
		defer nativeDo(t, base, nativeMaintenanceRequest(t, kind, id, false, nil))
		nativeDo(t, base, nativeMaintenanceRequest(t, kind, id, true, ttlTestNote("original catalog reason")))
		observe(false)
		nativeDo(t, base, nativeMaintenanceRequest(t, kind, id, false, nil))
		nativeMaintenanceObserve(t, base, kind, id, false, "")
		observe(true)
	}
}

func TestConsulNativeAgentMaintenanceLocalDisableACKKeepsIndependentStaleCatalog(t *testing.T) {
	s, base, cfg, _ := nativeDocumentSession(t, 8603)
	if cfg.Auth != "token" || os.Getenv("NYADB_CONSUL_AGENT_TOKEN_CONFIGURED") != "0" {
		t.Fatal("stale-catalog acceptance requires its separate owned no-Agent-token fixture")
	}
	l, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	id := "owned-" + t.Name()
	nativeDo(t, base, nativeAgentRegister(t, nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": id, "ID": id, "Meta": map[string]string{"original": "18446744073709551615"}}), nil))
	nativeDo(t, base, nativeCheckRegister(t, nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": id + "-ordinary", "ID": id + "-ordinary", "ServiceID": id, "TTL": "10m", "Status": "passing"})))
	originalService := nativeAgentServiceEvidenceMaps(t, base, false)[id]
	originalCheck := nativeAgentServiceEvidenceMaps(t, base, true)[id+"-ordinary"]
	full := Request{Method: http.MethodGet, Path: "/v1/health/service/" + url.PathEscape(id), Query: url.Values{"consistent": {""}}}
	passing := full.Clone()
	passing.Query.Set("passing", "true")
	for _, kind := range []string{"node", "service"} {
		reason := "original no-Agent-token " + kind + " 中文\n\r\x00"
		executeDocument(t, l, nativeMaintenanceRequest(t, kind, id, true, &reason))
		deadline := time.Now().Add(5 * time.Second)
		for {
			if bytes.Equal(bytes.TrimSpace(nativeDo(t, base, passing).Body), []byte("[]")) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("original critical maintenance never reached catalog")
			}
			time.Sleep(100 * time.Millisecond)
		}
		disabled := executeDocument(t, l, nativeMaintenanceRequest(t, kind, id, false, nil))
		original := disabled.Clone()
		if !disabled.Completed || disabled.WriteOutcomeUnknown || disabled.HTTP == nil || !disabled.HTTP.Acknowledged || !disabled.HTTP.Succeeded || len(disabled.HTTP.Body) != 0 || disabled.Native != nil {
			t.Fatal("complete local disable ACK was replaced with health state")
		}
		nativeMaintenanceObserve(t, base, kind, id, false, "")
		for observation := 0; observation < 3; observation++ {
			result := executeDocument(t, l, full)
			var entries []struct {
				Service struct{ ID string }
				Checks  []struct{ CheckID, Status, Notes string }
			}
			if result.HTTP == nil || json.Unmarshal(result.HTTP.Body, &entries) != nil {
				t.Fatal("original catalog response unavailable")
			}
			found := false
			for _, entry := range entries {
				for _, check := range entry.Checks {
					found = found || entry.Service.ID == id && check.CheckID == nativeMaintenanceCheckID(kind, id) && check.Status == "critical" && check.Notes == reason
				}
			}
			if !found || !bytes.Equal(bytes.TrimSpace(nativeDo(t, base, passing).Body), []byte("[]")) || !reflect.DeepEqual(original, disabled) || l.(*documentLease).recoveryOwned().Unknown != nil {
				t.Fatal("local ACK confirmed stale catalog, or later read changed original receipt")
			}
			time.Sleep(200 * time.Millisecond)
		}
		if !bytes.Equal(originalService, nativeAgentServiceEvidenceMaps(t, base, false)[id]) || !bytes.Equal(originalCheck, nativeAgentServiceEvidenceMaps(t, base, true)[id+"-ordinary"]) {
			t.Fatal("local disable changed original service or ordinary check")
		}
		// This cleanup uses explicit fixture management authority, outside the
		// production request codec. It never attributes catalog repair to disable.
		repaired := nativeAgentServiceEvidence(t, base, "/v1/catalog/deregister", nativeAgentServiceEvidenceJSON(t, map[string]string{"Node": s.KVScope().HTTP.AgentName, "CheckID": nativeMaintenanceCheckID(kind, id)}))
		if repaired.Status != http.StatusOK {
			t.Fatal("owned original catalog check cleanup failed")
		}
		if bytes.Equal(bytes.TrimSpace(nativeDo(t, base, passing).Body), []byte("[]")) {
			t.Fatal("explicit owned catalog repair did not restore passing health")
		}
	}
}

func TestConsulNativeAgentMaintenanceProcessedRepliesOriginalLeaseRecoveryAndNoReplay(t *testing.T) {
	base, cfg, token := nativeClient(t)
	for _, operation := range []string{"node-enable", "node-disable", "service-enable", "service-disable"} {
		kind := strings.Split(operation, "-")[0]
		enable := strings.HasSuffix(operation, "-enable")
		for _, mode := range []string{"lost", "partial", "http400", "http500", "http408", "invalid", "cancel", "close", "retire"} {
			t.Run(operation+"/"+mode, func(t *testing.T) {
				id := "owned-" + strings.ReplaceAll(t.Name(), "/", "-")
				nativeDo(t, base, nativeAgentRegister(t, nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": id, "ID": id}), nil))
				nativeDo(t, base, nativeMaintenanceRequest(t, kind, id, !enable, ttlTestNote("original before processed request")))
				defer nativeDo(t, base, nativeMaintenanceRequest(t, kind, id, false, nil))
				r := nativeMaintenanceRequest(t, kind, id, enable, ttlTestNote("original processed reason 中文\n\r\x00"))
				relay, calls, writes, processed := nativeAgentMutationFault(t, cfg, token, mode, r.Path)
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
				nativeMaintenanceObserve(t, base, kind, id, enable, "original processed reason 中文\n\r\x00")
				if original.HTTP.Request.Path != r.Path || !reflect.DeepEqual(url.Values(original.HTTP.Request.Query), r.Query) {
					t.Fatal("processed maintenance receipt lost original enable/reason/target")
				}
			})
		}
	}
}
