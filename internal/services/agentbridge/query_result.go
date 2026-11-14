package agentbridge

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

var ErrNativeResult = errors.New("database agent result requires a product value adapter")

// QuerySnapshot keeps all native result fields alongside the workbench grid.
// The grid cannot recreate timing, spatial, message, cursor or LOB sidecars.
type QuerySnapshot struct {
	Native json.RawMessage
	SQL    contract.SQLResult
}

type ValueDecoder func(string, json.RawMessage) (contract.Cell, error)

var integerText = regexp.MustCompile(`^[+-]?[0-9]+$`)
var decimalText = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

func DecodeQuery(receipt Receipt, decoder ValueDecoder) (QuerySnapshot, error) {
	snapshot := QuerySnapshot{Native: append(json.RawMessage(nil), receipt.Result...)}
	snapshot.SQL.NativeJSON = string(snapshot.Native)
	if !receipt.ExecutorAcknowledged || receipt.RemoteError != nil || len(receipt.Result) == 0 {
		return snapshot, ErrNativeResult
	}
	if decoder == nil {
		decoder = DecodeStandardValue
	}
	raw := bytes.TrimSpace(receipt.Result)
	if len(raw) == 0 {
		return snapshot, ErrNativeResult
	}
	sets := []json.RawMessage{raw}
	if raw[0] == '[' {
		if json.Unmarshal(raw, &sets) != nil || len(sets) == 0 || len(sets) > 256 {
			return snapshot, ErrNativeResult
		}
	}
	for _, set := range sets {
		fields, err := objectFields(set)
		if err != nil {
			return snapshot, ErrNativeResult
		}
		for _, name := range []string{"columns", "column_types", "rows", "affected_rows", "execution_time_ms", "truncated"} {
			if value, exists := fields[name]; !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return snapshot, ErrNativeResult
			}
		}
		var native struct {
			Columns   []string            `json:"columns"`
			Types     []string            `json:"column_types"`
			Rows      [][]json.RawMessage `json:"rows"`
			Affected  int64               `json:"affected_rows"`
			Time      int64               `json:"execution_time_ms"`
			Truncated bool                `json:"truncated"`
		}
		if json.Unmarshal(set, &native) != nil || len(native.Columns) != len(native.Types) || len(native.Columns) > 4096 || native.Time < 0 || native.Time > math.MaxInt64/int64(time.Millisecond) {
			return snapshot, ErrNativeResult
		}
		result := contract.ResultSet{AffectedRows: native.Affected, Columns: make([]contract.Column, len(native.Columns)), Rows: make([][]contract.Cell, len(native.Rows))}
		for i, name := range native.Columns {
			result.Columns[i] = contract.Column{Name: name, DatabaseType: native.Types[i]}
		}
		for i, row := range native.Rows {
			if len(row) != len(native.Columns) {
				return snapshot, ErrNativeResult
			}
			result.Rows[i] = make([]contract.Cell, len(row))
			for j, raw := range row {
				result.Rows[i][j], err = decoder(native.Types[j], append(json.RawMessage(nil), raw...))
				if err != nil {
					return snapshot, err
				}
			}
		}
		if native.Truncated {
			result.Truncation = []string{"agent_row_limit"}
			snapshot.SQL.Truncated = true
		}
		if native.Affected > 0 {
			if snapshot.SQL.AffectedRows > math.MaxInt64-native.Affected {
				return snapshot, ErrNativeResult
			}
			snapshot.SQL.AffectedRows += native.Affected
		}
		duration := time.Duration(native.Time) * time.Millisecond
		if snapshot.SQL.Duration > time.Duration(math.MaxInt64)-duration {
			return snapshot, ErrNativeResult
		}
		snapshot.SQL.Duration += duration
		snapshot.SQL.Sets = append(snapshot.SQL.Sets, result)
	}
	snapshot.SQL.SetLegacyView()
	return snapshot, nil
}

// DecodeStandardValue handles the common scalar wire forms. Product-specific
// types and deferred values require an injected decoder; no object is flattened
// into a guessed display value or mistaken for complete editable data.
func DecodeStandardValue(databaseType string, raw json.RawMessage) (contract.Cell, error) {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		return contract.Cell{Kind: contract.Null}, nil
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return contract.Cell{}, ErrNativeResult
	}
	if d.Decode(new(any)) != io.EOF {
		return contract.Cell{}, ErrNativeResult
	}
	text := ""
	switch v := value.(type) {
	case string:
		text = v
	case json.Number:
		text = string(v)
	case bool:
		if v {
			text = "true"
		} else {
			text = "false"
		}
	default:
		return contract.Cell{}, ErrNativeResult
	}
	typeName := normalizedType(databaseType)
	if pos := strings.IndexByte(typeName, '('); pos >= 0 {
		typeName = strings.TrimSpace(typeName[:pos])
	}
	cell := contract.Cell{Text: text}
	switch typeName {
	case "BIGINT", "INTEGER", "INT", "SMALLINT", "TINYINT", "INT2", "INT4", "INT8", "HUGEINT", "UBIGINT", "UINTEGER":
		if !integerText.MatchString(text) {
			return contract.Cell{}, ErrNativeResult
		}
		cell.Kind = contract.Integer
	case "DECIMAL", "NUMERIC", "NUMBER", "DECFLOAT":
		if !decimalText.MatchString(text) {
			return contract.Cell{}, ErrNativeResult
		}
		cell.Kind = contract.Decimal
	case "REAL", "FLOAT", "DOUBLE", "DOUBLE PRECISION":
		if text != "NaN" && text != "+Inf" && text != "-Inf" {
			if !decimalText.MatchString(text) {
				return contract.Cell{}, ErrNativeResult
			}
		}
		cell.Kind = contract.Floating
	case "BOOLEAN", "BOOL":
		if text != "true" && text != "false" {
			return contract.Cell{}, ErrNativeResult
		}
		cell.Kind = contract.Boolean
	case "BINARY", "BINARY VARYING", "VARBINARY", "LONGVARBINARY", "BLOB", "BINARY LARGE OBJECT", "BYTEA", "RAW":
		if _, ok := value.(string); !ok || !strings.HasPrefix(text, "0x") {
			return contract.Cell{}, ErrNativeResult
		}
		decoded, err := hex.DecodeString(text[2:])
		if err != nil {
			return contract.Cell{}, ErrNativeResult
		}
		cell.Kind, cell.Text, cell.Bytes = contract.Binary, "", decoded
	case "DATE", "TIME", "TIME WITH TIME ZONE", "TIMESTAMP", "TIMESTAMP WITH TIME ZONE", "DATETIME", "DATETIME2", "TIMESTAMPTZ":
		if _, ok := value.(string); !ok {
			return contract.Cell{}, ErrNativeResult
		}
		cell.Kind = contract.DateTime
	case "JSON", "JSONB":
		if _, ok := value.(string); !ok || !json.Valid([]byte(text)) {
			return contract.Cell{}, ErrNativeResult
		}
		cell.Kind = contract.JSON
	case "CHAR", "VARCHAR", "VARCHAR2", "CHARACTER", "CHARACTER VARYING", "NCHAR", "NVARCHAR", "NVARCHAR2", "TEXT", "LONGVARCHAR", "CLOB", "CHARACTER LARGE OBJECT", "NCLOB", "UUID":
		if _, ok := value.(string); !ok {
			return contract.Cell{}, ErrNativeResult
		}
		cell.Kind = contract.Text
	default:
		return contract.Cell{}, ErrNativeResult
	}
	return cell, nil
}
