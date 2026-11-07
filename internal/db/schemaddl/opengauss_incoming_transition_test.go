package schemaddl

import (
	"encoding/json"
	"testing"
)

func TestOpenGaussIncomingReboundDependenciesKeepContinuingUnknownProperties(t *testing.T) {
	before := map[string]any{"rows": []any{
		map[string]any{"classid": json.Number("2606"), "objid": "incoming:1", "objsubid": json.Number("0"), "refclassid": json.Number("1259"), "refobjid": "@child", "refobjsubid": "parent_id", "deptype": "a", "opaque": json.Number("90071992547409931234567890.12345678901234567890")},
		map[string]any{"classid": json.Number("2606"), "objid": "incoming:1", "objsubid": json.Number("0"), "refclassid": json.Number("2617"), "refobjid": json.Number("410"), "refobjsubid": json.Number("0"), "deptype": "n"},
	}, "aggregate_opaque": json.Number("90071992547409931234567891")}
	prototype := map[string]any{"rows": []any{
		map[string]any{"classid": json.Number("2606"), "objid": "incoming:1", "objsubid": json.Number("0"), "refclassid": json.Number("1259"), "refobjid": "@child", "refobjsubid": "parent_id", "deptype": "a", "opaque": json.Number("90071992547409931234567890.12345678901234567891")},
		map[string]any{"classid": json.Number("2606"), "objid": "incoming:1", "objsubid": json.Number("0"), "refclassid": json.Number("2617"), "refobjid": json.Number("411"), "refobjsubid": json.Number("0"), "deptype": "n"},
	}, "aggregate_opaque": json.Number("1")}
	input := Signature(before)
	expected, err := gaussIncomingExpectedDependencies(before, prototype, false)
	if err != nil || expected["aggregate_opaque"] != before["aggregate_opaque"] {
		t.Fatal("incoming dependency aggregate lost preserved values", err)
	}
	rows := expected["rows"].([]any)
	retained, added := false, false
	for _, raw := range rows {
		row := raw.(map[string]any)
		if row["refobjid"] == "@child" {
			retained = row["opaque"] == json.Number("90071992547409931234567890.12345678901234567890")
		}
		if row["refobjid"] == json.Number("411") {
			added = true
		}
		if row["refobjid"] == json.Number("410") {
			t.Fatal("removed operator dependency survived independent rebind")
		}
	}
	if !retained || !added || Signature(before) != input {
		t.Fatal("incoming typed edge changes replaced preserved unknown fields or inputs")
	}
}
