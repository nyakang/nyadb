package databend

import (
	"encoding/json"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"testing"
)

func TestDDLReceiptRequiresOriginalCompleteEmptyResult(t *testing.T) {
	valid := `{"columns":[],"column_types":[],"rows":[[]],"affected_rows":0,"truncated":false}`
	receipt := agentbridge.Receipt{Dispatched: true, ExecutorAcknowledged: true, Result: json.RawMessage(valid)}
	if !acknowledgedDDL(receipt) {
		t.Fatal("native empty DDL result rejected")
	}
	for _, raw := range []string{`{}`, `null`, `{"columns":[],"column_types":[],"rows":[],"affected_rows":0,"truncated":false}`, `{"columns":[],"column_types":[],"rows":[null],"affected_rows":0,"truncated":false}`, `{"columns":[],"column_types":[],"rows":[[]],"affected_rows":1,"truncated":false}`, `{"columns":[],"column_types":[],"rows":[[]],"affected_rows":0,"truncated":true}`} {
		r := receipt.Clone()
		r.Result = json.RawMessage(raw)
		if acknowledgedDDL(r) {
			t.Fatal("incomplete original accepted", raw)
		}
	}
	for _, mutate := range []func(*agentbridge.Receipt){func(r *agentbridge.Receipt) { r.Dispatched = false }, func(r *agentbridge.Receipt) { r.ExecutorAcknowledged = false }} {
		r := receipt.Clone()
		mutate(&r)
		if acknowledgedDDL(r) {
			t.Fatal("original acknowledgement missing")
		}
	}
}
