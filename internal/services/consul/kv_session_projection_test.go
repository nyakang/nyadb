package consul

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func sessionProjectionResponse(body string) Response {
	return Response{Protocol: "consul-http", Request: Request{Method: "GET", Path: "/v1/session/list"}, Status: 200, Body: []byte(body), Attempted: true, Ended: true, Captured: true, EOF: true, Acknowledged: true, Succeeded: true}
}
func TestSessionProjectionPreservesPresenceNullChecksAndUnsignedFields(t *testing.T) {
	id := sessionToolID
	body := `[{"ID":"` + id + `","Name":"","Node":null,"TTL":"10m","Behavior":"release","LockDelay":0,"CreateIndex":9007199254740993,"ModifyIndex":18446744073709551615,"Checks":null,"NodeChecks":[],"ServiceChecks":[{"ID":"original/check","Namespace":null,"future":{"value":18446744073709551615}}],"Namespace":"","future":null}]`
	native := sessionProjectionResponse(body)
	request, err := DocumentRequest(native.Request)
	if err != nil {
		t.Fatal(err)
	}
	result := documentResult(request, native)
	if result.HTTP.Sessions == nil || len(result.HTTP.Sessions.Rows) != 1 || !result.Completed || !result.HTTP.Succeeded || !bytes.Equal(result.HTTP.Body, []byte(body)) {
		t.Fatal("complete original Session projection missing")
	}
	row := result.HTTP.Sessions.Rows[0]
	if row.ID != id || row.Name != "" || row.LockDelay != 0 || row.CreateIndex != 9007199254740993 || row.ModifyIndex != math.MaxUint64 || row.NodeChecks == nil || row.Checks != nil || !slices.Equal(row.NullFields, []string{"Checks", "Node", "future"}) || slices.Contains(row.PresentFields, "Partition") {
		t.Fatal("missing/null/empty/unsigned Session fields conflated", row)
	}
	if len(row.ServiceChecks) != 1 || !slices.Equal(row.ServiceChecks[0].NullFields, []string{"Namespace"}) || !slices.Contains(row.ServiceChecks[0].PresentFields, "future") {
		t.Fatal("original service check presence changed")
	}
	cloned := result.Clone()
	cloned.HTTP.Sessions.Rows[0].NullFields[0] = "changed"
	cloned.HTTP.Sessions.Rows[0].ServiceChecks[0].PresentFields[0] = "changed"
	if row.NullFields[0] == "changed" || row.ServiceChecks[0].PresentFields[0] == "changed" {
		t.Fatal("Session projection clone shares original mutable slices")
	}
	without := result.Clone()
	without.HTTP.Sessions = nil
	if resultCost(&result)-resultCost(&without) != sessionProjectionCost(row) {
		t.Fatal("original Session projection not included in receipt accounting")
	}
}

