package consul

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func healthProjectionResult(t *testing.T, path, body string) contract.KVResult {
	t.Helper()
	r := Request{Method: "GET", Path: path}
	request, err := DocumentRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	return documentResult(request, Response{Request: r, Kind: "read", Protocol: "HTTP/1.1", Attempted: true, Ended: true, Captured: true, EOF: true, Acknowledged: true, Succeeded: true, Status: 200, Body: []byte(body)})
}

func TestHealthProjectionPreservesOriginalQualifiedChecksPresenceDurationsAndUnsignedIndices(t *testing.T) {
	body := `[{"Node":"one","CheckId":"same","Name":"","Status":"warning","Notes":null,"ServiceId":"original","ServiceName":"服务","ServiceTags":null,"Type":"ttl","Interval":"0s","Timeout":18446744073709551615,"CreateIndex":9007199254740993,"ModifyIndex":18446744073709551615,"Definition":{"TTL":"30s","Interval":9007199254740993,"Timeout":null,"future":[18446744073709551615]},"Namespace":"","future":null},{"Node":"two","CheckID":"same","Status":"future-status","ServiceTags":[],"Output":"","Definition":null}]`
	r := healthProjectionResult(t, "/v1/health/state/any", body)
	if r.HTTP.Health == nil || !r.HTTP.Health.Complete || !r.Completed || r.WriteOutcomeUnknown || !bytes.Equal(r.HTTP.Body, []byte(body)) || len(r.Native) != 0 {
		t.Fatal("health projection lost original HTTP identity/body/ACK")
	}
	rows := r.HTTP.Health.Checks
	if len(rows) != 2 || rows[0].ServiceID != "original" || rows[0].CreateIndex != 9007199254740993 || rows[0].ModifyIndex != math.MaxUint64 || rows[0].ServiceTags != nil || rows[1].ServiceTags == nil || rows[1].Status != "future-status" || rows[0].Interval.Text != "0s" || rows[0].Timeout.Nanoseconds == nil || *rows[0].Timeout.Nanoseconds != math.MaxUint64 || rows[0].Definition.Interval.Nanoseconds == nil || *rows[0].Definition.Interval.Nanoseconds != 9007199254740993 || rows[0].Definition.TTL.Text != "30s" || !slices.Contains(rows[0].NullFields, "Notes") || slices.Contains(rows[0].PresentFields, "Output") || !slices.Contains(rows[1].PresentFields, "Output") {
		t.Fatal("native values, aliases or absent/null/empty fields conflated", rows)
	}
	clone := r.Clone()
	*clone.HTTP.Health.Checks[0].Definition.Interval.Nanoseconds = 1
	*clone.HTTP.Health.Checks[0].Timeout.Nanoseconds = 1
	clone.HTTP.Health.Checks[0].NullFields[0] = "changed"
	clone.HTTP.Health.Checks[0].Definition.PresentFields[0] = "changed"
	if *rows[0].Timeout.Nanoseconds != math.MaxUint64 || *rows[0].Definition.Interval.Nanoseconds != 9007199254740993 || slices.Contains(rows[0].NullFields, "changed") || slices.Contains(rows[0].Definition.PresentFields, "changed") {
		t.Fatal("cloned health definitions alias original values")
	}
	without := r.Clone()
	without.HTTP.Health = nil
	if resultCost(&r)-resultCost(&without) != healthProjectionCost(*r.HTTP.Health) {
		t.Fatal("health projection omitted from document/session accounting")
	}
	c := contract.KVHTTPHealthCheck{CheckID: "_service_maintenance:original", Status: "critical"}
	if !c.Maintenance() || c.Status != "critical" {
		t.Fatal("local maintenance classification overwrote native status")
	}
	// Equal Node/CheckID in distinct returned namespaces is not the same row.
	qualified := healthProjectionResult(t, "/v1/health/state/any", `[{"Node":"one","CheckID":"same","Namespace":"a"},{"Node":"one","CheckID":"same","Namespace":"b"}]`)
	if qualified.HTTP.Health == nil || len(qualified.HTTP.Health.Checks) != 2 {
		t.Fatal("qualified checks collapsed across returned namespaces")
	}
}

