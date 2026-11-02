package schemaddl

import (
	"errors"
	"strings"
)

// PostgreSQLField represents catalog column SQL separately from catalog-only
// storage/ACL/statistics properties. Identity options and generated expressions
// retain their complete SQL, including sequence names and exact bounds.
type PostgreSQLField struct {
	Type, Collation, Default, Identity, IdentityOptions string
	Generation, GenerationStorage, Extras               string
	HasDefault, NotNull, Unsupported                    bool
}

func postgresFragment(raw string) ([]token, error) {
	return postgresTokens(raw, false)
}

func postgresTokens(raw string, statement bool) ([]token, error) {
	ts, err := postgresLex(raw)
	if err != nil {
		return nil, err
	}
	if len(ts) == 0 {
		return nil, errors.New("empty PostgreSQL definition")
	}
	var stack []string
	for _, t := range ts {
		if t.kind != 'p' {
			continue
		}
		switch t.text {
		case ";":
			return nil, errors.New("statement delimiter in PostgreSQL fragment")
		case "(", "[":
			stack = append(stack, t.text)
			if len(stack) > 128 {
				return nil, errors.New("PostgreSQL expression depth exceeded")
			}
		case ")", "]":
			if len(stack) == 0 || stack[len(stack)-1] == "(" && t.text != ")" || stack[len(stack)-1] == "[" && t.text != "]" {
				return nil, errors.New("unbalanced PostgreSQL expression")
			}
			stack = stack[:len(stack)-1]
		case ",":
			if len(stack) == 0 && !statement {
				return nil, errors.New("multiple PostgreSQL clauses in one fragment")
			}
		}
	}
	if len(stack) != 0 {
		return nil, errors.New("unbalanced PostgreSQL expression")
	}
	return ts, nil
}

func pgBalanced(ts []token, at int) (int, error) {
	if at >= len(ts) || ts[at].kind != 'p' || ts[at].text != "(" {
		return at, errors.New("expected PostgreSQL expression")
	}
	depth := 0
	for i := at; i < len(ts); i++ {
		if ts[i].kind != 'p' {
			continue
		}
		if ts[i].text == "(" {
			depth++
		}
		if ts[i].text == ")" {
			depth--
			if depth == 0 {
				return i + 1, nil
			}
		}
	}
	return at, errors.New("unterminated PostgreSQL expression")
}

func pgAttribute(ts []token, i int) bool {
	return ts[i].word("COLLATE") || ts[i].word("DEFAULT") || ts[i].word("GENERATED") || ts[i].word("NULL") || ts[i].word("NOT") && i+1 < len(ts) && ts[i+1].word("NULL")
}

