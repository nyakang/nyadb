package schemaddl

import (
	"errors"
	"fmt"
	"maps"
	"sort"
	"strconv"
)

// A private probe freezes the independent parent and selected empty child.
// Neither a mutable after-image nor a caller-supplied fingerprint can replace
// its expected type/operator/trigger effects during actual verification.
type OpenGaussIncomingTransitionProbe struct {
	baseline                      OpenGaussIncomingBaseline
	parent, child                 *OpenGaussCatalog
	parentNames, childNames       OpenGaussDesignNames
	parentRevision, childRevision string
}

func CaptureOpenGaussIncomingTransitionProbe(baseline OpenGaussIncomingBaseline, original Snapshot, desired Table, parent, child *OpenGaussCatalog, parentNames, childNames OpenGaussDesignNames) (out OpenGaussIncomingTransitionProbe, err error) {
	if parent == nil || child == nil || original.OpenGauss == nil || parent.RelationOID == original.OpenGauss.RelationOID || baseline.child == nil || child.RelationOID == baseline.child.RelationOID {
		return out, ErrConflict
	}
	bound, err := BindOpenGaussIncomingGraph(parentNames, baseline, original, desired, parent, child, &childNames)
	if err != nil {
		return out, err
	}
	if _, err = gaussCaptureIncomingEffects(baseline, child, parent, bound, &childNames); err != nil {
		return out, err
	}
	parentRevision, e1 := parent.Fingerprint()
	childRevision, e2 := child.Fingerprint()
	if e1 != nil || e2 != nil {
		return out, errors.Join(e1, e2)
	}
	out = OpenGaussIncomingTransitionProbe{
		baseline: baseline, parent: parent.Clone(), child: child.Clone(), parentNames: bound,
		childNames:     OpenGaussDesignNames{Columns: maps.Clone(childNames.Columns), Constraints: maps.Clone(childNames.Constraints)},
		parentRevision: parentRevision, childRevision: childRevision,
	}
	return out, nil
}

func gaussIncomingParentColumn(catalog *OpenGaussCatalog, name string) (map[string]any, error) {
	for _, entry := range catalog.Entries {
		if entry.Kind != "column" || entry.OID != catalog.RelationOID {
			continue
		}
		row, err := gaussJSON(entry.Data)
		if err != nil {
			return nil, err
		}
		if row["attname"] == name && row["attisdropped"] == false {
			return row, nil
		}
	}
	return nil, ErrConflict
}

func gaussIncomingConstraintRebound(constraint OpenGaussIncomingConstraint, original Snapshot, desired Table, prototype, actual *OpenGaussCatalog) (bool, error) {
	names := map[string]string{}
	for _, column := range desired.Columns() {
		names[column.ID] = column.Name
	}
	rebound := false
	for i, id := range constraint.ParentIDs {
		if names[id] == "" {
			return false, ErrConflict
		}
		before, e1 := gaussIncomingParentColumn(original.OpenGauss, constraint.ParentColumns[i])
		probe, e2 := gaussIncomingParentColumn(prototype, names[id])
		after, e3 := gaussIncomingParentColumn(actual, names[id])
		if e1 != nil || e2 != nil || e3 != nil {
			return false, errors.Join(e1, e2, e3)
		}
		for _, field := range []string{"atttypid", "atttypmod", "attcollation", "attndims"} {
			if before[field] == nil || probe[field] == nil || after[field] == nil || Signature(probe[field]) != Signature(after[field]) {
				return false, ErrConflict
			}
			rebound = rebound || Signature(before[field]) != Signature(after[field])
		}
	}
	return rebound, nil
}

// Keep unknown properties on continuing dependency edges. New/removed typed
// edges come from the independent declaration; matching uses only the native
// pg_depend/pg_shdepend key, never an unknown numeric property or after-image.
func gaussIncomingExpectedDependencies(before, prototype map[string]any, shared bool) (map[string]any, error) {
	if before == nil || prototype == nil {
		return nil, ErrConflict
	}
	fields := []string{"classid", "objid", "objsubid", "refclassid", "refobjid", "refobjsubid", "deptype"}
	if shared {
		fields = []string{"dbid", "classid", "objid", "objsubid", "refclassid", "refobjid", "deptype"}
	}
	identity := func(row map[string]any) string {
		key := map[string]any{}
		for _, field := range fields {
			key[field] = row[field]
		}
		return Signature(key)
	}
	prior := map[string]map[string]any{}
	values, _ := before["rows"].([]any)
	for _, value := range values {
		row, ok := value.(map[string]any)
		if !ok || prior[identity(row)] != nil {
			return nil, ErrConflict
		}
		prior[identity(row)] = row
	}
	var rows []any
	values, _ = prototype["rows"].([]any)
	for _, value := range values {
		row, ok := value.(map[string]any)
		if !ok {
			return nil, ErrConflict
		}
		if old := prior[identity(row)]; old != nil {
			rows = append(rows, old)
		} else {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return Signature(rows[i]) < Signature(rows[j]) })
	out := maps.Clone(before)
	out["rows"] = rows
	return out, nil
}

func VerifyOpenGaussIncomingTransition(probe OpenGaussIncomingTransitionProbe, original Snapshot, desired Table, parent, child *OpenGaussCatalog) error {
	return gaussVerifyIncomingTransition(probe, original, desired, parent, child, true)
}

