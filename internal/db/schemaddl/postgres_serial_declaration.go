package schemaddl

import (
	"errors"
	"strings"
)

// PostgreSQLRetainedSerialDeclaration adds the original owned serial sequence
// declarations which an independent CREATE cannot reproduce: its nextval
// default references the original sequence but does not acquire ownership of
// it. Only retained non-identity owner columns qualify. All original sequence
// records stay exact and undergo the usual actual-graph/catalog/value checks.
func PostgreSQLRetainedSerialDeclaration(original Snapshot, desired Table, expected PostgreSQLDeclarationGraph) (PostgreSQLDeclarationGraph, error) {
	if !original.Exists || original.Postgres == nil || !pgGraphSameScope(*original.Postgres, expected.Scope) {
		return PostgreSQLDeclarationGraph{}, ErrConflict
	}
	if _, err := PlanPostgreSQL(original, desired, original.Postgres.Namespace, true); err != nil {
		return PostgreSQLDeclarationGraph{}, err
	}
	before, err := ProjectPostgreSQLDeclarationGraph(*original.Postgres, pgPreservationNames(original.Table))
	if err != nil {
		return PostgreSQLDeclarationGraph{}, err
	}
	owners := map[string]bool{}
	old := map[string]Clause{}
	for _, c := range original.Table.Clauses {
		if c.Kind == "column" {
			old[c.ID] = c
		}
	}
	for _, c := range desired.Clauses {
		prior, retained := old[c.ID]
		if !retained || c.Kind != "column" {
			continue
		}
		a, e := ParsePostgreSQLField(prior.SQL)
		b, e2 := ParsePostgreSQLField(c.SQL)
		if e != nil || e2 != nil {
			return PostgreSQLDeclarationGraph{}, errors.Join(e, e2)
		}
		if a.Identity == "" && b.Identity == "" {
			owners["@sequence:@column:"+c.ID] = true
		}
	}
	keys := map[string]bool{}
	for _, r := range expected.Records {
		keys[pgPreservationRecordKey(r)] = true
	}
	// Copy the slice before appending; do not change the caller's independent
	// graph. Original identities were derived from automatic ownership edges,
	// never from a nextval string or a caller-supplied sequence name.
	expected = expected.Clone()
	for _, r := range before.Records {
		if !strings.HasPrefix(r.Object, "@sequence:") || !owners[r.Object] {
			continue
		}
		if keys[pgPreservationRecordKey(r)] {
			return PostgreSQLDeclarationGraph{}, errors.New("ambiguous retained PostgreSQL serial declaration")
		}
		keys[pgPreservationRecordKey(r)] = true
		expected.Records = append(expected.Records, r)
	}
	return expected, nil
}
