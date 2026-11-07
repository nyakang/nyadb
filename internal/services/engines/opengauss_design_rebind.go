package engines

import (
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// A native type conversion reparses dependent indexes and CHECK expressions.
// Captured dependency edges select those objects; only their typed semantic
// fields acquire independent CREATE values. ACLs, comments and unknown fields
// remain the original evidence even when the server rebuilds a physical index.
func gaussDesignTypeRebindings(expected map[string]map[string]any, before, probe schemaddl.OpenGaussDesignGraph, original contract.SchemaDesignSnapshot, desired schemaddl.Table) (map[string]bool, error) {
	changed := map[string]bool{}
	for _, old := range original.Schema.Table.Columns() {
		for _, next := range desired.Columns() {
			if old.ID != next.ID {
				continue
			}
			a, err := schemaddl.ParseOpenGaussField(old.SQL)
			if err != nil {
				return nil, err
			}
			b, err := schemaddl.ParseOpenGaussField(next.SQL)
			if err != nil {
				return nil, err
			}
			if a.Type != b.Type || a.Collation != b.Collation {
				key := "column:@table:@column:" + old.ID
				prior, next := before.Records[key], probe.Records[key]
				if prior == nil || next == nil {
					return nil, schemaddl.ErrConflict
				}
				for _, field := range []string{"atttypid", "atttypmod", "attcollation"} {
					if schemaddl.Signature(prior[field]) != schemaddl.Signature(next[field]) {
						changed["@column:"+old.ID] = true
					}
				}
			}
		}
	}
	objects := map[string]bool{}
	if dependencies := before.Records["dependencies:@table"]; dependencies != nil {
		rows, _ := dependencies["rows"].([]any)
		for _, raw := range rows {
			x, ok := raw.(map[string]any)
			if !ok || x["refobjid"] != "@table" {
				continue
			}
			column, _ := x["refobjsubid"].(string)
			object, _ := x["objid"].(string)
			if changed[column] && (strings.HasPrefix(object, "@constraint:") || strings.HasPrefix(object, "@incoming-constraint:") || strings.HasPrefix(object, "@constraint-index:") || strings.HasPrefix(object, "@index:")) {
				objects[object] = true
			}
		}
	}
	for key, row := range before.Records {
		field := ""
		if strings.HasPrefix(key, "constraint:") {
			field = "conkey"
		} else if strings.HasPrefix(key, "incoming_constraint:") {
			field = "confkey"
		} else if strings.HasPrefix(key, "index:") {
			field = "indkey"
		}
		if field != "" {
			columns, _ := row[field].([]any)
			for _, raw := range columns {
				column, _ := raw.(string)
				if changed[column] {
					objects[before.Objects[key]] = true
				}
			}
		}
		if strings.HasPrefix(key, "constraint:") || strings.HasPrefix(key, "incoming_constraint:") {
			index, _ := row["conindid"].(string)
			if objects[index] || objects[before.Objects[key]] {
				objects[before.Objects[key]] = true
				if index != "" {
					objects[index] = true
				}
			}
		}
	}
	// Native FK type changes recreate the child check triggers as well as the
	// constraint. Their scope follows the captured owning constraint, never an
	// arbitrary trigger OID or an unrelated field change.
	for _, object := range before.Objects {
		for owner := range objects {
			if (strings.HasPrefix(owner, "@constraint:") || strings.HasPrefix(owner, "@incoming-constraint:")) && strings.HasPrefix(object, "@internal-trigger:"+owner+":") {
				objects[object] = true
			}
		}
	}
	for key, row := range expected {
		if !objects[before.Objects[key]] {
			continue
		}
		prototype := probe.Records[key]
		if prototype == nil {
			continue // explicitly removed declarations are settled by the planner
		}
		var fields []string
		switch {
		case strings.HasPrefix(key, "index:"):
			fields = []string{"indclass", "indcollation", "indexprs", "indpred"}
		case strings.HasPrefix(key, "constraint:") || strings.HasPrefix(key, "incoming_constraint:"):
			fields = []string{"conbin", "consrc"}
			if row["contype"] == "f" {
				fields = append(fields, "conpfeqop", "conppeqop", "conffeqop")
			}
		case strings.HasPrefix(key, "column:"):
			fields = []string{"atttypid", "atttypmod", "attlen", "attbyval", "attalign", "attstorage", "attndims", "attcollation"}
		case strings.HasPrefix(key, "trigger:@internal-trigger:"):
			fields = []string{"tgowner"}
		}
		for _, field := range fields {
			if value, ok := prototype[field]; ok {
				row[field] = value
			}
		}
	}
	return objects, nil
}
