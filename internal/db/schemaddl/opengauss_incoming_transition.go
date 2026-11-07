package schemaddl

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strconv"
)

type gaussIncomingEffects struct {
	rows        map[string]map[string]any
	constraints map[uint64]string
	triggers    map[uint64]string
}

func gaussIncomingColumnNames(catalog *OpenGaussCatalog, names map[string]string) (map[int]string, error) {
	out := map[int]string{}
	for _, entry := range catalog.Entries {
		if entry.Kind != "column" || entry.OID != catalog.RelationOID {
			continue
		}
		row, err := gaussJSON(entry.Data)
		if err != nil {
			return nil, err
		}
		if row["attisdropped"] == false {
			name, _ := row["attname"].(string)
			id := name
			if names != nil {
				id = names[name]
			}
			if id == "" || entry.SubID <= 0 {
				return nil, ErrConflict
			}
			out[entry.SubID] = id
		}
	}
	return out, nil
}

func gaussIncomingIndexNames(parent *OpenGaussCatalog, names OpenGaussDesignNames) (map[uint64]string, error) {
	definition, err := parent.CopyDefinition()
	if err != nil {
		return nil, err
	}
	out := map[uint64]string{}
	for _, constraint := range definition.Constraints {
		if constraint.Kind != "f" && constraint.IndexOID != 0 {
			id := names.Constraints[constraint.Name]
			if id == "" {
				return nil, ErrConflict
			}
			out[constraint.IndexOID] = "@constraint-index:" + id
		}
	}
	for _, index := range definition.Indexes {
		if out[index.OID] == "" {
			id := names.Definitions["index:"+index.Name]
			if id == "" {
				return nil, ErrConflict
			}
			out[index.OID] = "@index:" + id
		}
	}
	return out, nil
}

func gaussIncomingCheckTrigger(entry OpenGaussCatalogEntry, row, constraint map[string]any, child, parent *OpenGaussCatalog) (string, error) {
	kind, err := gaussNumber(row["tgtype"])
	if err != nil || kind != 5 && kind != 17 {
		return "", ErrConflict
	}
	routine, suffix := uint64(1644), "check-ins"
	if kind == 17 {
		routine, suffix = 1645, "check-upd"
	}
	index, err := gaussNumber(constraint["conindid"])
	if err != nil || index == 0 {
		return "", ErrConflict
	}
	for field, expected := range map[string]uint64{"tgrelid": child.RelationOID, "tgconstrrelid": parent.RelationOID, "tgconstrindid": index, "tgfoid": routine, "tgnargs": 0} {
		actual, err := gaussNumber(row[field])
		if err != nil || actual != expected {
			return "", ErrConflict
		}
	}
	owner, err := gaussNumber(row["tgowner"])
	if err != nil || owner == 0 || row["tgisinternal"] != true || row["tgenabled"] != "O" || row["tgname"] != fmt.Sprintf("RI_ConstraintTrigger_c_%d", entry.OID) || row["tgargs"] != `\x` || row["tgqual"] != nil || row["tgdeferrable"] != constraint["condeferrable"] || row["tginitdeferred"] != constraint["condeferred"] {
		return "", ErrConflict
	}
	attrs, ok := row["tgattr"].([]any)
	if !ok || len(attrs) != 0 {
		return "", ErrConflict
	}
	for _, field := range []string{"tgordername", "tgorder", "tgtime", "tgfbody"} {
		if row[field] != nil {
			return "", ErrConflict
		}
	}
	return suffix, nil
}

