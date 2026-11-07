package schemaddl

import (
	"encoding/json"
	"errors"
)

// The incoming and outgoing halves of a cycle modify the same native table.
// First verify incoming effects from the frozen independent probe, restore
// exactly their original records/edges, then let the ordinary reference proof
// verify outgoing effects and every remaining raw native record. No unrelated
// record, permission or unknown property is filtered from that final proof.
func VerifyOpenGaussReferenceTransitionWithIncoming(baseline OpenGaussReferenceBaseline, original Snapshot, desired Table, reference OpenGaussReferenceProbe, child, parent *OpenGaussCatalog, incoming OpenGaussIncomingTransitionProbe) error {
	if incoming.baseline.child == nil || parent == nil || incoming.baseline.child.RelationOID != parent.RelationOID {
		return ErrConflict
	}
	if err := VerifyOpenGaussIncomingEffects(incoming, original, desired, child, parent); err != nil {
		return err
	}
	context, err := gaussRestoreIncomingReferenceContext(incoming, original, desired, child, parent)
	if err != nil {
		return err
	}
	return VerifyOpenGaussReferenceTransition(baseline, original, desired, reference, child, context)
}

func gaussRestoreIncomingReferenceContext(probe OpenGaussIncomingTransitionProbe, original Snapshot, desired Table, parent, child *OpenGaussCatalog) (*OpenGaussCatalog, error) {
	beforeNames, err := BindOpenGaussIncomingGraph(OpenGaussNamesForTable(original.Table), probe.baseline, original, desired, original.OpenGauss, probe.baseline.child, nil)
	if err != nil {
		return nil, err
	}
	actualNames, err := BindOpenGaussIncomingGraph(OpenGaussNamesForTable(desired), probe.baseline, original, desired, parent, child, nil)
	if err != nil {
		return nil, err
	}
	before, err := gaussCaptureIncomingEffects(probe.baseline, probe.baseline.child, original.OpenGauss, beforeNames, nil)
	if err != nil {
		return nil, err
	}
	actual, err := gaussCaptureIncomingEffects(probe.baseline, child, parent, actualNames, nil)
	if err != nil {
		return nil, err
	}
	out, err := gaussStripIncomingEffects(child, actual)
	if err != nil {
		return nil, err
	}
	selected := func(class, oid any) (bool, error) {
		classID, e1 := gaussNumber(class)
		objectID, e2 := gaussNumber(oid)
		return classID == child.Classes["pg_constraint"] && before.constraints[objectID] != "" || classID == child.Classes["pg_trigger"] && before.triggers[objectID] != "", errors.Join(e1, e2)
	}
	for _, entry := range probe.baseline.child.Entries {
		if entry.Kind == "constraint" && before.constraints[entry.OID] != "" || entry.Kind == "trigger" && before.triggers[entry.OID] != "" {
			entry.Data = append(json.RawMessage{}, entry.Data...)
			out.Entries = append(out.Entries, entry)
			continue
		}
		if entry.Kind != "dependencies" && entry.Kind != "shared_dependencies" && entry.Kind != "comments" && entry.Kind != "security_labels" {
			continue
		}
		row, err := gaussJSON(entry.Data)
		if err != nil {
			return nil, err
		}
		var restore []any
		values, _ := row["rows"].([]any)
		for _, value := range values {
			edge, ok := value.(map[string]any)
			if !ok {
				return nil, ErrConflict
			}
			classField, oidField := "classid", "objid"
			if entry.Kind == "comments" || entry.Kind == "security_labels" {
				classField, oidField = "classoid", "objoid"
			}
			keep, err := selected(edge[classField], edge[oidField])
			if err == nil && !keep && classField == "classid" {
				keep, err = selected(edge["refclassid"], edge["refobjid"])
			}
			if err != nil {
				return nil, err
			}
			if keep {
				restore = append(restore, edge)
			}
		}
		for i, current := range out.Entries {
			if current.Kind != entry.Kind || current.OID != entry.OID || current.SubID != entry.SubID {
				continue
			}
			native, err := gaussJSON(current.Data)
			if err != nil {
				return nil, err
			}
			rows, _ := native["rows"].([]any)
			native["rows"] = append(rows, restore...)
			out.Entries[i].Data, err = json.Marshal(native)
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}
