package schemaddl

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func pgStorageFixture(t *testing.T, rewritten bool) Snapshot {
	t.Helper()
	toast, index, file, typ := int64(17), int64(18), int64(100), "bigint"
	if rewritten {
		toast, index, file, typ = 27, 28, 200, "numeric(40,0)"
	}
	p := PostgreSQLCatalog{ServerVersion: 170000, Database: "fixture", Namespace: "fixture", DatabaseOID: 12, NamespaceOID: 11, RelationOID: 16}
	add := func(kind string, oid int64, sub int, data map[string]any) {
		raw, e := json.Marshal(data)
		if e != nil {
			t.Fatal(e)
		}
		p.Entries = append(p.Entries, PostgreSQLCatalogEntry{Kind: kind, OID: oid, SubID: sub, Data: raw})
	}
	for name, oid := range map[string]int64{"pg_class": 1259, "pg_type": 1247, "pg_attrdef": 2604, "pg_constraint": 2606, "pg_trigger": 2620} {
		add("catalog_class", oid, 0, map[string]any{"name": name})
	}
	relation := func(oid, storage, natts int64, name, kind, namespace string, child int64, hasIndex bool) {
		add("relation", oid, 0, map[string]any{"oid": oid, "relname": name, "relkind": kind, "relpersistence": "p", "relfilenode": storage, "reltoastrelid": child, "relnatts": natts, "relhasindex": hasIndex, "relhasrules": false, "relhastriggers": false, "relowner": 10, "owner_name": "owner", "namespace": namespace, "reltablespace": 0, "tablespace_name": "", "future_property": json.Number("9007199254740993")})
	}
	relation(16, file, 1, "t", "r", "fixture", toast, false)
	pgGraphChange(t, &p, "relation", 16, "reltype", 116)
	add("type", 116, 0, map[string]any{"oid": 116, "typrelid": 16, "typarray": 117})
	add("type", 117, 0, map[string]any{"oid": 117, "typelem": 116})
	relation(toast, file+1, 3, fmt.Sprint("pg_toast_", file), "t", "pg_toast", 0, true)
	relation(index, file+2, 2, fmt.Sprint("pg_toast_", file, "_index"), "i", "pg_toast", 0, false)
	var missing any = []any{json.Number("9007199254740993")}
	if rewritten {
		missing = nil
	}
	add("column", 16, 1, map[string]any{"attrelid": 16, "attnum": 1, "attname": "n", "attisdropped": false, "type_sql": typ, "attstorage": "p", "attcompression": "", "attstattarget": -1, "attislocal": true, "attinhcount": 0, "attcacheoff": -1, "atthasmissing": !rewritten, "attmissingval": missing, "attnotnull": false, "attidentity": "", "attgenerated": ""})
	for i, name := range []string{"chunk_id", "chunk_seq", "chunk_data"} {
		add("column", toast, i+1, map[string]any{"attrelid": toast, "attnum": i + 1, "attname": name, "attisdropped": false, "attcacheoff": -1, "atthasmissing": false, "attmissingval": nil, "future_property": json.Number("9007199254740993")})
	}
	name := fmt.Sprint("pg_toast_", file, "_index")
	add("index", index, 0, map[string]any{"indexrelid": index, "indrelid": toast, "relation_oid": toast, "indcheckxmin": false, "name": name, "definition": fmt.Sprintf("CREATE UNIQUE INDEX %s ON pg_toast.pg_toast_%d USING btree (chunk_id, chunk_seq)", name, file), "indkey": "1 2", "indisunique": true, "indisvalid": true, "indisready": true, "indislive": true, "future_property": json.Number("9007199254740993")})
	// Numeric OIDs are allowed to coincide across catalog classes. These
	// routines and their dependency records must survive storage remapping.
	for _, oid := range []int64{17, 27} {
		add("routine", oid, 0, map[string]any{"oid": oid, "prosrc": "private original", "future_property": json.Number("9007199254740993")})
	}
	edges := map[int64][]map[string]any{}
	edge := func(class, object, parent, sub int64, kind string) {
		edges[object] = append(edges[object], map[string]any{"classid": class, "objid": object, "objsubid": 0, "refclassid": 1259, "refobjid": parent, "refobjsubid": sub, "deptype": kind, "future_property": json.Number("9007199254740993")})
	}
	edge(1259, toast, 16, 0, "i")
	edge(1259, index, toast, 1, "a")
	edge(1259, index, toast, 2, "a")
	edge(1255, 17, 16, 0, "n")
	edge(1255, 27, 16, 0, "n")
	for oid, list := range edges {
		add("dependency", oid, 0, map[string]any{"edges": list})
	}
	table, e := p.Table()
	if e != nil {
		t.Fatal(e)
	}
	revision, e := p.Fingerprint()
	if e != nil {
		t.Fatal(e)
	}
	return Snapshot{Engine: "postgresql", Exists: true, Table: table, Postgres: &p, Revision: revision}
}

