package schemaddl

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// VerifyPostgreSQLUnselectedCatalog adds an unchanged-context guard to the
// independently intended declaration and retained-property proofs. Every
// captured record outside the selected target graph must survive under its
// ORIGINAL typed catalog identity. Explicit column removal permits only the
// independently derived native dropped-attribute transition; other captured
// fields stay unchanged. Both
// inputs must contain the original context (including objects which left the
// current dependency closure); a normal post-ALTER snapshot is insufficient.
//
// Without request-bound storage/reference evidence this rejects new context
// and native storage changes. Evidence normalizes only separately proved owned
// storage transitions and before-write references; unrequested attributes and
// external physical changes remain forbidden. This is not a sequence-value,
// concurrent-DDL or commit authorization proof. Rename has its complete proof.
func VerifyPostgreSQLUnselectedCatalog(original Snapshot, desired Table, actual Snapshot, expected PostgreSQLDeclarationGraph, schema string, allowDrop bool, storage ...PostgreSQLStorageTransition) error {
	if err := VerifyPostgreSQLRetainedCatalog(original, desired, actual, expected, schema, allowDrop); err != nil {
		return err
	}
	residuals, err := pgExpectedDroppedAttributes(original, desired)
	if err != nil {
		return err
	}
	context := actual.Postgres
	baseline := original.Postgres
	if len(storage) > 1 {
		return errors.New("ambiguous PostgreSQL storage transition evidence")
	}
	if len(storage) == 1 {
		proof := storage[0]
		if proof.context == nil || proof.baseline == nil || proof.original != Signature(original) || proof.desired != Signature(desired) || proof.actual != Signature(actual) {
			return errors.New("PostgreSQL storage evidence belongs to another request or receipt")
		}
		context = proof.context.Clone()
		baseline = proof.baseline.Clone()
	}
	return pgVerifyUnselectedCatalog(*baseline, pgPreservationNames(original.Table), *context, pgPreservationNames(desired), residuals...)
}

type pgContextKey struct {
	kind string
	oid  int64
	sub  int
}

func pgVerifyUnselectedCatalog(original PostgreSQLCatalog, originalNames PostgreSQLGraphNames, actual PostgreSQLCatalog, actualNames PostgreSQLGraphNames, residuals ...PostgreSQLCatalogEntry) error {
	if original.RelationOID <= 0 || original.RelationOID != actual.RelationOID || !pgGraphSameScope(original, actual) {
		return ErrConflict
	}
	before, err := pgUnselectedRecords(original, originalNames)
	if err != nil {
		return err
	}
	after, err := pgUnselectedRecords(actual, actualNames)
	if err != nil {
		return err
	}
	for _, entry := range residuals {
		key := pgContextKey{"record:column", entry.OID, entry.SubID}
		if entry.Kind != "column" || entry.OID != original.RelationOID || entry.SubID <= 0 || before[key] != nil {
			return errors.New("invalid PostgreSQL requested attribute residual")
		}
		before[key] = entry.Data
	}
	// Sort identities so the first failure is deterministic even if the
	// server returns records/edges in a different order. Never report values,
	// SQL, labels or user-controlled catalog field names in an error.
	keys := make([]pgContextKey, 0, len(before)+len(after))
	for key := range before {
		keys = append(keys, key)
	}
	for key := range after {
		if _, exists := before[key]; !exists {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		if a.oid != b.oid {
			return a.oid < b.oid
		}
		return a.sub < b.sub
	})
	for _, key := range keys {
		a, had := before[key]
		b, has := after[key]
		if had && has && string(a) == string(b) {
			continue
		}
		reason := "changed"
		if !had {
			reason = "added"
		} else if !has {
			reason = "missing or newly selected"
		}
		kind := "record"
		switch strings.TrimPrefix(key.kind, "record:") {
		case "relation", "column", "type", "default", "constraint", "index", "trigger", "sequence", "routine", "access_operator", "access_procedure", "operator_class", "operator_family", "cast", "collation", "namespace", "database", "rewrite", "policy", "publication", "publication_relation":
			kind = strings.TrimPrefix(key.kind, "record:")
		}
		return fmt.Errorf("%w: PostgreSQL unselected catalog %s %s (OID %d, subobject %d)", ErrConflict, kind, reason, key.oid, key.sub)
	}
	return nil
}

