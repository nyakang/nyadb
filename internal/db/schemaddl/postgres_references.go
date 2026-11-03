package schemaddl

import "sort"

type PostgreSQLTableReference struct{ Schema, Name string }

// PostgreSQLReferencedTables extracts only validated FOREIGN KEY identities.
// String contents, function names and navigation labels never become locks.
func PostgreSQLReferencedTables(table Table, schema string) ([]PostgreSQLTableReference, error) {
	seen := map[PostgreSQLTableReference]bool{}
	for _, c := range table.Clauses {
		if c.Kind != "constraint" {
			continue
		}
		body, err := pgConstraintBody(c, schema)
		if err != nil {
			return nil, err
		}
		ts, err := postgresFragment(body)
		if err != nil {
			return nil, err
		}
		if !ts[0].word("FOREIGN") {
			continue
		}
		for i, t := range ts {
			if !t.word("REFERENCES") {
				continue
			}
			ns, name, end, err := qualifiedName(ts, i+1)
			if err != nil || end < len(ts) && ts[end].text == "." {
				return nil, ErrUnsupported
			}
			if ns == "" {
				ns = schema
			}
			if PostgreSQLIdentifier(ns) != nil || PostgreSQLIdentifier(name) != nil {
				return nil, ErrUnsupported
			}
			if ns != schema || name != table.Name {
				seen[PostgreSQLTableReference{ns, name}] = true
			}
			break
		}
	}
	out := make([]PostgreSQLTableReference, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Schema != out[j].Schema {
			return out[i].Schema < out[j].Schema
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