func TestPostgreSQLStorageContextRetainsDependencyWrappersAndRejectsRebinding(t *testing.T) {
	before, after := pgStorageFixture(t, false), pgStorageFixture(t, true)
	entry := pgRecord("dependency", 90, 0, `{"future":9007199254740993,"edges":[{"classid":1255,"objid":90,"objsubid":0,"refclassid":1259,"refobjid":16,"refobjsubid":0,"deptype":"n"}]}`)
	before.Postgres.Entries = append(before.Postgres.Entries, entry)
	after.Postgres.Entries = append(after.Postgres.Entries, entry)
	pgRenameRefresh(t, &before)
	pgRenameRefresh(t, &after)
	draft := before.Table.Clone()
	draft.Clauses[0].SQL = "numeric(40,0)"
	proof, err := VerifyPostgreSQLStorage(before, draft, after, Snapshot{}, "fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := pgVerifyUnselectedCatalog(*before.Postgres, pgPreservationNames(before.Table), *proof.context, pgPreservationNames(draft)); err != nil {
		t.Fatal("unchanged opaque dependency wrapper was lost", err)
	}
	changed := after.Clone()
	pgGraphChange(t, changed.Postgres, "dependency", 90, "future", json.Number("9007199254740992"))
	pgRenameRefresh(t, &changed)
	proof, err = VerifyPostgreSQLStorage(before, draft, changed, Snapshot{}, "fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := pgVerifyUnselectedCatalog(*before.Postgres, pgPreservationNames(before.Table), *proof.context, pgPreservationNames(draft)); err == nil || strings.Contains(err.Error(), "900719925474099") {
		t.Fatal("storage normalization erased a changed dependency wrapper", err)
	}
	changed = after.Clone()
	pgGraphChange(t, changed.Postgres, "dependency", 27, "future", json.Number("9007199254740993"))
	pgRenameRefresh(t, &changed)
	if _, err := VerifyPostgreSQLStorage(before, draft, changed, Snapshot{}, "fixture", false); err == nil {
		t.Fatal("unknown numeric container property was rebound with a typed storage edge")
	}
}

