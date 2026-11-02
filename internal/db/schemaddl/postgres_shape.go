package schemaddl

import (
	"errors"
	"fmt"
	"sort"
)

// VerifyPostgreSQLShape compares an independently canonicalized declaration
// with the server result. It is the intended-shape check, not a replacement for
// retained-value verification or preservation of the complete original catalog.
func VerifyPostgreSQLShape(actual, expected Table, schema string) error {
	if actual.Name != expected.Name || actual.Options != expected.Options || actual.Postgres == nil || expected.Postgres == nil || len(actual.Clauses) != len(expected.Clauses) || len(actual.Definitions) != len(expected.Definitions) {
		return errors.New("PostgreSQL table differs from the requested shape")
	}
	a, b := actual.Clone(), expected.Clone()
	actualColumns, desiredColumns := a.Columns(), b.Columns()
	if len(actualColumns) != len(desiredColumns) {
		return errors.New("PostgreSQL column count differs from the requested shape")
	}
	for i := range actualColumns {
		if actualColumns[i].Name != desiredColumns[i].Name {
			return errors.New("PostgreSQL column order differs from the requested shape")
		}
	}
	// Table reloptions are named settings, not an ordered declaration.
	sort.Strings(a.Postgres.Parameters)
	sort.Strings(b.Postgres.Parameters)
	if Signature(a.Postgres) != Signature(b.Postgres) {
		return errors.New("PostgreSQL table properties differ from the requested shape")
	}
	byName := map[string]Clause{}
	for _, c := range b.Clauses {
		byName[c.Kind+":"+c.Name] = c
	}
	for _, c := range a.Clauses {
		desired, ok := byName[c.Kind+":"+c.Name]
		if !ok {
			return errors.New("unexpected PostgreSQL field or constraint")
		}
		delete(byName, c.Kind+":"+c.Name)
		if c.Kind == "constraint" {
			x, e := pgConstraintBody(c, schema)
			if e != nil {
				return e
			}
			y, e := pgConstraintBody(desired, schema)
			if e != nil {
				return e
			}
			if postgresSQLSignature(x) != postgresSQLSignature(y) {
				return fmt.Errorf("PostgreSQL constraint %q differs from the requested definition", c.Name)
			}
			continue
		}
		if Signature(c.Postgres) != Signature(desired.Postgres) {
			return fmt.Errorf("PostgreSQL column %q properties differ from the requested shape", c.Name)
		}
		x, e := ParsePostgreSQLField(c.SQL)
		if e != nil {
			return e
		}
		y, e := ParsePostgreSQLField(desired.SQL)
		if e != nil {
			return e
		}
		if x.Identity != "" || y.Identity != "" {
			xs, xn, xo, e := PostgreSQLSequenceName(x.IdentityOptions)
			if e != nil {
				return e
			}
			ys, yn, yo, e := PostgreSQLSequenceName(y.IdentityOptions)
			if e != nil {
				return e
			}
			if yn != "" {
				if ys == "" {
					ys = schema
				}
				if xs == "" {
					xs = schema
				}
				if ys != xs || yn != xn {
					return errors.New("PostgreSQL identity sequence association changed")
				}
			}
			x.IdentityOptions = xo
			y.IdentityOptions = yo
		}
		if pgFieldSignature(x) != pgFieldSignature(y) {
			return fmt.Errorf("PostgreSQL column %q differs from its requested definition", c.Name)
		}
	}
	definitions := map[string]Definition{}
	for _, d := range b.Definitions {
		definitions[d.Kind+":"+d.Name] = d
	}
	for _, d := range a.Definitions {
		expected, ok := definitions[d.Kind+":"+d.Name]
		if !ok {
			return errors.New("unexpected PostgreSQL index/trigger")
		}
		delete(definitions, d.Kind+":"+d.Name)
		if Signature(d.Postgres) != Signature(expected.Postgres) {
			return errors.New("PostgreSQL index/trigger properties changed")
		}
		x, e := DefinitionPostgreSQL(d, schema, a.Name)
		if e != nil {
			return e
		}
		y, e := DefinitionPostgreSQL(expected, schema, b.Name)
		if e != nil {
			return e
		}
		if postgresSQLSignature(x) != postgresSQLSignature(y) {
			return errors.New("PostgreSQL index/trigger definition differs from requested shape")
		}
	}
	if len(byName) != 0 || len(definitions) != 0 {
		return errors.New("missing requested PostgreSQL objects")
	}
	return nil
}
