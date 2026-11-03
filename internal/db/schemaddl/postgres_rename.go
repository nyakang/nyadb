package schemaddl

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// VerifyPostgreSQLRename proves preservation of the complete original catalog
// for a native table/column rename. OIDs, parse trees, permissions, dependency
// edges and unknown JSON properties must remain unchanged. Only requested
// names, PostgreSQL's associated type names and server-derived
// SQL presentation may differ. This does not authorize type-changing ALTER.
func VerifyPostgreSQLRename(original Snapshot, desired Table, actual Snapshot, schema string) error {
	plan, err := PlanPostgreSQL(original, desired, schema, false)
	if err != nil {
		return err
	}
	if plan.Mode != "rename_table" && plan.Mode != "rename_column" {
		return ErrUnsupported
	}
	if !actual.Exists || actual.Engine != original.Engine || actual.Postgres == nil || actual.Table.Name != desired.Name || actual.Postgres.RelationOID != original.Postgres.RelationOID {
		return errors.New("PostgreSQL rename changed the original table identity")
	}
	actualRevision, err := actual.Postgres.Fingerprint()
	if err != nil || actualRevision != actual.Revision {
		return errors.Join(ErrConflict, err)
	}
	canonical, err := actual.Postgres.Table()
	if err != nil || Signature(canonical) != Signature(actual.Table) {
		return errors.Join(ErrConflict, err)
	}
	if len(actual.Table.Clauses) != len(desired.Clauses) || len(actual.Table.Definitions) != len(desired.Definitions) {
		return errors.New("PostgreSQL rename changed the structural object set")
	}
	for i, c := range actual.Table.Clauses {
		wanted := desired.Clauses[i]
		if c.ID != wanted.ID || c.Kind != wanted.Kind || c.Name != wanted.Name || Signature(c.Postgres) != Signature(wanted.Postgres) {
			return errors.New("PostgreSQL rename changed field/constraint identity or properties")
		}
	}
	for i, d := range actual.Table.Definitions {
		wanted := desired.Definitions[i]
		if d.Name != wanted.Name || d.Kind != wanted.Kind || Signature(d.Postgres) != Signature(wanted.Postgres) {
			return errors.New("PostgreSQL rename changed index/trigger identity or properties")
		}
	}
	if Signature(actual.Table.Postgres) != Signature(desired.Postgres) {
		return errors.New("PostgreSQL rename changed table properties")
	}
	expected := original.Postgres.Clone()
	before, err := pgRenameRecords(expected)
	if err != nil {
		return err
	}
	after, err := pgRenameRecords(actual.Postgres)
	if err != nil {
		return err
	}
	if len(before) != len(after) {
		return errors.New("PostgreSQL rename changed the complete catalog object set")
	}
	root := expected.RelationOID
	relation, ok := before[pgRenameKey("relation", root, 0)]
	if !ok {
		return ErrConflict
	}
	rowType := pgRenameNumber(relation["reltype"])
	var arrayType int64
	if record, ok := before[pgRenameKey("type", rowType, 0)]; ok {
		arrayType = pgRenameNumber(record["typarray"])
	}
	changedTypes := map[int64]bool{}
	if plan.Mode == "rename_table" {
		changedTypes, err = pgRenameTypes(before, original.Postgres, rowType, arrayType, desired.Name)
		if err != nil {
			return err
		}
		relation["relname"] = desired.Name
	} else {
		// RENAME COLUMN recursively renames inherited descendants; qualify
		// them through captured inheritance edges, not by a shared label.
		descendants := map[int64]bool{root: true}
		for changed := true; changed; {
			changed = false
			for _, e := range expected.Entries {
				if e.Kind != "inheritance" {
					continue
				}
				r := before[pgRenameKey(e.Kind, e.OID, e.SubID)]
				child := pgRenameNumber(r["inhrelid"])
				if descendants[pgRenameNumber(r["inhparent"])] && !descendants[child] {
					descendants[child], changed = true, true
				}
			}
		}
		rootRenamed := false
		for _, e := range expected.Entries {
			if e.Kind != "column" || !descendants[e.OID] {
				continue
			}
			r := before[pgRenameKey(e.Kind, e.OID, e.SubID)]
			if r["attname"] == plan.RenameFrom && r["attisdropped"] != true {
				r["attname"] = plan.RenameTo
				rootRenamed = rootRenamed || e.OID == root
			}
		}
		if !rootRenamed {
			return ErrConflict
		}
	}
	// Derived SQL changes with catalog names. Only fields backed by complete
	// immutable authoritative records are excluded; their underlying parse
	// trees, IDs and all remaining fields are still compared byte-exactly
	// after JSON key/number normalization.
	for _, e := range expected.Entries {
		key := pgRenameKey(e.Kind, e.OID, e.SubID)
		a, b := before[key], after[key]
		if b == nil {
			return fmt.Errorf("PostgreSQL rename removed catalog record %s", key)
		}
		strip := func(field string, backing ...string) error {
			if Signature(a[field]) == Signature(b[field]) {
				return nil
			}
			if _, ok := a[field]; !ok {
				return fmt.Errorf("unexpected PostgreSQL derived field %s.%s", key, field)
			}
			if _, ok := b[field]; !ok {
				return fmt.Errorf("missing PostgreSQL derived field %s.%s", key, field)
			}
			for _, name := range backing {
				if _, ok := a[name]; !ok {
					return fmt.Errorf("incomplete PostgreSQL rename proof %s.%s", key, name)
				}
				if Signature(a[name]) != Signature(b[name]) {
					return fmt.Errorf("PostgreSQL rename changed authoritative field %s.%s", key, name)
				}
			}
			delete(a, field)
			delete(b, field)
			return nil
		}
		var checks []error
		switch e.Kind {
		case "relation":
			if Signature(a["view_definition"]) != Signature(b["view_definition"]) {
				backed := false
				for _, d := range expected.Entries {
					if d.Kind == "rewrite" {
						r := before[pgRenameKey(d.Kind, d.OID, d.SubID)]
						_, action := r["ev_action"]
						_, qual := r["ev_qual"]
						backed = backed || pgRenameNumber(r["ev_class"]) == e.OID && action && qual
					}
				}
				if !backed {
					return errors.New("PostgreSQL renamed view has no captured rewrite tree")
				}
			}
			checks = append(checks, strip("view_definition", "relkind"), strip("partition_bound", "relpartbound"))
			// The captured inheritance records below remain unchanged.
			if Signature(a["parents"]) != Signature(b["parents"]) {
				backed := false
				for _, d := range expected.Entries {
					if d.Kind == "inheritance" {
						r := before[pgRenameKey(d.Kind, d.OID, d.SubID)]
						backed = backed || pgRenameNumber(r["inhrelid"]) == e.OID
					}
				}
				if !backed {
					return errors.New("PostgreSQL renamed parent has no captured inheritance edge")
				}
			}
			checks = append(checks, strip("parents"))
		case "column":
			if plan.Mode == "rename_table" && changedTypes[pgRenameNumber(a["atttypid"])] {
				checks = append(checks, strip("type_sql", "atttypid", "atttypmod"))
			}
			if Signature(a["default_sql"]) != Signature(b["default_sql"]) {
				backed := false
				for _, d := range expected.Entries {
					if d.Kind != "default" {
						continue
					}
					r := before[pgRenameKey(d.Kind, d.OID, d.SubID)]
					if pgRenameNumber(r["adrelid"]) == e.OID && pgRenameNumber(r["adnum"]) == int64(e.SubID) {
						backed = true
					}
				}
				if !backed {
					return errors.New("PostgreSQL renamed default has no captured expression tree")
				}
				checks = append(checks, strip("default_sql"))
			}
		case "constraint":
			checks = append(checks, strip("definition", "conrelid", "confrelid", "conkey", "confkey", "conbin"))
		case "index":
			checks = append(checks, strip("definition", "indrelid", "indkey", "indclass", "indexprs", "indpred"))
		case "trigger":
			checks = append(checks, strip("definition", "tgrelid", "tgtype", "tgattr", "tgargs", "tgqual"))
		case "default":
			checks = append(checks, strip("definition", "adrelid", "adnum", "adbin"))
		case "rewrite":
			checks = append(checks, strip("definition", "ev_class", "ev_action", "ev_qual"))
		case "routine":
			checks = append(checks, strip("definition", "prosrc", "prosqlbody"))
		case "policy":
			checks = append(checks, strip("using_expression", "polqual"), strip("check_expression", "polwithcheck"))
		}
		if err := errors.Join(checks...); err != nil {
			return err
		}
		if Signature(a) != Signature(b) {
			var fields []string
			for name, value := range a {
				other, ok := b[name]
				if !ok || Signature(value) != Signature(other) {
					fields = append(fields, name)
				}
			}
			for name := range b {
				if _, ok := a[name]; !ok {
					fields = append(fields, name)
				}
			}
			sort.Strings(fields)
			return fmt.Errorf("PostgreSQL rename changed catalog record %s fields %s beyond requested names", key, strings.Join(fields, ", "))
		}
	}
	// Header scope is also authoritative; compare it independently of the
	// records instead of allowing another database to borrow matching OIDs.
	x, y := expected.Clone(), actual.Postgres.Clone()
	x.Entries, y.Entries = nil, nil
	if Signature(x) != Signature(y) {
		return errors.New("PostgreSQL rename changed original catalog scope")
	}
	return nil
}

