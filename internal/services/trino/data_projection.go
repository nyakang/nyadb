package trino

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// A data projection asks the original JDBC connection for native SQL text,
// before Java's getObject().toString() can discard typed content. It never
// changes DecodeJDBCValue or repairs a previously received SQL result.
type dataProjection struct {
	columns []contract.Column
	types   []*insertType
	selects []string
	bounded bool
}

type projectionBuilder struct {
	next int
	used map[string]bool
}

func (b *projectionBuilder) variable() string {
	for {
		b.next++
		name := fmt.Sprintf("__nyadb_projection_%d", b.next)
		if !b.used[name] {
			b.used[name] = true
			return name
		}
	}
}

func (b *projectionBuilder) text(t *insertType, value string) (string, error) {
	switch t.name {
	case "char":
		// Trino's CHAR-to-VARCHAR cast removes padding, while native result
		// values and map keys retain the declared character width.
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
		j, err := b.json(t, value)
		return "json_format(" + j + ")", err
	default:
		return "CAST(" + value + " AS VARCHAR)", nil
	}
}

// Composite JSON retains the HTTP wire shape: ROW is positional, decimal and
// binary children are strings, JSON children contain their original JSON text.
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
		for i, child := range t.args {
			j, err := b.json(child, fmt.Sprintf("(%s)[%d]", value, i+1))
			if err != nil {
				return "", err
			}
			fields[i] = j
		}
		return "IF(" + value + " IS NULL,CAST(NULL AS JSON),CAST(ARRAY[" + strings.Join(fields, ",") + "] AS JSON))", nil
	case "tinyint", "smallint", "integer", "bigint", "boolean", "real", "double":
		return "CAST(" + value + " AS JSON)", nil
	default:
		text, err := b.text(t, value)
		return "CAST(" + text + " AS JSON)", err
	}
}

func newDataProjection(t *contract.TableStructure, fields []string, bounded bool) (*dataProjection, error) {
	b := projectionBuilder{used: map[string]bool{}}
	known := map[string]contract.StructureColumn{}
	for _, c := range t.Columns {
		known[c.Name] = c
		b.used[strings.ToLower(c.Name)] = true
	}
	if len(fields) == 0 {
		for _, c := range t.Columns {
			fields = append(fields, c.Name)
		}
	}
	p := &dataProjection{bounded: bounded}
	for _, name := range fields {
		c, ok := known[name]
		if !ok {
			return nil, contract.ErrUnsupported
		}
		p.columns = append(p.columns, contract.Column{Name: name, DatabaseType: c.Type, Nullable: c.Nullable})
		typ, err := parseInsertType(c.Type)
		value := quoteName(name)
		expr := value
		if err == nil {
			expr, err = b.text(typ, value)
		}
		if err != nil {
			// Unsupported declarations retain the original explicitly incomplete
			// JDBC cell. Export will reject them rather than reinterpret Java text.
			typ, expr = nil, value
		} else if bounded {
			limited := fmt.Sprintf("substr(%s,1,%d)", expr, previewCellBytes)
			length := "length(" + expr + ")"
			if typ.kind == contract.Binary {
				limited = fmt.Sprintf("to_base64(substr(%s,1,%d))", value, previewCellBytes)
				length = "length(" + value + ")"
			}
			// One VARCHAR envelope per visible field avoids doubling the column
			// count. A SQL NULL differs from an empty value and from truncated text.
			expr = fmt.Sprintf("json_format(CAST(ARRAY[CAST(%s AS JSON),CAST(COALESCE(%s>%d,false) AS JSON)] AS JSON))", limited, length, previewCellBytes)
		}
		p.types = append(p.types, typ)
		p.selects = append(p.selects, expr+" AS "+quoteName(name))
	}
	return p, nil
}

func (p *dataProjection) decode(cols []contract.Column, row []contract.Cell) ([]contract.Column, []contract.Cell, error) {
	if len(cols) != len(p.columns) || len(row) != len(p.columns) {
		return nil, nil, ErrProtocol
	}
	values := make([]contract.Cell, len(row))
	for i, v := range row {
		if cols[i].Name != p.columns[i].Name {
			return nil, nil, ErrProtocol
		}
		t := p.types[i]
		if t == nil {
			values[i] = v.Clone()
			values[i].Truncated = v.Kind != contract.Null
			continue
		}
		if v.Kind == contract.Null {
			values[i] = v.Clone()
			continue
		}
		if v.Kind != contract.Text || v.Truncated {
			return nil, nil, ErrProtocol
		}
		text, truncated := v.Text, false
		if p.bounded {
			var envelope []json.RawMessage
			if json.Unmarshal([]byte(text), &envelope) != nil || len(envelope) != 2 || string(envelope[1]) != "true" && string(envelope[1]) != "false" {
				return nil, nil, ErrProtocol
			}
			truncated = string(envelope[1]) == "true"
			if string(envelope[0]) == "null" {
				if truncated {
					return nil, nil, ErrProtocol
				}
				values[i] = contract.Cell{Kind: contract.Null}
				continue
			}
			if json.Unmarshal(envelope[0], &text) != nil {
				return nil, nil, ErrProtocol
			}
		}
		cell := contract.Cell{Kind: t.kind, Text: text, Truncated: truncated}
		switch t.kind {
		case contract.Binary:
			v, err := base64.StdEncoding.Strict().DecodeString(text)
			if err != nil {
				return nil, nil, ErrProtocol
			}
			cell.Text, cell.Bytes = "", make([]byte, len(v))
			copy(cell.Bytes, v)
		case contract.JSON:
			if !truncated && !json.Valid([]byte(text)) {
				return nil, nil, ErrProtocol
			}
		case contract.Integer:
			if _, err := strconv.ParseInt(text, 10, 64); err != nil {
				return nil, nil, ErrProtocol
			}
		case contract.Decimal:
			if !decimalText.MatchString(text) {
				return nil, nil, ErrProtocol
			}
		case contract.Floating:
			if _, err := strconv.ParseFloat(text, 64); err != nil {
				return nil, nil, ErrProtocol
			}
		case contract.Boolean:
			if text != "true" && text != "false" {
				return nil, nil, ErrProtocol
			}
		}
		values[i] = cell
	}
	return append([]contract.Column(nil), p.columns...), values, nil
}

func (s *Session) dataProjection(t *contract.TableStructure, fields []string, bounded bool) (*dataProjection, error) {
	if s.managed == nil {
		return nil, nil
	}
	return newDataProjection(t, fields, bounded)
}

func readData(ctx context.Context, c rowReader, query string, p *dataProjection, consume func([]contract.Column, []contract.Cell) error) error {
	if r, ok := c.(*jdbcReader); ok {
		// One original cursor, without accumulating large complete export rows
		// into the bridge's default thousand-row response frame.
		r.pageSize = 1
	}
	return c.Read(ctx, query, func(cols []contract.Column, row []contract.Cell) error {
		if p != nil {
			var err error
			cols, row, err = p.decode(cols, row)
			if err != nil {
				return err
			}
		}
		return consume(cols, row)
	})
}
