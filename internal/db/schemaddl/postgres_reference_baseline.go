package schemaddl

import (
	"encoding/json"
	"errors"
)

// PostgreSQLReferenceBaseline retains BEFORE-write records which became
// relevant through the independent desired declaration. These remain
// immutable context and never become newly editable assets.
type PostgreSQLReferenceBaseline struct {
	original, desired, probe string
	catalog                  *PostgreSQLCatalog
}

// CapturePostgreSQLReferenceBaseline requires a read after probe rollback and
// before DDL. Original records must remain exact. Additional context must
// already occur in the probe's closure and must not belong to its target.
func CapturePostgreSQLReferenceBaseline(original Snapshot, desired Table, probe, beforeWrite Snapshot) (out PostgreSQLReferenceBaseline, err error) {
	if original.Postgres == nil || probe.Postgres == nil || beforeWrite.Postgres == nil || !original.Exists || !beforeWrite.Exists || !probe.Exists || original.Engine != "postgresql" || probe.Engine != original.Engine || beforeWrite.Engine != original.Engine || !pgGraphSameScope(*original.Postgres, *probe.Postgres) || !pgGraphSameScope(*original.Postgres, *beforeWrite.Postgres) || probe.Postgres.RelationOID == original.Postgres.RelationOID || beforeWrite.Postgres.RelationOID != original.Postgres.RelationOID || Signature(beforeWrite.Table) != Signature(original.Table) {
		return out, ErrConflict
	}
	// This validates the request but does not canonicalize its declaration.
	// The caller must also verify that the independently created probe is the
	// desired declaration; a reference baseline never grants Apply permission.
	if _, err = PlanPostgreSQL(original, desired, original.Postgres.Namespace, true); err != nil {
		return out, err
	}
	for _, snapshot := range []Snapshot{original, probe, beforeWrite} {
		revision, e := snapshot.Postgres.Fingerprint()
		table, e2 := snapshot.Postgres.Table()
		if e != nil || e2 != nil || revision != snapshot.Revision || Signature(table) != Signature(snapshot.Table) {
			return out, errors.Join(ErrConflict, e, e2)
		}
	}
	bindings, err := BuildPostgreSQLGraphBindings(*probe.Postgres, pgPreservationNames(probe.Table))
	if err != nil {
		return out, err
	}
	storage, err := pgReadStorage(probe.Postgres)
	if err != nil {
		return out, err
	}
	old, expected := map[pgContextKey]PostgreSQLCatalogEntry{}, map[pgContextKey]PostgreSQLCatalogEntry{}
	for _, entry := range original.Postgres.Entries {
		old[pgContextKey{entry.Kind, entry.OID, entry.SubID}] = entry
	}
	for _, entry := range probe.Postgres.Entries {
		expected[pgContextKey{entry.Kind, entry.OID, entry.SubID}] = entry
	}
	seen := map[pgContextKey]bool{}
	for _, entry := range beforeWrite.Postgres.Entries {
		key := pgContextKey{entry.Kind, entry.OID, entry.SubID}
		if prior, exists := old[key]; exists {
			if !pgStorageEqual(prior.Data, entry.Data) {
				return out, errors.New("PostgreSQL original context changed during definition validation")
			}
			seen[key] = true
			continue
		}
		prior, exists := expected[key]
		if !exists {
			return out, errors.New("PostgreSQL new reference has no independent before-write evidence")
		}
		class := bindings.Classes[pgGraphRecordClass(entry.Kind)]
		if class > 0 && bindings.Objects[PostgreSQLCatalogObject{class, entry.OID}] != "" || class == storage.class && storage.owned[entry.OID] || entry.Kind == "column" && (entry.OID == probe.Postgres.RelationOID || storage.owned[entry.OID]) {
			return out, errors.New("PostgreSQL ephemeral probe object cannot become original context")
		}
		switch entry.Kind {
		case "cast", "type", "routine", "operator", "access_method", "operator_class", "operator_family", "access_operator", "access_procedure", "collation", "range", "enum", "extension", "relation", "column", "index", "constraint", "trigger", "sequence":
			if !pgStorageEqual(prior.Data, entry.Data) {
				return out, errors.New("PostgreSQL new reference changed between probe and before-write capture")
			}
		case "dependency":
			if err := pgReferenceEdges(prior, entry, bindings, storage); err != nil {
				return out, err
			}
		default:
			return out, errors.New("unsupported PostgreSQL reference context expansion")
		}
	}
	if len(seen) != len(old) {
		return out, errors.New("PostgreSQL original context was lost during definition validation")
	}
	out = PostgreSQLReferenceBaseline{original: Signature(original), desired: Signature(desired), probe: Signature(probe), catalog: beforeWrite.Postgres.Clone()}
	return out, nil
}

func pgReferenceEdges(probe, before PostgreSQLCatalogEntry, bindings PostgreSQLGraphBindings, storage pgStorageCatalog) error {
	read := func(entry PostgreSQLCatalogEntry, filter bool) (map[string]int, error) {
		var data map[string]json.RawMessage
		if json.Unmarshal(entry.Data, &data) != nil {
			return nil, ErrConflict
		}
		var edges []map[string]json.RawMessage
		if entry.SubID != 0 || json.Unmarshal(data["edges"], &edges) != nil || len(edges) == 0 {
			return nil, ErrConflict
		}
		out := map[string]int{}
		delete(data, "edges")
		// Container properties have numeric (not class-qualified) identity.
		// Keep them exact rather than dropping them when filtering probe edges.
		out["wrapper:"+Signature(data)]++
		for _, edge := range edges {
			r := pgGraphRecord{entry: entry, data: edge}
			class, e := r.integer("classid")
			object, e2 := r.integer("objid")
			refClass, e3 := r.integer("refclassid")
			ref, e4 := r.integer("refobjid")
			if e != nil || e2 != nil || e3 != nil || e4 != nil || object != entry.OID {
				return nil, ErrConflict
			}
			ephemeral := bindings.Objects[PostgreSQLCatalogObject{class, object}] != "" || bindings.Objects[PostgreSQLCatalogObject{refClass, ref}] != "" || class == storage.class && storage.owned[object] || refClass == storage.class && storage.owned[ref]
			if ephemeral {
				if filter {
					continue
				}
				return nil, errors.New("PostgreSQL before-write reference contains a probe dependency")
			}
			out[Signature(edge)]++
		}
		return out, nil
	}
	a, e := read(probe, true)
	b, e2 := read(before, false)
	if e != nil || e2 != nil {
		return errors.Join(e, e2)
	}
	if Signature(a) != Signature(b) {
		return errors.New("PostgreSQL new reference dependencies differ from independent before-write evidence")
	}
	return nil
}
