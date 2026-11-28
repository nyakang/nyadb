package schemaddl

import (
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

// Manticore RT column changes preserve the complete original declaration and
// options. They never turn a MySQL/SQLite declaration into native SphinxQL.
func ManticoreIdentifier(name string) error {
	if name == "" || len(name) > 256 {
		return ErrUnsupported
	}
	for i, c := range []byte(name) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || i > 0 && c >= '0' && c <= '9') {
			return ErrUnsupported
		}
	}
	return nil
}
func manticoreQuote(name string) string { return "`" + name + "`" }

// This small native lexer handles backslash escapes in option literals; the
// SQLite/PostgreSQL lexer intentionally gives those bytes different meanings.
func manticoreTokens(raw string) ([]token, error) {
	if len(raw) > 1<<20 || !utf8.ValidString(raw) {
		return nil, ErrUnsupported
	}
	var out []token
	for i := 0; i < len(raw); {
		c, start := raw[i], i
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
			i++
			continue
		}
		if len(out) >= 65536 {
			return nil, ErrUnsupported
		}
		if c == '`' || c == '\'' {
			kind := byte('i')
			if c == '\'' {
				kind = 's'
			}
			i++
			var b strings.Builder
			closed := false
			for i < len(raw) {
				if raw[i] == 0 {
					return nil, ErrUnsupported
				}
				if c == '\'' && raw[i] == '\\' {
					if i+1 >= len(raw) {
						return nil, ErrUnsupported
					}
					b.WriteString(raw[i : i+2])
					i += 2
					continue
				}
				if raw[i] == c {
					if i+1 < len(raw) && raw[i+1] == c {
						b.WriteByte(c)
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				b.WriteByte(raw[i])
				i++
			}
			if !closed {
				return nil, ErrUnsupported
			}
			out = append(out, token{start, i, b.String(), kind})
			continue
		}
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c >= '0' && c <= '9' {
			i++
			for i < len(raw) {
				c = raw[i]
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c >= '0' && c <= '9') {
					break
				}
				i++
			}
			out = append(out, token{start, i, raw[start:i], 'w'})
			continue
		}
		if c != '(' && c != ')' && c != ',' && c != '=' {
			return nil, ErrUnsupported
		}
		i++
		out = append(out, token{start, i, raw[start:i], 'p'})
	}
	return out, nil
}

// ManticoreColumnDefinition accepts the exact native types verified by the
// original ADD/DROP operation. Other field/index/storage options stay explicit.
func ManticoreColumnDefinition(raw string) (string, error) {
	parts := strings.Fields(strings.ToLower(raw))
	if len(parts) == 0 {
		return "", ErrUnsupported
	}
	switch parts[0] {
	case "integer", "bigint", "float", "bool", "timestamp", "multi", "multi64", "json":
		if len(parts) != 1 {
			return "", ErrUnsupported
		}
	case "text":
		if len(parts) != 1 {
			return "", ErrUnsupported
		}
	case "string":
		if len(parts) != 1 && (len(parts) != 2 || parts[1] != "attribute") {
			return "", ErrUnsupported
		}
		return "string attribute", nil
	default:
		return "", ErrUnsupported
	}
	return parts[0], nil
}
func ParseManticoreRT(raw, name string) (Table, error) {
	var result Table
	if ManticoreIdentifier(name) != nil {
		return result, ErrUnsupported
	}
	ts, err := manticoreTokens(raw)
	if err != nil || len(ts) < 8 || !ts[0].word("CREATE") || !ts[1].word("TABLE") || !identifierToken(ts[2]) || ts[2].text != name || ts[3].text != "(" {
		return result, ErrUnsupported
	}
	result.Name = name
	seen := map[string]bool{}
	i := 4
	for {
		if i >= len(ts) || !identifierToken(ts[i]) || ManticoreIdentifier(ts[i].text) != nil {
			return Table{}, ErrUnsupported
		}
		field := ts[i].text
		i++
		start := i
		for i < len(ts) && ts[i].text != "," && ts[i].text != ")" {
			if ts[i].kind != 'w' {
				return Table{}, ErrUnsupported
			}
			i++
		}
		if i >= len(ts) || i == start {
			return Table{}, ErrUnsupported
		}
		body := raw[ts[start].start:ts[i-1].end]
		if _, e := ManticoreColumnDefinition(body); e != nil {
			return Table{}, e
		}
		key := strings.ToLower(field)
		if seen[key] || len(result.Clauses) >= 512 {
			return Table{}, ErrUnsupported
		}
		seen[key] = true
		result.Clauses = append(result.Clauses, Clause{ID: "native:" + field, Kind: "column", Name: field, SQL: body})
		if ts[i].text == ")" {
			break
		}
		i++
	}
	if result.Clauses[0].Name != "id" || strings.ToLower(result.Clauses[0].SQL) != "bigint" {
		return Table{}, ErrUnsupported
	}
	end := ts[i].end
	i++
	// Preserve original native option bytes, requiring complete key=value pairs.
	for i < len(ts) {
		if i+2 >= len(ts) || ts[i].kind != 'w' || ts[i+1].text != "=" || ts[i+2].kind != 's' {
			return Table{}, ErrUnsupported
		}
		i += 3
	}
	result.Options = strings.TrimSpace(raw[end:])
	return result, nil
}

