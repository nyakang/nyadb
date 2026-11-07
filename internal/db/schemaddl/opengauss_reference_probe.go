package schemaddl

import (
	"encoding/json"
	"errors"
	"fmt"
)

// OpenGaussReferenceProbe retains independently created native parent-side FK
// effects. Capture first proves that removing exactly those effects restores
// the complete before catalog. Actual Apply still needs a separately bound
// transition, callback validation and locks; this proof never authorizes DDL.
type OpenGaussReferenceProbe struct {
	baseline       OpenGaussReferenceBaseline
	child, parent  *OpenGaussCatalog
	constraints    map[uint64]string
	triggers       map[uint64]string
	childRevision  string
	parentRevision string
}

func CaptureOpenGaussReferenceProbe(baseline OpenGaussReferenceBaseline, original Snapshot, desired Table, child *OpenGaussCatalog, names OpenGaussDesignNames, parent *OpenGaussCatalog) (out OpenGaussReferenceProbe, err error) {
	if baseline.catalog == nil || original.OpenGauss == nil || child == nil || parent == nil || baseline.original != Signature(original) || baseline.desired != Signature(desired) || child.RelationOID == 0 || child.RelationOID == original.OpenGauss.RelationOID || child.RelationOID == parent.RelationOID || child.DatabaseOID != parent.DatabaseOID || child.RoleOID != parent.RoleOID || child.NamespaceOID != original.OpenGauss.NamespaceOID || child.Hooks || parent.Hooks || Signature(child.Classes) != Signature(parent.Classes) {
		return out, ErrConflict
	}
	childRevision, err := child.Fingerprint()
	if err != nil {
		return out, err
	}
	parentRevision, err := parent.Fingerprint()
	if err != nil {
		return out, err
	}
	constraints, triggers, err := gaussReferenceEffects(desired, child, names, parent)
	if err != nil {
		return out, err
	}
	if len(constraints) == 0 {
		return out, errors.New("native reference probe has no independently bound FK")
	}
	stripped, err := gaussRemoveReferenceProbe(parent, baseline.catalog, constraints, triggers)
	if err != nil {
		return out, err
	}
	if err = VerifyOpenGaussReferenceUnchanged(baseline, original, desired, stripped); err != nil {
		return out, errors.Join(errors.New("independent native probe changed retained parent context"), err)
	}
	out = OpenGaussReferenceProbe{baseline: baseline, child: child.Clone(), parent: parent.Clone(), constraints: constraints, triggers: triggers, childRevision: childRevision, parentRevision: parentRevision}
	return out, nil
}

// Probe rollback restores the exact original catalog. Explicit DROP retains
// native relhastriggers' positive hint until later maintenance. All probe FK,
// trigger and dependency records must be absent in either case; no filtering
// of the after catalog is allowed to hide incomplete cleanup.
func VerifyOpenGaussReferenceProbeReleased(probe OpenGaussReferenceProbe, original Snapshot, desired Table, after *OpenGaussCatalog, operation string) error {
	if probe.child == nil || probe.parent == nil || probe.baseline.catalog == nil || after == nil || probe.baseline.original != Signature(original) || probe.baseline.desired != Signature(desired) {
		return ErrConflict
	}
	childRevision, err := probe.child.Fingerprint()
	if err != nil || childRevision != probe.childRevision {
		return errors.Join(err, ErrConflict)
	}
	parentRevision, err := probe.parent.Fingerprint()
	if err != nil || parentRevision != probe.parentRevision {
		return errors.Join(err, ErrConflict)
	}
	expected := probe.baseline.catalog.Clone()
	if operation == "drop" {
		for i, entry := range expected.Entries {
			if entry.Kind != "relation" || entry.OID != expected.RelationOID {
				continue
			}
			row, err := gaussJSON(entry.Data)
			if err != nil {
				return err
			}
			before, ok := row["relhastriggers"].(bool)
			if !ok {
				return ErrConflict
			}
			row["relhastriggers"] = before || len(probe.triggers) > 0
			expected.Entries[i].Data, err = json.Marshal(row)
			if err != nil {
				return err
			}
		}
	} else if operation != "rollback" {
		return ErrUnsupported
	}
	want, err := expected.Fingerprint()
	if err != nil {
		return err
	}
	got, err := after.Fingerprint()
	if err != nil || got != want {
		return errors.Join(err, ErrConflict)
	}
	return nil
}

