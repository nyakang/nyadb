package dpiworker

import (
	"database/sql"
	"database/sql/driver"

	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

func decodeOutput(v value) (sql.Out, error) {
	if v.Kind != "out" || len(v.Bytes) != 0 {
		return sql.Out{}, errProtocol
	}
	var dest any
	switch v.Text {
	case "int64":
		dest = new(int64)
	case "text":
		dest = new(string)
	case "nullable-int64":
		dest = new(sql.NullInt64)
	case "nullable-text":
		dest = new(sql.NullString)
	default:
		return sql.Out{}, errProtocol
	}
	return sql.Out{Dest: dest}, nil
}

func encodeOutputs(args []driver.NamedValue) ([]value, error) {
	var outputs []value
	for _, arg := range args {
		out, ok := arg.Value.(sql.Out)
		if !ok {
			continue
		}
		var v any
		switch p := out.Dest.(type) {
		case *int64:
			v = *p
		case *string:
			v = *p
		case *sql.NullInt64:
			if p.Valid {
				v = p.Int64
			}
		case *sql.NullString:
			if p.Valid {
				v = p.String
			}
		default:
			return nil, errProtocol
		}
		if _, err := dpi.StageOutput(out, v); err != nil {
			return nil, err
		}
		encoded, err := encodeValue(v)
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, encoded)
	}
	return outputs, nil
}

func stageOutputs(args []driver.NamedValue, outputs []value) ([]func(), error) {
	var setters []func()
	for _, arg := range args {
		out, ok := arg.Value.(sql.Out)
		if !ok {
			continue
		}
		if len(setters) >= len(outputs) {
			return nil, errProtocol
		}
		v, err := decodeValue(outputs[len(setters)])
		if err != nil {
			return nil, err
		}
		set, err := dpi.StageOutput(out, v)
		if err != nil {
			return nil, err
		}
		setters = append(setters, set)
	}
	if len(setters) != len(outputs) {
		return nil, errProtocol
	}
	return setters, nil
}
