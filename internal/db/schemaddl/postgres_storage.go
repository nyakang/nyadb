package schemaddl

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// PostgreSQLStorageTransition is immutable, scope-bound verification evidence.
// It supplements the declaration/value/sequence proofs; it is not permission
// to commit. Its normalized catalog is used only by the unchanged-context
// comparison, never returned to the workbench or written back to PostgreSQL.
type PostgreSQLStorageTransition struct {
	original, desired, actual string
	context                   *PostgreSQLCatalog
	baseline                  *PostgreSQLCatalog
}

type pgStorageCatalog struct {
	p       PostgreSQLCatalog
	rows    map[pgContextKey]pgGraphRecord
	class   int64
	root    pgGraphRecord
	toast   int64
	indexes []int64
	owned   map[int64]bool
}

func pgReadStorage(p *PostgreSQLCatalog) (out pgStorageCatalog, err error) {
	if p == nil || p.RelationOID <= 0 {
		return out, ErrUnsupported
	}
	if _, err = p.Fingerprint(); err != nil {
		return out, err
	}
	out = pgStorageCatalog{p: *p.Clone(), rows: map[pgContextKey]pgGraphRecord{}, owned: map[int64]bool{}}
	for _, e := range p.Entries {
		r := pgGraphRecord{entry: e}
		if err = json.Unmarshal(e.Data, &r.data); err != nil {
			return out, err
		}
		out.rows[pgContextKey{e.Kind, e.OID, e.SubID}] = r
		if e.Kind == "catalog_class" {
			name, e := r.text("name")
			if e != nil {
				return out, e
			}
			if name == "pg_class" {
				out.class = r.entry.OID
			}
		}
	}
	if out.class <= 0 {
		return out, errors.New("PostgreSQL storage catalog classes are incomplete")
	}
	out.root = out.rows[pgContextKey{"relation", p.RelationOID, 0}]
	if n, e := out.root.integer("oid"); e != nil || n != p.RelationOID {
		return out, ErrConflict
	}
	if kind, e := out.root.text("relkind"); e != nil || kind != "r" {
		return out, ErrUnsupported
	}
	if out.toast, err = out.root.integer("reltoastrelid"); err != nil || out.toast < 0 || out.toast > math.MaxUint32 {
		return out, ErrConflict
	}
	if out.toast == 0 {
		return out, nil
	}
	toast := out.rows[pgContextKey{"relation", out.toast, 0}]
	if n, e := toast.integer("oid"); e != nil || n != out.toast {
		return out, ErrConflict
	}
	if kind, e := toast.text("relkind"); e != nil || kind != "t" {
		return out, ErrConflict
	}
	if namespace, e := toast.text("namespace"); e != nil || namespace != "pg_toast" {
		return out, ErrConflict
	}
	out.owned[out.toast] = true
	for key, r := range out.rows {
		if key.kind != "index" {
			continue
		}
		parent, e := r.integer("indrelid")
		if e != nil {
			return out, e
		}
		if parent != out.toast {
			continue
		}
		if n, e := r.integer("indexrelid"); e != nil || n != key.oid {
			return out, ErrConflict
		}
		index := out.rows[pgContextKey{"relation", key.oid, 0}]
		if kind, e := index.text("relkind"); e != nil || kind != "i" {
			return out, ErrConflict
		}
		if namespace, e := index.text("namespace"); e != nil || namespace != "pg_toast" {
			return out, ErrConflict
		}
		out.indexes = append(out.indexes, key.oid)
		out.owned[key.oid] = true
	}
	// Heap TOAST storage has one unique chunk index. Do not infer an alias
	// from an auto-generated name or silently choose among additional indexes.
	if len(out.indexes) != 1 {
		return out, errors.New("PostgreSQL TOAST chunk index is incomplete or ambiguous")
	}
	if n, e := toast.integer("relnatts"); e != nil || n != 3 {
		return out, errors.New("PostgreSQL TOAST storage columns are incomplete")
	}
	for i, name := range []string{"chunk_id", "chunk_seq", "chunk_data"} {
		r := out.rows[pgContextKey{"column", out.toast, i + 1}]
		owner, e := r.integer("attrelid")
		number, e2 := r.integer("attnum")
		if e != nil || e2 != nil || owner != out.toast || number != int64(i+1) {
			return out, ErrConflict
		}
		if column, e := r.text("attname"); e != nil || column != name {
			return out, errors.New("PostgreSQL TOAST chunk column identity differs")
		}
		if dropped, e := r.boolean("attisdropped"); e != nil || dropped {
			return out, ErrConflict
		}
	}
	index := out.rows[pgContextKey{"index", out.indexes[0], 0}]
	for _, field := range []string{"indisunique", "indisvalid", "indisready", "indislive"} {
		if flag, e := index.boolean(field); e != nil || !flag {
			return out, errors.New("PostgreSQL TOAST chunk index is not complete and usable")
		}
	}
	for oid := range out.owned {
		if err := pgStorageOwner(out, out.rows[pgContextKey{"relation", oid, 0}]); err != nil {
			return out, err
		}
	}
	return out, nil
}

