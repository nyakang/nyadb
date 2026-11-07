package schemaddl

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// VerifyOpenGaussReferenceTransition compares actual FK effects with the
// independent desired declaration, then removes the proven original/desired
// effects to compare every retained parent record. The runtime separately
// proves permissions, selected native callbacks and transaction-held locks.
func VerifyOpenGaussReferenceTransition(baseline OpenGaussReferenceBaseline, original Snapshot, desired Table, probe OpenGaussReferenceProbe, child, parent *OpenGaussCatalog) error {
	if baseline.catalog == nil || original.OpenGauss == nil || child == nil || parent == nil || baseline.original != Signature(original) || baseline.desired != Signature(desired) || child.DatabaseOID != parent.DatabaseOID || child.RoleOID != parent.RoleOID || child.Hooks || parent.Hooks || child.Name != desired.Name || child.Namespace != original.OpenGauss.Namespace || original.Exists && child.RelationOID != original.OpenGauss.RelationOID {
		return ErrConflict
	}
	before := baseline.catalog
	revision, err := before.Fingerprint()
	if err != nil || revision != baseline.revision {
		return errors.Join(err, ErrConflict)
	}
	oldConstraints, oldTriggers := map[uint64]string{}, map[uint64]string{}
	if original.Exists {
		oldConstraints, oldTriggers, err = gaussReferenceEffects(original.Table, original.OpenGauss, OpenGaussNamesForTable(original.Table), before)
		if err != nil {
			return fmt.Errorf("original native parent effects: %w", err)
		}
	}
	newNames := OpenGaussNamesForTable(desired)
	newConstraints, newTriggers, err := gaussReferenceEffects(desired, child, newNames, parent)
	if err != nil {
		return fmt.Errorf("actual native parent effects: %w", err)
	}
	if err = gaussReferenceRetainedIdentities(original, desired, child, oldConstraints, oldTriggers, newConstraints, newTriggers); err != nil {
		return err
	}
	if len(newConstraints) != 0 {
		if probe.child == nil || probe.parent == nil || probe.baseline.original != Signature(original) || probe.baseline.desired != Signature(desired) || probe.parent.RelationOID != before.RelationOID {
			return ErrConflict
		}
		childRevision, e1 := probe.child.Fingerprint()
		parentRevision, e2 := probe.parent.Fingerprint()
		if e1 != nil || e2 != nil || childRevision != probe.childRevision || parentRevision != probe.parentRevision {
			return errors.Join(e1, e2, ErrConflict)
		}
		wanted, err := gaussReferenceEffectRecords(probe.child, probe.parent, probe.constraints, probe.triggers)
		if err != nil {
			return err
		}
		actual, err := gaussReferenceEffectRecords(child, parent, newConstraints, newTriggers)
		if err != nil || Signature(wanted) != Signature(actual) {
			return errors.Join(err, errors.New("native parent FK effects differ from independent declaration"), ErrConflict)
		}
	} else if probe.child != nil {
		return errors.New("native desired parent foreign keys disappeared")
	}
	strippedBefore, err := gaussRemoveReferenceProbe(before, before, oldConstraints, oldTriggers)
	if err != nil {
		return err
	}
	strippedAfter, err := gaussRemoveReferenceProbe(parent, before, newConstraints, newTriggers)
	if err != nil {
		return err
	}
	want, e1 := strippedBefore.Fingerprint()
	got, e2 := strippedAfter.Fingerprint()
	if e1 != nil || e2 != nil || want != got {
		return errors.Join(e1, e2, errors.New("native design changed retained parent context"), ErrConflict)
	}
	return nil
}

func gaussReferenceRetainedIdentities(original Snapshot, desired Table, child *OpenGaussCatalog, oldConstraints, oldTriggers, newConstraints, newTriggers map[uint64]string) error {
	retained := map[string]bool{}
	for _, old := range original.Table.Clauses {
		if old.Kind != "constraint" {
			continue
		}
		for _, next := range desired.Clauses {
			if old.ID == next.ID && old.Name == next.Name && old.SQL == next.SQL {
				retained[old.ID] = true
			}
		}
	}
	// Native type/collation rebinds are selected only by captured FK key slots.
	for _, entry := range original.OpenGauss.Entries {
		id := oldConstraints[entry.OID]
		if entry.Kind != "constraint" || !retained[id] {
			continue
		}
		row, err := gaussJSON(entry.Data)
		if err != nil {
			return err
		}
		keys, ok := row["conkey"].([]any)
		if !ok {
			return ErrConflict
		}
		for _, raw := range keys {
			number, err := gaussNumber(raw)
			if err != nil {
				return err
			}
			for _, old := range original.Table.Columns() {
				if old.OpenGauss == nil || old.OpenGauss.Number != int(number) {
					continue
				}
				for _, next := range desired.Columns() {
					if old.ID != next.ID {
						continue
					}
					var before, after map[string]any
					for _, column := range original.OpenGauss.Entries {
						if column.Kind == "column" && column.OID == original.OpenGauss.RelationOID && column.SubID == int(number) {
							before, err = gaussJSON(column.Data)
							if err != nil {
								return err
							}
						}
					}
					for _, column := range child.Entries {
						if column.Kind != "column" || column.OID != child.RelationOID {
							continue
						}
						row, err := gaussJSON(column.Data)
						if err != nil {
							return err
						}
						if row["attname"] == next.Name && row["attisdropped"] == false {
							after = row
						}
					}
					if before == nil || after == nil {
						return ErrConflict
					}
					for _, field := range []string{"atttypid", "atttypmod", "attcollation"} {
						if Signature(before[field]) != Signature(after[field]) {
							retained[id] = false
						}
					}
				}
			}
		}
	}
	for oid, id := range oldConstraints {
		if retained[id] && newConstraints[oid] != id {
			return errors.New("native retained parent foreign key identity changed")
		}
	}
	for oid, id := range oldTriggers {
		constraint := id
		if at := strings.LastIndexByte(id, ':'); at >= 0 {
			constraint = id[:at]
		}
		if retained[constraint] && newTriggers[oid] != id {
			return errors.New("native retained parent action trigger identity changed")
		}
	}
	return nil
}