func TestSessionProjectionFailuresRetainOriginalACKAndCompleteBody(t *testing.T) {
	id := sessionToolID
	for name, body := range map[string]string{
		"null": "null", "object": "{}", "null-row": "[null]", "missing-id": "[{}]", "null-id": `[{"ID":null}]`,
		"duplicate-id":        `[{"ID":"` + id + `"},{"ID":"` + id + `"}]`,
		"duplicate-field":     `[{"ID":"` + id + `","Name":"first","Name":"second"}]`,
		"unsigned-overflow":   `[{"ID":"` + id + `","ModifyIndex":18446744073709551616}]`,
		"unsigned-fraction":   `[{"ID":"` + id + `","LockDelay":1.2}]`,
		"wrong-type":          `[{"ID":"` + id + `","TTL":10}]`,
		"null-check":          `[{"ID":"` + id + `","NodeChecks":[null]}]`,
		"wrong-service-check": `[{"ID":"` + id + `","ServiceChecks":[{"ID":null}]}]`,
		"invalid-unicode":     `[{"ID":"` + id + `","Name":"\ud800"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			native := sessionProjectionResponse(body)
			request, _ := DocumentRequest(native.Request)
			result := documentResult(request, native)
			if result.HTTP.Sessions != nil || result.HTTP.ProjectionError == "" || !result.Completed || !result.HTTP.Acknowledged || !result.HTTP.Succeeded || result.WriteOutcomeUnknown || !bytes.Equal(result.HTTP.Body, native.Body) {
				t.Fatal("optional projection changed original HTTP authority")
			}
		})
	}
	// Empty arrays remain an available observation, distinct from malformed/null.
	empty := documentResult(contract.KVRequest{}, sessionProjectionResponse("[]"))
	if empty.HTTP.Sessions == nil || empty.HTTP.Sessions.Rows == nil || len(empty.HTTP.Sessions.Rows) != 0 {
		t.Fatal("empty original list became unavailable")
	}
}

func TestSessionProjectionTargetsAreOriginalReadsAndInfoUUIDCannotChange(t *testing.T) {
	body := `[{"ID":"` + sessionToolID + `"}]`
	for _, path := range []string{"/v1/session/create", "/v1/session/renew/" + sessionToolID, "/v1/catalog/nodes", "/v1/session/info/INVALID"} {
		native := sessionProjectionResponse(body)
		native.Request.Path = path
		result := documentResult(contract.KVRequest{}, native)
		if result.HTTP.Sessions != nil {
			t.Fatal("foreign operation guessed as Session list", path)
		}
	}
	native := sessionProjectionResponse(body)
	native.Request.Path = "/v1/session/info/00000000-0000-0000-0000-000000000000"
	result := documentResult(contract.KVRequest{}, native)
	if result.HTTP.Sessions != nil || result.HTTP.ProjectionError == "" {
		t.Fatal("info projection replaced originally requested UUID")
	}
	for _, change := range []func(*Response){func(r *Response) { r.Captured = false }, func(r *Response) { r.EOF = false }, func(r *Response) { r.Status = 403; r.Succeeded = false }, func(r *Response) { r.Acknowledged = false }} {
		native := sessionProjectionResponse(body)
		change(&native)
		out := documentResult(contract.KVRequest{}, native)
		if out.HTTP.Sessions != nil {
			t.Fatal("incomplete/denied original response acquired a projection")
		}
	}
	native = sessionProjectionResponse(body)
	native.Request.Path = "/v1/session/node/original%20node"
	filtered := true
	native.Metadata.ACLFiltered = &filtered
	result = documentResult(contract.KVRequest{}, native)
	if result.HTTP.Sessions == nil || result.HTTP.Metadata.ACLFiltered == nil || !*result.HTTP.Metadata.ACLFiltered {
		t.Fatal("ACL filtering marker lost")
	}
}

func TestSessionProjectionBoundsRejectWholeProjectionWithoutPartialRows(t *testing.T) {
	rows := make([]string, sessionProjectionLimit+1)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"ID":"%08x-0000-0000-0000-000000000000"}`, i)
	}
	body := "[" + strings.Join(rows, ",") + "]"
	native := sessionProjectionResponse(body)
	out := documentResult(contract.KVRequest{}, native)
	if out.HTTP.Sessions != nil || out.HTTP.ProjectionError == "" || !out.HTTP.Acknowledged || !bytes.Equal(out.HTTP.Body, native.Body) {
		t.Fatal("row limit truncated original Session projection")
	}
	checks := make([]string, sessionProjectionLimit+1)
	raw, _ := json.Marshal(checks)
	body = `[{"ID":"` + sessionToolID + `","NodeChecks":` + string(raw) + `}]`
	native = sessionProjectionResponse(body)
	out = documentResult(contract.KVRequest{}, native)
	if out.HTTP.Sessions != nil || out.HTTP.ProjectionError == "" || !out.HTTP.Acknowledged {
		t.Fatal("nested check budget bypassed")
	}
	fields := map[string]any{"ID": sessionToolID}
	for i := 0; i < 256; i++ {
		fields[fmt.Sprintf("future%d", i)] = nil
	}
	raw, _ = json.Marshal([]any{fields})
	native = sessionProjectionResponse(string(raw))
	out = documentResult(contract.KVRequest{}, native)
	if out.HTTP.Sessions != nil || out.HTTP.ProjectionError == "" || !out.HTTP.Acknowledged {
		t.Fatal("extension field budget bypassed")
	}
}

func TestSessionProjectionTotalBudgetRejectsCompleteListWithoutRetainingRows(t *testing.T) {
	// Every individual object fits. Four valid rows exceed the combined typed
	// receipt budget only after the preceding rows have been decoded.
	name := strings.Repeat("x", projectionBudget/4)
	rows := make([]string, 4)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"ID":"%08x-0000-0000-0000-000000000000","Name":"%s"}`, i, name)
	}
	raw := []byte("[" + strings.Join(rows, ",") + "]")
	result, err := decodeSessionProjection(raw, "")
	if !errors.Is(err, ErrBounds) || result.Rows != nil {
		t.Fatal("combined projection budget retained a partial Session list", err)
	}
}
