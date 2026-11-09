package neo4j

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestGraphPropertyExtendedListParametersAndSharedGridCaptureCompleteMembers(t *testing.T) {
	for _, sample := range extendedListSamples() {
		before, _ := nativeExtendedListSample(t, sample.kind, sample.first)
		after, _ := nativeExtendedListSample(t, sample.kind, sample.after)
		r := propertyRequestTest()
		r.Before, r.After = before.Clone(), after.Clone()
		q, err := propertyWriteQuery(r)
		if err != nil || q.Parameters["type"] != "LIST<"+sample.nativeType+" NOT NULL> NOT NULL" {
			t.Fatal("extended list lost native comparison type", sample.name, err)
		}
		r.Before.Values[0].Text = "later"
		if len(r.After.Values[0].Properties) > 0 {
			r.After.Values[0].Properties[0].Value.Text = "later"
		} else {
			r.After.Values[0].Text = "later"
		}
		for param, want := range map[string]contract.GraphValue{"before": before, "after": after} {
			budget := valueBudget{limit: 1 << 20}
			captured, _, e := budget.capture(q.Parameters[param], true, 0)
			if e != nil || !reflect.DeepEqual(captured, want) {
				t.Fatal("native list parameters shared caller components", sample.name, param, e)
			}
		}
		raw, _ := json.Marshal(after)
		value, param, e := dataPropertyValue(contract.DataValue{Kind: contract.JSON, Text: string(raw)})
		budget := valueBudget{limit: 1 << 20}
		captured, _, captureErr := budget.capture(param, true, 0)
		if e != nil || captureErr != nil || !reflect.DeepEqual(value, after) || !reflect.DeepEqual(captured, after) {
			t.Fatal("shared grid lost complete extended list", sample.name, e, captureErr)
		}
	}
}
