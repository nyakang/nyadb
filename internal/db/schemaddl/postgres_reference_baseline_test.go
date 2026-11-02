package schemaddl

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func pgReferenceFixture(t *testing.T) (Snapshot, Table, Snapshot, Snapshot) {
	t.Helper()
	original := pgStorageFixture(t, false)
	desired := original.Table.Clone()
	desired.Postgres.Comment = "requested comment"
	probe := original.Clone()
	probe.Postgres.RelationOID = 36
	for i, entry := range probe.Postgres.Entries {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entry.Data, &fields); err != nil {
			t.Fatal(err)
		}
		r, err := pgStorageMapRecord(pgGraphRecord{entry: entry, data: fields}, map[int64]int64{16: 36, 17: 47, 18: 48}, 1259)
		if err != nil {
			t.Fatal(err)
		}
		if entry.Kind == "relation" && entry.OID == 16 {
			r.data["reltype"], r.data["relname"], r.data["comment"] = json.RawMessage(`136`), json.RawMessage(`"__probe"`), json.RawMessage(`"requested comment"`)
		}
		if entry.Kind == "type" {
			if entry.OID == 116 {
				r.entry.OID = 136
				r.data["oid"], r.data["typrelid"], r.data["typarray"] = json.RawMessage(`136`), json.RawMessage(`36`), json.RawMessage(`137`)
			} else if entry.OID == 117 {
				r.entry.OID = 137
				r.data["oid"], r.data["typelem"] = json.RawMessage(`137`), json.RawMessage(`136`)
			}
		}
		if entry.Kind == "dependency" {
			var edges []map[string]json.RawMessage
			if json.Unmarshal(r.data["edges"], &edges) != nil {
				t.Fatal("invalid fixture dependency")
			}
			var storageEdges []map[string]json.RawMessage
			for _, edge := range edges {
				x := pgGraphRecord{entry: entry, data: edge}
				class, _ := x.integer("classid")
				refClass, _ := x.integer("refclassid")
				if class != 1259 {
					continue // Original foreign context does not depend on a new probe.
				}
				if class == 1259 {
					edge["objid"], _ = pgStorageMapReference(edge["objid"], map[int64]int64{17: 47, 18: 48})
					object, _ := (pgGraphRecord{data: edge}).integer("objid")
					r.entry.OID = object
				}
				if refClass == 1259 {
					edge["refobjid"], _ = pgStorageMapReference(edge["refobjid"], map[int64]int64{16: 36, 17: 47})
				}
				storageEdges = append(storageEdges, edge)
			}
			r.data["edges"], _ = json.Marshal(storageEdges)
		}
		r.entry.Data, _ = json.Marshal(r.data)
		probe.Postgres.Entries[i] = r.entry
	}
	probe.Postgres.Entries = slices.DeleteFunc(probe.Postgres.Entries, func(entry PostgreSQLCatalogEntry) bool {
		if entry.Kind != "dependency" {
			return false
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(entry.Data, &fields)
		var edges []map[string]json.RawMessage
		_ = json.Unmarshal(fields["edges"], &edges)
		return len(edges) == 0
	})
	// Typed reference edges include one ephemeral probe endpoint. Its edge
	// cannot survive rollback, while the external edge and wrapper must.
	cast := pgRecord("cast", 90, 0, `{"oid":90,"castsource":20,"casttarget":1700,"future":9007199254740993}`)
	dependency := pgRecord("dependency", 90, 0, `{"future":9007199254740993,"edges":[{"classid":2605,"objid":90,"objsubid":0,"refclassid":1247,"refobjid":20,"refobjsubid":0,"deptype":"n"}]}`)
	before := original.Clone()
	before.Postgres.Entries = append(before.Postgres.Entries, cast, dependency)
	probe.Postgres.Entries = append(probe.Postgres.Entries, cast, dependency)
	for i, entry := range probe.Postgres.Entries {
		if entry.Kind == "dependency" && entry.OID == 90 {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(entry.Data, &fields)
			var edges []map[string]json.RawMessage
			_ = json.Unmarshal(fields["edges"], &edges)
			edges = append(edges, map[string]json.RawMessage{"classid": json.RawMessage(`2605`), "objid": json.RawMessage(`90`), "objsubid": json.RawMessage(`0`), "refclassid": json.RawMessage(`1259`), "refobjid": json.RawMessage(`36`), "refobjsubid": json.RawMessage(`0`), "deptype": json.RawMessage(`"n"`)})
			fields["edges"], _ = json.Marshal(edges)
			probe.Postgres.Entries[i].Data, _ = json.Marshal(fields)
		}
	}
	pgRenameRefresh(t, &probe)
	pgRenameRefresh(t, &before)
	return original, desired, probe, before
}