func gaussCaptureIncomingEffects(baseline OpenGaussIncomingBaseline, child, parent *OpenGaussCatalog, parentNames OpenGaussDesignNames, privateNames *OpenGaussDesignNames) (gaussIncomingEffects, error) {
	out := gaussIncomingEffects{rows: map[string]map[string]any{}, constraints: map[uint64]string{}, triggers: map[uint64]string{}}
	childColumns, err := gaussIncomingColumnNames(child, nil)
	if err != nil {
		return out, err
	}
	parentColumns, err := gaussIncomingColumnNames(parent, parentNames.Columns)
	if err != nil {
		return out, err
	}
	indexes, err := gaussIncomingIndexNames(parent, parentNames)
	if err != nil {
		return out, err
	}
	rawConstraints := map[uint64]map[string]any{}
	seenIDs := map[string]bool{}
	for _, entry := range child.Entries {
		if entry.Kind != "constraint" {
			continue
		}
		row, err := gaussJSON(entry.Data)
		if err != nil {
			return out, err
		}
		if row["contype"] != "f" {
			continue
		}
		parentOID, err := gaussNumber(row["confrelid"])
		if err != nil {
			return out, err
		}
		if parentOID != parent.RelationOID {
			continue
		}
		name, _ := row["conname"].(string)
		id := ""
		for _, constraint := range baseline.constraints {
			wanted := "incoming:" + strconv.FormatUint(constraint.OID, 10)
			if privateNames == nil && name == constraint.Name || privateNames != nil && privateNames.Constraints[name] == wanted {
				id = wanted
			}
		}
		if id == "" || seenIDs[id] {
			return out, ErrConflict
		}
		rawConstraints[entry.OID] = row
		out.constraints[entry.OID] = id
		seenIDs[id] = true
	}
	if len(rawConstraints) != len(baseline.constraints) {
		return out, ErrConflict
	}
	objects := map[uint64]map[uint64]string{
		child.Classes["pg_class"]:      {child.RelationOID: "@child", parent.RelationOID: "@parent"},
		child.Classes["pg_constraint"]: out.constraints,
		child.Classes["pg_trigger"]:    out.triggers,
	}
	for oid, id := range indexes {
		objects[child.Classes["pg_class"]][oid] = id
	}
	for _, entry := range child.Entries {
		if entry.Kind != "trigger" {
			continue
		}
		row, err := gaussJSON(entry.Data)
		if err != nil {
			return out, err
		}
		constraint, err := gaussNumber(row["tgconstraint"])
		if err != nil {
			return out, err
		}
		if id := out.constraints[constraint]; id != "" {
			suffix, err := gaussIncomingCheckTrigger(entry, row, rawConstraints[constraint], child, parent)
			if err != nil {
				return out, err
			}
			id += ":" + suffix
			if out.rows["trigger:"+id] != nil {
				return out, ErrConflict
			}
			out.triggers[entry.OID] = id
			row["tgname"], row["tgconstraint"] = id, out.constraints[constraint]
			row["tgrelid"], row["tgconstrrelid"] = "@child", "@parent"
			index, _ := gaussNumber(row["tgconstrindid"])
			if indexes[index] == "" {
				return out, ErrConflict
			}
			row["tgconstrindid"] = indexes[index]
			if row["oid"] != nil {
				row["oid"] = id
			}
			out.rows["trigger:"+id] = row
		}
	}
	if len(out.triggers) != 2*len(out.constraints) {
		return out, ErrConflict
	}
	for oid, raw := range rawConstraints {
		row := maps.Clone(raw)
		id := out.constraints[oid]
		index, _ := gaussNumber(row["conindid"])
		if indexes[index] == "" {
			return out, ErrConflict
		}
		row["conname"], row["conrelid"], row["confrelid"], row["conindid"] = id, "@child", "@parent", indexes[index]
		row["conkey"], err = gaussDesignAttributeVector(row["conkey"], childColumns)
		if err != nil {
			return out, err
		}
		row["confkey"], err = gaussDesignAttributeVector(row["confkey"], parentColumns)
		if err != nil {
			return out, err
		}
		if row["oid"] != nil {
			row["oid"] = id
		}
		out.rows["constraint:"+id] = row
	}
	selected := func(class, oid any) (bool, error) {
		classID, e1 := gaussNumber(class)
		objectID, e2 := gaussNumber(oid)
		return classID != child.Classes["pg_class"] && objects[classID][objectID] != "", errors.Join(e1, e2)
	}
	for _, entry := range child.Entries {
		if entry.Kind != "dependencies" && entry.Kind != "shared_dependencies" && entry.Kind != "comments" && entry.Kind != "security_labels" {
			continue
		}
		row, err := gaussJSON(entry.Data)
		if err != nil {
			return out, err
		}
		values, ok := row["rows"].([]any)
		if row["rows"] != nil && !ok {
			return out, ErrConflict
		}
		var rows []any
		for _, value := range values {
			edge, ok := value.(map[string]any)
			if !ok {
				return out, ErrConflict
			}
			classField, oidField := "classid", "objid"
			if entry.Kind == "comments" || entry.Kind == "security_labels" {
				classField, oidField = "classoid", "objoid"
			}
			affected, err := selected(edge[classField], edge[oidField])
			if err == nil && !affected && classField == "classid" {
				affected, err = selected(edge["refclassid"], edge["refobjid"])
			}
			if err != nil {
				return out, err
			}
			if !affected {
				continue
			}
			pairs := [][3]string{{classField, oidField, "objsubid"}}
			if classField == "classid" {
				pairs = append(pairs, [3]string{"refclassid", "refobjid", "refobjsubid"})
			}
			for _, pair := range pairs {
				class, e1 := gaussNumber(edge[pair[0]])
				oid, e2 := gaussNumber(edge[pair[1]])
				if e1 != nil || e2 != nil {
					return out, ErrConflict
				}
				if id := objects[class][oid]; id != "" {
					edge[pair[1]] = id
				}
				if class == child.Classes["pg_class"] && edge[pair[2]] != nil {
					number, err := gaussNumber(edge[pair[2]])
					if err != nil {
						return out, err
					}
					columns := map[int]string{}
					if oid == child.RelationOID {
						columns = childColumns
					} else if oid == parent.RelationOID {
						columns = parentColumns
					}
					if id := columns[int(number)]; id != "" {
						edge[pair[2]] = id
					}
				}
			}
			rows = append(rows, edge)
		}
		sort.Slice(rows, func(i, j int) bool { return Signature(rows[i]) < Signature(rows[j]) })
		row["rows"] = rows
		out.rows[entry.Kind] = row
	}
	return out, nil
}

