package schemaddl

import "testing"

func TestPostgreSQLReferenceTriggerFlagRequiresSelectedInternalTrigger(t *testing.T) {
	p, names := pgGraphFixture()
	pgGraphChange(t, &p, "relation", 650, "relhastriggers", false)
	q := p.Clone()
	pgGraphChange(t, q, "relation", 650, "relhastriggers", true)
	desired := Table{}
	for name, id := range names.Columns {
		desired.Clauses = append(desired.Clauses, Clause{ID: id, Kind: "column", Name: name})
	}
	for name, id := range names.Constraints {
		desired.Clauses = append(desired.Clauses, Clause{ID: id, Kind: "constraint", Name: name})
	}
	for name := range names.Indexes {
		desired.Definitions = append(desired.Definitions, Definition{Kind: "index", Name: name})
	}
	for name := range names.Triggers {
		desired.Definitions = append(desired.Definitions, Definition{Kind: "trigger", Name: name})
	}
	result, err := pgReferenceTriggerFlags(&p, q, desired)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range result.Entries {
		if e.Kind == "relation" && e.OID == 650 {
			fields, _ := pgPreservationFields(e.Data)
			if string(fields["relhastriggers"]) != "false" {
				t.Fatal("verified FK flag was not normalized")
			}
		}
	}
	for i, e := range q.Entries {
		if e.Kind == "trigger" && e.OID == 901 {
			q.Entries = append(q.Entries[:i], q.Entries[i+1:]...)
			break
		}
	}
	result, err = pgReferenceTriggerFlags(&p, q, desired)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range result.Entries {
		if e.Kind == "relation" && e.OID == 650 {
			fields, _ := pgPreservationFields(e.Data)
			if string(fields["relhastriggers"]) != "true" {
				t.Fatal("unproved external flag mutation was authorized")
			}
		}
	}
}