func TestPostgreSQLStorageValidatesAllNewAttributeSlotsAndCaches(t *testing.T) {
	base := pgStorageFixture(t, false)
	draft := base.Table.Clone()
	draft.Clauses = append(draft.Clauses, Clause{ID: "new:plain", Name: "plain", Kind: "column", SQL: "integer"})
	after := base.Clone()
	pgGraphChange(t, after.Postgres, "relation", 16, "relnatts", 2)
	after.Postgres.Entries = append(after.Postgres.Entries, pgRecord("column", 16, 2, `{"attrelid":16,"attnum":2,"attname":"plain","attisdropped":false,"type_sql":"integer","attstorage":"p","attcompression":"","attstattarget":-1,"attislocal":true,"attinhcount":0,"attcacheoff":-1,"atthasmissing":false,"attmissingval":null,"attnotnull":false,"attidentity":"","attgenerated":""}`))
	pgRenameRefresh(t, &after)
	if _, err := VerifyPostgreSQLStorage(base, draft, after, Snapshot{}, "fixture", false); err != nil {
		t.Fatal("nullable field without cache rejected", err)
	}
	for _, name := range []string{"missing slot", "wrong slot", "cache offset", "cache without default", "NULL cache vector", "empty cache vector"} {
		t.Run(name, func(t *testing.T) {
			bad := after.Clone()
			for i, entry := range bad.Postgres.Entries {
				if entry.Kind != "column" || entry.OID != 16 || entry.SubID != 2 {
					continue
				}
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(entry.Data, &fields)
				switch name {
				case "missing slot":
					bad.Postgres.Entries = slices.Delete(bad.Postgres.Entries, i, i+1)
				case "wrong slot":
					fields["attnum"] = json.RawMessage(`3`)
				case "cache offset":
					fields["attcacheoff"] = json.RawMessage(`8`)
				default:
					fields["atthasmissing"] = json.RawMessage(`true`)
					fields["attmissingval"] = json.RawMessage(`[9007199254740993]`)
					if name == "NULL cache vector" {
						fields["attmissingval"] = json.RawMessage(`[null]`)
					} else if name == "empty cache vector" {
						fields["attmissingval"] = json.RawMessage(`[]`)
					}
				}
				if name != "missing slot" {
					bad.Postgres.Entries[i].Data, _ = json.Marshal(fields)
				}
				break
			}
			// Missing physical evidence is rejected before table shape is used.
			bad.Revision, _ = bad.Postgres.Fingerprint()
			if name == "missing slot" {
				bad.Table, _ = bad.Postgres.Table()
			}
			if _, err := VerifyPostgreSQLStorage(base, draft, bad, Snapshot{}, "fixture", false); err == nil {
				t.Fatal("invalid physical evidence was accepted")
			}
		})
	}
}

func TestPostgreSQLStorageRewritePreservesTypedContextAndExactOpaqueProperties(t *testing.T) {
	before, after := pgStorageFixture(t, false), pgStorageFixture(t, true)
	draft := before.Table.Clone()
	draft.Clauses[0].SQL = "numeric(40,0)"
	originalSignature, actualSignature := Signature(before), Signature(after)
	proof, e := VerifyPostgreSQLStorage(before, draft, after, Snapshot{}, "fixture", false)
	if e != nil {
		t.Fatal(e)
	}
	if proof.original != originalSignature || proof.actual != actualSignature || Signature(before) != originalSignature || Signature(after) != actualSignature {
		t.Fatal("storage evidence mutated its request or receipt")
	}
	context, e := pgReadStorage(proof.context)
	if e != nil {
		t.Fatal(e)
	}
	if context.toast != 17 || len(context.indexes) != 1 || context.indexes[0] != 18 {
		t.Fatal("context did not retain original typed storage aliases")
	}
	for _, oid := range []int64{17, 27} {
		r := context.rows[pgContextKey{"routine", oid, 0}]
		if r.entry.OID != oid || string(r.data["prosrc"]) != `"private original"` || string(r.data["future_property"]) != "9007199254740993" {
			t.Fatal("storage mapping changed another catalog class")
		}
	}
	for _, oid := range []int64{17, 27} {
		r := context.rows[pgContextKey{"dependency", oid, 0}]
		var edges []map[string]json.RawMessage
		if json.Unmarshal(r.data["edges"], &edges) != nil {
			t.Fatal("typed dependencies were not regrouped")
		}
		found := false
		for _, edge := range edges {
			if string(edge["classid"]) == "1255" && string(edge["objid"]) == fmt.Sprint(oid) {
				found = true
			}
		}
		if !found {
			t.Fatal("foreign catalog dependency was erased or rebound")
		}
	}
	for _, change := range []struct {
		kind  string
		oid   int64
		field string
		value any
	}{
		{"relation", 27, "future_property", json.Number("9007199254740992")},
		{"relation", 27, "relowner", 11},
		{"column", 27, "future_property", json.Number("9007199254740992")},
		{"index", 28, "indisunique", false},
		{"index", 28, "definition", "CREATE UNIQUE INDEX pg_toast_200_index ON pg_toast.pg_toast_200 (chunk_seq)"},
	} {
		t.Run(change.kind+change.field, func(t *testing.T) {
			bad := after.Clone()
			pgGraphChange(t, bad.Postgres, change.kind, change.oid, change.field, change.value)
			pgRenameRefresh(t, &bad)
			_, e := VerifyPostgreSQLStorage(before, draft, bad, Snapshot{}, "fixture", false)
			if e == nil || strings.Contains(e.Error(), "900719925474099") || strings.Contains(e.Error(), "private") {
				t.Fatal("changed storage properties were accepted or exposed", e)
			}
		})
	}
}

