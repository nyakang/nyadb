package schemaddl

import (
	"encoding/json"
	"errors"
	"fmt"
)

var ErrPostgreSQLNotNull = errors.New("PostgreSQL unvalidated or inherited NOT NULL constraints are read-only")

// NOT NULL is column-owned in PG 18. Its name is not an editable clause, and
// must survive even though independent CREATE probes use different names.
func pgNotNullRecords(p *PostgreSQLCatalog) (map[int]PostgreSQLCatalogEntry, error) {
	out := map[int]PostgreSQLCatalogEntry{}
	if p == nil {
		return out, ErrConflict
	}
	for _, e := range p.Entries {
		if e.Kind != "constraint" {
			continue
		}
		var r struct {
			Kind     string `json:"contype"`
			Relation int64  `json:"conrelid,string"`
			Keys     []int  `json:"conkey"`
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(e.Data, &fields) != nil {
			return nil, ErrConflict
		}
		if json.Unmarshal(fields["contype"], &r.Kind) != nil || r.Kind != "n" {
			continue
		}
		rel, err := (pgGraphRecord{data: fields}).integer("conrelid")
		if err != nil {
			return nil, err
		}
		if rel != p.RelationOID {
			continue
		}
		if p.ServerVersion < 180000 || json.Unmarshal(fields["conkey"], &r.Keys) != nil || len(r.Keys) != 1 || r.Keys[0] <= 0 || out[r.Keys[0]].OID != 0 {
			return nil, ErrConflict
		}
		out[r.Keys[0]] = e
	}
	return out, nil
}

// PostgreSQLNotNullEditable can be used by the field form without I/O. Raw
// authoritative records remain in the immutable snapshot, not in UI inputs.
func PostgreSQLNotNullEditable(p *PostgreSQLCatalog, columnID string) error {
	records, err := pgNotNullRecords(p)
	if err != nil {
		return err
	}
	for number, e := range records {
		if columnID != fmt.Sprintf("column:%d", number) {
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(e.Data, &fields) != nil {
			return ErrConflict
		}
		r := pgGraphRecord{data: fields}
		valid, ve := r.boolean("convalidated")
		enforced, ee := r.boolean("conenforced")
		local, le := r.boolean("conislocal")
		inherited, ie := r.integer("coninhcount")
		parent, pe := r.integer("conparentid")
		if errors.Join(ve, ee, le, ie, pe) != nil || !valid || !enforced || !local || inherited != 0 || parent != 0 {
			return ErrPostgreSQLNotNull
		}
	}
	return nil
}

func pgVerifyNotNullNames(original, actual *PostgreSQLCatalog, desired Table) error {
	before, err := pgNotNullRecords(original)
	if err != nil {
		return err
	}
	after, err := pgNotNullRecords(actual)
	if err != nil {
		return err
	}
	for _, c := range desired.Columns() {
		f, err := ParsePostgreSQLField(c.SQL)
		if err != nil {
			return err
		}
		if !f.NotNull {
			continue
		}
		for number, e := range before {
			if c.ID != fmt.Sprintf("column:%d", number) {
				continue
			}
			next := after[number]
			var a, b map[string]json.RawMessage
			if next.OID == 0 || json.Unmarshal(e.Data, &a) != nil || json.Unmarshal(next.Data, &b) != nil {
				return ErrConflict
			}
			// Type rewrites may rebuild the constraint, but never rename it or
			// discard its original comment/validation/locality properties.
			for _, field := range []string{"conname", "comment", "conenforced", "convalidated", "conislocal", "coninhcount", "connoinherit", "conparentid"} {
				if Signature(a[field]) != Signature(b[field]) {
					return errors.New("PostgreSQL retained NOT NULL properties changed")
				}
			}
		}
	}
	return nil
}

// PostgreSQLRetainedNotNullDeclaration carries only original column-owned
// constraint properties into independent evidence. The planner forbids editing
// unsupported constraints; preservation separately verifies their raw records.
func PostgreSQLRetainedNotNullDeclaration(original Snapshot, desired Table, expected PostgreSQLDeclarationGraph) (PostgreSQLDeclarationGraph, error) {
	before, err := ProjectPostgreSQLDeclarationGraph(*original.Postgres, pgPreservationNames(original.Table))
	if err != nil {
		return PostgreSQLDeclarationGraph{}, err
	}
	retained := map[string]bool{}
	for _, c := range desired.Columns() {
		f, err := ParsePostgreSQLField(c.SQL)
		if err != nil {
			return PostgreSQLDeclarationGraph{}, err
		}
		if f.NotNull {
			retained["@notnull:@column:"+c.ID] = true
		}
	}
	records := map[string]PostgreSQLGraphRecord{}
	for _, r := range before.Records {
		if r.Kind == "constraint" && retained[r.Object] {
			records[r.Object] = r
		}
	}
	out := expected.Clone()
	for i, r := range out.Records {
		old, ok := records[r.Object]
		if r.Kind != "constraint" || !ok {
			continue
		}
		a, err := pgPreservationFields(old.Declaration)
		if err != nil {
			return PostgreSQLDeclarationGraph{}, err
		}
		b, err := pgPreservationFields(r.Declaration)
		if err != nil {
			return PostgreSQLDeclarationGraph{}, err
		}
		for _, field := range []string{"conenforced", "convalidated", "conislocal", "coninhcount", "connoinherit", "conparentid"} {
			if value, ok := a[field]; ok {
				b[field] = value
			}
		}
		out.Records[i].Declaration, err = pgGraphCanonicalJSON(b)
		if err != nil {
			return PostgreSQLDeclarationGraph{}, err
		}
	}
	return out, nil
}
