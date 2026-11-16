package databend

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type dataType struct {
	kind                   contract.CellKind
	sql, name              string
	bits, precision, scale int
	unsigned, nullable     bool
	args                   []*dataType
}
type dataTypeParser struct {
	source string
	pos    int
}

func (p *dataTypeParser) space() {
	for p.pos < len(p.source) && p.source[p.pos] == ' ' {
		p.pos++
	}
}
func (p *dataTypeParser) word() string {
	p.space()
	start := p.pos
	for p.pos < len(p.source) && (p.source[p.pos] >= 'A' && p.source[p.pos] <= 'Z' || p.source[p.pos] >= '0' && p.source[p.pos] <= '9') {
		p.pos++
	}
	return p.source[start:p.pos]
}
func (p *dataTypeParser) take(c byte) bool {
	p.space()
	if p.pos < len(p.source) && p.source[p.pos] == c {
		p.pos++
		return true
	}
	return false
}
func parseDataType(source string) (*dataType, error) {
	if len(source) > 8192 {
		return nil, contract.ErrUnsupported
	}
	p := dataTypeParser{source: strings.ToUpper(strings.TrimSpace(source))}
	t, err := p.parse(0)
	p.space()
	if err != nil || p.pos != len(p.source) {
		return nil, contract.ErrUnsupported
	}
	return t, nil
}
func (p *dataTypeParser) parse(depth int) (*dataType, error) {
	if depth > 16 {
		return nil, contract.ErrUnsupported
	}
	name := p.word()
	t := &dataType{sql: name, name: name}
	if name == "ARRAY" || name == "MAP" || name == "NULLABLE" {
		if !p.take('(') {
			return nil, contract.ErrUnsupported
		}
		for {
			child, err := p.parse(depth + 1)
			if err != nil {
				return nil, err
			}
			t.args = append(t.args, child)
			if p.take(')') {
				break
			}
			if !p.take(',') || len(t.args) >= 2 {
				return nil, contract.ErrUnsupported
			}
		}
		if name == "NULLABLE" {
			if len(t.args) != 1 {
				return nil, contract.ErrUnsupported
			}
			child := t.args[0]
			child.nullable = true
			child.sql = "NULLABLE(" + child.sql + ")"
			return child, nil
		}
		if name == "ARRAY" && len(t.args) != 1 || name == "MAP" && len(t.args) != 2 {
			return nil, contract.ErrUnsupported
		}
		t.kind = contract.JSON
		parts := []string{}
		for _, a := range t.args {
			parts = append(parts, a.sql)
		}
		t.sql = name + "(" + strings.Join(parts, ",") + ")"
		return t, nil
	}
	switch name {
	case "VARCHAR", "STRING", "TEXT":
		t.kind, t.sql = contract.Text, "STRING"
	case "BINARY", "VARBINARY":
		t.kind, t.sql = contract.Binary, "BINARY"
	case "BOOLEAN", "BOOL":
		t.kind, t.sql = contract.Boolean, "BOOLEAN"
	case "FLOAT", "FLOAT32":
		t.kind, t.sql, t.bits = contract.Floating, "FLOAT32", 32
	case "DOUBLE", "FLOAT64":
		t.kind, t.sql, t.bits = contract.Floating, "FLOAT64", 64
	case "DATE", "TIMESTAMP":
		t.kind = contract.DateTime
	case "VARIANT":
		t.kind = contract.JSON
	case "DECIMAL":
		if !p.take('(') {
			return nil, contract.ErrUnsupported
		}
		precision, e1 := strconv.Atoi(p.word())
		if !p.take(',') {
			return nil, contract.ErrUnsupported
		}
		scale, e2 := strconv.Atoi(p.word())
		if e1 != nil || e2 != nil || precision < 1 || precision > 76 || scale < 0 || scale > precision || !p.take(')') {
			return nil, contract.ErrUnsupported
		}
		t.kind, t.precision, t.scale = contract.Decimal, precision, scale
		t.sql = "DECIMAL(" + strconv.Itoa(precision) + "," + strconv.Itoa(scale) + ")"
	default:
		aliases := map[string]int{"TINYINT": 8, "SMALLINT": 16, "INT": 32, "INTEGER": 32, "BIGINT": 64}
		bits, ok := aliases[name]
		if !ok {
			prefix := "INT"
			if strings.HasPrefix(name, "UINT") {
				prefix = "UINT"
				t.unsigned = true
			}
			if !strings.HasPrefix(name, prefix) {
				return nil, contract.ErrUnsupported
			}
			var err error
			bits, err = strconv.Atoi(strings.TrimPrefix(name, prefix))
			if err != nil || bits != 8 && bits != 16 && bits != 32 && bits != 64 {
				return nil, contract.ErrUnsupported
			}
		}
		before := p.pos
		if p.word() == "UNSIGNED" {
			t.unsigned = true
		} else {
			p.pos = before
		}
		t.kind, t.bits = contract.Integer, bits
		t.sql = "INT" + strconv.Itoa(bits)
		if t.unsigned {
			t.sql = "U" + t.sql
		}
	}
	return t, nil
}

