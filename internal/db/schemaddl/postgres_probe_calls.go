package schemaddl

import (
	"sort"
)

// PostgreSQLProbeCall describes an explicit routine call in an expression. It is
// lexical evidence only: operators, casts, types and delegated callbacks still
// require independent pre-probe checks before general Apply can be enabled.
type PostgreSQLProbeCall struct {
	Schema, Name string
}

// PostgreSQLIndexProbeCalls never asks the server to parse or plan an
// expression. Index validation itself may evaluate immutable constant calls,
// so explicit calls must be checked BEFORE the independent CREATE probe.
func PostgreSQLIndexProbeCalls(d Definition, schema, table string) ([]PostgreSQLProbeCall, error) {
	if d.Kind != "index" {
		return nil, ErrUnsupported
	}
	raw, err := DefinitionPostgreSQL(d, schema, table)
	if err != nil {
		return nil, err
	}
	ts, err := postgresTokens(raw, true)
	if err != nil {
		return nil, err
	}
	_, classCalls, err := pgIndexProbeLayout(ts)
	if err != nil {
		return nil, err
	}
	at := 0
	for at < len(ts) && !ts[at].word("ON") {
		at++
	}
	at++
	if at < len(ts) && ts[at].word("ONLY") {
		at++
	}
	_, _, at, err = qualifiedName(ts, at)
	if err != nil {
		return nil, err
	}
	if at < len(ts) && ts[at].word("USING") {
		at += 2 // Access method names are not expression routine calls.
	}
	if at >= len(ts) || ts[at].text != "(" {
		return nil, ErrUnsupported
	}
	return pgExpressionProbeCalls(ts, at+1, classCalls)
}

// PostgreSQLDeclarationProbeCall keeps the owning clause and evaluation scope.
// Sequence defaults, for example, may be declared on an empty probe but must
// not be evaluated while populating a new column on an existing table.
type PostgreSQLDeclarationProbeCall struct {
	PostgreSQLProbeCall
	Scope, ClauseID string
}

// PostgreSQLDeclarationProbeCalls collects explicit expression calls without
// asking the server to parse them. Trigger callback declarations are excluded:
// CREATE TRIGGER does not execute its callback; its WHEN expression is checked.
func PostgreSQLDeclarationProbeCalls(table Table, schema string) ([]PostgreSQLDeclarationProbeCall, error) {
	var out []PostgreSQLDeclarationProbeCall
	add := func(raw, scope, id string) error {
		ts, err := postgresTokens(raw, true)
		if err != nil {
			return err
		}
		calls, err := pgExpressionProbeCalls(ts, 0, nil)
		if err != nil {
			return err
		}
		for _, call := range calls {
			out = append(out, PostgreSQLDeclarationProbeCall{call, scope, id})
		}
		return nil
	}
	for _, clause := range table.Clauses {
		switch clause.Kind {
		case "column":
			f, err := ParsePostgreSQLField(clause.SQL)
			if err != nil || f.Unsupported {
				return nil, ErrUnsupported
			}
			if f.HasDefault {
				if err := add(f.Default, "default", clause.ID); err != nil {
					return nil, err
				}
			}
			if f.Generation != "" {
				if err := add(f.Generation, "generated", clause.ID); err != nil {
					return nil, err
				}
			}
		case "constraint":
			body, err := pgConstraintBody(clause, schema)
			if err != nil {
				return nil, err
			}
			ts, err := postgresTokens(body, true)
			if err != nil {
				return nil, err
			}
			// PK/UNIQUE/FK lists and REFERENCES identities are not calls.
			if ts[0].word("CHECK") || ts[0].word("EXCLUDE") {
				if err := add(body, "constraint", clause.ID); err != nil {
					return nil, err
				}
			}
		default:
			return nil, ErrUnsupported
		}
	}
	for _, d := range table.Definitions {
		if d.Kind == "index" {
			calls, err := PostgreSQLIndexProbeCalls(d, schema, table.Name)
			if err != nil {
				return nil, err
			}
			for _, call := range calls {
				out = append(out, PostgreSQLDeclarationProbeCall{call, "index", d.Name})
			}
			continue
		}
		raw, err := DefinitionPostgreSQL(d, schema, table.Name)
		if err != nil {
			return nil, err
		}
		ts, err := postgresTokens(raw, true)
		if err != nil {
			return nil, err
		}
		for i := 0; i < len(ts); i++ {
			if ts[i].word("EXECUTE") {
				break
			}
			if !ts[i].word("WHEN") {
				continue
			}
			end, err := pgBalanced(ts, i+1)
			if err != nil {
				return nil, err
			}
			if err := add(raw[ts[i+1].end:ts[end-1].start], "trigger_when", d.Name); err != nil {
				return nil, err
			}
			i = end - 1
		}
	}
	return out, nil
}