func pgUnselectedRecords(p PostgreSQLCatalog, names PostgreSQLGraphNames) (map[pgContextKey]json.RawMessage, error) {
	bindings, err := BuildPostgreSQLGraphBindings(p, names)
	if err != nil {
		return nil, err
	}
	out := map[pgContextKey]json.RawMessage{}
	for _, entry := range p.Entries {
		if entry.OID > 1<<32-1 || entry.SubID > 1<<31-1 {
			return nil, errors.New("invalid PostgreSQL context record identity")
		}
		fields, err := pgPreservationFields(entry.Data)
		if err != nil {
			return nil, err
		}
		if entry.Kind == "dependency" {
			if err := pgUnselectedDependencies(entry, fields, bindings, out); err != nil {
				return nil, err
			}
			continue
		}
		class := bindings.Classes[pgGraphRecordClass(entry.Kind)]
		if strings.HasPrefix(entry.Kind, "security_label:") {
			n, err := strconv.ParseUint(strings.TrimPrefix(entry.Kind, "security_label:"), 10, 32)
			if err != nil || n == 0 {
				return nil, errors.New("invalid PostgreSQL context security label class")
			}
			class = int64(n)
			if entry.SubID > 0 && (class != bindings.Classes["pg_class"] || bindings.Attributes[entry.OID][entry.SubID] == "") {
				class = 0 // Unselected/dropped subobjects must also survive.
			}
		}
		if entry.Kind == "column" {
			r := pgGraphRecord{entry: entry, data: fields}
			owner, oe := r.integer("attrelid")
			number, ne := r.integer("attnum")
			if oe != nil || ne != nil || owner != entry.OID || number <= 0 || number != int64(entry.SubID) {
				return nil, errors.New("invalid PostgreSQL context attribute identity")
			}
			dropped, err := r.boolean("attisdropped")
			if err != nil {
				return nil, err
			}
			if dropped {
				class = 0 // Projection skips these; never silently authorize loss.
			}
		}
		if class != 0 && bindings.Objects[PostgreSQLCatalogObject{class, entry.OID}] != "" {
			continue
		}
		raw, err := pgGraphCanonicalJSON(fields)
		if err != nil {
			return nil, err
		}
		out[pgContextKey{"record:" + entry.Kind, entry.OID, entry.SubID}] = raw
	}
	return out, nil
}

// Containers group pg_depend rows by numeric objid, which is NOT unique across
// catalog classes. Split by typed SOURCE, retaining incoming references to
// selected targets, duplicate edges, unknown edge and wrapper properties.
func pgUnselectedDependencies(entry PostgreSQLCatalogEntry, fields map[string]json.RawMessage, bindings PostgreSQLGraphBindings, out map[pgContextKey]json.RawMessage) error {
	var edges []map[string]json.RawMessage
	if entry.SubID != 0 || json.Unmarshal(fields["edges"], &edges) != nil || len(edges) == 0 {
		return errors.New("invalid PostgreSQL context dependency container")
	}
	delete(fields, "edges")
	if len(fields) != 0 {
		raw, err := pgGraphCanonicalJSON(fields)
		if err != nil {
			return err
		}
		// Preserve every unknown wrapper, also when its selected source is
		// rebuilt/deleted. There is no authorization for that loss yet.
		out[pgContextKey{"dependency_wrapper", entry.OID, 0}] = raw
	}
	byClass := map[int64][]json.RawMessage{}
	for _, edge := range edges {
		r := pgGraphRecord{entry: entry, data: edge}
		identity := map[string]int64{}
		for _, field := range []string{"classid", "objid", "refclassid", "refobjid", "objsubid", "refobjsubid"} {
			n, err := r.integer(field)
			subobject := field == "objsubid" || field == "refobjsubid"
			if err != nil || n < 0 || n > 1<<32-1 || !subobject && n == 0 || subobject && n > 1<<31-1 {
				return errors.New("invalid PostgreSQL context dependency identity")
			}
			identity[field] = n
		}
		deptype, err := r.text("deptype")
		if err != nil || len(deptype) != 1 || identity["objid"] != entry.OID {
			return errors.New("invalid PostgreSQL context dependency source")
		}
		class := identity["classid"]
		if bindings.Objects[PostgreSQLCatalogObject{class, entry.OID}] != "" {
			continue
		}
		raw, err := pgGraphCanonicalJSON(edge)
		if err != nil {
			return err
		}
		byClass[class] = append(byClass[class], raw)
	}
	for class, edges := range byClass {
		sort.Slice(edges, func(i, j int) bool { return string(edges[i]) < string(edges[j]) })
		raw, err := pgGraphCanonicalJSON(edges)
		if err != nil {
			return err
		}
		out[pgContextKey{"dependency:" + strconv.FormatInt(class, 10), entry.OID, 0}] = raw
	}
	return nil
}
