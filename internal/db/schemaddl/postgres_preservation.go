package schemaddl

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// VerifyPostgreSQLRetainedCatalog verifies the original target's retained
// properties after the independently intended declaration graph has matched.
// It preserves unknown properties, original ACLs, comments and security labels
// across native rebuilds, and requires retained table/type/attribute/sequence
// identities to survive. Physical rewrite state, unselected incoming/context
// records, sequence current values and concurrent changes need separate proofs;
// success here does not authorize a production commit.
func VerifyPostgreSQLRetainedCatalog(original Snapshot, desired Table, actual Snapshot, expected PostgreSQLDeclarationGraph, schema string, allowDrop bool) error {
	plan, err := PlanPostgreSQL(original, desired, schema, allowDrop)
	if err != nil {
		return err
	}
	if !original.Exists || plan.Mode != "alter" && plan.Mode != "noop" {
		return ErrUnsupported
	}
	if !actual.Exists || actual.Engine != original.Engine || actual.Postgres == nil || actual.Table.Name != desired.Name || actual.Postgres.RelationOID != original.Postgres.RelationOID || !pgGraphSameScope(*original.Postgres, *actual.Postgres) {
		return errors.New("PostgreSQL retained catalog changed the original table scope")
	}
	revision, err := actual.Postgres.Fingerprint()
	if err != nil || revision != actual.Revision {
		return errors.Join(ErrConflict, err)
	}
	canonical, err := actual.Postgres.Table()
	if err != nil || Signature(canonical) != Signature(actual.Table) {
		return errors.Join(ErrConflict, err)
	}
	beforeNames, afterNames := pgPreservationNames(original.Table), pgPreservationNames(desired)
	before, err := BuildPostgreSQLGraphBindings(*original.Postgres, beforeNames)
	if err != nil {
		return err
	}
	after, err := BuildPostgreSQLGraphBindings(*actual.Postgres, afterNames)
	if err != nil {
		return err
	}
	if err := pgPreservationIdentities(before, after, original.Postgres.RelationOID); err != nil {
		return err
	}
	oldGraph, err := ProjectPostgreSQLDeclarationGraph(*original.Postgres, beforeNames)
	if err != nil {
		return err
	}
	newGraph, err := ProjectPostgreSQLDeclarationGraph(*actual.Postgres, afterNames)
	if err != nil {
		return err
	}
	if err := ComparePostgreSQLDeclarationGraphs(newGraph, expected); err != nil {
		return err
	}
	byKey := map[string]PostgreSQLGraphRecord{}
	for _, record := range newGraph.Records {
		byKey[pgPreservationRecordKey(record)] = record
	}
	for _, record := range oldGraph.Records {
		next, retained := byKey[pgPreservationRecordKey(record)]
		if !retained {
			// The independent graph verifies deleted assets; retained physical
			// column positions and root/sequence OIDs are checked separately.
			continue
		}
		// A known declarative catalog field may change only after matching the
		// independent intended graph. Unknown and noneditable fields must also
		// match the ORIGINAL, not a fresh CREATE's default for that property.
		if record.Kind == "dependency" {
			if err := pgPreservationUnknownEdges(record, next); err != nil {
				return err
			}
			continue
		}
		if err := pgPreservationOpaque(record, next, original.Table, desired); err != nil {
			return err
		}
		a, err := pgPreservationFields(record.Declaration)
		if err != nil {
			return err
		}
		b, err := pgPreservationFields(next.Declaration)
		if err != nil {
			return err
		}
		if record.Kind == "column" {
			if err := pgPreservationSequenceProperties(a["identity_sequence"], b["identity_sequence"]); err != nil {
				return err
			}
		}
		for _, field := range pgPreservationDeclaredFields(record.Kind) {
			delete(a, field)
			delete(b, field)
		}
		if err := pgPreservationCompare(record.Kind, a, b); err != nil {
			return err
		}
	}
	if err := pgPreservationDependencyContainers(*original.Postgres, *actual.Postgres, before, after); err != nil {
		return err
	}
	return pgPreservationLabels(*original.Postgres, *actual.Postgres, before, after)
}