// PlanManticoreRT accepts exactly one ADD or DROP against an existing RT table.
// Original session/document proof and preflight remain adapter responsibilities.
func PlanManticoreRT(original Snapshot, desired Table, allowDrop bool) (Plan, error) {
	var plan Plan
	if original.Engine != "manticoresearch" || !original.Exists || original.Postgres != nil || original.OpenGauss != nil || original.SQLite != nil || original.Databend != nil {
		return plan, ErrUnsupported
	}
	parsed, err := ParseManticoreRT(original.DDL, original.Table.Name)
	if err != nil || !reflect.DeepEqual(parsed, original.Table) {
		return plan, ErrUnsupported
	}
	if desired.Name != parsed.Name || desired.Options != parsed.Options || len(desired.Definitions) != 0 || desired.Postgres != nil || desired.OpenGauss != nil || len(desired.Clauses) == 0 || len(desired.Clauses) > 512 {
		return plan, ErrUnsupported
	}
	old := map[string]Clause{}
	for _, c := range parsed.Clauses {
		old[c.ID] = c
	}
	seenID, seenName := map[string]bool{}, map[string]bool{}
	var added, dropped []Clause
	var retained []Clause
	for _, c := range desired.Clauses {
		name := strings.ToLower(c.Name)
		if c.ID == "" || seenID[c.ID] || seenName[name] || c.Kind != "column" || c.Postgres != nil || c.OpenGauss != nil || ManticoreIdentifier(c.Name) != nil {
			return plan, ErrUnsupported
		}
		seenID[c.ID] = true
		seenName[name] = true
		if prior, ok := old[c.ID]; ok {
			if c != prior {
				return plan, ErrUnsupported
			}
			retained = append(retained, c)
		} else {
			if c.Name == "id" {
				return plan, ErrUnsupported
			}
			if _, err = ManticoreColumnDefinition(c.SQL); err != nil {
				return plan, err
			}
			added = append(added, c)
		}
	}
	var expected []Clause
	for _, c := range parsed.Clauses {
		if !seenID[c.ID] {
			dropped = append(dropped, c)
		} else {
			expected = append(expected, c)
		}
	}
	if !slices.Equal(expected, retained) {
		return plan, ErrUnsupported
	}
	if len(added)+len(dropped) == 0 {
		plan.Mode = "noop"
		return plan, nil
	}
	if len(added)+len(dropped) != 1 {
		return plan, ErrUnsupported
	}
	if len(dropped) == 1 {
		c := dropped[0]
		if !allowDrop || c.Name == "id" {
			return plan, ErrUnsupported
		}
		plan.Mode = "native_drop"
		plan.Dropped = []string{c.Name}
		plan.SQL = []string{"ALTER TABLE " + manticoreQuote(parsed.Name) + " DROP COLUMN " + manticoreQuote(c.Name)}
	} else {
		c := added[0]
		expectedTable := parsed.Clone()
		expectedTable.Clauses = append(expectedTable.Clauses, c)
		if VerifyManticoreRT(desired, expectedTable) != nil {
			return plan, ErrUnsupported
		}
		body, _ := ManticoreColumnDefinition(c.SQL)
		plan.Mode = "native_add"
		plan.SQL = []string{"ALTER TABLE " + manticoreQuote(parsed.Name) + " ADD COLUMN " + manticoreQuote(c.Name) + " " + body}
	}
	return plan, nil
}

// Native SHOW CREATE groups text before attributes and expands string to
// string attribute. No other requested definition or option may disappear.
func VerifyManticoreRT(desired, actual Table) error {
	if desired.Name != actual.Name || desired.Options != actual.Options || len(desired.Clauses) != len(actual.Clauses) || len(actual.Definitions) != 0 {
		return ErrConflict
	}
	normalized := func(t Table) ([]Clause, error) {
		var text, attrs []Clause
		for _, c := range t.Clauses {
			body, e := ManticoreColumnDefinition(c.SQL)
			if e != nil {
				return nil, e
			}
			c.ID = ""
			c.SQL = body
			if body == "text" {
				text = append(text, c)
			} else {
				attrs = append(attrs, c)
			}
		}
		if len(attrs) == 0 || attrs[0].Name != "id" {
			return nil, ErrConflict
		}
		return append(append(attrs[:1:1], text...), attrs[1:]...), nil
	}
	want, e := normalized(desired)
	if e != nil {
		return e
	}
	got, e := normalized(actual)
	if e != nil {
		return e
	}
	if !slices.Equal(want, got) {
		return ErrConflict
	}
	return nil
}
