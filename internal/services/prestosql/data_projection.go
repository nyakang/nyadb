package prestosql

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const dataPreviewUnits = 1024
const dataPreviewBytes = 16 << 20

type dataProjection struct {
	columns []contract.StructureColumn
	types   []*insertType
	selects []string
	limit   int
}

type projectionBuilder struct {
	next int
	used map[string]bool
}

func (b *projectionBuilder) variable() string {
	for {
		b.next++
		name := fmt.Sprintf("__nyadb_value_%d", b.next)
		if !b.used[name] {
			b.used[name] = true
			return name
		}
	}
}
func (b *projectionBuilder) text(t *insertType, value string) (string, error) {
	switch t.name {
	case "char":
		length := t.length
		if length < 0 {
			length = 1
		}
		return fmt.Sprintf("rpad(CAST(%s AS VARCHAR),%d,' ')", value, length), nil
	case "varbinary":
		return "to_base64(" + value + ")", nil
	case "json":
		return "json_format(" + value + ")", nil
	case "array", "map", "row":
		expr, err := b.json(t, value)
		return "json_format(" + expr + ")", err
	default:
		return "CAST(" + value + " AS VARCHAR)", nil
	}
}

// Composite cells use positional ROW arrays, exact scalar numbers, decimal
// and binary strings, and original JSON text for a JSON-typed child.
func (b *projectionBuilder) json(t *insertType, value string) (string, error) {
	switch t.name {
	case "array":
		x := b.variable()
		child, err := b.json(t.args[0], x)
		return "CAST(transform(" + value + "," + x + " -> " + child + ") AS JSON)", err
	case "map":
		key := t.args[0]
		if key.name == "array" || key.name == "map" || key.name == "row" || key.name == "json" {
			return "", contract.ErrUnsupported
		}
		k, v := b.variable(), b.variable()
		keyText, err := b.text(key, k)
		if err != nil {
			return "", err
		}
		keys := "transform_keys(" + value + ",(" + k + "," + v + ") -> " + keyText + ")"
		k, v = b.variable(), b.variable()
		child, err := b.json(t.args[1], v)
		return "CAST(transform_values(" + keys + ",(" + k + "," + v + ") -> " + child + ") AS JSON)", err
	case "row":
		fields := make([]string, len(t.args))
		for i, a := range t.args {
			child, err := b.json(a, fmt.Sprintf("(%s)[%d]", value, i+1))
			if err != nil {
				return "", err
			}
			fields[i] = child
		}
		return "IF(" + value + " IS NULL,CAST(NULL AS JSON),CAST(ARRAY[" + strings.Join(fields, ",") + "] AS JSON))", nil
	case "tinyint", "smallint", "integer", "bigint", "boolean", "real", "double":
		return "CAST(" + value + " AS JSON)", nil
	default:
		expr, err := b.text(t, value)
		return "CAST(" + expr + " AS JSON)", err
	}
}

func newDataProjection(m *contract.TableStructure, limit int) (*dataProjection, error) {
	if m == nil || len(m.Columns) == 0 || len(m.Columns) > 512 || limit < 1 || limit > contract.DataEditMaxBytes {
		return nil, contract.ErrDataPage
	}
	p := &dataProjection{limit: limit}
	b := projectionBuilder{used: map[string]bool{}}
	for _, c := range m.Columns {
		b.used[strings.ToLower(c.Name)] = true
	}
	for i, c := range m.Columns {
		typ, err := parseInsertType(c.Type)
		if err != nil {
			return nil, err
		}
		c.ValueKind = typ.kind
		column := quoteName("__nyadb_data") + "." + quoteName(c.Name)
		text, err := b.text(typ, column)
		if err != nil {
			return nil, err
		}
		limited := fmt.Sprintf("substr(%s,1,%d)", text, limit)
		length := "length(" + text + ")"
		if typ.kind == contract.Binary {
			limited = fmt.Sprintf("to_base64(substr(%s,1,%d))", column, limit)
			length = "length(" + column + ")"
		}
		envelope := fmt.Sprintf("json_format(CAST(ARRAY[CAST(%s AS JSON),CAST(COALESCE(%s>%d,false) AS JSON)] AS JSON))", limited, length, limit)
		p.columns = append(p.columns, c)
		p.types = append(p.types, typ)
		p.selects = append(p.selects, envelope+" AS "+quoteName(fmt.Sprintf("__nyadb_cell_%d", i)))
	}
	return p, nil
}

func (p *dataProjection) decode(result queryResult) ([][]contract.DataCell, error) {
	if !result.completed || result.truncated || len(result.columns) != len(p.columns) {
		return nil, ErrResponse
	}
	for i, name := range result.columns {
		if name != fmt.Sprintf("__nyadb_cell_%d", i) {
			return nil, ErrResponse
		}
	}
	var rows [][]contract.DataCell
	budget := dataPreviewBytes
	for _, row := range result.rows {
		if len(row) != len(p.columns) {
			return nil, ErrResponse
		}
		values := make([]contract.DataCell, len(row))
		for i, raw := range row {
			var text string
			if json.Unmarshal(raw, &text) != nil {
				return nil, ErrResponse
			}
			var envelope []json.RawMessage
			if json.Unmarshal([]byte(text), &envelope) != nil || len(envelope) != 2 || string(envelope[1]) != "true" && string(envelope[1]) != "false" {
				return nil, ErrResponse
			}
			value := contract.Cell{Kind: p.types[i].kind, Truncated: string(envelope[1]) == "true"}
			if string(envelope[0]) == "null" {
				if value.Truncated {
					return nil, ErrResponse
				}
				value.Kind = contract.Null
			} else {
				if json.Unmarshal(envelope[0], &value.Text) != nil || !utf8.ValidString(value.Text) {
					return nil, ErrResponse
				}
				if value.Kind == contract.Binary {
					decoded, err := base64.StdEncoding.Strict().DecodeString(value.Text)
					if err != nil || len(decoded) > p.limit {
						return nil, ErrResponse
					}
					value.Bytes = append([]byte{}, decoded...)
					value.Text = ""
				} else {
					if utf8.RuneCountInString(value.Text) > p.limit {
						return nil, ErrResponse
					}
					if !value.Truncated {
						switch value.Kind {
						case contract.Integer:
							if _, err := strconv.ParseInt(value.Text, 10, 64); err != nil {
								return nil, ErrResponse
							}
						case contract.Decimal:
							if !decimalText.MatchString(value.Text) {
								return nil, ErrResponse
							}
						case contract.Floating:
							if _, err := strconv.ParseFloat(value.Text, 64); err != nil {
								return nil, ErrResponse
							}
						case contract.Boolean:
							if value.Text != "true" && value.Text != "false" {
								return nil, ErrResponse
							}
						case contract.JSON:
							if !json.Valid([]byte(value.Text)) {
								return nil, ErrResponse
							}
						}
					}
				}
			}
			budget -= len(value.Text) + len(value.Bytes) + 64
			if budget < 0 {
				return nil, contract.ErrDataPage
			}
			copy := value.Clone()
			values[i] = contract.DataCell{Value: &copy, Text: copy.Display(), Null: copy.Kind == contract.Null, Truncated: copy.Truncated}
		}
		rows = append(rows, values)
	}
	return rows, nil
}
