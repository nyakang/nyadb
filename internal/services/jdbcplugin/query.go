package jdbcplugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// Diagnostic contains only the original plugin's typed fields. It contains no
// native message, SQL or connection values. A SQL category alone does not prove
// a server rejection: PrestoSQL350 uses it for lost HTTP write responses too.
type Diagnostic struct {
	Category      string
	SQLState      string
	VendorCode    int32
	HasVendorCode bool
}

type Query struct {
	SQL, Catalog, Schema string
	MaxRows              int
	TimeoutSeconds       int
}

// Execute requests typed native diagnostics and quarantines ambiguous errors
// before releasing the serialized request gate. It never retries SQL. A native
// failure acknowledgement is not proof that a write was rolled back or absent.
func (c *Client) Execute(ctx context.Context, query Query) (Receipt, error) {
	if strings.TrimSpace(query.SQL) == "" || query.MaxRows < 1 || query.MaxRows > 10000 || query.TimeoutSeconds < 0 || query.TimeoutSeconds > 3600 {
		return Receipt{}, ErrRequest
	}
	params := map[string]any{"sql": query.SQL, "database": query.Catalog, "schema": query.Schema, "maxRows": query.MaxRows}
	if query.TimeoutSeconds != 0 {
		params["timeoutSecs"] = query.TimeoutSeconds
	}
	return c.call(ctx, "executeQuery", params, true)
}

func decodeDiagnostic(raw json.RawMessage) (*Diagnostic, error) {
	fields, err := object(raw)
	if err != nil {
		return nil, ErrProtocol
	}
	flag, exists := fields["execution_error"]
	if !exists {
		// Pin the original successful query envelope too. An unrecognized
		// result must not smuggle a native error message into public output.
		if len(fields) != 6 {
			return nil, ErrProtocol
		}
		for _, key := range []string{"columns", "rows", "large_value_cells", "affected_rows", "execution_time_ms", "truncated"} {
			if _, ok := fields[key]; !ok {
				return nil, ErrProtocol
			}
		}
		return nil, nil
	}
	var failed bool
	if json.Unmarshal(flag, &failed) != nil || !failed || len(fields) != 2 {
		return nil, ErrProtocol
	}
	problem, err := object(fields["error"])
	if err != nil || len(problem) != 1 {
		return nil, ErrProtocol
	}
	typed, err := object(problem["diagnostics"])
	if err != nil || len(typed) < 1 || len(typed) > 3 {
		return nil, ErrProtocol
	}
	d := &Diagnostic{}
	for key, value := range typed {
		switch key {
		case "category":
			if json.Unmarshal(value, &d.Category) != nil {
				return nil, ErrProtocol
			}
		case "sqlState":
			if json.Unmarshal(value, &d.SQLState) != nil || len(d.SQLState) != 5 || strings.IndexFunc(d.SQLState, func(r rune) bool { return r < '0' || r > '9' && r < 'A' || r > 'Z' }) >= 0 {
				return nil, ErrProtocol
			}
		case "vendorCode":
			if json.Unmarshal(value, &d.VendorCode) != nil || d.VendorCode == 0 {
				return nil, ErrProtocol
			}
			d.HasVendorCode = true
		default:
			return nil, ErrProtocol
		}
	}
	switch d.Category {
	case "sql", "connection", "timeout", "protocol":
		return d, nil
	default:
		return nil, ErrProtocol
	}
}

func (c *Client) settleQuery(receipt Receipt, executionError error) (Receipt, error) {
	if !receipt.ExecutorAcknowledged {
		return receipt, executionError
	}
	var parseError error
	if !receipt.RemoteFailure {
		receipt.Diagnostic, parseError = decodeDiagnostic(receipt.Result)
		if parseError != nil {
			// Retain the complete original privately; do not serialize an
			// unvalidated structured error's possible native message.
			receipt.Result = nil
		}
		if parseError == nil && receipt.Diagnostic == nil {
			return receipt, executionError
		}
	}
	receipt.RemoteFailure = true
	err := errors.Join(executionError, ErrRemote, parseError)
	d := receipt.Diagnostic
	if parseError != nil || d == nil || d.Category != "sql" || !d.HasVendorCode {
		c.stop(ErrLost)
		<-c.processDone
		receipt.ProcessExitConfirmed = true
		err = errors.Join(err, ErrLost)
	}
	return receipt, err
}
