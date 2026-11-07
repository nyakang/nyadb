package schemaddl

import (
	"errors"
	"strings"
)

// PostgreSQLConstraintUnvalidated reads the catalog validation flag without
// mistaking expression literals for the top-level NOT VALID clause.
func PostgreSQLConstraintUnvalidated(c Clause, schema string) (bool, error) {
	body, err := pgConstraintBody(c, schema)
	if err != nil {
		return false, err
	}
	ts, err := postgresFragment(body)
	if err != nil {
		return false, err
	}
	return len(ts) >= 2 && ts[len(ts)-2].word("NOT") && ts[len(ts)-1].word("VALID"), nil
}

// RebindPostgreSQLDefinition changes only the validated CREATE name and ON
// target spans. Literals, expressions and function bodies are never replaced.
// It is used for transactional definition validation, not cross-profile DDL.
func RebindPostgreSQLDefinition(d Definition, schema, from, to, name string) (Definition, error) {
	if PostgreSQLIdentifier(to) != nil || PostgreSQLIdentifier(name) != nil {
		return Definition{}, ErrUnsupported
	}
	raw, err := DefinitionPostgreSQL(d, schema, from)
	if err != nil {
		return Definition{}, err
	}
	ts, err := postgresLex(raw)
	if err != nil {
		return Definition{}, err
	}
	at := 1
	if ts[at].word("UNIQUE") || ts[at].word("CONSTRAINT") {
		at++
	}
	at++
	nameAt := at
	on := -1
	for i := at + 1; i < len(ts); i++ {
		if ts[i].word("ON") {
			on = i
			break
		}
	}
	if on < 0 {
		return Definition{}, ErrUnsupported
	}
	start := on + 1
	if ts[start].word("ONLY") {
		start++
	}
	_, _, end, err := qualifiedName(ts, start)
	if err != nil {
		return Definition{}, err
	}
	d.Name = name
	d.Postgres = d.Postgres.Clone()
	d.OpenGauss = d.OpenGauss.Clone()
	d.SQL = raw[:ts[nameAt].start] + Quote(name) + raw[ts[nameAt].end:ts[start].start] + Quote(schema) + "." + Quote(to) + raw[ts[end-1].end:]
	return d, nil
}

// RebindPostgreSQLConstraint also rebinds a self-reference in FOREIGN KEY.
// Other schema references and expression/string tokens stay unchanged.
func RebindPostgreSQLConstraint(c Clause, schema, from, to, name string) (Clause, error) {
	if PostgreSQLIdentifier(to) != nil || PostgreSQLIdentifier(name) != nil {
		return Clause{}, ErrUnsupported
	}
	body, err := pgConstraintBody(c, schema)
	if err != nil {
		return Clause{}, err
	}
	ts, err := postgresFragment(body)
	if err != nil {
		return Clause{}, err
	}
	if ts[0].word("FOREIGN") {
		for i, t := range ts {
			if !t.word("REFERENCES") {
				continue
			}
			start := i + 1
			if ts[start].word("ONLY") {
				start++
			}
			refSchema, refName, end, e := qualifiedName(ts, start)
			if e != nil {
				return Clause{}, e
			}
			if refSchema == schema && refName == from {
				body = body[:ts[start].start] + Quote(schema) + "." + Quote(to) + body[ts[end-1].end:]
			}
			break
		}
	}
	c.Name = name
	c.SQL = "CONSTRAINT " + Quote(name) + " " + body
	c.Postgres = c.Postgres.Clone()
	c.OpenGauss = c.OpenGauss.Clone()
	return c, nil
}

// PostgreSQLSequenceName extracts the optional SEQUENCE NAME identity option.
// The remaining complete options retain exact bounds/cache and comments.
func PostgreSQLSequenceName(options string) (schema, name, remaining string, err error) {
	if strings.TrimSpace(options) == "" {
		return "", "", "", nil
	}
	ts, err := postgresFragment(options)
	if err != nil {
		return "", "", "", err
	}
	remaining = options
	for i, t := range ts {
		if !t.word("SEQUENCE") {
			continue
		}
		if i+1 >= len(ts) || !ts[i+1].word("NAME") {
			return "", "", "", ErrUnsupported
		}
		if name != "" {
			return "", "", "", errors.New("duplicate identity sequence name")
		}
		start := i + 2
		var end int
		schema, name, end, err = qualifiedName(ts, start)
		if err != nil {
			return "", "", "", err
		}
		if name == "" || PostgreSQLIdentifier(name) != nil || schema != "" && PostgreSQLIdentifier(schema) != nil || end < len(ts) && ts[end].text == "." {
			return "", "", "", ErrUnsupported
		}
		if ts[start].kind == 'w' {
			if schema != "" {
				schema = pgTokenName(ts[start])
			} else {
				name = pgTokenName(ts[start])
			}
		}
		if schema != "" && ts[end-1].kind == 'w' {
			name = pgTokenName(ts[end-1])
		}
		remaining = strings.TrimSpace(options[:t.start] + "\n" + options[ts[end-1].end:])
	}
	return schema, name, remaining, nil
}