func pgExpressionProbeCalls(ts []token, start int, classCalls map[int]bool) ([]PostgreSQLProbeCall, error) {
	seen := map[PostgreSQLProbeCall]bool{}
	for i := start; i+1 < len(ts); i++ {
		// Cast type/typmod grammar cannot conceal executable expressions.
		if ts[i].text == ":" && ts[i+1].text == ":" {
			_, end, err := pgProbeTypeAt(ts, i+2)
			if err != nil {
				return nil, err
			}
			i = end - 1
			continue
		}
		if ts[i].word("CAST") && ts[i+1].text == "(" && (i == 0 || ts[i-1].text != ".") {
			end, err := pgBalanced(ts, i+1)
			if err != nil {
				return nil, err
			}
			depth, found := 0, false
			for j := i + 2; j < end-1; j++ {
				if ts[j].text == "(" || ts[j].text == "[" {
					depth++
				}
				if ts[j].text == ")" || ts[j].text == "]" {
					depth--
				}
				if depth == 0 && ts[j].word("AS") {
					_, at, err := pgProbeTypeAt(ts[:end-1], j+1)
					if err != nil || at != end-1 || found {
						return nil, ErrUnsupported
					}
					// Mark only the type span; still inspect every operand call.
					if classCalls == nil {
						classCalls = map[int]bool{}
					}
					for k := j + 1; k < at; k++ {
						classCalls[k] = true
					}
					found = true
				}
			}
			if !found {
				return nil, ErrUnsupported
			}
		}
		if !identifierToken(ts[i]) || ts[i+1].text != "(" {
			continue
		}
		if classCalls[i] {
			continue // Class option grammar is checked by the class binding.
		}
		if i == 0 || ts[i-1].text != "." {
			// A typmod followed by a string is a typed literal, not a call.
			if _, end, err := pgProbeTypeAt(ts, i); err == nil && end < len(ts) && ts[end].kind == 's' {
				i = end
				continue
			}
			for _, word := range []string{"CURRENT_TIME", "CURRENT_TIMESTAMP", "LOCALTIME", "LOCALTIMESTAMP"} {
				if ts[i].word(word) {
					_, end, err := pgProbeTypeAt(ts, i)
					if err != nil {
						return nil, err
					}
					i = end - 1
					break
				}
			}
			if i+1 >= len(ts) || ts[i+1].text != "(" {
				continue
			}
		}
		if ts[i].word("OPERATOR") && (i == 0 || ts[i-1].text != ".") {
			_, end, err := pgExplicitProbeOperator(ts, i)
			if err != nil {
				return nil, err
			}
			i = end - 1
			continue // Dispatch identity is captured by PostgreSQLProbeOperators.
		}
		// These grammar constructs are not pg_proc calls. Quoted spellings
		// remain identifiers, so a function named "coalesce" is still checked.
		syntax := false
		for _, word := range []string{"CHECK", "EXCLUDE", "INCLUDE", "WITH", "WHERE", "CASE", "WHEN", "THEN", "ELSE", "CAST", "ROW", "ARRAY", "COALESCE", "GREATEST", "LEAST", "NULLIF", "IN", "NOT", "AND", "OR"} {
			syntax = syntax || ts[i].word(word) && (i == 0 || ts[i-1].text != ".")
		}
		if i > 0 && ts[i-1].word("USING") {
			syntax = true
		}
		if syntax {
			continue
		}
		call := PostgreSQLProbeCall{Name: pgTokenName(ts[i])}
		if i >= 2 && ts[i-1].text == "." {
			if !identifierToken(ts[i-2]) || i >= 3 && ts[i-3].text == "." {
				return nil, ErrUnsupported
			}
			call.Schema = pgTokenName(ts[i-2])
		}
		if PostgreSQLIdentifier(call.Name) != nil || call.Schema != "" && PostgreSQLIdentifier(call.Schema) != nil {
			return nil, ErrUnsupported
		}
		seen[call] = true
	}
	out := make([]PostgreSQLProbeCall, 0, len(seen))
	for call := range seen {
		out = append(out, call)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Schema != out[j].Schema {
			return out[i].Schema < out[j].Schema
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