func pgPreservationNames(t Table) PostgreSQLGraphNames {
	names := PostgreSQLGraphNames{Columns: map[string]string{}, Constraints: map[string]string{}, Indexes: map[string]string{}, Triggers: map[string]string{}}
	for _, clause := range t.Clauses {
		if clause.Kind == "column" {
			names.Columns[clause.Name] = clause.ID
		} else if clause.Kind == "constraint" {
			names.Constraints[clause.Name] = clause.ID
		}
	}
	for _, definition := range t.Definitions {
		if definition.Kind == "index" {
			names.Indexes[definition.Name] = definition.Name
		} else if definition.Kind == "trigger" {
			names.Triggers[definition.Name] = definition.Name
		}
	}
	return names
}

func pgPreservationRecordKey(r PostgreSQLGraphRecord) string {
	return r.Kind + ":" + Signature([]string{r.Object, r.Attribute})
}

func pgPreservationIdentities(before, after PostgreSQLGraphBindings, root int64) error {
	objects := map[string]PostgreSQLCatalogObject{}
	for object, identity := range after.Objects {
		objects[identity] = object
	}
	for object, identity := range before.Objects {
		if identity != "@table" && identity != "@row-type" && identity != "@row-array" && !strings.HasPrefix(identity, "@sequence:") {
			continue
		}
		next, retained := objects[identity]
		if (!retained && !strings.HasPrefix(identity, "@sequence:")) || retained && next != object {
			return errors.New("PostgreSQL replaced a retained table/type/sequence identity")
		}
	}
	positions := map[string]int{}
	for number, identity := range after.Attributes[root] {
		positions[identity] = number
	}
	for number, identity := range before.Attributes[root] {
		if next, retained := positions[identity]; retained && next != number {
			return errors.New("PostgreSQL replaced a retained attribute identity")
		}
	}
	return nil
}

func pgPreservationFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if !json.Valid(raw) || json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, errors.New("invalid PostgreSQL original property evidence")
	}
	return fields, nil
}

func pgPreservationCompare(kind string, before, after map[string]json.RawMessage) error {
	a, err := pgGraphCanonicalJSON(before)
	if err != nil {
		return err
	}
	b, err := pgGraphCanonicalJSON(after)
	if err != nil {
		return err
	}
	if string(a) == string(b) {
		return nil
	}
	var fields []string
	for field, value := range before {
		x, _ := pgGraphCanonicalJSON(value)
		y, _ := pgGraphCanonicalJSON(after[field])
		if _, exists := after[field]; !exists || string(x) != string(y) {
			fields = append(fields, field)
		}
	}
	for field := range after {
		if _, exists := before[field]; !exists {
			fields = append(fields, field)
		}
	}
	sort.Strings(fields)
	return fmt.Errorf("PostgreSQL retained %s properties differ in fields %s", kind, strings.Join(fields, ", "))
}

func pgPreservationOpaque(before, after PostgreSQLGraphRecord, original, desired Table) error {
	a, err := pgPreservationFields(before.Opaque)
	if err != nil {
		return err
	}
	b, err := pgPreservationFields(after.Opaque)
	if err != nil {
		return err
	}
	patch := func(field string, old, next any) error {
		if Signature(old) == Signature(next) {
			return nil
		}
		if _, exists := a[field]; !exists {
			return errors.New("PostgreSQL requested property has no original catalog evidence")
		}
		a[field], err = json.Marshal(next)
		return err
	}
	if before.Kind == "relation" && before.Object == "@table" {
		x, y := original.Postgres, desired.Postgres
		if err := errors.Join(patch("comment", x.Comment, y.Comment), patch("relreplident", x.ReplicaIdentity, y.ReplicaIdentity), patch("relrowsecurity", x.RowSecurity, y.RowSecurity), patch("relforcerowsecurity", x.ForceRowSecurity, y.ForceRowSecurity)); err != nil {
			return err
		}
	}
	if before.Kind == "index" && original.Postgres.ReplicaIdentity == "i" && desired.Postgres.ReplicaIdentity != "i" && string(a["indisreplident"]) == "true" {
		// The explicit table request removes the old index designation.
		// Retaining i still requires that original designated index to survive.
		a["indisreplident"] = json.RawMessage("false")
	}
	if before.Kind == "column" && before.Object == "@table" {
		for _, x := range original.Columns() {
			if "@column:"+x.ID != before.Attribute {
				continue
			}
			for _, y := range desired.Columns() {
				if y.ID == x.ID {
					if err := patch("comment", x.Postgres.Comment, y.Postgres.Comment); err != nil {
						return err
					}
				}
			}
		}
	}
	if before.Kind == "index" || before.Kind == "relation" || before.Kind == "trigger" {
		for _, x := range original.Definitions {
			if "@"+x.Kind+":"+x.Name != before.Object || x.Kind != "index" && x.Kind != "trigger" {
				continue
			}
			for _, y := range desired.Definitions {
				if y.Name == x.Name && y.Kind == x.Kind {
					oldComment, nextComment := "", ""
					if x.Postgres != nil {
						oldComment = x.Postgres.Comment
					}
					if y.Postgres != nil {
						nextComment = y.Postgres.Comment
					}
					if err := patch("comment", oldComment, nextComment); err != nil {
						return err
					}
				}
			}
		}
	}
	return pgPreservationCompare(before.Kind, a, b)
}