func TestPostgreSQLReferenceBaselinePreservesIndependentContextAndRequest(t *testing.T) {
	original, desired, probe, before := pgReferenceFixture(t)
	signatures := []string{Signature(original), Signature(desired), Signature(probe), Signature(before)}
	evidence, err := CapturePostgreSQLReferenceBaseline(original, desired, probe, before)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.original != signatures[0] || evidence.desired != signatures[1] || evidence.probe != signatures[2] || Signature(evidence.catalog) != Signature(before.Postgres) {
		t.Fatal("reference evidence is not bound to immutable request values")
	}
	pgGraphChange(t, before.Postgres, "cast", 90, "future", json.Number("9007199254740992"))
	if Signature(evidence.catalog) == Signature(before.Postgres) {
		t.Fatal("reference evidence shares caller-owned buffers")
	}
	if Signature(original) != signatures[0] || Signature(desired) != signatures[1] || Signature(probe) != signatures[2] {
		t.Fatal("reference capture mutated original request")
	}
}

func TestPostgreSQLReferenceBaselineRejectsChangedLostOrEphemeralContext(t *testing.T) {
	for _, name := range []string{"original", "reference", "wrapper", "missing", "unknown", "probe object", "probe edge", "duplicate edge", "scope", "engine", "canonical", "revision", "same target"} {
		t.Run(name, func(t *testing.T) {
			original, desired, probe, before := pgReferenceFixture(t)
			switch name {
			case "original":
				pgGraphChange(t, before.Postgres, "relation", 16, "future_property", json.Number("9007199254740992"))
			case "reference":
				pgGraphChange(t, before.Postgres, "cast", 90, "future", json.Number("9007199254740992"))
			case "wrapper":
				pgGraphChange(t, before.Postgres, "dependency", 90, "future", json.Number("9007199254740992"))
			case "missing":
				before.Postgres.Entries = slices.DeleteFunc(before.Postgres.Entries, func(e PostgreSQLCatalogEntry) bool { return e.Kind == "routine" && e.OID == 17 })
			case "unknown":
				before.Postgres.Entries = append(before.Postgres.Entries, pgRecord("cast", 91, 0, `{"oid":91}`))
			case "probe object":
				for _, entry := range probe.Postgres.Entries {
					if entry.Kind == "relation" && entry.OID == 36 {
						before.Postgres.Entries = append(before.Postgres.Entries, entry)
					}
				}
			case "probe edge", "duplicate edge":
				for i, entry := range before.Postgres.Entries {
					if entry.Kind != "dependency" || entry.OID != 90 {
						continue
					}
					var fields map[string]json.RawMessage
					_ = json.Unmarshal(entry.Data, &fields)
					var edges []map[string]json.RawMessage
					_ = json.Unmarshal(fields["edges"], &edges)
					if name == "probe edge" {
						edges[0]["refclassid"], edges[0]["refobjid"] = json.RawMessage(`1259`), json.RawMessage(`36`)
					} else {
						edges = append(edges, edges[0])
					}
					fields["edges"], _ = json.Marshal(edges)
					before.Postgres.Entries[i].Data, _ = json.Marshal(fields)
				}
			case "scope":
				before.Postgres.DatabaseOID++
			case "engine":
				probe.Engine = "sqlite"
			case "same target":
				probe = original.Clone()
			}
			pgRenameRefresh(t, &before)
			if name == "canonical" {
				probe.Table.Name = "forged"
			} else if name == "revision" {
				probe.Revision = "stale"
			}
			if _, err := CapturePostgreSQLReferenceBaseline(original, desired, probe, before); err == nil || strings.Contains(err.Error(), "900719925474099") {
				t.Fatal("invalid context acquired a reference baseline or exposed its values", err)
			}
		})
	}
}

func TestPostgreSQLStorageRejectsReferenceEvidenceFromAnotherRequest(t *testing.T) {
	original, desired, probe, before := pgReferenceFixture(t)
	evidence, err := CapturePostgreSQLReferenceBaseline(original, desired, probe, before)
	if err != nil {
		t.Fatal(err)
	}
	actual := before.Clone()
	pgGraphChange(t, actual.Postgres, "relation", 16, "comment", desired.Postgres.Comment)
	pgRenameRefresh(t, &actual)
	if _, err := VerifyPostgreSQLStorage(original, desired, actual, probe, "fixture", false, evidence); err != nil {
		t.Fatal("original reference evidence was rejected", err)
	}
	for _, name := range []string{"original", "desired", "probe", "missing", "ambiguous"} {
		t.Run(name, func(t *testing.T) {
			foreign := evidence
			switch name {
			case "original":
				foreign.original = "other"
			case "desired":
				foreign.desired = "other"
			case "probe":
				foreign.probe = "other"
			case "missing":
				foreign.catalog = nil
			}
			references := []PostgreSQLReferenceBaseline{foreign}
			if name == "ambiguous" {
				references = append(references, foreign)
			}
			if _, err := VerifyPostgreSQLStorage(original, desired, actual, probe, "fixture", false, references...); err == nil {
				t.Fatal("foreign or incomplete reference evidence authorized a storage comparison")
			}
		})
	}
}