func TestPostgreSQLStorageRejectsUnrequestedRewriteAndMissingValueChanges(t *testing.T) {
	base := pgStorageFixture(t, false)
	badName := base.Clone()
	pgGraphChange(t, badName.Postgres, "relation", 17, "relname", "unrequested_toast_name")
	pgRenameRefresh(t, &badName)
	if _, e := VerifyPostgreSQLStorage(base, base.Table, badName, Snapshot{}, "fixture", false); e == nil {
		t.Fatal("retained TOAST relation was renamed")
	}
	for _, change := range []struct {
		field string
		value any
	}{
		{"relfilenode", 900}, {"reltoastrelid", 0}, {"relnatts", 2}, {"relhasindex", true},
	} {
		bad := base.Clone()
		pgGraphChange(t, bad.Postgres, "relation", 16, change.field, change.value)
		pgRenameRefresh(t, &bad)
		if _, e := VerifyPostgreSQLStorage(base, base.Table, bad, Snapshot{}, "fixture", false); e == nil {
			t.Fatal("unrequested physical change accepted", change.field)
		}
	}
	for _, field := range []string{"atthasmissing", "attmissingval"} {
		bad := base.Clone()
		for i, entry := range bad.Postgres.Entries {
			if entry.Kind == "column" && entry.OID == 16 {
				var data map[string]json.RawMessage
				_ = json.Unmarshal(entry.Data, &data)
				if field == "atthasmissing" {
					data[field] = json.RawMessage(`false`)
					data["attmissingval"] = json.RawMessage(`null`)
				} else {
					data[field] = json.RawMessage(`[9007199254740992]`)
				}
				bad.Postgres.Entries[i].Data, _ = json.Marshal(data)
			}
		}
		pgRenameRefresh(t, &bad)
		if _, e := VerifyPostgreSQLStorage(base, base.Table, bad, Snapshot{}, "fixture", false); e == nil || strings.Contains(e.Error(), "900719925474099") {
			t.Fatal("missing value changed without rewrite", e)
		}
	}
}

func TestPostgreSQLStorageOIDRemappingIsClassBoundAndPreservesUnknownValues(t *testing.T) {
	r := pgGraphRecord{entry: PostgreSQLCatalogEntry{Kind: "routine", OID: 27}, data: map[string]json.RawMessage{"oid": json.RawMessage(`"27"`), "future": json.RawMessage(`9007199254740993`)}}
	mapped, e := pgStorageMapRecord(r, map[int64]int64{27: 17}, 1259)
	if e != nil || mapped.entry.OID != 27 || string(mapped.data["oid"]) != `"27"` || string(mapped.data["future"]) != "9007199254740993" {
		t.Fatal("foreign OID or exact literal was remapped", e)
	}
	if v, e := pgStorageMapReference(json.RawMessage(`"27"`), map[int64]int64{27: 17}); e != nil || string(v) != `"17"` {
		t.Fatal("typed JSON OID representation was lost", e)
	}
}