func pgStorageRewrite(original, desired Table) (rewrite, move, addToast bool, err error) {
	if original.Postgres == nil || desired.Postgres == nil {
		return false, false, false, ErrUnsupported
	}
	move = original.Postgres.Tablespace != desired.Postgres.Tablespace
	rewrite = original.Postgres.Persistence != desired.Postgres.Persistence || original.Postgres.AccessMethod != desired.Postgres.AccessMethod
	old := map[string]Clause{}
	for _, c := range original.Columns() {
		old[c.ID] = c
	}
	for _, c := range desired.Columns() {
		f, e := ParsePostgreSQLField(c.SQL)
		if e != nil || f.Unsupported {
			return false, false, false, errors.Join(ErrUnsupported, e)
		}
		prior, kept := old[c.ID]
		if !kept {
			addToast = true
			if f.HasDefault || f.Identity != "" || f.Generation != "" {
				rewrite = true
			}
			continue
		}
		a, e := ParsePostgreSQLField(prior.SQL)
		if e != nil || a.Unsupported {
			return false, false, false, errors.Join(ErrUnsupported, e)
		}
		if postgresSQLSignature(a.Type) != postgresSQLSignature(f.Type) || postgresSQLSignature(a.Generation) != postgresSQLSignature(f.Generation) || a.GenerationStorage != f.GenerationStorage {
			rewrite, addToast = true, true
		}
	}
	return rewrite, move, addToast, nil
}

// VerifyPostgreSQLStorage verifies the target's physical metadata and the
// complete owned TOAST lineage of a native ALTER. A declaration change can
// permit a rewrite/move; retained typed row values and the actual executor SQL
// must still be verified independently. A fresh TOAST lineage requires the
// independently created desired probe, not defaults fabricated by the client.
func VerifyPostgreSQLStorage(original Snapshot, desired Table, actual, probe Snapshot, schema string, allowDrop bool, references ...PostgreSQLReferenceBaseline) (proof PostgreSQLStorageTransition, err error) {
	plan, err := PlanPostgreSQL(original, desired, schema, allowDrop)
	if err != nil {
		return proof, err
	}
	if !original.Exists || plan.Mode != "alter" && plan.Mode != "noop" {
		return proof, ErrUnsupported
	}
	if !actual.Exists || actual.Engine != original.Engine || actual.Postgres == nil || actual.Table.Name != desired.Name || actual.Postgres.RelationOID != original.Postgres.RelationOID || !pgGraphSameScope(*original.Postgres, *actual.Postgres) {
		return proof, ErrConflict
	}
	canonical, e := actual.Postgres.Table()
	revision, e2 := actual.Postgres.Fingerprint()
	if e != nil || e2 != nil || Signature(canonical) != Signature(actual.Table) || revision != actual.Revision {
		return proof, errors.Join(ErrConflict, e, e2)
	}
	before, err := pgReadStorage(original.Postgres)
	if err != nil {
		return proof, err
	}
	after, err := pgReadStorage(actual.Postgres)
	if err != nil {
		return proof, err
	}
	rewrite, move, addToast, err := pgStorageRewrite(original.Table, desired)
	if err != nil {
		return proof, err
	}
	fileChanged, err := pgStorageRoot(before, after, original.Table, desired, rewrite, move)
	if err != nil {
		return proof, err
	}
	if err = pgStorageColumns(before, after, original.Table, desired, fileChanged && rewrite); err != nil {
		return proof, err
	}
	if before.toast != after.toast && (before.toast != 0 && !rewrite || before.toast == 0 && !addToast) {
		return proof, errors.New("PostgreSQL TOAST ownership changed without a storage-producing declaration")
	}
	if before.toast != 0 && before.toast != after.toast && !fileChanged {
		return proof, errors.New("PostgreSQL TOAST lineage was replaced without a heap rewrite")
	}
	context := actual.Postgres.Clone()
	if before.toast != 0 && after.toast == 0 {
		if err = pgStorageValidateProbe(original, probe); err != nil {
			return proof, err
		}
		reference, e := pgReadStorage(probe.Postgres)
		if e != nil || reference.toast != 0 {
			return proof, errors.Join(errors.New("independent desired PostgreSQL declaration still requires TOAST storage"), e)
		}
		context, err = pgStorageRemovedContext(before, after)
		if err != nil {
			return proof, err
		}
	} else if after.toast != 0 {
		reference := before
		fresh := before.toast == 0
		if fresh {
			if err = pgStorageValidateProbe(original, probe); err != nil {
				return proof, err
			}
			reference, err = pgReadStorage(probe.Postgres)
			if err != nil || reference.toast == 0 {
				return proof, errors.Join(ErrUnsupported, err)
			}
		}
		aliases := map[int64]int64{after.toast: reference.toast, after.indexes[0]: reference.indexes[0], after.p.RelationOID: reference.p.RelationOID}
		if !fresh && before.toast == after.toast {
			aliases[after.toast], aliases[after.indexes[0]] = before.toast, before.indexes[0]
		}
		if err = pgStorageCompareLineage(reference, after, aliases, rewrite || move, fresh, original.Table, desired); err != nil {
			return proof, err
		}
		context, err = pgStorageContext(before, after, reference, aliases, fresh, original.Table, desired)
		if err != nil {
			return proof, err
		}
	}
	baseline := original.Postgres.Clone()
	if len(references) > 1 {
		return proof, errors.New("ambiguous PostgreSQL reference before-image")
	}
	if len(references) == 1 {
		x := references[0]
		if x.catalog == nil || x.original != Signature(original) || x.desired != Signature(desired) || x.probe != Signature(probe) {
			return proof, errors.New("PostgreSQL reference evidence belongs to another definition request")
		}
		baseline = x.catalog.Clone()
	}
	context, err = pgReferenceTriggerFlags(baseline, context, desired)
	if err != nil {
		return proof, err
	}
	proof = PostgreSQLStorageTransition{original: Signature(original), desired: Signature(desired), actual: Signature(actual), context: context, baseline: baseline}
	return proof, nil
}

