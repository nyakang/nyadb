package trino

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

type insertAttempt struct {
	State      contract.DataWriteStatementState
	Affected   int64
	NativeJSON string
}
type insertExecutor interface {
	insert(context.Context, string) (insertAttempt, error)
}
type httpInsertAttempt struct {
	Dispatched, Terminal bool
	Native               json.RawMessage
}

func (c *Client) insert(ctx context.Context, source string) (insertAttempt, error) {
	a := httpInsertAttempt{}
	r, err := c.run(ctx, source, nil, &a)
	out := insertAttempt{State: contract.DataWriteNotSent}
	if !a.Dispatched {
		return out, err
	}
	out.State = contract.DataWriteUnknown
	raw, _ := json.Marshal(a)
	out.NativeJSON = string(raw)
	var queryError *QueryError
	if errors.As(err, &queryError) && len(a.Native) > 0 {
		out.State = contract.DataWriteFailed
		return out, err
	}
	if a.Terminal && r.UpdateType == "INSERT" && r.UpdateCount != nil && *r.UpdateCount >= 0 {
		var terminal struct {
			UpdateType  string `json:"updateType"`
			UpdateCount *int64 `json:"updateCount"`
		}
		if json.Unmarshal(a.Native, &terminal) != nil || terminal.UpdateType != "INSERT" || terminal.UpdateCount == nil || *terminal.UpdateCount != *r.UpdateCount {
			return out, ErrProtocol
		}
		out.State = contract.DataWriteAcknowledged
		out.Affected = *r.UpdateCount
		return out, nil
	}
	if err == nil {
		err = ErrProtocol
	}
	return out, err
}
func (r *jdbcReader) insert(ctx context.Context, source string) (insertAttempt, error) {
	receipt, err := r.session.Call(ctx, "execute_query", map[string]any{"sql": source, "maxRows": 1, "timeoutSecs": r.timeout})
	out := insertAttempt{State: contract.DataWriteNotSent}
	if !receipt.Dispatched {
		return out, managedError(err)
	}
	out.State = contract.DataWriteUnknown
	raw, _ := json.Marshal(receipt)
	out.NativeJSON = string(raw)
	if receipt.ExecutorAcknowledged && receipt.RemoteError != nil && receipt.RemoteError.OperationOutcome == "not_started" {
		out.State = contract.DataWriteFailed
		return out, managedError(err)
	}
	snapshot, decodeErr := agentbridge.DecodeQuery(receipt, DecodeJDBCValue)
	if decodeErr == nil && len(snapshot.SQL.Sets) == 1 {
		set := snapshot.SQL.Sets[0]
		if len(set.Columns) == 0 && len(set.Rows) == 0 && len(set.Truncation) == 0 && set.AffectedRows >= 0 {
			out.State = contract.DataWriteAcknowledged
			out.Affected = set.AffectedRows
			// A cancellation race keeps the original executor acknowledgement.
			return out, nil
		}
	}
	if err == nil {
		err = agentbridge.ErrNativeResult
	}
	return out, managedError(err)
}