func pgRenameKey(kind string, oid int64, sub int) string {
	return fmt.Sprintf("%s:%d:%d", kind, oid, sub)
}

func pgRenameRecords(p *PostgreSQLCatalog) (map[string]map[string]any, error) {
	if _, err := p.Fingerprint(); err != nil {
		return nil, err
	}
	result := map[string]map[string]any{}
	for _, e := range p.Entries {
		d := json.NewDecoder(strings.NewReader(string(e.Data)))
		d.UseNumber()
		var r map[string]any
		if err := d.Decode(&r); err != nil {
			return nil, err
		}
		result[pgRenameKey(e.Kind, e.OID, e.SubID)] = r
	}
	return result, nil
}

func pgRenameNumber(v any) int64 {
	var raw string
	switch x := v.(type) {
	case json.Number:
		raw = x.String()
	case string:
		raw = x
	}
	n, _ := strconv.ParseInt(raw, 10, 64)
	return n
}

func pgRenameUnusedArrayName(used map[string]int64, name string, versions ...int) (string, error) {
	if len(versions) > 0 && versions[0] < 160000 {
		for pass := 1; pass <= len(used)+1; pass++ {
			prefix := strings.Repeat("_", pass)
			if len(prefix) >= 63 {
				return "", ErrUnsupported
			}
			clipped := name
			if len(clipped) > 63-len(prefix) {
				clipped = clipped[:63-len(prefix)]
				for !utf8.ValidString(clipped) {
					clipped = clipped[:len(clipped)-1]
				}
			}
			candidate := prefix + clipped
			if used[candidate] == 0 {
				return candidate, nil
			}
		}
		return "", ErrUnsupported
	}
	for pass := 0; pass <= len(used); pass++ {
		suffix := ""
		if pass > 0 {
			suffix = "_" + strconv.Itoa(pass)
		}
		clipped := name
		if len(clipped) > 62-len(suffix) {
			clipped = clipped[:62-len(suffix)]
			for !utf8.ValidString(clipped) {
				clipped = clipped[:len(clipped)-1]
			}
		}
		candidate := "_" + clipped + suffix
		if used[candidate] == 0 {
			return candidate, nil
		}
	}
	return "", errors.New("PostgreSQL rename cannot allocate an associated array type name")
}

