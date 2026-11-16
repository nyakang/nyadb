package kvworkspace

import (
	"encoding/json"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

// Original JSON-RPC bytes are deliberately base64, and result JSON is text.
// A receipt consumer therefore cannot round native zxids or session IDs through
// a JavaScript number. No etcd headers or leases are invented for znodes.
func encodeZNodeOperationReceipt(r Receipt) ([]byte, error) {
	z := r.Result.ZNode
	if z == nil {
		return nil, jsondocument.ErrInvalid
	}
	type scopeRecord struct {
		ProfileID, Generation, Version string
		Prefix                         []byte
		ZNode                          contract.KVZNodeIdentity
	}
	type nativeRecord struct {
		Captured, Acknowledged, CancellationRequested, ProcessExitConfirmed bool
		ErrorCode                                                           *string
		OriginalJSONRPC                                                     []byte `json:"original_jsonrpc_base64"`
		ResultJSON                                                          string `json:"result_json"`
	}
	record := struct {
		Version             int          `json:"version"`
		Protocol            string       `json:"protocol"`
		Scope               scopeRecord  `json:"scope"`
		Method              string       `json:"method"`
		AuthoredJSON        string       `json:"authored_json"`
		CanonicalJSON       string       `json:"canonical_json"`
		Attempted           bool         `json:"attempted"`
		Completed           bool         `json:"completed"`
		WriteOutcomeUnknown bool         `json:"write_outcome_unknown"`
		Code                string       `json:"code"`
		ZNode               nativeRecord `json:"znode"`
		ProjectionError     string       `json:"projection_error,omitempty"`
		Error               string       `json:"error,omitempty"`
	}{Version: 1, Protocol: r.Scope.ZNode.Protocol,
		Scope:  scopeRecord{ProfileID: strconv.FormatInt(r.Scope.Namespace.ProfileID, 10), Generation: strconv.FormatUint(r.Scope.Generation, 10), Version: r.Scope.Version, Prefix: r.Scope.Prefix, ZNode: r.Scope.ZNode},
		Method: r.Result.Request.Method, AuthoredJSON: r.Authored.JSON, CanonicalJSON: r.Result.Request.JSON,
		Attempted: r.Result.Attempted, Completed: r.Result.Completed, WriteOutcomeUnknown: r.Result.WriteOutcomeUnknown, Code: r.Result.Code,
		ZNode:           nativeRecord{Captured: z.Captured, Acknowledged: z.Acknowledged, CancellationRequested: z.CancellationRequested, ProcessExitConfirmed: z.ProcessExitConfirmed, OriginalJSONRPC: z.Native, ResultJSON: r.Result.JSON},
		ProjectionError: r.Result.ProjectionError, Error: r.Error}
	if z.ErrorCode != nil {
		code := strconv.FormatInt(*z.ErrorCode, 10)
		record.ZNode.ErrorCode = &code
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if len(raw) > 128<<20 {
		return nil, jsondocument.ErrBounds
	}
	return raw, err
}
