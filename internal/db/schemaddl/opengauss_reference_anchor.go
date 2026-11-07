package schemaddl

import (
	"errors"
	"fmt"
	"strings"
)

// OpenGaussReferenceAnchor builds an empty, private relation with only native
// column types and NOT VALID foreign keys to one external parent. Defaults,
// generated expressions, indexes and user programs never run for this lock.
func OpenGaussReferenceAnchor(source Table, schema, name string, parent PostgreSQLTableReference) (Table, OpenGaussDesignNames, error) {
	anchor := Table{Name: name, OpenGauss: &OpenGaussTableProperties{Persistence: "p"}}
	names := OpenGaussDesignNames{Columns: map[string]string{}, Constraints: map[string]string{}}
	if PostgreSQLIdentifier(name) != nil || PostgreSQLIdentifier(schema) != nil || parent.Schema == schema && parent.Name == source.Name {
		return anchor, names, ErrUnsupported
	}
	for _, col := range source.Columns() {
		field, err := ParseOpenGaussField(col.SQL)
		if err != nil {
			return anchor, names, err
		}
		switch strings.ToLower(field.Type) {
		case "bigserial", "serial8":
			field.Type = "bigint"
		case "serial", "serial4":
			field.Type = "integer"
		case "smallserial", "serial2":
			field.Type = "smallint"
		}
		// Reconstruct only type and collation, retaining no expression SQL.
		declaration, err := (PostgreSQLField{Type: field.Type, Collation: field.Collation}).SQL()
		if err != nil {
			return anchor, names, err
		}
		anchor.Clauses = append(anchor.Clauses, Clause{ID: col.ID, Kind: "column", Name: col.Name, SQL: declaration})
		names.Columns[col.Name] = col.ID
	}
	for _, clause := range source.Clauses {
		if clause.Kind != "constraint" {
			continue
		}
		refs, err := OpenGaussDesignReferencedTables(Table{Name: source.Name, Clauses: []Clause{clause}}, schema)
		if err != nil {
			return anchor, names, err
		}
		if len(refs) != 1 || refs[0] != parent {
			continue
		}
		private := fmt.Sprintf("%s_f_%d", name, len(names.Constraints))
		bound, err := RebindPostgreSQLConstraint(clause, schema, source.Name, name, private)
		if err != nil {
			return anchor, names, err
		}
		unvalidated, err := PostgreSQLConstraintUnvalidated(bound, schema)
		if err != nil {
			return anchor, names, err
		}
		if !unvalidated {
			bound.SQL += " NOT VALID"
		}
		bound.OpenGauss = nil
		anchor.Clauses = append(anchor.Clauses, bound)
		names.Constraints[private] = clause.ID
	}
	if len(names.Constraints) == 0 {
		return anchor, names, errors.New("native reference anchor has no selected external foreign key")
	}
	return anchor, names, nil
}
