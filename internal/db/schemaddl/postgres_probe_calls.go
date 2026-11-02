package schemaddl

import (
	"sort"
)

// PostgreSQLProbeCall describes an explicit routine call in an index. It is
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
	seen := map[PostgreSQLProbeCall]bool{}
	for i := at + 1; i+1 < len(ts); i++ {
		if !identifierToken(ts[i]) || ts[i+1].text != "(" {
			continue
		}
		// These grammar constructs are not pg_proc calls. Quoted spellings
		// remain identifiers, so a function named "coalesce" is still checked.
		syntax := false
		for _, word := range []string{"INCLUDE", "WITH", "WHERE", "CASE", "WHEN", "THEN", "ELSE", "CAST", "ROW", "ARRAY", "COALESCE", "GREATEST", "LEAST", "NULLIF", "IN", "NOT", "AND", "OR"} {
			syntax = syntax || ts[i].word(word) && (i == 0 || ts[i-1].text != ".")
		}
		if syntax {
			continue
		}
		if ts[i].word("OPERATOR") {
			return nil, ErrUnsupported // Explicit operator dispatch needs its own proof.
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
