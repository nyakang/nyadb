package consul

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func agentProjectionResult(t *testing.T, path, body string) contract.KVResult {
	t.Helper()
	r := Request{Method: "GET", Path: path}
	request, err := DocumentRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	return documentResult(request, Response{Request: r, Kind: "read", Status: 200, Attempted: true, Ended: true, Captured: true, EOF: true, Acknowledged: true, Succeeded: true, Body: []byte(body)})
}

func TestAgentProjectionRetainsOriginalMapOrderValuesPresenceAndCompleteJSON(t *testing.T) {
	first := `{"ID":"z:原","Service":"服务","Tags":[],"Meta":{"原":"值"},"Address":null,"Port":65535,"future":{"exact":18446744073709551615}}`
	second := `{"Id":"a","Service":"服务","Tags":null,"Meta":{},"Port":0,"Namespace":""}`
	body := ` {"z:原": ` + first + `, "a": ` + second + `} `
	r := agentProjectionResult(t, "/v1/agent/services", body)
	if r.HTTP.Agent == nil || !r.HTTP.Agent.ProjectionComplete || !r.HTTP.Agent.Complete || len(r.HTTP.Agent.Rows) != 2 || !r.Completed || !bytes.Equal(r.HTTP.Body, []byte(body)) || len(r.Native) != 0 {
		t.Fatal("original agent map body/acknowledgement unavailable", r.ProjectionError)
	}
	rows := r.HTTP.Agent.Rows
	if rows[0].Key != "z:原" || rows[1].Key != "a" || rows[0].Port != 65535 || rows[0].Tags == nil || rows[1].Tags != nil || rows[1].Meta == nil || !slices.Contains(rows[0].NullFields, "Address") || slices.Contains(rows[1].PresentFields, "Address") || !bytes.Equal(rows[0].Raw, []byte(first)) {
		t.Fatal("map order, exact original JSON or field presence changed", rows)
	}
	clone := r.Clone()
	clone.HTTP.Agent.Rows[0].Raw[0] = 'x'
	clone.HTTP.Agent.Rows[0].Meta["原"] = "changed"
	clone.HTTP.Agent.Rows[0].PresentFields[0] = "changed"
	if rows[0].Raw[0] != '{' || rows[0].Meta["原"] != "值" || slices.Contains(rows[0].PresentFields, "changed") {
		t.Fatal("clone shares original Agent entry bytes or metadata")
	}
	without := r.Clone()
	without.HTTP.Agent = nil
	if resultCost(&r)-resultCost(&without) != agentProjectionCost(*r.HTTP.Agent) {
		t.Fatal("Agent evidence omitted from original lease accounting")
	}
	// Local-agent checks may omit Node, unlike catalog health checks.
	check := `{"c":{"CheckId":"c","Name":"","ServiceId":"s","ServiceName":"服务","Status":"future","Output":null,"ServiceTags":[],"Definition":{"TTL":"30s","future":9007199254740993}}}`
	r = agentProjectionResult(t, "/v1/agent/checks", check)
	if r.HTTP.Agent == nil || r.HTTP.Agent.Rows[0].Node != "" || r.HTTP.Agent.Rows[0].ServiceID != "s" || r.HTTP.Agent.Rows[0].Status != "future" || !slices.Contains(r.HTTP.Agent.Rows[0].NullFields, "Output") {
		t.Fatal("local checks were treated as catalog checks", r.ProjectionError)
	}
}

func TestAgentProjectionRejectsMalformedForeignAndOversizedMapsWithoutChangingACK(t *testing.T) {
	for _, body := range []string{
		"null", "[]", `{"s":null}`, `{"s":{"ID":"other","Service":"svc"}}`,
		`{"s":{"ID":"s","Id":"other","Service":"svc"}}`, `{"s":{"ID":"s","Service":null}}`,
		`{"s":{"ID":"s","Service":"svc","ID":"s"}}`, `{"s":{"ID":"s","Service":"svc"},"s":{"ID":"s","Service":"svc"}}`,
		`{"s":{"ID":"s","Service":"svc","Port":65536}}`, `{"s":{"ID":"s","Service":"svc","Port":1.5}}`,
		`{"s":{"ID":"s","Service":"svc","Tags":[null]}}`, `{"s":{"ID":"s","Service":"svc","Meta":{"x":null}}}`, "{} {}",
	} {
		r := agentProjectionResult(t, "/v1/agent/services", body)
		if r.HTTP.Agent != nil || r.ProjectionError == "" || !r.Completed || !r.HTTP.Acknowledged || !r.HTTP.Succeeded || r.WriteOutcomeUnknown || !bytes.Equal(r.HTTP.Body, []byte(body)) {
			t.Fatal("optional Agent projection replaced the original receipt", body)
		}
	}
	large := `{"s":{"ID":"s","Service":"svc","future":"` + strings.Repeat("x", projectionBudget) + `"}}`
	if _, err := decodeAgentProjection([]byte(large), "services"); !errors.Is(err, ErrBounds) {
		t.Fatal("Agent projection exceeded original32MiB evidence budget", err)
	}
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i <= entryLimit; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"%d":{"ID":"%d","Service":"svc"}`, i, i)
	}
	b.WriteByte('}')
	if _, err := decodeAgentProjection([]byte(b.String()), "services"); !errors.Is(err, ErrBounds) {
		t.Fatal("oversized Agent map was truncated or published", err)
	}
}

func TestAgentProjectionFilteredEmptyMapsRetainOriginalScopeAndAcknowledgement(t *testing.T) {
	for _, kind := range []string{"services", "checks"} {
		r := agentProjectionResult(t, "/v1/agent/"+kind, "{ }")
		if r.HTTP.Agent == nil || r.HTTP.Agent.Rows == nil || len(r.HTTP.Agent.Rows) != 0 || !r.HTTP.Agent.Complete {
			t.Fatal("valid empty original map lost")
		}
		r.HTTP.Metadata.ACLFiltered = new(true)
		projectDocumentAgent(&r)
		if r.HTTP.Agent == nil || !r.HTTP.Agent.ProjectionComplete || r.HTTP.Agent.Complete || !r.Completed {
			t.Fatal("ACL-filtered empty map became a global absence claim")
		}
		for _, damage := range []func(*contract.KVHTTPResult){
			func(h *contract.KVHTTPResult) { h.Status = 403 },
			func(h *contract.KVHTTPResult) { h.Captured = false },
			func(h *contract.KVHTTPResult) { h.EOF = false },
			func(h *contract.KVHTTPResult) { h.Acknowledged = false },
			func(h *contract.KVHTTPResult) { h.Succeeded = false },
			func(h *contract.KVHTTPResult) { h.Request.Body = []byte{} },
		} {
			bad := r.Clone()
			bad.HTTP.Agent = nil
			damage(bad.HTTP)
			projectDocumentAgent(&bad)
			if bad.HTTP.Agent != nil {
				t.Fatal("failed/incomplete/body-carrying receipt promoted")
			}
		}
	}
	// Native maps can contain more than the per-row field bound of256.
	rows := map[string]any{}
	for i := 0; i < 300; i++ {
		id := fmt.Sprint(i)
		rows[id] = map[string]any{"ID": id, "Service": "svc"}
	}
	body, _ := json.Marshal(rows)
	r := agentProjectionResult(t, "/v1/agent/services", string(body))
	if r.HTTP.Agent == nil || len(r.HTTP.Agent.Rows) != 300 {
		t.Fatal("original Agent map confused with one row's field bound", r.ProjectionError)
	}
}
