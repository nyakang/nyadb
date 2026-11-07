package schemaddl

import (
	"errors"
	"maps"
	"strconv"
)

// Incoming graph joins are bound from both full native catalogs and captured
// constraint identities. Numeric values and private name patterns never infer
// which original constraint, relation or physical key slot they represent.
func BindOpenGaussIncomingGraph(names OpenGaussDesignNames, baseline OpenGaussIncomingBaseline, original Snapshot, desired Table, parent, child *OpenGaussCatalog, privateNames *OpenGaussDesignNames) (OpenGaussDesignNames, error) {
	if baseline.child == nil || parent == nil || child == nil || original.OpenGauss == nil || baseline.original != Signature(original) || baseline.desired != Signature(desired) {
		return names, ErrConflict
	}
	before := baseline.child
	prior := original.OpenGauss
	for _, catalog := range []*OpenGaussCatalog{before, parent, child} {
		if _, err := catalog.Fingerprint(); err != nil {
			return names, err
		}
	}
	revision, _ := before.Fingerprint()
	if revision != baseline.revision || parent.Database != prior.Database || parent.DatabaseOID != prior.DatabaseOID || parent.Namespace != prior.Namespace || parent.NamespaceOID != prior.NamespaceOID || parent.RoleOID != prior.RoleOID || parent.NativeByteOrder != prior.NativeByteOrder || Signature(parent.Classes) != Signature(prior.Classes) || child.DatabaseOID != parent.DatabaseOID || child.RoleOID != parent.RoleOID || child.Hooks || parent.Hooks || child.NativeByteOrder != parent.NativeByteOrder || Signature(child.Classes) != Signature(parent.Classes) || child.RelationOID == parent.RelationOID {
		return names, ErrConflict
	}
	if privateNames == nil && (child.RelationOID != before.RelationOID || child.Name != before.Name || child.Namespace != before.Namespace || child.NamespaceOID != before.NamespaceOID || parent.RelationOID != original.OpenGauss.RelationOID) {
		return names, ErrConflict
	}
	out := names
	out.Columns = maps.Clone(names.Columns)
	out.Constraints = maps.Clone(names.Constraints)
	out.Definitions = maps.Clone(names.Definitions)
	out.ReferenceRelations = maps.Clone(names.ReferenceRelations)
	out.IncomingConstraints = maps.Clone(names.IncomingConstraints)
	out.ReferenceColumns = maps.Clone(names.ReferenceColumns)
	for oid, columns := range out.ReferenceColumns {
		out.ReferenceColumns[oid] = maps.Clone(columns)
	}
	out.ReferenceSequences = append([]OpenGaussCopySequence{}, names.ReferenceSequences...)
	for i := range out.ReferenceSequences {
		out.ReferenceSequences[i].Comment = gaussCloneComment(out.ReferenceSequences[i].Comment)
	}
	if out.ReferenceRelations == nil {
		out.ReferenceRelations = map[uint64]string{}
	}
	if out.IncomingConstraints == nil {
		out.IncomingConstraints = map[uint64]string{}
	}
	if out.ReferenceColumns == nil {
		out.ReferenceColumns = map[uint64]map[int]string{}
	}
	alias := "@reference:" + Quote(before.Namespace) + "." + Quote(before.Name)
	for _, oid := range []uint64{before.RelationOID, child.RelationOID} {
		if previous := out.ReferenceRelations[oid]; previous != "" && previous != alias {
			return names, ErrConflict
		}
		out.ReferenceRelations[oid] = alias
	}
	columnIDs := map[string]string{}
	for _, entry := range before.Entries {
		if entry.Kind != "column" || entry.OID != before.RelationOID {
			continue
		}
		row, err := gaussJSON(entry.Data)
		if err != nil {
			return names, err
		}
		if row["attisdropped"] == false {
			name, ok := row["attname"].(string)
			if !ok {
				return names, ErrConflict
			}
			columnIDs[name] = "incoming-column:" + strconv.FormatUint(before.RelationOID, 10) + ":" + name
		}
	}
	for _, catalog := range []*OpenGaussCatalog{before, child} {
		columns := map[int]string{}
		for _, entry := range catalog.Entries {
			if entry.Kind != "column" || entry.OID != catalog.RelationOID {
				continue
			}
			row, err := gaussJSON(entry.Data)
			if err != nil {
				return names, err
			}
			if row["attisdropped"] == false {
				name, _ := row["attname"].(string)
				id := columnIDs[name]
				if id == "" || privateNames != nil && catalog == child && privateNames.Columns[name] != id {
					return names, ErrConflict
				}
				columns[entry.SubID] = id
			}
		}
		out.ReferenceColumns[catalog.RelationOID] = columns
	}
	matched := map[uint64]bool{}
	matchedIDs := map[string]bool{}
	for _, entry := range child.Entries {
		if entry.Kind != "constraint" {
			continue
		}
		row, err := gaussJSON(entry.Data)
		if err != nil {
			return names, err
		}
		if row["contype"] != "f" {
			continue
		}
		parentOID, err := gaussNumber(row["confrelid"])
		if err != nil {
			return names, err
		}
		if parentOID != parent.RelationOID {
			continue
		}
		name, _ := row["conname"].(string)
		id := ""
		for _, constraint := range baseline.constraints {
			wanted := "incoming:" + strconv.FormatUint(constraint.OID, 10)
			if privateNames == nil && name == constraint.Name || privateNames != nil && privateNames.Constraints[name] == wanted {
				if id != "" {
					return names, ErrConflict
				}
				id = wanted
			}
		}
		if id == "" || matchedIDs[id] {
			return names, errors.New("native incoming graph has an unbound foreign key")
		}
		found := false
		for _, parentEntry := range parent.Entries {
			if parentEntry.Kind == "incoming_constraint" && parentEntry.OID == entry.OID {
				parentRow, err := gaussJSON(parentEntry.Data)
				if err != nil || Signature(parentRow) != Signature(row) || matched[entry.OID] {
					return names, ErrConflict
				}
				found = true
			}
		}
		if !found {
			return names, ErrConflict
		}
		matched[entry.OID] = true
		matchedIDs[id] = true
		if old := out.IncomingConstraints[entry.OID]; old != "" && old != id {
			return names, ErrConflict
		}
		out.IncomingConstraints[entry.OID] = id
	}
	if len(matched) != len(baseline.constraints) {
		return names, ErrConflict
	}
	return out, nil
}
