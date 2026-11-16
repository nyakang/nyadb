package trino

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type insertType struct {
	name, sql                string
	kind                     contract.CellKind
	args                     []*insertType
	precision, scale, length int
}
type typeParser struct {
	source string
	pos    int
}

func (p *typeParser) space() {
	for p.pos < len(p.source) && p.source[p.pos] == ' ' {
		p.pos++
	}
}
func (p *typeParser) take(s string) bool {
	p.space()
	if strings.HasPrefix(p.source[p.pos:], s) {
		p.pos += len(s)
		return true
	}
	return false
}
func (p *typeParser) word() string {
	p.space()
	start := p.pos
	for p.pos < len(p.source) {
		c := p.source[p.pos]
		if c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			break
		}
		p.pos++
	}
	return strings.ToLower(p.source[start:p.pos])
}
func parseInsertType(source string) (*insertType, error) {
	if len(source) > 8192 {
		return nil, contract.ErrUnsupported
	}
	p := typeParser{source: strings.TrimSpace(source)}
	t, err := p.parse(0)
	p.space()
	if err != nil || p.pos != len(p.source) {
		return nil, contract.ErrUnsupported
	}
	return t, nil
}
func (p *typeParser) parse(depth int) (*insertType, error) {
	if depth > 16 {
		return nil, contract.ErrUnsupported
	}
	start := p.pos
	name := p.word()
	t := &insertType{name: name, kind: valueKind(name), precision: 3, length: -1}
	switch name {
	case "bigint", "integer", "smallint", "tinyint", "double", "real", "boolean", "varbinary", "json", "date", "uuid", "ipaddress":
	case "varchar", "char":
		if p.take("(") {
			n, err := strconv.Atoi(p.word())
			if err != nil || n < 1 || !p.take(")") {
				return nil, contract.ErrUnsupported
			}
			t.length = n
		}
	case "decimal":
		if !p.take("(") {
			return nil, contract.ErrUnsupported
		}
		n, err := strconv.Atoi(p.word())
		if err != nil || n < 1 || n > 38 {
			return nil, contract.ErrUnsupported
		}
		t.precision = n
		if p.take(",") {
			n, err = strconv.Atoi(p.word())
			if err != nil || n < 0 || n > t.precision {
				return nil, contract.ErrUnsupported
			}
			t.scale = n
		}
		if !p.take(")") {
			return nil, contract.ErrUnsupported
		}
		t.kind = contract.Decimal
	case "time", "timestamp":
		if p.take("(") {
			n, err := strconv.Atoi(p.word())
			if err != nil || n < 0 || n > 12 || !p.take(")") {
				return nil, contract.ErrUnsupported
			}
			t.precision = n
		}
		pos := p.pos
		if p.word() == "with" {
			if p.word() != "time" || p.word() != "zone" {
				return nil, contract.ErrUnsupported
			}
		} else {
			p.pos = pos
		}
	case "array", "map", "row":
		t.kind = contract.JSON
		if !p.take("(") {
			return nil, contract.ErrUnsupported
		}
		for {
			if len(t.args) >= 512 {
				return nil, contract.ErrUnsupported
			}
			before := p.pos
			a, err := p.parse(depth + 1)
			p.space()
			if name == "row" && (err != nil || p.pos < len(p.source) && p.source[p.pos] != ',' && p.source[p.pos] != ')') {
				p.pos = before
				p.space()
				if p.take("\"") {
					closed := false
					for p.pos < len(p.source) {
						if p.source[p.pos] == '"' {
							p.pos++
							if p.pos < len(p.source) && p.source[p.pos] == '"' {
								p.pos++
								continue
							}
							closed = true
							break
						}
						p.pos++
					}
					if !closed {
						return nil, contract.ErrUnsupported
					}
				} else {
					if p.word() == "" {
						return nil, contract.ErrUnsupported
					}
				}
				a, err = p.parse(depth + 1)
			}
			if err != nil {
				return nil, err
			}
			t.args = append(t.args, a)
			if p.take(")") {
				break
			}
			if !p.take(",") {
				return nil, contract.ErrUnsupported
			}
		}
		if name == "array" && len(t.args) != 1 || name == "map" && len(t.args) != 2 {
			return nil, contract.ErrUnsupported
		}
	default:
		return nil, contract.ErrUnsupported
	}
	t.sql = p.source[start:p.pos]
	return t, nil
}

var temporalFraction = regexp.MustCompile(`\.[0-9]+`)
var floatLiteral = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