// These fixed identities and action rules come from native 6.0 catalogs and
// createForeignKeyTriggers. The runtime must also verify their native pg_proc
// implementations before validation; OIDs alone are not callback evidence.
func gaussCheckReferenceTrigger(entry OpenGaussCatalogEntry, row, constraint map[string]any, child, parent *OpenGaussCatalog) (uint64, error) {
	kind, err := gaussNumber(row["tgtype"])
	if err != nil || kind != 9 && kind != 17 {
		return 0, ErrConflict
	}
	actionField := "confdeltype"
	offset := uint64(0)
	if kind == 17 {
		actionField, offset = "confupdtype", 1
	}
	action, _ := constraint[actionField].(string)
	routine := map[string]uint64{"c": 1646, "r": 1648, "n": 1650, "d": 1652, "a": 1654}[action]
	if routine == 0 {
		return 0, ErrConflict
	}
	index, err := gaussNumber(constraint["conindid"])
	if err != nil || index == 0 {
		return 0, ErrConflict
	}
	for field, expected := range map[string]uint64{"tgrelid": parent.RelationOID, "tgconstrrelid": child.RelationOID, "tgconstrindid": index, "tgfoid": routine + offset, "tgnargs": 0, "tgowner": parent.RoleOID} {
		actual, err := gaussNumber(row[field])
		if err != nil || actual != expected {
			return 0, errors.Join(err, ErrConflict)
		}
	}
	if row["tgisinternal"] != true || row["tgenabled"] != "O" || row["tgname"] != fmt.Sprintf("RI_ConstraintTrigger_a_%d", entry.OID) || row["tgargs"] != `\x` || row["tgqual"] != nil {
		return 0, ErrConflict
	}
	for _, field := range []string{"tgordername", "tgorder", "tgtime", "tgfbody"} {
		if row[field] != nil {
			return 0, ErrConflict
		}
	}
	deferrable, deferred := false, false
	if action == "a" {
		deferrable, _ = constraint["condeferrable"].(bool)
		deferred, _ = constraint["condeferred"].(bool)
	}
	attrs, ok := row["tgattr"].([]any)
	if !ok || len(attrs) != 0 || row["tgdeferrable"] != deferrable || row["tginitdeferred"] != deferred {
		return 0, ErrConflict
	}
	return kind, nil
}

func gaussRemoveReferenceProbe(parent, before *OpenGaussCatalog, constraints, triggers map[uint64]string) (*OpenGaussCatalog, error) {
	removed := map[uint64]map[uint64]string{parent.Classes["pg_constraint"]: constraints, parent.Classes["pg_trigger"]: triggers}
	selected := func(class, oid any) (bool, error) {
		classID, err := gaussNumber(class)
		if err != nil {
			return false, err
		}
		objectID, err := gaussNumber(oid)
		return removed[classID][objectID] != "", err
	}
	var priorHint bool
	for _, entry := range before.Entries {
		if entry.Kind == "relation" && entry.OID == before.RelationOID {
			row, err := gaussJSON(entry.Data)
			if err != nil {
				return nil, err
			}
			var ok bool
			priorHint, ok = row["relhastriggers"].(bool)
			if !ok {
				return nil, ErrConflict
			}
		}
	}
	out := parent.Clone()
	out.Entries = nil
	for _, entry := range parent.Entries {
		if entry.Kind == "incoming_constraint" && constraints[entry.OID] != "" || entry.Kind == "trigger" && triggers[entry.OID] != "" {
			continue
		}
		row, err := gaussJSON(entry.Data)
		if err != nil {
			return nil, err
		}
		if entry.Kind == "relation" && entry.OID == parent.RelationOID {
			if row["relhastriggers"] != (priorHint || len(triggers) > 0) {
				return nil, ErrConflict
			}
			row["relhastriggers"] = priorHint
		}
		if entry.Kind == "dependencies" || entry.Kind == "shared_dependencies" || entry.Kind == "comments" || entry.Kind == "security_labels" {
			rows, hasRows := row["rows"].([]any)
			if row["rows"] != nil && !hasRows {
				return nil, ErrConflict
			}
			var kept []any
			for _, value := range rows {
				edge, ok := value.(map[string]any)
				if !ok {
					return nil, ErrConflict
				}
				remove := false
				if entry.Kind == "comments" || entry.Kind == "security_labels" {
					remove, err = selected(edge["classoid"], edge["objoid"])
				} else {
					remove, err = selected(edge["classid"], edge["objid"])
					if err == nil && !remove {
						remove, err = selected(edge["refclassid"], edge["refobjid"])
					}
				}
				if err != nil {
					return nil, err
				}
				if !remove {
					kept = append(kept, edge)
				}
			}
			if len(kept) == 0 {
				row["rows"] = nil
			} else {
				row["rows"] = kept
			}
		}
		entry.Data, err = json.Marshal(row)
		if err != nil {
			return nil, err
		}
		out.Entries = append(out.Entries, entry)
	}
	return out, nil
}