// ParsePostgreSQLField understands PostgreSQL's catalog declaration grammar.
// Unknown column constraints remain readable but locked instead of becoming
// ALTER TYPE input. Nested CASE, arrays, E/dollar strings and quoted type names
// cannot split a default/generated expression into column attributes.
func ParsePostgreSQLField(raw string) (PostgreSQLField, error) {
	f := PostgreSQLField{}
	ts, err := postgresFragment(raw)
	if err != nil {
		return f, err
	}
	depth, at := 0, len(ts)
	for i, t := range ts {
		if depth == 0 && pgAttribute(ts, i) {
			at = i
			break
		}
		if t.kind == 'p' && (t.text == "(" || t.text == "[") {
			depth++
		}
		if t.kind == 'p' && (t.text == ")" || t.text == "]") {
			depth--
		}
		if depth == 0 && (t.word("CONSTRAINT") || t.word("PRIMARY") || t.word("UNIQUE") || t.word("REFERENCES") || t.word("CHECK")) {
			at = i
			f.Unsupported = true
			break
		}
	}
	if at == 0 || !identifierToken(ts[0]) {
		return f, errors.New("PostgreSQL column type is missing")
	}
	f.Type = strings.TrimSpace(raw[ts[0].start:ts[at-1].end])
	for _, t := range ts[:at] {
		if t.kind == 's' || t.kind == 'p' && !strings.Contains(".()[],+-", t.text) {
			return f, errors.New("invalid PostgreSQL type declaration")
		}
	}
	seen := map[string]bool{}
	for at < len(ts) {
		start := at
		key := ""
		switch {
		case ts[at].word("COLLATE"):
			key = "collation"
			at++
			first := at
			_, _, next, e := qualifiedName(ts, at)
			if e != nil {
				return f, e
			}
			at = next
			f.Collation = raw[ts[first].start:ts[at-1].end]
		case ts[at].word("NOT") && at+1 < len(ts) && ts[at+1].word("NULL"):
			key = "null"
			f.NotNull = true
			at += 2
		case ts[at].word("NULL"):
			key = "null"
			at++
		case ts[at].word("DEFAULT"):
			key = "default"
			f.HasDefault = true
			at++
			first := at
			depth, caseDepth := 0, 0
			for at < len(ts) {
				x := ts[at]
				if depth == 0 && caseDepth == 0 && at > first && x.word("NOT") && at+1 < len(ts) && ts[at+1].word("NULL") {
					break
				}
				if x.kind == 'p' && (x.text == "(" || x.text == "[") {
					depth++
				}
				if x.kind == 'p' && (x.text == ")" || x.text == "]") {
					depth--
				}
				if x.word("CASE") {
					caseDepth++
				}
				if x.word("END") && caseDepth > 0 {
					caseDepth--
				}
				at++
			}
			if first == at || caseDepth != 0 {
				return f, errors.New("invalid PostgreSQL default expression")
			}
			f.Default = strings.TrimSpace(raw[ts[first].start:ts[at-1].end])
		case ts[at].word("GENERATED"):
			key = "generation"
			at++
			mode := "ALWAYS"
			if at < len(ts) && ts[at].word("ALWAYS") {
				at++
			} else if at+1 < len(ts) && ts[at].word("BY") && ts[at+1].word("DEFAULT") {
				mode = "BY DEFAULT"
				at += 2
			} else {
				return f, errors.New("invalid PostgreSQL generation mode")
			}
			if at >= len(ts) || !ts[at].word("AS") {
				return f, errors.New("missing PostgreSQL generation AS")
			}
			at++
			if at < len(ts) && ts[at].word("IDENTITY") {
				f.Identity = mode
				at++
				if at < len(ts) && ts[at].kind == 'p' && ts[at].text == "(" {
					first := at
					at, err = pgBalanced(ts, at)
					if err != nil {
						return f, err
					}
					f.IdentityOptions = strings.TrimSpace(raw[ts[first].end:ts[at-1].start])
					if _, err = postgresFragment(f.IdentityOptions); err != nil {
						return f, err
					}
				}
			} else {
				if mode != "ALWAYS" {
					return f, errors.New("generated expressions require ALWAYS")
				}
				first := at
				at, err = pgBalanced(ts, at)
				if err != nil {
					return f, err
				}
				f.Generation = strings.TrimSpace(raw[ts[first].end:ts[at-1].start])
				if _, err = postgresFragment(f.Generation); err != nil {
					return f, err
				}
				if at >= len(ts) || !(ts[at].word("STORED") || ts[at].word("VIRTUAL")) {
					return f, errors.New("generated storage is missing")
				}
				f.GenerationStorage = strings.ToUpper(ts[at].text)
				at++
			}
		default:
			f.Unsupported = true
			f.Extras = strings.TrimSpace(raw[ts[at].start:])
			at = len(ts)
		}
		if key != "" && seen[key] {
			return f, errors.New("duplicate PostgreSQL column attribute")
		}
		seen[key] = true
		if at == start {
			return f, ErrUnsupported
		}
	}
	if f.HasDefault && (f.Identity != "" || f.Generation != "") {
		return f, errors.New("generated/identity columns cannot also have defaults")
	}
	if f.Identity != "" {
		if seen["null"] && !f.NotNull {
			return f, errors.New("identity columns cannot be nullable")
		}
		f.NotNull = true
	}
	return f, nil
}

func (f PostgreSQLField) SQL() (string, error) {
	if f.Unsupported || f.Extras != "" {
		return "", ErrUnsupported
	}
	parts := []string{strings.TrimSpace(f.Type)}
	if f.Collation != "" {
		parts = append(parts, "COLLATE "+f.Collation)
	}
	if f.Identity != "" {
		if f.Identity != "ALWAYS" && f.Identity != "BY DEFAULT" {
			return "", ErrUnsupported
		}
		part := "GENERATED " + f.Identity + " AS IDENTITY"
		if f.IdentityOptions != "" {
			part += " (" + f.IdentityOptions + "\n)"
		}
		parts = append(parts, part)
	} else if f.Generation != "" {
		parts = append(parts, "GENERATED ALWAYS AS ("+f.Generation+"\n) "+f.GenerationStorage)
	} else if f.IdentityOptions != "" || f.GenerationStorage != "" {
		return "", ErrUnsupported
	}
	if f.HasDefault {
		parts = append(parts, "DEFAULT "+f.Default)
	}
	if f.NotNull {
		parts = append(parts, "NOT NULL")
	}
	raw := strings.Join(parts, "\n")
	parsed, err := ParsePostgreSQLField(raw)
	if err != nil || parsed.Unsupported || pgFieldSignature(parsed) != pgFieldSignature(f) {
		return "", errors.Join(ErrUnsupported, err)
	}
	return raw, nil
}

func postgresSQLSignature(raw string) string {
	ts, err := postgresLex(raw)
	if err != nil {
		return "invalid:" + raw
	}
	var parts []string
	for _, t := range ts {
		value := t.text
		if t.kind == 'w' {
			value = strings.ToLower(value)
		}
		// Literal spelling is retained; backslash/Unicode escapes are never
		// interpreted using a different connection's string settings.
		if t.kind == 's' {
			value = raw[t.start:t.end]
		}
		parts = append(parts, string(t.kind)+":"+value)
	}
	return Signature(parts)
}

func pgFieldSignature(f PostgreSQLField) string {
	f.Type = postgresSQLSignature(f.Type)
	f.Collation = postgresSQLSignature(f.Collation)
	f.Default = postgresSQLSignature(f.Default)
	f.IdentityOptions = postgresSQLSignature(f.IdentityOptions)
	f.Generation = postgresSQLSignature(f.Generation)
	return Signature(f)
}
