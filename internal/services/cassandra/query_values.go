package cassandra

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func quoteName(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func typeName(t *Type) string {
	if t == nil {
		return "unknown"
	}
	names := map[uint16]string{1: "ascii", 2: "bigint", 3: "blob", 4: "boolean", 5: "counter", 6: "decimal", 7: "double", 8: "float", 9: "int", 10: "text", 11: "timestamp", 12: "uuid", 13: "varchar", 14: "varint", 15: "timeuuid", 16: "inet", 17: "date", 18: "time", 19: "smallint", 20: "tinyint"}
	if name, ok := names[t.Code]; ok {
		return name
	}
	switch t.Code {
	case 0:
		if t.Custom == "org.apache.cassandra.db.marshal.DurationType" {
			return "duration"
		}
		return "custom:" + t.Custom
	case 0x30:
		return quoteName(t.Keyspace) + "." + quoteName(t.Name)
	case 0x20, 0x21, 0x22, 0x31:
		kind := map[uint16]string{0x20: "list", 0x21: "map", 0x22: "set", 0x31: "tuple"}[t.Code]
		args := make([]string, len(t.Arguments))
		for i, a := range t.Arguments {
			args[i] = typeName(a)
		}
		return kind + "<" + strings.Join(args, ",") + ">"
	}
	return "unknown"
}

type encodedValue struct {
	Type    string          `json:"type"`
	Null    bool            `json:"null,omitempty"`
	Missing bool            `json:"missing,omitempty"`
	Value   *string         `json:"value,omitempty"`
	Bytes   *[]byte         `json:"bytes,omitempty"`
	Items   *[]encodedValue `json:"items,omitempty"`
	Entries *[]encodedEntry `json:"entries,omitempty"`
	Fields  *[]encodedField `json:"fields,omitempty"`
}
type encodedEntry struct{ Key, Value encodedValue }
type encodedField struct {
	Name  string
	Value encodedValue
}

func encodeValue(v Value, budget *int) (encodedValue, error) {
	if v.Type == nil {
		return encodedValue{}, ErrProtocol
	}
	out := encodedValue{Type: typeName(v.Type), Null: v.Null, Missing: v.Missing}
	*budget -= len(out.Type)*6 + 128
	if *budget < 0 {
		return out, ErrBounds
	}
	if v.Null || v.Missing {
		return out, nil
	}
	switch v.Type.Code {
	case 0x20, 0x22, 0x31:
		items := make([]encodedValue, len(v.Items))
		for i, item := range v.Items {
			x, err := encodeValue(item, budget)
			if err != nil {
				return out, err
			}
			items[i] = x
		}
		out.Items = &items
	case 0x21:
		entries := make([]encodedEntry, len(v.Entries))
		for i, entry := range v.Entries {
			k, err := encodeValue(entry.Key, budget)
			if err != nil {
				return out, err
			}
			value, err := encodeValue(entry.Value, budget)
			if err != nil {
				return out, err
			}
			entries[i] = encodedEntry{k, value}
		}
		out.Entries = &entries
	case 0x30:
		fields := make([]encodedField, len(v.Fields))
		for i, field := range v.Fields {
			*budget -= len(field.Name) * 6
			value, err := encodeValue(field.Value, budget)
			if err != nil {
				return out, err
			}
			fields[i] = encodedField{field.Name, value}
		}
		out.Fields = &fields
	default:
		if v.Type.Code == 3 || v.Type.Code == 0 && v.Type.Custom != "org.apache.cassandra.db.marshal.DurationType" {
			*budget -= len(v.Raw) * 2
			raw := make([]byte, len(v.Raw))
			copy(raw, v.Raw)
			out.Bytes = &raw
		} else {
			*budget -= len(v.Text) * 6
			text := v.Text
			out.Value = &text
		}
	}
	if *budget < 0 {
		return out, ErrBounds
	}
	return out, nil
}
func nativeCell(v Value) (contract.Cell, error) {
	if v.Null {
		return contract.Cell{Kind: contract.Null}, nil
	}
	if v.Type == nil || v.Missing {
		return contract.Cell{}, ErrProtocol
	}
	cell := contract.Cell{Kind: contract.Text, Text: v.Text}
	switch v.Type.Code {
	case 2, 5, 9, 14, 19, 20:
		cell.Kind = contract.Integer
	case 3:
		cell.Kind = contract.Binary
		cell.Text = ""
		cell.Bytes = make([]byte, len(v.Raw))
		copy(cell.Bytes, v.Raw)
	case 4:
		cell.Kind = contract.Boolean
	case 6:
		cell.Kind = contract.Decimal
	case 7, 8:
		cell.Kind = contract.Floating
	case 11:
		n, err := strconv.ParseInt(v.Text, 10, 64)
		if err != nil {
			return cell, ErrProtocol
		}
		cell.Kind = contract.DateTime
		cell.Text = time.Unix(n/1000, n%1000*int64(time.Millisecond)).UTC().Format("2006-01-02T15:04:05.000Z")
	case 17:
		n, err := strconv.ParseInt(v.Text, 10, 64)
		if err != nil {
			return cell, ErrProtocol
		}
		cell.Kind = contract.DateTime
		cell.Text = time.Unix(n*86400, 0).UTC().Format("2006-01-02")
	case 18:
		n, err := strconv.ParseInt(v.Text, 10, 64)
		if err != nil {
			return cell, ErrProtocol
		}
		cell.Kind = contract.DateTime
		cell.Text = time.Unix(0, n).UTC().Format("15:04:05.000000000")
	case 0:
		if v.Type.Custom != "org.apache.cassandra.db.marshal.DurationType" {
			cell.Kind = contract.Binary
			cell.Text = ""
			cell.Bytes = make([]byte, len(v.Raw))
			copy(cell.Bytes, v.Raw)
		}
	case 0x20, 0x21, 0x22, 0x30, 0x31:
		budget := 16 << 20
		value, err := encodeValue(v, &budget)
		if err != nil {
			return cell, err
		}
		raw, err := json.Marshal(value)
		if err != nil || len(raw) > 16<<20 {
			return cell, ErrBounds
		}
		cell.Kind = contract.JSON
		cell.Text = string(raw)
	}
	return cell, nil
}
func nativeColumns(cols []Column) []contract.Column {
	out := make([]contract.Column, len(cols))
	for i, c := range cols {
		out[i] = contract.Column{Name: c.Name, DatabaseType: typeName(c.Type), Nullable: true}
	}
	return out
}
func previewCell(c contract.Cell) contract.Cell {
	const limit = 1 << 20
	if len(c.Bytes) > limit {
		c.Bytes = append([]byte(nil), c.Bytes[:limit]...)
		c.Truncated = true
	}
	if len(c.Text) > limit {
		n := limit
		for n > 0 && !utf8.ValidString(c.Text[:n]) {
			n--
		}
		c.Text = c.Text[:n]
		c.Truncated = true
	}
	return c
}
