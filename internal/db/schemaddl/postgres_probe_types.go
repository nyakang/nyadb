package schemaddl

import (
	"sort"
	"strings"
)

// PostgreSQLProbeType identifies a declaration type without invoking the
// server's type parser or input functions. Array dimensions share one type OID.
type PostgreSQLProbeType struct {
	Schema, Name string
	Array        bool
}

// PostgreSQLProbeTypes collects column types, explicit casts and typed literals
// throughout the independent CREATE declaration. It is not an expression type
// resolver: operators, implicit callbacks and concurrent changes need their own
// checks before the general designer can publish Apply.
func PostgreSQLProbeTypes(table Table, schema string) ([]PostgreSQLProbeType, error) {
	seen := map[PostgreSQLProbeType]bool{}
	expressions := []string{}
	for _, clause := range table.Clauses {
		if clause.Kind == "column" {
			field, err := ParsePostgreSQLField(clause.SQL)
			if err != nil || field.Unsupported {
				return nil, ErrUnsupported
			}
			ts, err := postgresTokens(field.Type, false)
			if err != nil {
				return nil, err
			}
			ref, end, err := pgProbeTypeAt(ts, 0)
			if err != nil || end != len(ts) {
				return nil, ErrUnsupported
			}
			seen[ref] = true
			if field.HasDefault {
				expressions = append(expressions, field.Default)
			}
			if field.Generation != "" {
				expressions = append(expressions, field.Generation)
			}
		} else if clause.Kind == "constraint" {
			if _, err := RebindPostgreSQLConstraint(clause, schema, table.Name, table.Name, clause.Name); err != nil {
				return nil, err
			}
			expressions = append(expressions, clause.SQL)
		} else {
			return nil, ErrUnsupported
		}
	}
	for _, definition := range table.Definitions {
		raw, err := DefinitionPostgreSQL(definition, schema, table.Name)
		if err != nil {
			return nil, err
		}
		expressions = append(expressions, raw)
	}
	for _, raw := range expressions {
		ts, err := postgresTokens(raw, true)
		if err != nil {
			return nil, err
		}
		for i := 0; i < len(ts); i++ {
			// Unknown literals acquire their input type during expression
			// transformation. Numeric spelling can resolve to int4, int8 or
			// numeric (including unary minus at a boundary); check all three
			// candidates instead of letting an unlisted source hide a cast.
			if ts[i].kind == 's' {
				seen[PostgreSQLProbeType{Name: "text"}] = true
			}
			if ts[i].kind == 'w' && ts[i].text[0] >= '0' && ts[i].text[0] <= '9' {
				for _, name := range []string{"int4", "int8", "numeric"} {
					seen[PostgreSQLProbeType{Name: name}] = true
				}
			}
			if ts[i].word("TRUE") || ts[i].word("FALSE") {
				seen[PostgreSQLProbeType{Name: "bool"}] = true
			}
			if (ts[i].word("B") || ts[i].word("X") || ts[i].word("N")) && i+1 < len(ts) && ts[i+1].kind == 's' && ts[i].end == ts[i+1].start {
				if !ts[i].word("N") {
					seen[PostgreSQLProbeType{Name: "bit"}] = true
				}
				continue
			}
			if ts[i].kind == 'p' && ts[i].text == ":" && i+1 < len(ts) && ts[i+1].kind == 'p' && ts[i+1].text == ":" {
				ref, end, err := pgProbeTypeAt(ts, i+2)
				if err != nil {
					return nil, err
				}
				seen[ref] = true
				i = end - 1
				continue
			}
			if ts[i].word("CAST") && i+1 < len(ts) && ts[i+1].text == "(" && (i == 0 || ts[i-1].text != ".") {
				end, err := pgBalanced(ts, i+1)
				if err != nil {
					return nil, err
				}
				depth, found := 0, false
				for j := i + 2; j < end-1; j++ {
					if ts[j].kind == 'p' {
						if ts[j].text == "(" || ts[j].text == "[" {
							depth++
						}
						if ts[j].text == ")" || ts[j].text == "]" {
							depth--
						}
					}
					if depth == 0 && ts[j].word("AS") {
						ref, at, err := pgProbeTypeAt(ts[:end-1], j+1)
						if err != nil || at != end-1 || found {
							return nil, ErrUnsupported
						}
						seen[ref], found = true, true
					}
				}
				if !found {
					return nil, ErrUnsupported
				}
				// Continue through the operand: nested casts also need checking.
			}
			if !identifierToken(ts[i]) || pgProbeLiteralKeyword(ts[i]) || i > 0 && (ts[i-1].text == "." || ts[i-1].text == ":") {
				continue
			}
			ref, end, err := pgProbeTypeAt(ts, i)
			if err == nil && end < len(ts) && ts[end].kind == 's' {
				seen[ref] = true
			}
		}
	}
	out := make([]PostgreSQLProbeType, 0, len(seen))
	for ref := range seen {
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Schema != out[j].Schema {
			return out[i].Schema < out[j].Schema
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return !out[i].Array && out[j].Array
	})
	return out, nil
}