// Select only FKs owned by the captured child and explicitly declared IDs.
// Parent action records must exactly match the independent child catalog.
func gaussReferenceEffects(desired Table, child *OpenGaussCatalog, names OpenGaussDesignNames, parent *OpenGaussCatalog) (map[uint64]string, map[uint64]string, error) {
	definition, err := child.CopyDefinition()
	if err != nil {
		return nil, nil, err
	}
	wanted := map[string]bool{}
	for _, clause := range desired.Clauses {
		if clause.Kind == "constraint" {
			body, err := pgConstraintBody(clause, child.Namespace)
			if err != nil {
				return nil, nil, err
			}
			tokens, err := postgresFragment(body)
			if err != nil {
				return nil, nil, err
			}
			if tokens[0].word("FOREIGN") {
				wanted[clause.ID] = true
			}
		}
	}
	constraints := map[uint64]string{}
	constraintRows := map[uint64]map[string]any{}
	for _, constraint := range definition.Constraints {
		if constraint.Kind != "f" || constraint.ReferenceOID != parent.RelationOID {
			continue
		}
		id := names.Constraints[constraint.Name]
		if id == "" || !wanted[id] || constraint.ReferenceSchema != parent.Namespace || constraint.ReferenceName != parent.Name {
			return nil, nil, errors.New("native reference probe FK has no requested binding")
		}
		constraints[constraint.OID] = id
	}
	if len(constraints) == 0 {
		return constraints, map[uint64]string{}, nil
	}
	for _, entry := range child.Entries {
		if _, selected := constraints[entry.OID]; entry.Kind == "constraint" && selected {
			row, err := gaussJSON(entry.Data)
			if err != nil {
				return nil, nil, err
			}
			constraintRows[entry.OID] = row
		}
	}
	if len(constraintRows) != len(constraints) {
		return nil, nil, ErrConflict
	}
	incoming := map[uint64]bool{}
	triggers := map[uint64]string{}
	triggerKinds := map[uint64]map[uint64]bool{}
	for _, entry := range parent.Entries {
		row, err := gaussJSON(entry.Data)
		if err != nil {
			return nil, nil, err
		}
		if entry.Kind == "incoming_constraint" {
			if _, selected := constraints[entry.OID]; selected {
				if Signature(row) != Signature(constraintRows[entry.OID]) {
					return nil, nil, errors.New("native parent FK differs from independent child catalog")
				}
				incoming[entry.OID] = true
			}
		}
		if entry.Kind != "trigger" {
			continue
		}
		constraint, err := gaussNumber(row["tgconstraint"])
		if err != nil {
			return nil, nil, err
		}
		id := constraints[constraint]
		if id == "" {
			continue
		}
		kind, err := gaussCheckReferenceTrigger(entry, row, constraintRows[constraint], child, parent)
		if err != nil {
			return nil, nil, err
		}
		if triggerKinds[constraint] == nil {
			triggerKinds[constraint] = map[uint64]bool{}
		}
		if triggerKinds[constraint][kind] {
			return nil, nil, errors.New("duplicate native parent FK action trigger")
		}
		triggerKinds[constraint][kind] = true
		triggers[entry.OID] = fmt.Sprintf("%s:%d", id, kind)
	}
	if len(incoming) != len(constraints) || len(triggers) != 2*len(constraints) {
		return nil, nil, errors.New("native parent FK or action triggers missing")
	}
	return constraints, triggers, nil
}