func pgRenameTypes(records map[string]map[string]any, p *PostgreSQLCatalog, rowType, arrayType int64, name string) (map[int64]bool, error) {
	types := map[int64]map[string]any{}
	used := map[string]int64{}
	for _, e := range p.Entries {
		if e.Kind != "type" {
			continue
		}
		r := records[pgRenameKey(e.Kind, e.OID, e.SubID)]
		if pgRenameNumber(r["typnamespace"]) == p.NamespaceOID {
			label, ok := r["typname"].(string)
			if !ok || e.SubID != 0 || PostgreSQLIdentifier(label) != nil || used[label] != 0 {
				return nil, errors.New("incomplete PostgreSQL type namespace for rename")
			}
			types[e.OID] = r
			used[label] = e.OID
		}
	}
	row := types[rowType]
	if row == nil || row["typtype"] != "c" || pgRenameNumber(row["typrelid"]) != p.RelationOID || row["typname"] != records[pgRenameKey("relation", p.RelationOID, 0)]["relname"] {
		return nil, errors.New("PostgreSQL rename requires the complete associated row type")
	}
	changed := map[int64]bool{}
	rename := func(oid int64, label string) {
		r := types[oid]
		delete(used, r["typname"].(string))
		r["typname"] = label
		used[label] = oid
		changed[oid] = true
	}
	occupied := used[name]
	if occupied != 0 {
		// PostgreSQL can move a conflicting autogenerated array out of the
		// way. That array's base association and all opaque fields must stay
		// intact; a conflicting ordinary type cannot be silently replaced.
		array := types[occupied]
		element := pgRenameNumber(array["typelem"])
		base := types[element]
		if array["typisdefined"] != true || base == nil || pgRenameNumber(base["typarray"]) != occupied {
			return nil, errors.New("PostgreSQL rename conflicts with an existing non-array type")
		}
		moved, err := pgRenameUnusedArrayName(used, name, p.ServerVersion)
		if err != nil {
			return nil, err
		}
		rename(occupied, moved)
	}
	rename(rowType, name)
	if arrayType != 0 && arrayType != occupied {
		array := types[arrayType]
		if array == nil || pgRenameNumber(array["typelem"]) != rowType {
			return nil, errors.New("PostgreSQL rename requires the complete associated array type")
		}
		label, err := pgRenameUnusedArrayName(used, name, p.ServerVersion)
		if err != nil {
			return nil, err
		}
		rename(arrayType, label)
	}
	return changed, nil
}