// A verified outgoing FK creates core internal triggers on its referenced
// relation. Permit only the derived false-to-true relhastriggers transition;
// every trigger itself remains subject to the independent declaration graph.
func pgReferenceTriggerFlags(before, after *PostgreSQLCatalog, desired Table) (*PostgreSQLCatalog, error) {
	b, err := BuildPostgreSQLGraphBindings(*after, pgPreservationNames(desired))
	if err != nil {
		return nil, err
	}
	parents := map[int64]bool{}
	for _, entry := range after.Entries {
		if entry.Kind != "trigger" || !strings.HasPrefix(b.Objects[PostgreSQLCatalogObject{b.Classes["pg_trigger"], entry.OID}], "@internal-trigger:") {
			continue
		}
		fields, err := pgPreservationFields(entry.Data)
		if err != nil {
			return nil, err
		}
		parent, err := (pgGraphRecord{data: fields}).integer("tgrelid")
		if err != nil {
			return nil, err
		}
		if parent != after.RelationOID {
			parents[parent] = true
		}
	}
	original := map[int64]PostgreSQLCatalogEntry{}
	for _, entry := range before.Entries {
		if entry.Kind == "relation" {
			original[entry.OID] = entry
		}
	}
	out := after.Clone()
	for i, entry := range out.Entries {
		if entry.Kind != "relation" || !parents[entry.OID] || original[entry.OID].OID == 0 {
			continue
		}
		a, err := pgPreservationFields(original[entry.OID].Data)
		if err != nil {
			return nil, err
		}
		fields, err := pgPreservationFields(entry.Data)
		if err != nil {
			return nil, err
		}
		if string(a["relhastriggers"]) == "false" && string(fields["relhastriggers"]) == "true" {
			fields["relhastriggers"] = json.RawMessage("false")
			out.Entries[i].Data, err = pgGraphCanonicalJSON(fields)
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func pgStorageValidateProbe(original, probe Snapshot) error {
	if !probe.Exists || probe.Engine != original.Engine || probe.Postgres == nil || probe.Postgres.RelationOID == original.Postgres.RelationOID || !pgGraphSameScope(*original.Postgres, *probe.Postgres) {
		return errors.New("independent PostgreSQL TOAST storage evidence is required")
	}
	table, e := probe.Postgres.Table()
	revision, e2 := probe.Postgres.Fingerprint()
	if e != nil || e2 != nil || Signature(table) != Signature(probe.Table) || revision != probe.Revision {
		return errors.Join(ErrConflict, e, e2)
	}
	return nil
}

// A rewriting type change can make the independently desired heap require no
// TOAST storage. Only the complete disappearance of the original generated
// assets is permitted. Foreign dependencies, labels and unknown dependency
// wrappers/edge properties have no such removal permission. Reintroducing the
// proved original assets below is solely an unchanged-context comparison aid;
// the returned workbench snapshot still contains the server's actual catalog.
func pgStorageRemovedContext(before, after pgStorageCatalog) (*PostgreSQLCatalog, error) {
	if err := pgStorageCompareEdges(before, before, map[int64]int64{}); err != nil {
		return nil, err
	}
	edges, err := pgStorageEdges(before, map[int64]int64{})
	if err != nil || len(edges) != 3 {
		return nil, errors.Join(errors.New("PostgreSQL removed TOAST storage has additional dependencies"), err)
	}
	assets := pgStorageAssetRows(before)
	for key := range assets {
		if key.kind != "relation" && key.kind != "column" && key.kind != "index" {
			return nil, errors.New("PostgreSQL removed TOAST storage has additional metadata")
		}
		if _, exists := after.rows[key]; exists {
			return nil, errors.New("PostgreSQL removed TOAST storage still has catalog assets")
		}
	}
	out := after.p.Clone()
	dependencies := map[int64]map[string]json.RawMessage{}
	for _, entry := range out.Entries {
		if entry.Kind != "dependency" {
			continue
		}
		fields := after.rows[pgContextKey{entry.Kind, entry.OID, entry.SubID}].data
		dependencies[entry.OID] = fields
	}
	for _, catalog := range []pgStorageCatalog{after, before} {
		for key, record := range catalog.rows {
			if key.kind != "dependency" {
				continue
			}
			var list []map[string]json.RawMessage
			if json.Unmarshal(record.data["edges"], &list) != nil {
				return nil, ErrConflict
			}
			for _, edge := range list {
				r := pgGraphRecord{entry: record.entry, data: edge}
				class, e := r.integer("classid")
				object, e2 := r.integer("objid")
				refClass, e3 := r.integer("refclassid")
				ref, e4 := r.integer("refobjid")
				if e != nil || e2 != nil || e3 != nil || e4 != nil || object != key.oid {
					return nil, ErrConflict
				}
				owned := class == before.class && before.owned[object] || refClass == before.class && before.owned[ref]
				if !owned {
					continue
				}
				if catalog.p.RelationOID == after.p.RelationOID && catalog.toast == 0 {
					return nil, errors.New("PostgreSQL removed TOAST storage still has dependencies")
				}
				if len(record.data) != 1 || len(edge) != 7 {
					return nil, errors.New("PostgreSQL TOAST removal cannot discard unknown dependency properties")
				}
				fields := dependencies[object]
				if fields == nil {
					fields = map[string]json.RawMessage{}
					dependencies[object] = fields
				}
				var current []map[string]json.RawMessage
				if raw, exists := fields["edges"]; exists && json.Unmarshal(raw, &current) != nil {
					return nil, ErrConflict
				}
				current = append(current, edge)
				fields["edges"], _ = json.Marshal(current)
			}
		}
	}
	out.Entries = nil
	for _, entry := range after.p.Entries {
		if entry.Kind == "dependency" {
			continue
		}
		if entry.Kind == "relation" && entry.OID == after.p.RelationOID {
			fields := after.root.data
			fields["reltoastrelid"] = append(json.RawMessage(nil), before.root.data["reltoastrelid"]...)
			entry.Data, _ = json.Marshal(fields)
		}
		out.Entries = append(out.Entries, entry)
	}
	for _, asset := range assets {
		out.Entries = append(out.Entries, asset.entry)
	}
	for oid, fields := range dependencies {
		raw, e := json.Marshal(fields)
		if e != nil {
			return nil, e
		}
		out.Entries = append(out.Entries, PostgreSQLCatalogEntry{Kind: "dependency", OID: oid, Data: raw})
	}
	if _, err := out.Fingerprint(); err != nil {
		return nil, err
	}
	return out, nil
}

func pgStorageRoot(before, after pgStorageCatalog, original, desired Table, rewrite, move bool) (bool, error) {
	a, e := before.root.integer("relfilenode")
	b, e2 := after.root.integer("relfilenode")
	if e != nil || e2 != nil || a <= 0 || b <= 0 || a > math.MaxUint32 || b > math.MaxUint32 {
		return false, errors.New("PostgreSQL heap storage address is incomplete")
	}
	if a != b && !rewrite && !move {
		return false, errors.New("PostgreSQL heap storage moved without a requested rewrite")
	}
	oldN, e := before.root.integer("relnatts")
	newN, e2 := after.root.integer("relnatts")
	kept := map[string]bool{}
	for _, c := range original.Columns() {
		kept[c.ID] = true
	}
	added := int64(0)
	for _, c := range desired.Columns() {
		if !kept[c.ID] {
			added++
		}
	}
	if e != nil || e2 != nil || oldN < 1 || newN != oldN+added || newN > 1600 {
		return false, errors.New("PostgreSQL physical attribute slots differ from the requested plan")
	}
	for _, field := range []string{"relhasindex", "relhasrules", "relhastriggers"} {
		old, e := before.root.boolean(field)
		actual, e2 := after.root.boolean(field)
		present := false
		for k, r := range after.rows {
			parentField := map[string]string{"index": "indrelid", "trigger": "tgrelid", "rewrite": "ev_class"}[k.kind]
			if parentField == "" || field != "relhas"+map[string]string{"index": "index", "trigger": "triggers", "rewrite": "rules"}[k.kind] {
				continue
			}
			parent, e := r.integer(parentField)
			if e != nil {
				return false, e
			}
			present = present || parent == after.p.RelationOID
		}
		// PostgreSQL retains conservative flags after DROP until maintenance.
		if e != nil || e2 != nil || actual != (old || present) {
			return false, errors.New("PostgreSQL heap program/index flags changed unexpectedly")
		}
	}
	return a != b, nil
}

func pgStorageColumns(before, after pgStorageCatalog, original, desired Table, rewritten bool) error {
	for _, catalog := range []pgStorageCatalog{before, after} {
		natts, err := catalog.root.integer("relnatts")
		if err != nil || natts < 1 || natts > 1600 {
			return ErrConflict
		}
		count := int64(0)
		for key, column := range catalog.rows {
			if key.kind != "column" || key.oid != catalog.p.RelationOID {
				continue
			}
			owner, e := column.integer("attrelid")
			number, e2 := column.integer("attnum")
			if e != nil || e2 != nil || owner != catalog.p.RelationOID || number != int64(key.sub) || number < 1 || number > natts {
				return ErrConflict
			}
			if err := pgStorageAttributeCache(column, catalog.p.ServerVersion); err != nil {
				return err
			}
			count++
		}
		if count != natts {
			return errors.New("PostgreSQL physical attribute evidence is incomplete")
		}
	}
	columns := map[string]pgGraphRecord{}
	for key, r := range before.rows {
		if key.kind == "column" && key.oid == before.p.RelationOID {
			name, e := r.text("attname")
			if e != nil {
				return e
			}
			if _, exists := columns[name]; exists {
				return ErrConflict
			}
			columns[name] = r
		}
	}
	kept := map[string]Clause{}
	for _, c := range original.Columns() {
		kept[c.ID] = c
	}
	newSlot, _ := before.root.integer("relnatts")
	for _, c := range desired.Columns() {
		prior, ok := kept[c.ID]
		if !ok {
			newSlot++
			next := after.rows[pgContextKey{"column", after.p.RelationOID, int(newSlot)}]
			field, e := ParsePostgreSQLField(c.SQL)
			name, e2 := next.text("attname")
			missing, e3 := next.boolean("atthasmissing")
			dropped, e4 := next.boolean("attisdropped")
			if e != nil || e2 != nil || e3 != nil || e4 != nil || name != c.Name || dropped {
				return ErrConflict
			}
			if missing && (!field.HasDefault || field.Identity != "" || field.Generation != "") {
				return errors.New("PostgreSQL new attribute has an unrequested missing-value cache")
			}
			continue
		} // New values/default cache contents still need their own row proof.
		old := columns[prior.Name]
		if old.data == nil {
			return errors.New("original PostgreSQL physical attribute is missing")
		}
		next := after.rows[pgContextKey{"column", after.p.RelationOID, old.entry.SubID}]
		if name, e := next.text("attname"); e != nil || name != c.Name {
			return ErrConflict
		}
		if pgStorageEqual(old.data["atthasmissing"], next.data["atthasmissing"]) && pgStorageEqual(old.data["attmissingval"], next.data["attmissingval"]) {
			continue
		}
		if !rewritten || string(next.data["atthasmissing"]) != "false" || string(next.data["attmissingval"]) != "null" {
			return errors.New("PostgreSQL retained missing-value cache changed without a heap rewrite")
		}
	}
	return nil
}

func pgStorageAttributeCache(r pgGraphRecord, version int) error {
	if version < 180000 {
		if n, e := r.integer("attcacheoff"); e != nil || n != -1 {
			return errors.New("unsupported PostgreSQL physical attribute offset")
		}
	} else if _, exists := r.data["attcacheoff"]; exists {
		return ErrConflict
	}
	flag, e := r.boolean("atthasmissing")
	value, exists := r.data["attmissingval"]
	if e != nil || !exists || !flag && string(value) != "null" {
		return errors.New("PostgreSQL missing-value evidence is inconsistent")
	}
	if flag {
		var values []json.RawMessage
		if json.Unmarshal(value, &values) != nil || len(values) != 1 || string(values[0]) == "null" {
			return errors.New("PostgreSQL missing-value vector is incomplete")
		}
	}
	return nil
}

func pgStorageEqual(a, b json.RawMessage) bool {
	decode := func(raw json.RawMessage) (any, error) {
		d := json.NewDecoder(strings.NewReader(string(raw)))
		d.UseNumber()
		var v any
		e := d.Decode(&v)
		return v, e
	}
	x, e := decode(a)
	y, e2 := decode(b)
	return e == nil && e2 == nil && Signature(x) == Signature(y)
}

func pgStorageMapNumber(raw json.RawMessage, aliases map[int64]int64) (json.RawMessage, error) {
	r := pgGraphRecord{entry: PostgreSQLCatalogEntry{Kind: "storage"}, data: map[string]json.RawMessage{"value": raw}}
	n, e := r.integer("value")
	if e != nil {
		return nil, e
	}
	if v, ok := aliases[n]; ok {
		return json.Marshal(v)
	}
	// Normalize the JSON number/string representation without floating point.
	return json.Marshal(n)
}

func pgStorageMapReference(raw json.RawMessage, aliases map[int64]int64) (json.RawMessage, error) {
	r := pgGraphRecord{entry: PostgreSQLCatalogEntry{Kind: "storage"}, data: map[string]json.RawMessage{"value": raw}}
	n, err := r.integer("value")
	if err != nil {
		return nil, err
	}
	v, changed := aliases[n]
	if !changed || v == n {
		return append(json.RawMessage(nil), raw...), nil
	}
	if strings.HasPrefix(strings.TrimSpace(string(raw)), `"`) {
		return json.Marshal(fmt.Sprint(v))
	}
	return json.Marshal(v)
}

func pgStorageMapRecord(r pgGraphRecord, aliases map[int64]int64, class int64) (pgGraphRecord, error) {
	out := pgGraphRecord{entry: r.entry, data: map[string]json.RawMessage{}}
	for key, value := range r.data {
		out.data[key] = append(json.RawMessage(nil), value...)
	}
	asset := r.entry.Kind == "relation" || r.entry.Kind == "index" || r.entry.Kind == "column" || r.entry.Kind == fmt.Sprintf("security_label:%d", class)
	if v, ok := aliases[r.entry.OID]; asset && ok {
		out.entry.OID = v
	}
	for _, field := range map[string][]string{"relation": {"oid", "reltoastrelid"}, "column": {"attrelid"}, "index": {"indexrelid", "indrelid", "relation_oid"}}[r.entry.Kind] {
		if raw, ok := out.data[field]; ok {
			mapped, e := pgStorageMapReference(raw, aliases)
			if e != nil {
				return out, e
			}
			out.data[field] = mapped
		}
	}
	return out, nil
}

func pgStorageAssetRows(s pgStorageCatalog) map[pgContextKey]pgGraphRecord {
	out := map[pgContextKey]pgGraphRecord{}
	for key, r := range s.rows {
		if !s.owned[key.oid] || key.kind == "dependency" {
			continue
		}
		if key.kind == "relation" || key.kind == "column" || key.kind == "index" || key.kind == fmt.Sprintf("security_label:%d", s.class) {
			out[key] = r
		}
	}
	return out
}

func pgStorageCompareLineage(before, after pgStorageCatalog, aliases map[int64]int64, mayMove, fresh bool, original, desired Table) error {
	old := pgStorageAssetRows(before)
	seen := map[pgContextKey]bool{}
	for _, r := range pgStorageAssetRows(after) {
		mapped, e := pgStorageMapRecord(r, aliases, after.class)
		if e != nil {
			return e
		}
		key := pgContextKey{mapped.entry.Kind, mapped.entry.OID, mapped.entry.SubID}
		prior, ok := old[key]
		if !ok || seen[key] {
			return errors.New("PostgreSQL TOAST lineage has unrequested catalog assets")
		}
		seen[key] = true
		if e = pgStorageNormalizeAsset(before, after, prior, &mapped, mayMove, fresh, original, desired); e != nil {
			return e
		}
		a, _ := json.Marshal(prior.data)
		b, _ := json.Marshal(mapped.data)
		if !pgStorageEqual(a, b) {
			return fmt.Errorf("PostgreSQL TOAST lineage changed retained %s properties", r.entry.Kind)
		}
	}
	if len(seen) != len(old) {
		return errors.New("PostgreSQL TOAST lineage lost catalog assets")
	}
	return pgStorageCompareEdges(before, after, aliases)
}

func pgStorageNormalizeAsset(before, after pgStorageCatalog, old pgGraphRecord, next *pgGraphRecord, mayMove, fresh bool, original, desired Table) error {
	if next.entry.Kind == "relation" {
		a, e := old.integer("relfilenode")
		b, e2 := next.integer("relfilenode")
		if e != nil || e2 != nil || a <= 0 || b <= 0 || b > math.MaxUint32 || a != b && !mayMove && !fresh {
			return errors.New("PostgreSQL TOAST storage address changed unexpectedly")
		}
		name, e := next.text("relname")
		if e != nil || PostgreSQLIdentifier(name) != nil {
			return ErrConflict
		}
		actualOID := after.toast
		if old.entry.OID == before.indexes[0] {
			actualOID = after.indexes[0]
		}
		if !fresh && old.entry.OID == actualOID && !pgStorageEqual(old.data["relname"], next.data["relname"]) {
			return errors.New("retained PostgreSQL TOAST asset was renamed unexpectedly")
		}
		if e := pgStorageOwner(after, *next); e != nil {
			return e
		}
		for _, field := range []string{"relname", "relfilenode"} {
			next.data[field] = old.data[field]
		}
		if fresh || original.Postgres.Owner != desired.Postgres.Owner {
			for _, field := range []string{"relowner", "owner_name"} {
				next.data[field] = old.data[field]
			}
		}
		if !fresh && original.Postgres.Persistence != desired.Postgres.Persistence {
			if !pgStorageEqual(next.data["relpersistence"], after.root.data["relpersistence"]) {
				return errors.New("PostgreSQL TOAST persistence differs from its owning heap")
			}
			next.data["relpersistence"] = old.data["relpersistence"]
		}
		if fresh || original.Postgres.Tablespace != desired.Postgres.Tablespace {
			if !pgStorageEqual(next.data["reltablespace"], after.root.data["reltablespace"]) {
				return errors.New("PostgreSQL TOAST tablespace differs from its owning heap")
			}
			for _, field := range []string{"reltablespace", "tablespace_name"} {
				next.data[field] = old.data[field]
			}
		}
	} else if next.entry.Kind == "index" {
		oldName, e := old.text("name")
		newName, e2 := next.text("name")
		oldRelation := before.rows[pgContextKey{"relation", before.indexes[0], 0}]
		newRelation := after.rows[pgContextKey{"relation", after.indexes[0], 0}]
		if e != nil || e2 != nil || !pgStorageEqual(old.data["name"], oldRelation.data["relname"]) || !pgStorageEqual(next.data["name"], newRelation.data["relname"]) {
			return errors.New("PostgreSQL TOAST index name differs from its captured relation")
		}
		oldTable, e := before.rows[pgContextKey{"relation", before.toast, 0}].text("relname")
		newTable, e2 := after.rows[pgContextKey{"relation", after.toast, 0}].text("relname")
		if e != nil || e2 != nil {
			return ErrConflict
		}
		oldSQL, e := old.text("definition")
		newSQL, e2 := next.text("definition")
		if e != nil || e2 != nil {
			return ErrConflict
		}
		a, e := RebindPostgreSQLDefinition(Definition{Kind: "index", Name: oldName, SQL: oldSQL}, "pg_toast", oldTable, "__nyadb_storage_toast", "__nyadb_storage_index")
		b, e2 := RebindPostgreSQLDefinition(Definition{Kind: "index", Name: newName, SQL: newSQL}, "pg_toast", newTable, "__nyadb_storage_toast", "__nyadb_storage_index")
		if e != nil || e2 != nil || postgresSQLSignature(a.SQL) != postgresSQLSignature(b.SQL) {
			return errors.New("PostgreSQL TOAST chunk index definition changed")
		}
		if flag, e := next.boolean("indcheckxmin"); e != nil {
			return e
		} else if !mayMove && !fresh && !pgStorageEqual(old.data["indcheckxmin"], next.data["indcheckxmin"]) {
			return errors.New("retained PostgreSQL TOAST index horizon changed")
		} else {
			_ = flag
		}
		for _, field := range []string{"name", "definition", "indcheckxmin"} {
			next.data[field] = old.data[field]
		}
	}
	return nil
}

func pgStorageOwner(s pgStorageCatalog, r pgGraphRecord) error {
	if !pgStorageEqual(r.data["relowner"], s.root.data["relowner"]) || !pgStorageEqual(r.data["owner_name"], s.root.data["owner_name"]) {
		return errors.New("PostgreSQL TOAST asset belongs to another owner")
	}
	return nil
}

func pgStorageEdges(s pgStorageCatalog, aliases map[int64]int64) (map[string]int, error) {
	out := map[string]int{}
	for key, r := range s.rows {
		if key.kind != "dependency" {
			continue
		}
		var edges []map[string]json.RawMessage
		if json.Unmarshal(r.data["edges"], &edges) != nil {
			return nil, ErrConflict
		}
		for _, edge := range edges {
			e := pgGraphRecord{entry: r.entry, data: edge}
			class, err := e.integer("classid")
			if err != nil {
				return nil, err
			}
			refClass, err := e.integer("refclassid")
			if err != nil {
				return nil, err
			}
			object, err := e.integer("objid")
			if err != nil {
				return nil, err
			}
			ref, err := e.integer("refobjid")
			if err != nil {
				return nil, err
			}
			if !(class == s.class && s.owned[object] || refClass == s.class && s.owned[ref]) {
				continue
			}
			for _, field := range []string{"classid", "refclassid", "objid", "refobjid", "objsubid", "refobjsubid"} {
				mapping := map[int64]int64{}
				if field == "objid" && class == s.class || field == "refobjid" && refClass == s.class {
					mapping = aliases
				}
				v, e := pgStorageMapNumber(edge[field], mapping)
				if e != nil {
					return nil, e
				}
				edge[field] = v
			}
			out[Signature(edge)]++
		}
	}
	return out, nil
}

func pgStorageCompareEdges(before, after pgStorageCatalog, aliases map[int64]int64) error {
	a, e := pgStorageEdges(before, map[int64]int64{})
	b, e2 := pgStorageEdges(after, aliases)
	if e != nil || e2 != nil {
		return errors.Join(e, e2)
	}
	if Signature(a) != Signature(b) {
		return errors.New("PostgreSQL TOAST ownership/dependency edges changed unexpectedly")
	}
	// Each storage relation must be linked by an actual typed dependency.
	for _, s := range []pgStorageCatalog{before, after} {
		for child := range s.owned {
			parent := s.toast
			if child == s.toast {
				parent = s.p.RelationOID
			}
			found := false
			chunks := map[int64]bool{}
			for k, r := range s.rows {
				if k.kind != "dependency" {
					continue
				}
				var edges []map[string]json.RawMessage
				if json.Unmarshal(r.data["edges"], &edges) != nil {
					return ErrConflict
				}
				for _, edge := range edges {
					x := pgGraphRecord{entry: r.entry, data: edge}
					class, e := x.integer("classid")
					refClass, e2 := x.integer("refclassid")
					object, e3 := x.integer("objid")
					ref, e4 := x.integer("refobjid")
					sub, e5 := x.integer("objsubid")
					refSub, e6 := x.integer("refobjsubid")
					kind, e7 := x.text("deptype")
					if e != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil || e7 != nil {
						return ErrConflict
					}
					if class == s.class && refClass == s.class && object == child && ref == parent && sub == 0 {
						if child == s.toast && refSub == 0 && kind == "i" {
							found = true
						}
						if child != s.toast && kind == "a" && (refSub == 1 || refSub == 2) {
							chunks[refSub] = true
						}
					}
				}
			}
			if !found && !(child != s.toast && chunks[1] && chunks[2]) {
				return errors.New("PostgreSQL TOAST storage has no captured ownership edge")
			}
		}
	}
	return nil
}

func pgStorageContext(before, after, reference pgStorageCatalog, aliases map[int64]int64, fresh bool, original, desired Table) (*PostgreSQLCatalog, error) {
	out := after.p.Clone()
	out.Entries = nil
	old := pgStorageAssetRows(reference)
	assets := pgStorageAssetRows(after)
	contextAliases := aliases
	if fresh {
		contextAliases = map[int64]int64{}
	}
	dependencies := map[int64][]map[string]json.RawMessage{}
	wrappers := map[int64]map[string]json.RawMessage{}
	for _, entry := range after.p.Entries {
		r := after.rows[pgContextKey{entry.Kind, entry.OID, entry.SubID}]
		if entry.Kind == "dependency" {
			var edges []map[string]json.RawMessage
			if entry.SubID != 0 || json.Unmarshal(r.data["edges"], &edges) != nil || len(edges) == 0 {
				return nil, ErrConflict
			}
			wrapper := map[string]json.RawMessage{}
			for field, value := range r.data {
				if field != "edges" {
					wrapper[field] = append(json.RawMessage(nil), value...)
				}
			}
			wrappers[entry.OID] = wrapper
			for _, edge := range edges {
				x := pgGraphRecord{entry: entry, data: edge}
				class, e := x.integer("classid")
				refClass, e2 := x.integer("refclassid")
				object, e3 := x.integer("objid")
				ref, e4 := x.integer("refobjid")
				if e != nil || e2 != nil || e3 != nil || e4 != nil || object != entry.OID {
					return nil, ErrConflict
				}
				if fresh && (class == after.class && after.owned[object] || refClass == after.class && after.owned[ref]) {
					if len(wrapper) != 0 {
						return nil, errors.New("PostgreSQL probe storage filtering cannot discard unknown dependency container properties")
					}
					continue
				}
				if !fresh {
					if class == after.class {
						edge["objid"], e = pgStorageMapReference(edge["objid"], aliases)
						if e != nil {
							return nil, e
						}
						if n, ok := aliases[object]; ok {
							if n != object && len(wrapper) != 0 {
								return nil, errors.New("PostgreSQL storage aliases cannot rebind unknown dependency container properties")
							}
							object = n
						}
					}
					if refClass == after.class {
						edge["refobjid"], e = pgStorageMapReference(edge["refobjid"], aliases)
						if e != nil {
							return nil, e
						}
					}
				}
				dependencies[object] = append(dependencies[object], edge)
			}
			continue
		}
		_, asset := assets[pgContextKey{entry.Kind, entry.OID, entry.SubID}]
		if fresh && asset {
			continue
		}
		mapped, e := pgStorageMapRecord(r, contextAliases, after.class)
		if e != nil {
			return nil, e
		}
		if asset {
			prior, ok := old[pgContextKey{mapped.entry.Kind, mapped.entry.OID, mapped.entry.SubID}]
			if !ok {
				return nil, ErrUnsupported
			}
			if e := pgStorageNormalizeAsset(reference, after, prior, &mapped, true, fresh, original, desired); e != nil {
				return nil, e
			}
			// Preserve original typed JSON representation for strict context,
			// after separately comparing all nonpermitted metadata above.
			mapped.data = prior.data
		}
		if fresh && mapped.entry.Kind == "relation" && mapped.entry.OID == after.p.RelationOID {
			mapped.data["reltoastrelid"] = append(json.RawMessage(nil), before.root.data["reltoastrelid"]...)
		}
		mapped.entry.Data, e = json.Marshal(mapped.data)
		if e != nil {
			return nil, e
		}
		out.Entries = append(out.Entries, mapped.entry)
	}
	ids := make([]int64, 0, len(dependencies))
	for id := range dependencies {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		fields := wrappers[id]
		if fields == nil {
			fields = map[string]json.RawMessage{}
		}
		fields["edges"], _ = json.Marshal(dependencies[id])
		data, e := json.Marshal(fields)
		if e != nil {
			return nil, e
		}
		out.Entries = append(out.Entries, PostgreSQLCatalogEntry{Kind: "dependency", OID: id, Data: data})
	}
	if _, e := out.Fingerprint(); e != nil {
		return nil, e
	}
	return out, nil
}