func gaussVerifyIncomingTransition(probe OpenGaussIncomingTransitionProbe, original Snapshot, desired Table, parent, child *OpenGaussCatalog, verifyContext bool) error {
	baseline := probe.baseline
	if baseline.child == nil || probe.parent == nil || probe.child == nil || parent == nil || child == nil || original.OpenGauss == nil || baseline.original != Signature(original) || baseline.desired != Signature(desired) {
		return ErrConflict
	}
	for _, check := range []struct {
		catalog  *OpenGaussCatalog
		revision string
	}{{baseline.child, baseline.revision}, {probe.parent, probe.parentRevision}, {probe.child, probe.childRevision}} {
		revision, err := check.catalog.Fingerprint()
		if err != nil || revision != check.revision {
			return errors.Join(err, ErrConflict)
		}
	}
	if child.RelationOID != baseline.child.RelationOID || child.DatabaseOID != baseline.child.DatabaseOID || child.NamespaceOID != baseline.child.NamespaceOID || child.RoleOID != baseline.child.RoleOID || child.Name != baseline.child.Name || child.Namespace != baseline.child.Namespace || child.Hooks || parent.Hooks || parent.RelationOID != original.OpenGauss.RelationOID || parent.Name != desired.Name || parent.NamespaceOID != original.OpenGauss.NamespaceOID || parent.DatabaseOID != original.OpenGauss.DatabaseOID || parent.RoleOID != original.OpenGauss.RoleOID {
		return ErrConflict
	}
	actualNames, err := BindOpenGaussIncomingGraph(OpenGaussNamesForTable(desired), baseline, original, desired, parent, child, nil)
	if err != nil {
		return err
	}
	beforeNames, err := BindOpenGaussIncomingGraph(OpenGaussNamesForTable(original.Table), baseline, original, desired, original.OpenGauss, baseline.child, nil)
	if err != nil {
		return err
	}
	before, err := gaussCaptureIncomingEffects(baseline, baseline.child, original.OpenGauss, beforeNames, nil)
	if err != nil {
		return err
	}
	prototype, err := gaussCaptureIncomingEffects(baseline, probe.child, probe.parent, probe.parentNames, &probe.childNames)
	if err != nil {
		return err
	}
	actual, err := gaussCaptureIncomingEffects(baseline, child, parent, actualNames, nil)
	if err != nil {
		return err
	}
	expected := maps.Clone(before.rows)
	anyRebound := false
	for _, constraint := range baseline.constraints {
		id := "incoming:" + strconv.FormatUint(constraint.OID, 10)
		rebound, err := gaussIncomingConstraintRebound(constraint, original, desired, probe.parent, parent)
		if err != nil {
			return err
		}
		anyRebound = anyRebound || rebound
		for oid, name := range before.constraints {
			if name == id && !rebound && actual.constraints[oid] != id {
				return errors.New("native retained incoming constraint identity changed")
			}
		}
		row := maps.Clone(before.rows["constraint:"+id])
		if rebound {
			for _, field := range []string{"conpfeqop", "conppeqop", "conffeqop"} {
				row[field] = prototype.rows["constraint:"+id][field]
			}
		}
		expected["constraint:"+id] = row
		for _, suffix := range []string{"check-ins", "check-upd"} {
			triggerID := id + ":" + suffix
			for oid, name := range before.triggers {
				if name == triggerID && !rebound && actual.triggers[oid] != triggerID {
					return errors.New("native retained incoming check trigger identity changed")
				}
			}
			row := maps.Clone(before.rows["trigger:"+triggerID])
			if rebound {
				row["tgowner"] = prototype.rows["trigger:"+triggerID]["tgowner"]
			}
			expected["trigger:"+triggerID] = row
		}
	}
	if anyRebound {
		for _, kind := range []string{"dependencies", "shared_dependencies"} {
			expected[kind], err = gaussIncomingExpectedDependencies(before.rows[kind], prototype.rows[kind], kind == "shared_dependencies")
			if err != nil {
				return err
			}
		}
	}
	for key, row := range expected {
		if Signature(row) != Signature(actual.rows[key]) {
			var fields []string
			for field, value := range row {
				if Signature(value) != Signature(actual.rows[key][field]) {
					fields = append(fields, field)
				}
			}
			return fmt.Errorf("native incoming effect differs from independent expected record: %s (%v)", key, fields)
		}
	}
	for key := range actual.rows {
		if expected[key] == nil {
			return ErrConflict
		}
	}
	if !verifyContext {
		return nil
	}
	strippedBefore, err := gaussStripIncomingEffects(baseline.child, before)
	if err != nil {
		return err
	}
	strippedAfter, err := gaussStripIncomingEffects(child, actual)
	if err != nil {
		return err
	}
	want, e1 := strippedBefore.Fingerprint()
	got, e2 := strippedAfter.Fingerprint()
	if e1 != nil || e2 != nil || want != got {
		return errors.Join(e1, e2, errors.New("native design changed retained incoming child context"), ErrConflict)
	}
	return nil
}

// Cyclic references share one native child catalog. Their reference-side proof
// compares the complete retained context after restoring only these already
// verified incoming effects. This accessor still verifies the selected effects
// against the frozen independent probe; it cannot certify retained context.
func VerifyOpenGaussIncomingEffects(probe OpenGaussIncomingTransitionProbe, original Snapshot, desired Table, parent, child *OpenGaussCatalog) error {
	return gaussVerifyIncomingTransition(probe, original, desired, parent, child, false)
}

func OpenGaussIncomingNeedsChildLock(probe OpenGaussIncomingTransitionProbe, original Snapshot, desired Table, actual *OpenGaussCatalog) (bool, error) {
	if probe.parent == nil || probe.baseline.child == nil || probe.baseline.original != Signature(original) || probe.baseline.desired != Signature(desired) || actual == nil {
		return false, ErrConflict
	}
	needed := false
	for _, constraint := range probe.baseline.constraints {
		rebound, err := gaussIncomingConstraintRebound(constraint, original, desired, probe.parent, actual)
		if err != nil {
			return false, err
		}
		needed = needed || rebound
	}
	return needed, nil
}
