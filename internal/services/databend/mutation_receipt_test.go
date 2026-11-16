package databend

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

func mutationTestReceipt(label, value string) agentbridge.Receipt {
	return agentbridge.Receipt{Dispatched: true, ExecutorAcknowledged: true,
		Result: json.RawMessage(`{"columns":["number of rows ` + label + `"],"column_types":["uint64"],"rows":[[` + value + `]],"affected_rows":0,"execution_time_ms":1,"truncated":false}`)}
}

func TestDatabendNativeMutationCountDoesNotInferOneRowOrTransaction(t *testing.T) {
	for _, v := range []struct {
		kind, label, value string
		count              int64
	}{{"insert", "inserted", "1", 1}, {"update", "updated", "2", 2}, {"delete", "deleted", "0", 0}} {
		original := mutationTestReceipt(v.label, v.value)
		original.CancellationRequested = true
		r, err := DecodeMutationReceipt(v.kind, original)
		if err != nil || r.State != contract.DataWriteAcknowledged || r.AffectedRows != v.count || string(r.Original.Result) != string(original.Result) || !r.Original.CancellationRequested {
			t.Fatal("native acknowledged count or original cancellation receipt changed", r, err)
		}
		if string(original.Result) != string(r.Original.Result) || !json.Valid(r.Original.Result) {
			t.Fatal("native observation rewrote common affected_rows")
		}
		r.Original.Result[0] = '['
		if original.Result[0] != '{' {
			t.Fatal("mutation observation shares original bytes")
		}
	}
}

func TestDatabendUncertainMutationNeverBecomesAcknowledgedFromAnExecutorError(t *testing.T) {
	for _, outcome := range []string{"unknown", "completed", "not_started"} {
		raw := agentbridge.Receipt{Dispatched: true, ExecutorAcknowledged: true,
			RemoteError: &agentbridge.RemoteError{OperationOutcome: outcome, SessionDisposition: "keep"}}
		r, err := DecodeMutationReceipt("insert", raw)
		want := contract.DataWriteUnknown
		if outcome == "not_started" {
			want = contract.DataWriteFailed
		}
		if err == nil || r.State != want || r.AffectedRows != 0 || r.Original.RemoteError.OperationOutcome != outcome {
			t.Fatal("unknown or partial batch became acknowledged/replayable", r, err)
		}
	}
	r, err := DecodeMutationReceipt("insert", agentbridge.Receipt{})
	if err != nil || r.State != contract.DataWriteNotSent {
		t.Fatal("queued cancellation became dispatched")
	}
}

func TestDatabendRejectsMissingAmbiguousTruncatedOrContradictoryMutationCount(t *testing.T) {
	for _, change := range []string{"ack", "label", "type", "rows", "negative", "overflow", "affected", "truncated", "batch"} {
		t.Run(change, func(t *testing.T) {
			r := mutationTestReceipt("updated", "1")
			var fields map[string]json.RawMessage
			if json.Unmarshal(r.Result, &fields) != nil {
				t.Fatal("test receipt invalid")
			}
			switch change {
			case "ack":
				r.ExecutorAcknowledged = false
			case "label":
				fields["columns"] = json.RawMessage(`["number of rows deleted"]`)
			case "type":
				fields["column_types"] = json.RawMessage(`["int64"]`)
			case "rows":
				fields["rows"] = json.RawMessage(`[[1],[1]]`)
			case "negative":
				fields["rows"] = json.RawMessage(`[[-1]]`)
			case "overflow":
				fields["rows"] = json.RawMessage(`[[9223372036854775808]]`)
			case "affected":
				fields["affected_rows"] = json.RawMessage(`2`)
			case "truncated":
				fields["truncated"] = json.RawMessage(`true`)
			}
			r.Result, _ = json.Marshal(fields)
			if change == "batch" {
				r.Result = append(append(append([]byte{'['}, r.Result...), ','), append(r.Result, ']')...)
			}
			observed, err := DecodeMutationReceipt("update", r)
			if !errors.Is(err, agentbridge.ErrNativeResult) || observed.State != contract.DataWriteUnknown || observed.AffectedRows != 0 {
				t.Fatal("incomplete/multiple-row native count became acknowledged", observed, err)
			}
		})
	}
}
