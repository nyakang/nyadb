package schemaddl

import (
	"sort"
	"strings"
)

type PostgreSQLProbeOperator struct{ Schema, Name string }
type PostgreSQLProbeClass struct{ Schema, Name string }
type PostgreSQLProbeIndexKey struct {
	Column string // Empty for an expression; never guess a result type.
	Class  PostgreSQLProbeClass
}
type PostgreSQLProbeIndexLayout struct {
	Method string
	Keys   []PostgreSQLProbeIndexKey
}

// PostgreSQLProbeOperators collects explicit symbols and comparison syntax in
// every declaration expression. No SQL is sent to PostgreSQL for parsing or
// planning. This is lexical evidence, not a complete expression type resolver.
func PostgreSQLProbeOperators(table Table, schema string) ([]PostgreSQLProbeOperator, error) {
	var expressions []string
	for _, clause := range table.Clauses {
		switch clause.Kind {
		case "column":
			f, err := ParsePostgreSQLField(clause.SQL)
			if err != nil || f.Unsupported {
				return nil, ErrUnsupported
			}
			if f.HasDefault {
				expressions = append(expressions, f.Default)
			}
			if f.Generation != "" {
				expressions = append(expressions, f.Generation)
			}
		case "constraint":
			body, err := pgConstraintBody(clause, schema)
			if err != nil {
				return nil, err
			}
			expressions = append(expressions, body)
		default:
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
	seen := map[PostgreSQLProbeOperator]bool{}
	add := func(name string) { seen[PostgreSQLProbeOperator{Name: name}] = true }
	for _, raw := range expressions {
		ts, err := postgresTokens(raw, true)
		if err != nil {
			return nil, err
		}
		index := len(ts) > 1 && ts[0].word("CREATE") && (ts[1].word("INDEX") || ts[1].word("UNIQUE"))
		classCalls := map[int]bool{}
		if index {
			_, classCalls, err = pgIndexProbeLayout(ts)
			if err != nil {
				return nil, err
			}
		}
		var cases []bool
		for i := 0; i < len(ts); i++ {
			if ts[i].word("CASE") {
				cases = append(cases, i+1 < len(ts) && !ts[i+1].word("WHEN"))
			}
			if ts[i].word("END") && len(cases) > 0 {
				cases = cases[:len(cases)-1]
			}
			if index && i+1 < len(ts) && ts[i+1].text == "(" && (classCalls[i] || ts[i].word("WITH")) {
				end, err := pgBalanced(ts, i+1)
				if err != nil {
					return nil, err
				}
				i = end - 1
				continue
			}
			if ts[i].word("OPERATOR") && i+1 < len(ts) && ts[i+1].text == "(" && (i == 0 || ts[i-1].text != ".") {
				ref, end, err := pgExplicitProbeOperator(ts, i)
				if err != nil {
					return nil, err
				}
				seen[ref] = true
				i = end - 1
				continue
			}
			if pgProbeOperatorToken(ts[i]) {
				end := i + 1
				for end < len(ts) && pgProbeOperatorToken(ts[end]) && ts[end-1].end == ts[end].start {
					end++
				}
				name := raw[ts[i].start:ts[end-1].end]
				// Named argument => is grammar, not a pg_operator dispatch.
				if name != "=>" {
					add(pgProbeOperatorName(name))
				}
				i = end - 1
				continue
			}
			if ts[i].kind == 'p' && ts[i].text == ":" && i+1 < len(ts) && ts[i+1].text == "=" && ts[i].end == ts[i+1].start {
				i++
				continue
			}
			if ts[i].word("IN") {
				add("=")
				add("<>")
			}
			if ts[i].word("GREATEST") || ts[i].word("LEAST") {
				add("<")
				add(">")
			}
			if ts[i].word("BETWEEN") {
				add(">=")
				add("<=")
			}
			if ts[i].word("DISTINCT") || ts[i].word("NULLIF") || ts[i].word("WHEN") && len(cases) > 0 && cases[len(cases)-1] {
				add("=")
			}
			if ts[i].word("GREATEST") || ts[i].word("LEAST") {
				add("<")
				add(">")
			}
			if ts[i].word("LIKE") || ts[i].word("ILIKE") {
				name := "~~"
				if ts[i].word("ILIKE") {
					name += "*"
				}
				if i > 0 && ts[i-1].word("NOT") {
					name = "!" + name
				}
				add(name)
			}
			if ts[i].word("SIMILAR") {
				add("~")
			}
		}
	}
	out := make([]PostgreSQLProbeOperator, 0, len(seen))
	for ref := range seen {
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Schema != out[j].Schema {
			return out[i].Schema < out[j].Schema
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func pgProbeOperatorToken(t token) bool {
	return t.kind == 'p' && strings.Contains("+-*/<>=~!@#%^&|?", t.text)
}
func pgProbeOperatorName(name string) string {
	if name == "!=" {
		return "<>"
	}
	return name
}

func pgExplicitProbeOperator(ts []token, at int) (PostgreSQLProbeOperator, int, error) {
	ref := PostgreSQLProbeOperator{}
	if at+2 >= len(ts) || !ts[at].word("OPERATOR") || ts[at+1].text != "(" {
		return ref, at, ErrUnsupported
	}
	at += 2
	if identifierToken(ts[at]) {
		if at+1 >= len(ts) || ts[at+1].text != "." {
			return ref, at, ErrUnsupported
		}
		ref.Schema = pgTokenName(ts[at])
		at += 2
		if PostgreSQLIdentifier(ref.Schema) != nil {
			return ref, at, ErrUnsupported
		}
	}
	first := at
	for at < len(ts) && pgProbeOperatorToken(ts[at]) {
		if at > first && ts[at-1].end != ts[at].start {
			return ref, at, ErrUnsupported
		}
		ref.Name += ts[at].text
		at++
	}
	if first == at || at >= len(ts) || ts[at].text != ")" || len(ref.Name) > 63 || ref.Name == "=>" {
		return ref, at, ErrUnsupported
	}
	ref.Name = pgProbeOperatorName(ref.Name)
	return ref, at + 1, nil
}

// PostgreSQLIndexProbeLayout binds one index and reads key/class grammar. It
// retains an empty Column for expressions instead of inferring hidden types.
func PostgreSQLIndexProbeLayout(d Definition, schema, table string) (PostgreSQLProbeIndexLayout, error) {
	if d.Kind != "index" {
		return PostgreSQLProbeIndexLayout{}, ErrUnsupported
	}
	raw, err := DefinitionPostgreSQL(d, schema, table)
	if err != nil {
		return PostgreSQLProbeIndexLayout{}, err
	}
	ts, err := postgresTokens(raw, true)
	if err != nil {
		return PostgreSQLProbeIndexLayout{}, err
	}
	layout, _, err := pgIndexProbeLayout(ts)
	return layout, err
}

func pgIndexProbeLayout(ts []token) (PostgreSQLProbeIndexLayout, map[int]bool, error) {
	layout := PostgreSQLProbeIndexLayout{Method: "btree"}
	classCalls := map[int]bool{}
	at := 0
	for at < len(ts) && !ts[at].word("ON") {
		at++
	}
	at++
	if at < len(ts) && ts[at].word("ONLY") {
		at++
	}
	_, _, at, err := qualifiedName(ts, at)
	if err != nil {
		return layout, nil, err
	}
	if at < len(ts) && ts[at].word("USING") {
		at++
		if at >= len(ts) || !identifierToken(ts[at]) {
			return layout, nil, ErrUnsupported
		}
		layout.Method = pgTokenName(ts[at])
		at++
		if PostgreSQLIdentifier(layout.Method) != nil {
			return layout, nil, ErrUnsupported
		}
	}
	end, err := pgBalanced(ts, at)
	if err != nil {
		return layout, nil, err
	}
	start := at + 1
	depth := 0
	for i := start; i < end; i++ {
		if i == end-1 || ts[i].text == "," && depth == 0 {
			key, call, err := pgProbeIndexKey(ts, start, i)
			if err != nil {
				return layout, nil, err
			}
			layout.Keys = append(layout.Keys, key)
			if call >= 0 {
				classCalls[call] = true
			}
			start = i + 1
			continue
		}
		if ts[i].kind == 'p' {
			if ts[i].text == "(" || ts[i].text == "[" {
				depth++
			}
			if ts[i].text == ")" || ts[i].text == "]" {
				depth--
			}
		}
	}
	if len(layout.Keys) == 0 || len(layout.Keys) > 32 {
		return layout, nil, ErrUnsupported
	}
	return layout, classCalls, nil
}

func pgProbeIndexKey(ts []token, at, end int) (PostgreSQLProbeIndexKey, int, error) {
	key := PostgreSQLProbeIndexKey{}
	classCall := -1
	if at >= end {
		return key, classCall, ErrUnsupported
	}
	if ts[at].text == "(" {
		next, err := pgBalanced(ts[:end], at)
		if err != nil {
			return key, classCall, err
		}
		at = next
	} else {
		first := at
		_, _, next, err := qualifiedName(ts[:end], at)
		if err != nil {
			return key, classCall, err
		}
		if next < end && ts[next].text == "(" {
			at, err = pgBalanced(ts[:end], next)
			if err != nil {
				return key, classCall, err
			}
		} else {
			if next != first+1 {
				return key, classCall, ErrUnsupported
			}
			key.Column = pgTokenName(ts[first])
			at = next
		}
	}
	if at < end && ts[at].word("COLLATE") {
		_, _, next, err := qualifiedName(ts[:end], at+1)
		if err != nil {
			return key, classCall, err
		}
		at = next
	}
	if at < end && identifierToken(ts[at]) && !ts[at].word("ASC") && !ts[at].word("DESC") && !ts[at].word("NULLS") {
		first := at
		_, _, next, err := qualifiedName(ts[:end], at)
		if err != nil {
			return key, classCall, err
		}
		key.Class.Name = pgTokenName(ts[first])
		if next == first+3 {
			key.Class.Schema = key.Class.Name
			key.Class.Name = pgTokenName(ts[first+2])
		}
		if PostgreSQLIdentifier(key.Class.Name) != nil || key.Class.Schema != "" && PostgreSQLIdentifier(key.Class.Schema) != nil {
			return key, classCall, ErrUnsupported
		}
		at = next
		if at < end && ts[at].text == "(" {
			classCall = at - 1
			at, err = pgBalanced(ts[:end], at)
			if err != nil {
				return key, classCall, err
			}
		}
	}
	if at < end && (ts[at].word("ASC") || ts[at].word("DESC")) {
		at++
	}
	if at < end && ts[at].word("NULLS") {
		at++
		if at >= end || !ts[at].word("FIRST") && !ts[at].word("LAST") {
			return key, classCall, ErrUnsupported
		}
		at++
	}
	if at != end {
		return key, classCall, ErrUnsupported
	}
	return key, classCall, nil
}