func TestHealthProjectionServiceInstancesRetainOriginalNestedMapsAndRepeatedNodeChecks(t *testing.T) {
	instance := `{"Node":{"Id":"native-node-id","Node":"one","Address":"127.0.0.1","TaggedAddresses":{"lan":"127.0.0.1"},"Meta":{"native":"value"},"NodeMeta":{"legacy":"value"},"CreateIndex":18446744073709551615},"Service":{"ID":"original-%s","Service":"服务","Address":null,"Tags":[],"Meta":{"中文":"原值"},"TaggedAddresses":{"lan":{"Address":"127.0.0.1","Port":0}},"Weights":{"Passing":1,"Warning":0},"Port":65535,"EnableTagOverride":false,"ModifyIndex":9007199254740993},"Checks":[{"Node":"one","CheckID":"serfHealth","Status":"passing","ServiceTags":null}]}`
	first := strings.Replace(instance, "%s", "one", 1)
	second := strings.Replace(instance, "%s", "two", 1)
	body := "[" + first + "," + second + "]"
	r := healthProjectionResult(t, "/v1/health/service/"+"%E6%9C%8D%E5%8A%A1", body)
	if r.HTTP.Health == nil || r.HTTP.Health.Kind != "service-instances" || len(r.HTTP.Health.Instances) != 2 {
		t.Fatal("valid service instances or repeated node checks rejected", r.ProjectionError)
	}
	i := r.HTTP.Health.Instances[0]
	if i.Node.ID != "native-node-id" || i.Node.CreateIndex != math.MaxUint64 || i.Node.Meta["native"] != "value" || i.Node.NodeMeta["legacy"] != "value" || i.Service.Port != 65535 || i.Service.ModifyIndex != 9007199254740993 || i.Service.Tags == nil || !slices.Contains(i.Service.NullFields, "Address") || i.Service.TaggedAddresses["lan"].Port != 0 {
		t.Fatal("original nested values or presence lost", i)
	}
	clone := r.Clone()
	changed := &clone.HTTP.Health.Instances[0]
	changed.Node.Meta["native"] = "changed"
	changed.Node.NodeMeta["legacy"] = "changed"
	changed.Service.Meta["中文"] = "changed"
	changed.Service.Weights.PresentFields[0] = "changed"
	a := changed.Service.TaggedAddresses["lan"]
	a.PresentFields[0] = "changed"
	changed.Service.TaggedAddresses["lan"] = a
	changed.Checks[0].PresentFields[0] = "changed"
	if i.Node.Meta["native"] == "changed" || i.Node.NodeMeta["legacy"] == "changed" || i.Service.Meta["中文"] == "changed" || slices.Contains(i.Service.Weights.PresentFields, "changed") || slices.Contains(i.Service.TaggedAddresses["lan"].PresentFields, "changed") || slices.Contains(i.Checks[0].PresentFields, "changed") {
		t.Fatal("nested clone shares original service maps/checks")
	}
	without := r.Clone()
	without.HTTP.Health = nil
	if resultCost(&r)-resultCost(&without) != healthProjectionCost(*r.HTTP.Health) {
		t.Fatal("nested service evidence not charged exactly")
	}
}

