package kvworkspace

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func healthRequestToolsTestState(t *testing.T) (*State, *Document, *toolTestLease) {
	s, d, _, l := requestToolTestState(t)
	for _, kind := range []contract.KVRequestToolKind{contract.KVToolHTTPHealthState, contract.KVToolHTTPHealthNode, contract.KVToolHTTPHealthChecks, contract.KVToolHTTPHealthService} {
		d.presentation.Tools = append(d.presentation.Tools, contract.KVRequestTool{Kind: kind})
	}
	return s, d, l
}

func TestHealthToolDraftsRetainPartialParametersAndGenerationNeverDispatches(t *testing.T) {
	s, d, l := healthRequestToolsTestState(t)
	kind := contract.KVToolHTTPHealthService
	s.OpenRequestTool(d, kind)
	input := contract.KVRequestToolInput{HTTPHealth: contract.KVHTTPHealthToolInput{Service: "原服务", Passing: "false", IncludeFilter: true, Filter: "partial `", IncludeNear: true, Near: "", Index: "18446744073709551615", Wait: "partial-"}}
	s.SetRequestToolInput(d, kind, input)
	original := d.Request()
	raw, err := encodeRequestToolInput(kind, input)
	if err != nil {
		t.Fatal(err)
	}
	var record struct{ Input contract.KVRequestToolInput }
	if json.Unmarshal(raw, &record) != nil || record.Input != input {
		t.Fatal("partial health input or option presence lost")
	}
	s.OpenRequestTool(d, contract.KVToolHTTPHealthNode)
	s.OpenRequestTool(d, kind)
	got, _ := d.RequestToolInput(kind)
	if got != input || d.Request() != original || !d.Dirty() || l.executes.Load() != 0 || l.connects.Load() != 0 {
		t.Fatal("tool switch discarded health draft or dispatched")
	}
	active := false
	s.deps.Active = func(*Document) bool { return active }
	s.GenerateRequestTool(d)
	if d.Request() != original {
		t.Fatal("parked form changed request")
	}
	active = true
	s.GenerateRequestTool(d)
	if d.Request() == original || d.Busy() || d.confirmation != nil || l.executes.Load() != 0 || l.connects.Load() != 0 {
		t.Fatal("health text generation acquired execution authority")
	}
	s.OpenRequestTool(d, kind)
	changed := input
	changed.HTTPHealth.Filter = strings.Repeat("x", inputLimit+1)
	s.SetRequestToolInput(d, kind, changed)
	got, _ = d.RequestToolInput(kind)
	if got != input || d.error == "" {
		t.Fatal("health fields bypassed complete draft bound")
	}
	invalid := input
	invalid.HTTPHealth.Near = string([]byte{255})
	if _, err := encodeRequestToolInput(kind, invalid); err == nil {
		t.Fatal("invalid Unicode silently normalized in health export")
	}
}
