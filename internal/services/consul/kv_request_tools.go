package consul

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type requestBuilder struct{}

func (*Session) KVRequestBuilder() contract.KVRequestBuilder { return requestBuilder{} }

func optionalToolUint(text string) (*uint64, error) {
	if text == "" {
		return nil, nil
	}
	v, err := strconv.ParseUint(text, 10, 64)
	if err != nil || strconv.FormatUint(v, 10) != text {
		return nil, ErrInvalid
	}
	return &v, nil
}

func (requestBuilder) BuildKVRequest(kind contract.KVRequestToolKind, input contract.KVRequestToolInput) (contract.KVRequest, error) {
	switch kind {
	case contract.KVToolHTTPHealthState, contract.KVToolHTTPHealthNode, contract.KVToolHTTPHealthChecks, contract.KVToolHTTPHealthService:
		return buildHTTPHealthTool(kind, input)
	}
	if input.HTTPHealth != (contract.KVHTTPHealthToolInput{}) {
		return contract.KVRequest{}, ErrInvalid
	}
	switch kind {
	case contract.KVToolHTTPSessionList, contract.KVToolHTTPNodeSessions:
		return buildHTTPSessionListTool(kind, input)
	case contract.KVToolHTTPSessionInfo, contract.KVToolHTTPSessionCreate, contract.KVToolHTTPSessionDestroy, contract.KVToolHTTPAcquire, contract.KVToolHTTPRelease:
		return buildHTTPSessionTool(kind, input)
	}
	if input.HTTPSession != (contract.KVHTTPSessionToolInput{}) {
		return contract.KVRequest{}, ErrInvalid
	}
	// Reject hidden/incompatible inputs instead of silently dropping semantics.
	if len(input.Key) > 4096 || len(input.ValueBase64) > base64.StdEncoding.EncodedLen(valueLimit) || len(input.TransactionJSON) > requestLimit {
		return contract.KVRequest{}, ErrBounds
	}
	flags, err := optionalToolUint(input.Flags)
	if err != nil {
		return contract.KVRequest{}, err
	}
	index, err := optionalToolUint(input.CompareIndex)
	if err != nil {
		return contract.KVRequest{}, err
	}
	if kind != contract.KVToolPut && (input.ValueBase64 != "" || flags != nil) || kind != contract.KVToolPut && kind != contract.KVToolDelete && index != nil || kind != contract.KVToolDelete && input.Recursive || kind != contract.KVToolList && input.KeysOnly || kind != contract.KVToolTransaction && input.TransactionJSON != "" || kind == contract.KVToolTransaction && input.Key != "" {
		return contract.KVRequest{}, ErrInvalid
	}
	var native Request
	switch kind {
	case contract.KVToolRead:
		native, err = KeyReadRequest(input.Key)
	case contract.KVToolList:
		native, err = KeyListRequest(input.Key, input.KeysOnly)
	case contract.KVToolRaw:
		// A transaction get returns a JSON entry, not an original raw HTTP
		// value. Do not pretend it preserves raw GET semantics.
		if !canonicalHTTPKey(input.Key) {
			return contract.KVRequest{}, ErrUnsupported
		}
		native, err = KeyReadRequest(input.Key)
		native.Query = url.Values{"raw": {""}}
	case contract.KVToolPut:
		var value []byte
		value, err = base64.StdEncoding.Strict().DecodeString(input.ValueBase64)
		if err != nil || base64.StdEncoding.EncodeToString(value) != input.ValueBase64 {
			return contract.KVRequest{}, ErrInvalid
		}
		if len(value) > valueLimit {
			return contract.KVRequest{}, ErrBounds
		}
		native, err = KeyPutRequest(input.Key, value, flags, index)
	case contract.KVToolDelete:
		native, err = KeyDeleteRequest(input.Key, index, input.Recursive)
	case contract.KVToolTransaction:
		// Preserve the original transaction bytes, including exact uint64
		// numbers and whitespace; existing native validation decides writes.
		native = Request{Method: "PUT", Path: "/v1/txn", Body: []byte(input.TransactionJSON)}
	default:
		return contract.KVRequest{}, ErrUnsupported
	}
	if err != nil {
		return contract.KVRequest{}, err
	}
	return DocumentRequest(native)
}

func requestTools(prefix string) []contract.KVRequestTool {
	key := prefix
	if key == "" {
		key = "example"
	}
	txn, _ := json.MarshalIndent([]TxnOperation{{KV: &TxnKV{Verb: "get-tree", Key: key}}}, "", "  ")
	return []contract.KVRequestTool{
		{Kind: contract.KVToolRead, Default: contract.KVRequestToolInput{Key: prefix}},
		{Kind: contract.KVToolList, Default: contract.KVRequestToolInput{Key: prefix, KeysOnly: true}},
		{Kind: contract.KVToolRaw, Default: contract.KVRequestToolInput{Key: prefix}},
		{Kind: contract.KVToolPut, Default: contract.KVRequestToolInput{Key: prefix}},
		{Kind: contract.KVToolDelete, Default: contract.KVRequestToolInput{Key: prefix}},
		{Kind: contract.KVToolTransaction, Default: contract.KVRequestToolInput{TransactionJSON: string(txn)}},
		{Kind: contract.KVToolHTTPSessionInfo},
		{Kind: contract.KVToolHTTPSessionCreate, Default: contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{TTL: "10m"}}},
		{Kind: contract.KVToolHTTPSessionDestroy},
		{Kind: contract.KVToolHTTPAcquire, Default: contract.KVRequestToolInput{Key: prefix}},
		{Kind: contract.KVToolHTTPRelease, Default: contract.KVRequestToolInput{Key: prefix}},
		{Kind: contract.KVToolHTTPSessionList},
		{Kind: contract.KVToolHTTPNodeSessions},
		{Kind: contract.KVToolHTTPHealthState, Default: contract.KVRequestToolInput{HTTPHealth: contract.KVHTTPHealthToolInput{State: "any"}}},
		{Kind: contract.KVToolHTTPHealthNode},
		{Kind: contract.KVToolHTTPHealthChecks},
		{Kind: contract.KVToolHTTPHealthService},
	}
}

var _ contract.KVRequestBuilderProvider = (*Session)(nil)