// These are understood fields whose declarative semantics are checked against
// the independent target. This is deliberately not an allowlist of arbitrary
// future catalog properties. ACLs, locality, identity links and unknown keys
// remain in the ORIGINAL comparison, including on rebuilt objects.
func pgPreservationDeclaredFields(kind string) []string {
	return strings.Fields(map[string]string{
		"relation":   "relpersistence relam access_method reltablespace tablespace_name reloptions relchecks __nyadb_ordered_attributes",
		"column":     "atttypid atttypmod attndims attcollation attlen attbyval attalign attstorage attcompression attstattarget attnotnull atthasdef attidentity attgenerated identity_sequence",
		"default":    "adbin",
		"constraint": "contype condeferrable condeferred convalidated conindid confrelid confupdtype confdeltype confmatchtype conkey confkey confdelsetcols conpfeqop conppeqop conffeqop conexclop conbin connoinherit",
		"index":      "indnatts indnkeyatts indisunique indisprimary indisexclusion indnullsnotdistinct indkey indcollation indclass indoption indexprs indpred",
		"trigger":    "tgfoid tgtype tgenabled enabled tgconstrrelid tgconstrindid tgconstraint tgdeferrable tginitdeferred tgnargs tgattr tgargs tgqual tgoldtable tgnewtable",
		"sequence":   "seqtypid",
	}[kind])
}

func pgPreservationUnknownEdges(before, after PostgreSQLGraphRecord) error {
	unknown := func(record PostgreSQLGraphRecord) (map[string]json.RawMessage, error) {
		fields, err := pgPreservationFields(record.Declaration)
		if err != nil {
			return nil, err
		}
		var edges []map[string]json.RawMessage
		if json.Unmarshal(fields["edges"], &edges) != nil || edges == nil {
			return nil, errors.New("invalid PostgreSQL retained dependency evidence")
		}
		delete(fields, "edges")
		var residual []json.RawMessage
		for _, edge := range edges {
			remaining := make(map[string]json.RawMessage, len(edge))
			for field, value := range edge {
				remaining[field] = value
			}
			for _, field := range []string{"classid", "objid", "objsubid", "refclassid", "refobjid", "refobjsubid", "deptype"} {
				delete(remaining, field)
			}
			if len(remaining) != 0 {
				// Unknown properties stay attached to their typed original edge;
				// swapping them between references cannot preserve the evidence.
				raw, err := pgGraphCanonicalJSON(edge)
				if err != nil {
					return nil, err
				}
				residual = append(residual, raw)
			}
		}
		sort.Slice(residual, func(i, j int) bool { return string(residual[i]) < string(residual[j]) })
		fields["unknown_edges"], err = json.Marshal(residual)
		return fields, err
	}
	a, err := unknown(before)
	if err != nil {
		return err
	}
	b, err := unknown(after)
	if err != nil {
		return err
	}
	return pgPreservationCompare("dependency", a, b)
}

func pgPreservationSequenceProperties(before, after json.RawMessage) error {
	if len(before) == 0 || string(before) == "null" || len(after) == 0 || string(after) == "null" {
		return nil // The independent declaration proves identity addition/removal.
	}
	a, err := pgPreservationFields(before)
	if err != nil {
		return err
	}
	b, err := pgPreservationFields(after)
	if err != nil {
		return err
	}
	for _, field := range strings.Fields("seqrelid seqtypid seqstart seqincrement seqmax seqmin seqcache seqcycle schema name") {
		delete(a, field)
		delete(b, field)
	}
	return pgPreservationCompare("identity_sequence", a, b)
}

