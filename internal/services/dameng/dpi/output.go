package dpi

import (
	"database/sql"
	"errors"
	"strconv"
	"unicode/utf8"
)

// MaxOutputTextBytes bounds scalar output parameters; full LOBs use RowReader.
const MaxOutputTextBytes = 8192

// MaxOutputParameters also keeps complete acknowledgements within the IPC frame.
const MaxOutputParameters = 128

// OutputKind validates the deliberately small, output-only sql.Out boundary.
// Names describe wire types, never caller addresses or current destination values.
func OutputKind(out sql.Out) (string, error) {
	if !out.In {
		switch p := out.Dest.(type) {
		case *int64:
			if p != nil {
				return "int64", nil
			}
		case *sql.NullInt64:
			if p != nil {
				return "nullable-int64", nil
			}
		case *string:
			if p != nil {
				return "text", nil
			}
		case *sql.NullString:
			if p != nil {
				return "nullable-text", nil
			}
		}
	}
	return "", errors.New("DM8 output requires a nonnil integer/text destination; input-output is unverified")
}

// StageOutput validates a complete value without changing the destination.
// Call the returned setter only after the entire original operation is acknowledged.
func StageOutput(out sql.Out, value any) (func(), error) {
	if _, err := OutputKind(out); err != nil {
		return nil, err
	}
	if value != nil {
		switch v := value.(type) {
		case int64:
		case string:
			if len(v) > MaxOutputTextBytes || !utf8.ValidString(v) {
				return nil, errors.New("DM8 output text is incomplete or exceeds its bound")
			}
		default:
			return nil, errors.New("DM8 output value type is invalid")
		}
	}
	switch p := out.Dest.(type) {
	case *int64:
		if v, ok := value.(int64); ok {
			return func() { *p = v }, nil
		}
	case *string:
		if v, ok := value.(string); ok {
			return func() { *p = v }, nil
		}
	case *sql.NullInt64:
		if value == nil {
			return func() { *p = sql.NullInt64{} }, nil
		}
		if v, ok := value.(int64); ok {
			return func() { *p = sql.NullInt64{Int64: v, Valid: true} }, nil
		}
	case *sql.NullString:
		if value == nil {
			return func() { *p = sql.NullString{} }, nil
		}
		if v, ok := value.(string); ok {
			return func() { *p = sql.NullString{String: v, Valid: true} }, nil
		}
	}
	return nil, errors.New("DM8 output cannot be assigned losslessly to its destination")
}

func newOutputBinding(out sql.Out) (*binding, error) {
	_, err := OutputKind(out)
	if err != nil {
		return nil, err
	}
	b := &binding{direction: 4, output: &out, ctype: 1003, dtype: 2, precision: MaxOutputTextBytes, bytes: make([]byte, MaxOutputTextBytes+1)}
	// Native BIGINT output binding silently truncates fractional DECIMALs.
	// Transfer integer outputs as text too, then parse without numeric coercion.
	// Unchanged indicators cannot masquerade as NULL or an empty returned value.
	b.length = -2
	b.pin.Pin(&b.bytes[0])
	b.pin.Pin(&b.length)
	return b, nil
}

func (b *binding) stageOutput() (func(), error) {
	if b.length == -1 {
		return StageOutput(*b.output, nil)
	}
	if b.length < 0 || b.length > int64(len(b.bytes)) {
		return nil, errors.New("DM8 output indicator is incomplete")
	}
	text := string(b.bytes[:b.length])
	if len(text) > MaxOutputTextBytes || !utf8.ValidString(text) {
		return nil, errors.New("DM8 output text is incomplete or exceeds its bound")
	}
	switch b.output.Dest.(type) {
	case *int64, *sql.NullInt64:
		v, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, errors.New("DM8 integer output is fractional, out of range or invalid")
		}
		return StageOutput(*b.output, v)
	}
	return StageOutput(*b.output, text)
}
