// Package prestosql adapts the original external JDBC plugin for PrestoSQL.
package prestosql

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"time"

	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
)

var ErrResponse = errors.New("original PrestoSQL query result is invalid or unsupported")

// queryResult has no inferred database types: the original plugin supplies
// column labels, not result-column type metadata. Raw cells preserve numeric
// lexemes and do not mistake text beginning with 0x for binary values.
type queryResult struct {
	columns      []string
	rows         [][]json.RawMessage
	affectedRows int64
	duration     time.Duration
	truncated    bool
	completed    bool
}

func resultFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrResponse
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return nil, ErrResponse
		}
		key, ok := token.(string)
		if !ok {
			return nil, ErrResponse
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, ErrResponse
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, ErrResponse
		}
		fields[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') || d.Decode(new(any)) != io.EOF {
		return nil, ErrResponse
	}
	return fields, nil
}

func array(raw json.RawMessage) ([]json.RawMessage, error) {
	var items []json.RawMessage
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &items) != nil || items == nil {
		return nil, ErrResponse
	}
	return items, nil
}

func decodeResult(receipt jdbcplugin.Receipt) (queryResult, error) {
	var result queryResult
	if !receipt.Dispatched || !receipt.ExecutorAcknowledged || receipt.RemoteFailure || receipt.Diagnostic != nil || len(receipt.Result) > 64<<20 {
		return result, ErrResponse
	}
	fields, err := resultFields(receipt.Result)
	if err != nil || len(fields) != 6 {
		return result, ErrResponse
	}
	for _, key := range []string{"columns", "rows", "large_value_cells", "affected_rows", "execution_time_ms", "truncated"} {
		if _, exists := fields[key]; !exists {
			return result, ErrResponse
		}
	}
	columns, err := array(fields["columns"])
	if err != nil || len(columns) > 10000 {
		return queryResult{}, ErrResponse
	}
	for _, raw := range columns {
		var name string
		if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &name) != nil {
			return queryResult{}, ErrResponse
		}
		result.columns = append(result.columns, name)
	}
	rows, err := array(fields["rows"])
	if err != nil || len(rows) > 10000 || len(columns) == 0 && len(rows) != 0 {
		return queryResult{}, ErrResponse
	}
	for _, raw := range rows {
		cells, err := array(raw)
		if err != nil || len(cells) != len(columns) {
			return queryResult{}, ErrResponse
		}
		result.rows = append(result.rows, cells)
	}
	// The original plugin defers locator values only for Oracle/OceanBase.
	// Refuse unexpected references instead of representing a preview as complete.
	large, err := array(fields["large_value_cells"])
	if err != nil || len(large) != 0 {
		return queryResult{}, ErrResponse
	}
	var milliseconds int64
	if string(fields["affected_rows"]) == "null" || json.Unmarshal(fields["affected_rows"], &result.affectedRows) != nil || result.affectedRows < 0 || len(columns) > 0 && result.affectedRows != 0 {
		return queryResult{}, ErrResponse
	}
	if string(fields["execution_time_ms"]) == "null" || json.Unmarshal(fields["execution_time_ms"], &milliseconds) != nil || milliseconds < 0 || milliseconds > math.MaxInt64/int64(time.Millisecond) {
		return queryResult{}, ErrResponse
	}
	result.duration = time.Duration(milliseconds) * time.Millisecond
	if string(fields["truncated"]) != "true" && string(fields["truncated"]) != "false" || json.Unmarshal(fields["truncated"], &result.truncated) != nil || len(columns) == 0 && result.truncated {
		return queryResult{}, ErrResponse
	}
	result.completed = true
	return result, nil
}
