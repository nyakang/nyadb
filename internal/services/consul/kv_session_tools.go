package consul

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// These pure builders are not advertised by requestTools until the native
// workspace inputs and original lifecycle have their own acceptance evidence.
func buildHTTPSessionTool(kind contract.KVRequestToolKind, input contract.KVRequestToolInput) (contract.KVRequest, error) {
	session := input.HTTPSession
	for _, value := range []string{input.Key, session.ID, session.Name, session.Node, session.TTL, session.LockDelay, session.Behavior} {
		if len(value) > 4096 {
			return contract.KVRequest{}, ErrBounds
		}
	}
	if len(input.ValueBase64) > base64.StdEncoding.EncodedLen(valueLimit) || len(session.NodeChecksJSON) > requestLimit {
		return contract.KVRequest{}, ErrBounds
	}
	input.HTTPSession = contract.KVHTTPSessionToolInput{}
	if kind == contract.KVToolHTTPSessionCreate {
		if input != (contract.KVRequestToolInput{}) || session.ID != "" || !session.IncludeName && session.Name != "" || !session.IncludeNode && session.Node != "" {
			return contract.KVRequest{}, ErrInvalid
		}
		for _, value := range []string{session.Name, session.Node, session.TTL, session.LockDelay, session.Behavior} {
			if !validText(value, 4096) {
				return contract.KVRequest{}, ErrInvalid
			}
		}
		body := map[string]any{}
		if session.IncludeName {
			body["Name"] = session.Name
		}
		if session.IncludeNode {
			body["Node"] = session.Node
		}
		for key, value := range map[string]string{"TTL": session.TTL, "LockDelay": session.LockDelay, "Behavior": session.Behavior} {
			if value != "" {
				body[key] = value
			}
		}
		if session.NodeChecksJSON != "" {
			body["NodeChecks"] = json.RawMessage(session.NodeChecksJSON)
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return contract.KVRequest{}, ErrInvalid
		}
		return DocumentRequest(Request{Method: "PUT", Path: "/v1/session/create", Body: raw})
	}
	id := session.ID
	session.ID = ""
	if !validID(id) || session != (contract.KVHTTPSessionToolInput{}) {
		return contract.KVRequest{}, ErrInvalid
	}
	switch kind {
	case contract.KVToolHTTPSessionInfo, contract.KVToolHTTPSessionDestroy:
		if input != (contract.KVRequestToolInput{}) {
			return contract.KVRequest{}, ErrInvalid
		}
		method, path := "GET", "/v1/session/info/"
		if kind == contract.KVToolHTTPSessionDestroy {
			method, path = "PUT", "/v1/session/destroy/"
		}
		return DocumentRequest(Request{Method: method, Path: path + id})
	case contract.KVToolHTTPAcquire, contract.KVToolHTTPRelease:
		if input.CompareIndex != "" || input.TransactionJSON != "" || input.Recursive || input.KeysOnly {
			return contract.KVRequest{}, ErrInvalid
		}
		// Transaction lock/unlock has a different response and failure contract.
		// Do not silently substitute it for an authored HTTP true/false operation.
		path, err := KeyPath(input.Key)
		if err != nil {
			return contract.KVRequest{}, err
		}
		flags, err := optionalToolUint(input.Flags)
		if err != nil {
			return contract.KVRequest{}, err
		}
		value, err := base64.StdEncoding.Strict().DecodeString(input.ValueBase64)
		if err != nil || base64.StdEncoding.EncodeToString(value) != input.ValueBase64 {
			return contract.KVRequest{}, ErrInvalid
		}
		if len(value) > valueLimit {
			return contract.KVRequest{}, ErrBounds
		}
		operation := "acquire"
		if kind == contract.KVToolHTTPRelease {
			operation = "release"
		}
		query := url.Values{operation: {id}}
		if flags != nil {
			query.Set("flags", strconv.FormatUint(*flags, 10))
		}
		return DocumentRequest(Request{Method: "PUT", Path: path, Body: value, Query: query})
	}
	return contract.KVRequest{}, ErrUnsupported
}