func gaussStripIncomingEffects(catalog *OpenGaussCatalog, effects gaussIncomingEffects) (*OpenGaussCatalog, error) {
	out := catalog.Clone()
	out.Entries = nil
	selected := func(class, oid any) (bool, error) {
		classID, e1 := gaussNumber(class)
		objectID, e2 := gaussNumber(oid)
		return classID == catalog.Classes["pg_constraint"] && effects.constraints[objectID] != "" || classID == catalog.Classes["pg_trigger"] && effects.triggers[objectID] != "", errors.Join(e1, e2)
	}
	for _, entry := range catalog.Entries {
		if entry.Kind == "constraint" && effects.constraints[entry.OID] != "" || entry.Kind == "trigger" && effects.triggers[entry.OID] != "" {
			continue
		}
		row, err := gaussJSON(entry.Data)
		if err != nil {
			return nil, err
		}
		if entry.Kind == "dependencies" || entry.Kind == "shared_dependencies" || entry.Kind == "comments" || entry.Kind == "security_labels" {
			values, _ := row["rows"].([]any)
			var retained []any
			for _, value := range values {
				edge, ok := value.(map[string]any)
				if !ok {
					return nil, ErrConflict
				}
				classField, oidField := "classid", "objid"
				if entry.Kind == "comments" || entry.Kind == "security_labels" {
					classField, oidField = "classoid", "objoid"
				}
				remove, err := selected(edge[classField], edge[oidField])
				if err == nil && !remove && classField == "classid" {
					remove, err = selected(edge["refclassid"], edge["refobjid"])
				}
				if err != nil {
					return nil, err
				}
				if !remove {
					retained = append(retained, edge)
				}
			}
			row["rows"] = retained
			entry.Data, err = json.Marshal(row)
			if err != nil {
				return nil, err
			}
		}
		out.Entries = append(out.Entries, entry)
	}
	return out, nil
}
