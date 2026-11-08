package qdrant

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

func decodeJSON(raw []byte, target any) error {
	err := jsondocument.Decode(raw, target, responseLimit)
	if errors.Is(err, jsondocument.ErrBounds) {
		return ErrBounds
	}
	if err != nil {
		return ErrProtocol
	}
	return nil
}

func object(raw []byte) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if decodeJSON(raw, &fields) != nil || fields == nil {
		return nil, ErrProtocol
	}
	return fields, nil
}

// NativeResult preserves original number spellings and nested values. A native
// status of ok does not imply that a mutation was completed (wait=false).
func NativeResult(raw []byte) (json.RawMessage, error) {
	fields, err := object(raw)
	var status string
	if err != nil || decodeJSON(fields["status"], &status) != nil || status != "ok" || len(fields["result"]) == 0 {
		return nil, ErrProtocol
	}
	return bytes.Clone(fields["result"]), nil
}