func quoteText(s string) string {
	return "'" + strings.NewReplacer("\\", "\\\\", "'", "''", "\x00", "\\0").Replace(s) + "'"
}

var dataDecimal = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?$`)
var dataFloat = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

func dataLiteral(t *dataType, v contract.DataValue) (string, error) {
	if t == nil || v.Default {
		return "", contract.ErrUnsupported
	}
	if len(v.Text)+len(v.Bytes) > contract.DataEditMaxBytes {
		return "", contract.ErrDataEditLimit
	}
	if !utf8.ValidString(v.Text) {
		return "", connections.ErrInvalid
	}
	if v.Null || v.Kind == contract.Null {
		if !t.nullable || v.Text != "" || len(v.Bytes) != 0 || v.Binary {
			return "", connections.ErrInvalid
		}
		return "CAST(NULL AS " + t.sql + ")", nil
	}
	if v.Kind != t.kind || t.kind != contract.Binary && (v.Binary || len(v.Bytes) > 0) {
		return "", connections.ErrInvalid
	}
	s := v.Text
	expr := ""
	switch t.kind {
	case contract.Text:
		expr = quoteText(s)
	case contract.Binary:
		if s != "" {
			return "", connections.ErrInvalid
		}
		expr = "unhex(" + quoteText(hex.EncodeToString(v.Bytes)) + ")"
	case contract.Integer:
		var err error
		if t.unsigned {
			_, err = strconv.ParseUint(s, 10, t.bits)
		} else {
			_, err = strconv.ParseInt(s, 10, t.bits)
		}
		if err != nil {
			return "", connections.ErrInvalid
		}
		expr = quoteText(s)
	case contract.Decimal:
		if !dataDecimal.MatchString(s) {
			return "", connections.ErrInvalid
		}
		parts := strings.Split(strings.TrimLeft(s, "+-"), ".")
		fraction := ""
		if len(parts) == 2 {
			fraction = strings.TrimRight(parts[1], "0")
		}
		if len(strings.TrimLeft(parts[0], "0")) > t.precision-t.scale || len(fraction) > t.scale {
			return "", contract.ErrDataEditLimit
		}
		expr = quoteText(s)
	case contract.Floating:
		value, err := strconv.ParseFloat(s, t.bits)
		if err != nil || !dataFloat.MatchString(s) || math.IsNaN(value) || math.IsInf(value, 0) {
			return "", connections.ErrInvalid
		}
		expr = quoteText(s)
	case contract.Boolean:
		if s != "true" && s != "false" {
			return "", connections.ErrInvalid
		}
		expr = s
	case contract.DateTime:
		layout := "2006-01-02"
		if t.name == "TIMESTAMP" {
			layout = "2006-01-02 15:04:05.999999"
		}
		if _, err := time.Parse(layout, s); err != nil {
			return "", connections.ErrInvalid
		}
		if dot := strings.IndexByte(s, '.'); dot >= 0 && len(s)-dot-1 > 6 {
			return "", connections.ErrInvalid
		}
		expr = quoteText(s)
	case contract.JSON:
		value, err := strictDataJSON(s)
		if err != nil {
			return "", err
		}
		if t.name == "VARIANT" {
			return "CAST(parse_json(" + quoteText(s) + ") AS " + t.sql + ")", nil
		}
		return structuredDataLiteral(t, value)
	default:
		return "", contract.ErrUnsupported
	}
	return "CAST(" + expr + " AS " + t.sql + ")", nil
}

func strictDataJSON(s string) (any, error) {
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 32 {
			return nil, connections.ErrInvalid
		}
		token, err := d.Token()
		if err != nil {
			return nil, connections.ErrInvalid
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return token, nil
		}
		switch delim {
		case '[':
			var a []any
			for d.More() {
				v, e := read(depth + 1)
				if e != nil || len(a) >= 65536 {
					return nil, connections.ErrInvalid
				}
				a = append(a, v)
			}
			if end, e := d.Token(); e != nil || end != json.Delim(']') {
				return nil, connections.ErrInvalid
			}
			return a, nil
		case '{':
			m := map[string]any{}
			for d.More() {
				k, e := d.Token()
				key, ok := k.(string)
				if e != nil || !ok {
					return nil, connections.ErrInvalid
				}
				if _, exists := m[key]; exists {
					return nil, connections.ErrInvalid
				}
				v, e := read(depth + 1)
				if e != nil {
					return nil, e
				}
				m[key] = v
			}
			if end, e := d.Token(); e != nil || end != json.Delim('}') {
				return nil, connections.ErrInvalid
			}
			return m, nil
		default:
			return nil, connections.ErrInvalid
		}
	}
	v, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return nil, connections.ErrInvalid
	}
	return v, nil
}

func structuredDataLiteral(t *dataType, value any) (string, error) {
	// JSON null remains a Variant literal even inside Nullable/Array/Map.
	// SQL NULL is supplied only through an explicit DataValue.Null or a
	// nullable non-Variant container element.
	if t.name == "VARIANT" {
		raw, err := json.Marshal(value)
		if err != nil {
			return "", connections.ErrInvalid
		}
		return "CAST(parse_json(" + quoteText(string(raw)) + ") AS " + t.sql + ")", nil
	}
	if value == nil {
		if !t.nullable {
			return "", connections.ErrInvalid
		}
		return "CAST(NULL AS " + t.sql + ")", nil
	}
	if t.name == "ARRAY" {
		a, ok := value.([]any)
		if !ok {
			return "", connections.ErrInvalid
		}
		parts := []string{}
		for _, v := range a {
			s, err := structuredDataLiteral(t.args[0], v)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		return "CAST([" + strings.Join(parts, ",") + "] AS " + t.sql + ")", nil
	}
	if t.name == "MAP" {
		m, ok := value.(map[string]any)
		if !ok {
			return "", connections.ErrInvalid
		}
		keys := []string{}
		for key := range m {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		keySQL, valSQL := []string{}, []string{}
		for _, key := range keys {
			v := any(key)
			if t.args[0].kind == contract.Integer {
				v = json.Number(key)
			} else if t.args[0].kind != contract.Text {
				return "", contract.ErrUnsupported
			}
			k, err := structuredDataLiteral(t.args[0], v)
			if err != nil {
				return "", err
			}
			s, err := structuredDataLiteral(t.args[1], m[key])
			if err != nil {
				return "", err
			}
			keySQL = append(keySQL, k)
			valSQL = append(valSQL, s)
		}
		return "CAST(MAP([" + strings.Join(keySQL, ",") + "],[" + strings.Join(valSQL, ",") + "]) AS " + t.sql + ")", nil
	}
	v := contract.DataValue{Kind: t.kind}
	switch x := value.(type) {
	case json.Number:
		if t.kind != contract.Integer && t.kind != contract.Decimal && t.kind != contract.Floating {
			return "", connections.ErrInvalid
		}
		v.Text = x.String()
	case string:
		if t.kind != contract.Text && t.kind != contract.DateTime && t.kind != contract.Decimal {
			return "", connections.ErrInvalid
		}
		v.Text = x
	case bool:
		if t.kind != contract.Boolean {
			return "", connections.ErrInvalid
		}
		v.Text = strconv.FormatBool(x)
	default:
		return "", connections.ErrInvalid
	}
	return dataLiteral(t, v)
}