// pg_depend rows are grouped by numeric OID in the raw snapshot, which can
// contain several catalog classes. Preserve unknown wrapper fields for every
// retained typed source; the graph's edge projection cannot discard them.
func pgPreservationDependencyContainers(original, actual PostgreSQLCatalog, before, after PostgreSQLGraphBindings) error {
	retained := map[string]bool{}
	current := map[string]bool{}
	for _, identity := range after.Objects {
		current[identity] = true
	}
	for _, identity := range before.Objects {
		retained[identity] = current[identity]
	}
	containers := func(p PostgreSQLCatalog, bindings PostgreSQLGraphBindings) (map[string]json.RawMessage, error) {
		out := map[string]json.RawMessage{}
		for _, entry := range p.Entries {
			if entry.Kind != "dependency" {
				continue
			}
			fields, err := pgPreservationFields(entry.Data)
			if err != nil {
				return nil, err
			}
			var edges []map[string]json.RawMessage
			if json.Unmarshal(fields["edges"], &edges) != nil || edges == nil {
				return nil, errors.New("invalid PostgreSQL original dependency container")
			}
			delete(fields, "edges")
			if len(fields) == 0 {
				continue
			}
			raw, err := pgGraphCanonicalJSON(fields)
			if err != nil {
				return nil, err
			}
			for _, fields := range edges {
				r := pgGraphRecord{entry: entry, data: fields}
				class, ce := r.integer("classid")
				oid, oe := r.integer("objid")
				if ce != nil || oe != nil || oid != entry.OID {
					return nil, errors.New("invalid PostgreSQL original dependency container identity")
				}
				identity := bindings.Objects[PostgreSQLCatalogObject{class, oid}]
				if !retained[identity] {
					continue
				}
				if old, exists := out[identity]; exists && string(old) != string(raw) {
					return nil, errors.New("ambiguous PostgreSQL original dependency container")
				}
				out[identity] = raw
			}
		}
		return out, nil
	}
	a, err := containers(original, before)
	if err != nil {
		return err
	}
	b, err := containers(actual, after)
	if err != nil {
		return err
	}
	return pgPreservationCompare("dependency_container", a, b)
}

func pgPreservationLabels(original, actual PostgreSQLCatalog, before, after PostgreSQLGraphBindings) error {
	retained := map[string]bool{}
	originalIdentities := map[string]bool{}
	for _, identity := range before.Objects {
		originalIdentities[identity] = true
	}
	for _, identity := range after.Objects {
		retained[identity] = originalIdentities[identity]
	}
	labels := func(catalog PostgreSQLCatalog, bindings PostgreSQLGraphBindings) (map[string]json.RawMessage, error) {
		out := map[string]json.RawMessage{}
		for _, entry := range catalog.Entries {
			if !strings.HasPrefix(entry.Kind, "security_label:") {
				continue
			}
			class, err := strconv.ParseInt(strings.TrimPrefix(entry.Kind, "security_label:"), 10, 64)
			if err != nil || class <= 0 {
				return nil, errors.New("invalid PostgreSQL security label catalog class")
			}
			identity := bindings.Objects[PostgreSQLCatalogObject{class, entry.OID}]
			if identity == "" || !retained[identity] {
				continue // Unselected context and deleted assets have other proofs.
			}
			attribute := ""
			if entry.SubID > 0 {
				if class != bindings.Classes["pg_class"] {
					return nil, errors.New("unbound PostgreSQL security label subobject")
				}
				attribute = bindings.Attributes[entry.OID][entry.SubID]
				if attribute == "" {
					return nil, errors.New("unbound PostgreSQL security label attribute")
				}
				if identity == "@table" {
					kept := false
					for _, id := range after.Attributes[actual.RelationOID] {
						kept = kept || id == attribute
					}
					if !kept {
						continue
					}
				}
			}
			key := Signature([]string{identity, attribute})
			if _, exists := out[key]; exists {
				return nil, errors.New("duplicate PostgreSQL retained security label identity")
			}
			out[key] = entry.Data
		}
		return out, nil
	}
	a, err := labels(original, before)
	if err != nil {
		return err
	}
	b, err := labels(actual, after)
	if err != nil {
		return err
	}
	return pgPreservationCompare("security_label", a, b)
}
