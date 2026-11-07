package schemaddl

import "sort"

// Collect names without asking the server to parse an expression, which can
// already fold constants. Only the overlapping identifier grammar is shared.
func OpenGaussDesignCollations(table Table, schema string) ([]PostgreSQLProbeClass, error) {
	var declarations []string
	for _, col := range table.Clauses {
		declarations = append(declarations, col.SQL)
	}
	for _, definition := range table.Definitions {
		body, err := gaussDesignDefinition(definition, schema, table.Name)
		if err != nil {
			return nil, err
		}
		declarations = append(declarations, body)
	}
	seen := map[PostgreSQLProbeClass]bool{}
	for _, raw := range declarations {
		tokens, err := postgresTokens(raw, true)
		if err != nil {
			return nil, err
		}
		for i := 0; i < len(tokens); i++ {
			if !tokens[i].word("COLLATE") {
				continue
			}
			_, _, end, err := qualifiedName(tokens, i+1)
			if err != nil {
				return nil, ErrUnsupported
			}
			namespace, name := "", pgTokenName(tokens[i+1])
			if end == i+4 {
				namespace, name = name, pgTokenName(tokens[i+3])
			}
			if err != nil || PostgreSQLIdentifier(name) != nil || namespace != "" && PostgreSQLIdentifier(namespace) != nil {
				return nil, ErrUnsupported
			}
			seen[PostgreSQLProbeClass{Schema: namespace, Name: name}] = true
			i = end - 1
		}
	}
	var out []PostgreSQLProbeClass
	for name := range seen {
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Schema != out[j].Schema {
			return out[i].Schema < out[j].Schema
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