func pgStorageWithoutToast(t *testing.T, snapshot Snapshot) Snapshot {
	t.Helper()
	storage, err := pgReadStorage(snapshot.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	out := snapshot.Clone()
	pgGraphChange(t, out.Postgres, "relation", storage.p.RelationOID, "reltoastrelid", 0)
	entries := out.Postgres.Entries[:0]
	for _, entry := range out.Postgres.Entries {
		if _, owned := pgStorageAssetRows(storage)[pgContextKey{entry.Kind, entry.OID, entry.SubID}]; owned {
			continue
		}
		if entry.Kind == "dependency" {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(entry.Data, &fields)
			var edges []map[string]json.RawMessage
			_ = json.Unmarshal(fields["edges"], &edges)
			edges = slices.DeleteFunc(edges, func(edge map[string]json.RawMessage) bool {
				r := pgGraphRecord{data: edge}
				class, _ := r.integer("classid")
				object, _ := r.integer("objid")
				refClass, _ := r.integer("refclassid")
				ref, _ := r.integer("refobjid")
				return class == storage.class && storage.owned[object] || refClass == storage.class && storage.owned[ref]
			})
			if len(edges) == 0 {
				continue
			}
			fields["edges"], _ = json.Marshal(edges)
			entry.Data, _ = json.Marshal(fields)
		}
		entries = append(entries, entry)
	}
	out.Postgres.Entries = entries
	pgRenameRefresh(t, &out)
	return out
}

func TestPostgreSQLStorageRemovalRequiresIndependentWholeAssetDisappearance(t *testing.T) {
	original := pgStorageFixture(t, false)
	// Removal requires native ownership edges only; unknown edge payloads
	// cannot silently acquire deletion permission through a heap type edit.
	for i, entry := range original.Postgres.Entries {
		if entry.Kind != "dependency" {
			continue
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(entry.Data, &fields)
		var edges []map[string]json.RawMessage
		_ = json.Unmarshal(fields["edges"], &edges)
		for _, edge := range edges {
			delete(edge, "future_property")
		}
		fields["edges"], _ = json.Marshal(edges)
		original.Postgres.Entries[i].Data, _ = json.Marshal(fields)
	}
	pgRenameRefresh(t, &original)
	draft := original.Table.Clone()
	draft.Clauses[0].SQL = "integer"
	after := original.Clone()
	pgGraphChange(t, after.Postgres, "relation", 16, "relfilenode", 200)
	pgGraphChange(t, after.Postgres, "column", 16, "type_sql", "integer")
	pgGraphChange(t, after.Postgres, "column", 16, "atthasmissing", false)
	pgGraphChange(t, after.Postgres, "column", 16, "attmissingval", nil)
	after = pgStorageWithoutToast(t, after)
	_, _, probe, _ := pgReferenceFixture(t)
	pgGraphChange(t, probe.Postgres, "column", 36, "type_sql", "integer")
	probe = pgStorageWithoutToast(t, probe)
	beforeSignature, afterSignature := Signature(original), Signature(after)
	proof, err := VerifyPostgreSQLStorage(original, draft, after, probe, "fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := pgVerifyUnselectedCatalog(*original.Postgres, pgPreservationNames(original.Table), *proof.context, pgPreservationNames(draft)); err != nil {
		t.Fatal("verified removal changed unrelated context", err)
	}
	if Signature(original) != beforeSignature || Signature(after) != afterSignature {
		t.Fatal("verification-only removal context mutated workbench records")
	}
	for _, name := range []string{"missing probe", "remaining probe storage", "remaining asset", "foreign dependency", "unknown edge", "unknown wrapper", "label", "no rewrite"} {
		t.Run(name, func(t *testing.T) {
			before, actual, reference := original.Clone(), after.Clone(), probe.Clone()
			switch name {
			case "missing probe":
				reference = Snapshot{}
			case "remaining probe storage":
				_, _, reference, _ = pgReferenceFixture(t)
			case "remaining asset":
				for _, entry := range before.Postgres.Entries {
					if entry.Kind == "relation" && entry.OID == 17 {
						actual.Postgres.Entries = append(actual.Postgres.Entries, entry)
					}
				}
			case "foreign dependency":
				actual.Postgres.Entries = append(actual.Postgres.Entries, pgRecord("dependency", 91, 0, `{"edges":[{"classid":1255,"objid":91,"objsubid":0,"refclassid":1259,"refobjid":17,"refobjsubid":0,"deptype":"n"}]}`))
			case "unknown edge", "unknown wrapper":
				if name == "unknown wrapper" {
					pgGraphChange(t, before.Postgres, "dependency", 18, "future", json.Number("9007199254740993"))
				} else {
					before = pgStorageFixture(t, false)
				}
			case "label":
				before.Postgres.Entries = append(before.Postgres.Entries, pgRecord("security_label:1259", 17, 0, `{"labels":[{"provider":"fixture","label":"private"}]}`))
			case "no rewrite":
				pgGraphChange(t, actual.Postgres, "relation", 16, "relfilenode", 100)
			}
			pgRenameRefresh(t, &before)
			pgRenameRefresh(t, &actual)
			if _, err := VerifyPostgreSQLStorage(before, draft, actual, reference, "fixture", false); err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "900719925474099") {
				t.Fatal("unproved TOAST removal accepted or payload exposed", err)
			}
		})
	}
}