// Keep every selected effect/property, including dependency edges and unknown
// fields. Only explicitly captured child relation, key slots, constraint and
// internal trigger identities receive stable names. Parent/index OIDs remain
// exact native identities; arbitrary numeric fields are never normalized.
func gaussReferenceEffectRecords(child, parent *OpenGaussCatalog, constraints, triggers map[uint64]string) (map[string]any, error) {
	objects := map[uint64]map[uint64]string{
		parent.Classes["pg_class"]:      {child.RelationOID: "@child"},
		parent.Classes["pg_constraint"]: constraints,
		parent.Classes["pg_trigger"]:    triggers,
	}
	selected := func(class, oid any) (bool, error) {
		classID, err := gaussNumber(class)
		if err != nil {
			return false, err
		}
		objectID, err := gaussNumber(oid)
		return classID != parent.Classes["pg_class"] && objects[classID][objectID] != "", err
	}
	normalize := func(class, oid any) (any, error) {
		classID, err := gaussNumber(class)
		if err != nil {
			return nil, err
		}
		objectID, err := gaussNumber(oid)
		if err != nil {
			return nil, err
		}
		if name := objects[classID][objectID]; name != "" {
			return name, nil
		}
		return oid, nil
	}
	out := map[string]any{}
	for _, entry := range parent.Entries {
		row, err := gaussJSON(entry.Data)
		if err != nil {
			return nil, err
		}
		if entry.Kind == "incoming_constraint" && constraints[entry.OID] != "" {
			id := constraints[entry.OID]
			row["conname"], row["conrelid"] = id, "@child"
			// Key numbers come from the selected child's full native columns.
			keys, ok := row["conkey"].([]any)
			if !ok {
				return nil, ErrConflict
			}
			for i, value := range keys {
				number, err := gaussNumber(value)
				if err != nil || number == 0 {
					return nil, ErrConflict
				}
				// The independent desired declaration and actual table can have
				// dropped-slot holes; native FK key order follows column names.
				found := false
				for _, column := range child.Entries {
					if column.Kind != "column" || column.OID != child.RelationOID || column.SubID != int(number) {
						continue
					}
					attribute, err := gaussJSON(column.Data)
					if err != nil || attribute["attisdropped"] != false {
						return nil, ErrConflict
					}
					keys[i], found = attribute["attname"], true
				}
				if !found {
					return nil, ErrConflict
				}
			}
			out["constraint:"+id] = row
		}
		if entry.Kind == "trigger" && triggers[entry.OID] != "" {
			id := triggers[entry.OID]
			row["tgname"], row["tgconstrrelid"] = id, "@child"
			row["tgconstraint"], err = normalize(strconv.FormatUint(parent.Classes["pg_constraint"], 10), row["tgconstraint"])
			if err != nil {
				return nil, err
			}
			out["trigger:"+id] = row
		}
		if entry.Kind != "dependencies" && entry.Kind != "shared_dependencies" && entry.Kind != "comments" && entry.Kind != "security_labels" {
			continue
		}
		var normalized []any
		values, ok := row["rows"].([]any)
		if row["rows"] != nil && !ok {
			return nil, ErrConflict
		}
		for _, value := range values {
			edge, ok := value.(map[string]any)
			if !ok {
				return nil, ErrConflict
			}
			objectClass, objectID := "classid", "objid"
			if entry.Kind == "comments" || entry.Kind == "security_labels" {
				objectClass, objectID = "classoid", "objoid"
			}
			affected, err := selected(edge[objectClass], edge[objectID])
			if err == nil && !affected && objectClass == "classid" {
				affected, err = selected(edge["refclassid"], edge["refobjid"])
			}
			if err != nil {
				return nil, err
			}
			if !affected {
				continue
			}
			edge[objectID], err = normalize(edge[objectClass], edge[objectID])
			if err == nil && objectClass == "classid" {
				edge["refobjid"], err = normalize(edge["refclassid"], edge["refobjid"])
			}
			if err != nil {
				return nil, err
			}
			normalized = append(normalized, edge)
		}
		sort.Slice(normalized, func(i, j int) bool { return Signature(normalized[i]) < Signature(normalized[j]) })
		out[entry.Kind] = normalized
	}
	return out, nil
}