func pgProbeLiteralKeyword(t token) bool {
	for _, word := range strings.Fields("CASE WHEN THEN ELSE END AS TRUE FALSE NULL AND OR NOT IN IS BETWEEN LIKE ILIKE SIMILAR ESCAPE FROM FOR AT ZONE DEFAULT COLLATE CONSTRAINT CHECK EXECUTE FUNCTION PROCEDURE DISTINCT ALL") {
		if t.word(word) {
			return true
		}
	}
	return false
}

func pgProbeTypeAt(ts []token, at int) (PostgreSQLProbeType, int, error) {
	ref := PostgreSQLProbeType{}
	if at >= len(ts) || !identifierToken(ts[at]) {
		return ref, at, ErrUnsupported
	}
	first := ts[at]
	ref.Name = pgTokenName(first)
	at++
	if at < len(ts) && ts[at].text == "." {
		if at+1 >= len(ts) || !identifierToken(ts[at+1]) {
			return ref, at, ErrUnsupported
		}
		ref.Schema, ref.Name = ref.Name, pgTokenName(ts[at+1])
		at += 2
		if at < len(ts) && ts[at].text == "." {
			return ref, at, ErrUnsupported
		}
	} else if first.kind == 'w' {
		if first.word("DOUBLE") && at < len(ts) && ts[at].word("PRECISION") {
			ref.Name = "float8"
			at++
		} else if (first.word("CHARACTER") || first.word("CHAR") || first.word("BIT")) && at < len(ts) && ts[at].word("VARYING") {
			if first.word("BIT") {
				ref.Name = "varbit"
			} else {
				ref.Name = "varchar"
			}
			at++
		} else if name, ok := map[string]string{"boolean": "bool", "smallint": "int2", "integer": "int4", "int": "int4", "bigint": "int8", "real": "float4", "decimal": "numeric", "character": "bpchar", "char": "bpchar"}[ref.Name]; ok {
			ref.Name = name
		}
	}
	modifier := func() error {
		if at >= len(ts) || ts[at].text != "(" {
			return nil
		}
		end, err := pgBalanced(ts, at)
		if err != nil {
			return err
		}
		needNumber := true
		for j := at + 1; j < end-1; j++ {
			if needNumber && (ts[j].text == "+" || ts[j].text == "-") && j+1 < end-1 {
				j++
			}
			if needNumber {
				if ts[j].kind != 'w' || strings.Trim(ts[j].text, "0123456789") != "" {
					return ErrUnsupported
				}
			} else if ts[j].text != "," {
				return ErrUnsupported
			}
			needNumber = !needNumber
		}
		if needNumber {
			return ErrUnsupported
		}
		at = end
		return nil
	}
	if err := modifier(); err != nil {
		return ref, at, err
	}
	if ref.Schema == "" && first.kind == 'w' && (first.word("TIMESTAMP") || first.word("TIME")) && at < len(ts) && (ts[at].word("WITH") || ts[at].word("WITHOUT")) {
		with := ts[at].word("WITH")
		if at+2 >= len(ts) || !ts[at+1].word("TIME") || !ts[at+2].word("ZONE") {
			return ref, at, ErrUnsupported
		}
		if with {
			if first.word("TIME") {
				ref.Name = "timetz"
			} else {
				ref.Name = "timestamptz"
			}
		}
		at += 3
	}
	if ref.Schema == "" && first.word("INTERVAL") && at < len(ts) {
		part := func(t token) bool {
			return t.word("YEAR") || t.word("MONTH") || t.word("DAY") || t.word("HOUR") || t.word("MINUTE") || t.word("SECOND")
		}
		if part(ts[at]) {
			at++
			if at < len(ts) && ts[at].word("TO") {
				at++
				if at >= len(ts) || !part(ts[at]) {
					return ref, at, ErrUnsupported
				}
				at++
			}
			if err := modifier(); err != nil {
				return ref, at, err
			}
		}
	}
	for at < len(ts) && ts[at].text == "[" {
		at++
		if at < len(ts) && ts[at].kind == 'w' && strings.Trim(ts[at].text, "0123456789") == "" {
			at++
		}
		if at >= len(ts) || ts[at].text != "]" {
			return ref, at, ErrUnsupported
		}
		ref.Array = true
		at++
	}
	if at < len(ts) && ts[at].word("ARRAY") {
		ref.Array = true
		at++
		if at < len(ts) && ts[at].text == "[" {
			at++
			if at < len(ts) && ts[at].kind == 'w' && strings.Trim(ts[at].text, "0123456789") == "" {
				at++
			}
			if at >= len(ts) || ts[at].text != "]" {
				return ref, at, ErrUnsupported
			}
			at++
		}
	}
	if PostgreSQLIdentifier(ref.Name) != nil || ref.Schema != "" && PostgreSQLIdentifier(ref.Schema) != nil {
		return ref, at, ErrUnsupported
	}
	return ref, at, nil
}