func TestHealthProjectionRejectsMalformedForeignAndBoundedClaimsWithoutChangingNativeACK(t *testing.T) {
	for name, body := range map[string]string{
		"null": "null", "object": "{}", "null-row": "[null]", "no-node": `[{"CheckID":"check"}]`, "null-id": `[{"Node":"one","CheckID":null}]`,
		"duplicate-field": `[{"Node":"one","CheckID":"check","CheckID":"other"}]`, "duplicate-qualified-check": `[{"Node":"one","CheckID":"check"},{"Node":"one","CheckID":"check"}]`,
		"alias-conflict": `[{"Node":"one","CheckID":"check","CheckId":"other"}]`, "service-alias-conflict": `[{"Node":"one","CheckID":"check","ServiceID":"one","ServiceId":"other"}]`,
		"unsigned-overflow": `[{"Node":"one","CheckID":"check","ModifyIndex":18446744073709551616}]`, "fraction": `[{"Node":"one","CheckID":"check","CreateIndex":1.5}]`,
		"port-overflow": `[{"Node":"one","CheckID":"check","ExposedPort":65536}]`, "negative-duration": `[{"Node":"one","CheckID":"check","Definition":{"TTL":-1}}]`,
		"top-duration-overflow": `[{"Node":"one","CheckID":"check","Timeout":18446744073709551616}]`, "top-negative-duration": `[{"Node":"one","CheckID":"check","Interval":-1}]`,
		"null-tag": `[{"Node":"one","CheckID":"check","ServiceTags":[null]}]`, "trailing": "[] []",
	} {
		t.Run(name, func(t *testing.T) {
			r := healthProjectionResult(t, "/v1/health/state/any", body)
			if r.HTTP.Health != nil || r.ProjectionError == "" || !r.Completed || !r.HTTP.Acknowledged || !r.HTTP.Succeeded || r.WriteOutcomeUnknown || !bytes.Equal(r.HTTP.Body, []byte(body)) {
				t.Fatal("optional projection replaced original native receipt")
			}
		})
	}
	for path, body := range map[string]string{
		"/v1/health/node/original":    `[{"Node":"foreign","CheckID":"check"}]`,
		"/v1/health/checks/original":  `[{"Node":"one","CheckID":"check","ServiceName":"foreign"}]`,
		"/v1/health/state/passing":    `[{"Node":"one","CheckID":"check","Status":"warning"}]`,
		"/v1/health/service/original": `[{"Node":{"Node":"one"},"Service":{"ID":"original","Service":"foreign"}}]`,
	} {
		r := healthProjectionResult(t, path, body)
		if r.HTTP.Health != nil || r.ProjectionError == "" || !r.Completed {
			t.Fatal("foreign target promoted into current health projection", path)
		}
	}
	// One original response containing nested service checks has one shared
	// row bound; it cannot multiply the cap by creating many sub-arrays.
	checks := make([]map[string]any, entryLimit)
	for i := range checks {
		checks[i] = map[string]any{"Node": "one", "CheckID": strconv.Itoa(i)}
	}
	body, _ := json.Marshal([]any{map[string]any{"Node": map[string]any{"Node": "one"}, "Service": map[string]any{"ID": "id", "Service": "original"}, "Checks": checks}})
	if _, err := decodeHealthProjection(body, healthTarget{"service", "original"}); !errors.Is(err, ErrBounds) {
		t.Fatal("nested response exceeded original shared row bound", err)
	}
	large := `[{"Node":"one","CheckID":"check","Output":"` + strings.Repeat("x", projectionBudget) + `"}]`
	if _, err := decodeHealthProjection([]byte(large), healthTarget{"state", "any"}); !errors.Is(err, ErrBounds) {
		t.Fatal("typed health payload exceeded original32MiB projection bound", err)
	}
	r := healthProjectionResult(t, "/v1/health/state/any", `[ ]`)
	if r.HTTP.Health == nil || r.HTTP.Health.Checks == nil || len(r.HTTP.Health.Checks) != 0 {
		t.Fatal("valid whitespace empty array rejected")
	}
	r.HTTP.Metadata.ACLFiltered = new(true)
	out := documentResult(r.Request, Response{Request: Request{Method: "GET", Path: "/v1/health/state/any"}, Status: 200, Body: []byte(`[]`), Captured: true, EOF: true, Acknowledged: true, Succeeded: true, Metadata: Metadata{ACLFiltered: new(true)}})
	if out.HTTP.Health == nil || !out.HTTP.Health.ProjectionComplete || out.HTTP.Health.Complete || !out.HTTP.Acknowledged {
		t.Fatal("ACL boundary changed native ACK or claimed full scoped inventory")
	}
}
