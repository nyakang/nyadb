package databend

import (
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

// MutationReceipt observes one original INSERT/UPDATE/DELETE execution. The
// pinned JDBC reports the native count as a uint64 result row while its common
// affected_rows field is zero. Neither field alone authorizes a one-row edit,
// proves a transaction commit, or permits replay. The captured original receipt
// is independent of this typed observation, including cancellation races.
type MutationReceipt struct {
	Kind         string
	State        contract.DataWriteStatementState
	AffectedRows int64
	Original     agentbridge.Receipt
}

// DecodeMutationReceipt expects a captured operation kind, never a kind guessed
// from arbitrary SQL or result labels. Callers retain each independent statement
// and its unsent suffix. An unknown original batch cannot be decomposed into
// invented per-statement successes using later reads.
func DecodeMutationReceipt(kind string, receipt agentbridge.Receipt) (MutationReceipt, error) {
	out := MutationReceipt{Kind: kind, State: contract.DataWriteNotSent, Original: receipt.Clone()}
	labels := map[string]string{
		"insert": "number of rows inserted", "update": "number of rows updated", "delete": "number of rows deleted",
	}
	label, ok := labels[kind]
	if !ok {
		return out, contract.ErrUnsupported
	}
	if !receipt.Dispatched {
		return out, nil
	}
	out.State = contract.DataWriteUnknown
	if receipt.ExecutorAcknowledged && receipt.RemoteError != nil {
		if receipt.RemoteError.OperationOutcome == "not_started" {
			out.State = contract.DataWriteFailed
		}
		return out, out.Original.RemoteError
	}
	q, err := agentbridge.DecodeQuery(receipt, DecodeJDBCValue)
	if err != nil || q.SQL.Truncated || len(q.SQL.Sets) != 1 {
		return out, agentbridge.ErrNativeResult
	}
	set := q.SQL.Sets[0]
	if len(set.Columns) != 1 || set.Columns[0].Name != label || set.Columns[0].DatabaseType != "uint64" || len(set.Rows) != 1 || len(set.Rows[0]) != 1 || set.Rows[0][0].Kind != contract.Integer || set.Rows[0][0].Truncated || set.AffectedRows < 0 {
		return out, agentbridge.ErrNativeResult
	}
	count, err := strconv.ParseInt(set.Rows[0][0].Text, 10, 64)
	if err != nil || count < 0 || set.AffectedRows != 0 && set.AffectedRows != count {
		return out, agentbridge.ErrNativeResult
	}
	out.State, out.AffectedRows = contract.DataWriteAcknowledged, count
	return out, nil
}