func insertLiteral(t *insertType, v contract.DataValue) (string, error) {
	if v.Default {
		return "", contract.ErrUnsupported
	}
	if len(v.Text)+len(v.Bytes) > contract.DataEditMaxBytes {
		return "", contract.ErrDataEditLimit
	}
	if !utf8.ValidString(v.Text) || strings.ContainsRune(v.Text, 0) {
		return "", connections.ErrInvalid
	}
	if v.Null || v.Kind == contract.Null {
		if v.Text != "" || len(v.Bytes) != 0 || v.Binary {
			return "", connections.ErrInvalid
		}
		return "CAST(NULL AS " + t.sql + ")", nil
	}
	if v.Kind != t.kind || t.kind != contract.Binary && (v.Binary || len(v.Bytes) != 0) {
		return "", connections.ErrInvalid
	}
	s := v.Text
	expr := ""
	switch t.kind {
	case contract.Text:
		if t.length >= 0 && utf8.RuneCountInString(s) > t.length {
			return "", contract.ErrDataEditLimit
		}
		expr = quoteText(s)
	case contract.Integer:
		bits := 64
		switch t.name {
		case "integer":
			bits = 32
		case "smallint":
			bits = 16
		case "tinyint":
			bits = 8
		}
		if _, err := strconv.ParseInt(s, 10, bits); err != nil {
			return "", connections.ErrInvalid
		}
		expr = quoteText(s)
	case contract.Decimal:
		if !decimalText.MatchString(s) {
			return "", connections.ErrInvalid
		}
		parts := strings.Split(strings.TrimLeft(s, "+-"), ".")
		whole := strings.TrimLeft(parts[0], "0")
		fraction := ""
		if len(parts) > 1 {
			fraction = strings.TrimRight(parts[1], "0")
		}
		if len(whole) > t.precision-t.scale || len(fraction) > t.scale {
			return "", contract.ErrDataEditLimit
		}
		expr = quoteText(s)
	case contract.Floating:
		if !floatLiteral.MatchString(s) && s != "NaN" && s != "Infinity" && s != "-Infinity" {
			return "", connections.ErrInvalid
		}
		bits := 64
		if t.name == "real" {
			bits = 32
		}
		if _, err := strconv.ParseFloat(s, bits); err != nil {
			return "", connections.ErrInvalid
		}
		expr = quoteText(s)
	case contract.Boolean:
		if s != "true" && s != "false" {
			return "", connections.ErrInvalid
		}
		expr = s
	case contract.Binary:
		if s != "" {
			return "", connections.ErrInvalid
		}
		expr = "X'" + hex.EncodeToString(v.Bytes) + "'"
	case contract.DateTime:
		for _, f := range temporalFraction.FindAllString(s, -1) {
			if len(strings.TrimRight(f[1:], "0")) > t.precision {
				return "", contract.ErrDataEditLimit
			}
		}
		expr = quoteText(s)
	case contract.JSON:
		d := json.NewDecoder(strings.NewReader(s))
		d.UseNumber()
		value, err := insertJSON(d, 0)
		if err != nil {
			return "", connections.ErrInvalid
		}
		if _, err = d.Token(); err != io.EOF {
			return "", connections.ErrInvalid
		}
		if t.name == "json" {
			expr = "json_parse(" + quoteText(s) + ")"
		} else {
			expr, err = compositeLiteral(t, value)
			if err != nil {
				return "", err
			}
		}
	default:
		return "", contract.ErrUnsupported
	}
	return "CAST(" + expr + " AS " + t.sql + ")", nil
}

// Token decoding rejects duplicate object keys and preserves numeric lexemes.
func insertJSON(d *json.Decoder, depth int) (any, error) {
	if depth > 32 {
		return nil, connections.ErrInvalid
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '[':
			var out []any
			for d.More() {
				v, e := insertJSON(d, depth+1)
				if e != nil {
					return nil, e
				}
				out = append(out, v)
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return nil, connections.ErrInvalid
			}
			return out, nil
		case '{':
			out := map[string]any{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return nil, e
				}
				key, ok := k.(string)
				if !ok {
					return nil, connections.ErrInvalid
				}
				if _, ok = out[key]; ok {
					return nil, connections.ErrInvalid
				}
				v, e := insertJSON(d, depth+1)
				if e != nil {
					return nil, e
				}
				out[key] = v
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return nil, connections.ErrInvalid
			}
			return out, nil
		}
		return nil, connections.ErrInvalid
	}
	return t, nil
}
func compositeLiteral(t *insertType, value any) (string, error) {
	if value == nil {
		return "CAST(NULL AS " + t.sql + ")", nil
	}
	if t.name == "json" {
		raw, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		return insertLiteral(t, contract.DataValue{Kind: contract.JSON, Text: string(raw)})
	}
	if t.name == "array" || t.name == "row" {
		values, ok := value.([]any)
		if !ok || t.name == "row" && len(values) != len(t.args) {
			return "", connections.ErrInvalid
		}
		parts := make([]string, len(values))
		for i, v := range values {
			a := t.args[0]
			if t.name == "row" {
				a = t.args[i]
			}
			s, e := compositeLiteral(a, v)
			if e != nil {
				return "", e
			}
			parts[i] = s
		}
		expr := "ARRAY[" + strings.Join(parts, ",") + "]"
		if t.name == "row" {
			expr = "ROW(" + strings.Join(parts, ",") + ")"
		}
		return "CAST(" + expr + " AS " + t.sql + ")", nil
	}
	if t.name == "map" {
		values, ok := value.(map[string]any)
		if !ok {
			return "", connections.ErrInvalid
		}
		keys := make([]string, 0, len(values))
		for k := range values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		encodedKeys, encodedValues := make([]string, len(keys)), make([]string, len(keys))
		for i, k := range keys {
			s, e := compositeLiteral(t.args[0], k)
			if e != nil {
				return "", e
			}
			encodedKeys[i] = s
			s, e = compositeLiteral(t.args[1], values[k])
			if e != nil {
				return "", e
			}
			encodedValues[i] = s
		}
		return "CAST(MAP(ARRAY[" + strings.Join(encodedKeys, ",") + "],ARRAY[" + strings.Join(encodedValues, ",") + "]) AS " + t.sql + ")", nil
	}
	v := contract.DataValue{Kind: t.kind}
	switch x := value.(type) {
	case string:
		v.Text = x
	case json.Number:
		v.Text = x.String()
	case bool:
		v.Text = strconv.FormatBool(x)
	default:
		return "", connections.ErrInvalid
	}
	if t.kind == contract.Binary {
		raw, e := base64.StdEncoding.Strict().DecodeString(v.Text)
		if e != nil {
			return "", connections.ErrInvalid
		}
		v.Text = ""
		v.Bytes = bytes.Clone(raw)
		v.Binary = true
	}
	if t.kind == contract.JSON {
		raw, e := json.Marshal(value)
		if e != nil {
			return "", e
		}
		v.Text = string(raw)
	}
	return insertLiteral(t, v)
}
