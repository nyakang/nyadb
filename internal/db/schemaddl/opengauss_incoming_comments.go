package schemaddl

import (
	"encoding/json"
	"errors"
	"maps"
	"sort"
	"strconv"
)

// Native key-type ALTER can rebuild incoming constraints and lose their
// comments. Restore only a missing original comment on a proven rebuilt FK;
// never overwrite a different comment or hide a retained identity change.
// The runtime executes these statements under native permissions, then reads
// the real child again for the complete incoming/composed transition proof.
func OpenGaussIncomingCommentRestoration(probe OpenGaussIncomingTransitionProbe, original Snapshot, desired Table, parent, child *OpenGaussCatalog) ([]string, error) {
	if probe.baseline.child == nil || probe.parent == nil || parent == nil || child == nil {
		return nil, ErrConflict
	}
	names, err := BindOpenGaussIncomingGraph(OpenGaussNamesForTable(desired), probe.baseline, original, desired, parent, child, nil)
	if err != nil {
		return nil, err
	}
	effects, err := gaussCaptureIncomingEffects(probe.baseline, child, parent, names, nil)
	if err != nil {
		return nil, err
	}
	actualOIDs := map[uint64]uint64{}
	constraints := map[uint64]OpenGaussIncomingConstraint{}
	for _, constraint := range probe.baseline.constraints {
		constraints[constraint.OID] = constraint
		id := "incoming:" + strconv.FormatUint(constraint.OID, 10)
		for oid, selected := range effects.constraints {
			if selected == id {
				actualOIDs[constraint.OID] = oid
			}
		}
	}
	out := child.Clone()
	index, rows, err := gaussIncomingRawComments(out)
	if err != nil {
		return nil, err
	}
	_, before, err := gaussIncomingRawComments(probe.baseline.child)
	if err != nil {
		return nil, err
	}
	var statements []string
	for _, raw := range before["rows"].([]any) {
		row, ok := raw.(map[string]any)
		if !ok {
			return nil, ErrConflict
		}
		class, e1 := gaussNumber(row["classoid"])
		oid, e2 := gaussNumber(row["objoid"])
		if e1 != nil || e2 != nil {
			return nil, errors.Join(e1, e2, ErrConflict)
		}
		constraint, selected := constraints[oid]
		if class != child.Classes["pg_constraint"] || !selected {
			continue
		}
		next := maps.Clone(row)
		next["objoid"] = json.Number(strconv.FormatUint(actualOIDs[oid], 10))
		found := false
		for _, current := range rows["rows"].([]any) {
			value, ok := current.(map[string]any)
			if !ok {
				return nil, ErrConflict
			}
			if Signature(value["classoid"]) != Signature(next["classoid"]) || Signature(value["objoid"]) != Signature(next["objoid"]) || Signature(value["objsubid"]) != Signature(next["objsubid"]) {
				continue
			}
			if found || Signature(value) != Signature(next) {
				return nil, errors.New("native incoming constraint comment changed")
			}
			found = true
		}
		if found {
			continue
		}
		rebound, err := gaussIncomingConstraintRebound(constraint, original, desired, probe.parent, parent)
		subID, subErr := gaussNumber(row["objsubid"])
		comment, valid := row["description"].(string)
		if err != nil || subErr != nil || !rebound || actualOIDs[oid] == 0 || actualOIDs[oid] == oid || subID != 0 || !valid || len(row) != 4 {
			return nil, errors.Join(err, subErr, errors.New("native incoming comment cannot be restored without changing original metadata"))
		}
		statement, err := gaussDesignComment("CONSTRAINT "+Quote(constraint.Name)+" ON "+Quote(child.Namespace)+"."+Quote(child.Name), &comment)
		if err != nil {
			return nil, err
		}
		statements = append(statements, statement)
		rows["rows"] = append(rows["rows"].([]any), next)
	}
	out.Entries[index].Data, err = json.Marshal(rows)
	if err != nil {
		return nil, err
	}
	// This virtual catalog grants no retained-context proof. It validates that
	// missing comments are the sole discrepancy in the selected native effects;
	// final verification must capture the actual statement results independently.
	if err = VerifyOpenGaussIncomingEffects(probe, original, desired, parent, out); err != nil {
		return nil, err
	}
	sort.Strings(statements)
	return statements, nil
}

func gaussIncomingRawComments(catalog *OpenGaussCatalog) (int, map[string]any, error) {
	index := -1
	var comments map[string]any
	for i, entry := range catalog.Entries {
		if entry.Kind != "comments" || entry.OID != catalog.RelationOID || entry.SubID != 0 {
			continue
		}
		if index >= 0 {
			return 0, nil, ErrConflict
		}
		var err error
		comments, err = gaussJSON(entry.Data)
		if err != nil {
			return 0, nil, err
		}
		if comments["rows"] == nil {
			comments["rows"] = []any{}
		}
		if _, ok := comments["rows"].([]any); !ok {
			return 0, nil, ErrConflict
		}
		index = i
	}
	if index < 0 {
		return 0, nil, ErrConflict
	}
	return index, comments, nil
}
