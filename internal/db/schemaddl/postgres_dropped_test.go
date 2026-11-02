package schemaddl

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func pgDroppedFieldFixture() PostgreSQLCatalogEntry {
	return pgRecord("column", 700, 3, `{"attrelid":"700","attname":"value","attnum":3,"atttypid":"25","atttypmod":-1,"attlen":-1,"attbyval":false,"attalign":"i","attndims":0,"attcollation":"100","attnotnull":true,"atthasdef":true,"attidentity":"","attgenerated":"s","attisdropped":false,"attislocal":true,"attinhcount":0,"attstorage":"e","attcompression":"p","attstattarget":73,"attoptions":["n_distinct=0.5"],"attfdwoptions":null,"attacl":["reader=r/owner"],"attcacheoff":-1,"atthasmissing":true,"attmissingval":[9007199254740993],"type_sql":"text","default_sql":"private expression","collation_sql":"pg_catalog.C","comment":"private comment","identity_sequence":null,"future":{"exact":9007199254740993,"private":"retained"}}`)
}

func TestPostgreSQLDroppedAttributeDerivesVersionedRemovalAndKeepsUnknownPhysicalState(t *testing.T) {
	entry := pgDroppedFieldFixture()
	before := Signature(entry)
	for _, version := range []int{140000, 150000, 160000, 170000, 180000} {
		out, err := pgNativeDroppedAttribute(entry, version)
		if err != nil {
			t.Fatal(err)
		}
		if out.OID != entry.OID || out.SubID != entry.SubID || out.Kind != entry.Kind || Signature(entry) != before {
			t.Fatal("dropped attribute lost original identity or modified input")
		}
		a, err := pgPreservationFields(entry.Data)
		if err != nil {
			t.Fatal(err)
		}
		b, err := pgPreservationFields(out.Data)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"attlen", "attbyval", "attalign", "attstorage", "attcompression", "atttypmod", "attndims", "attcollation", "attrelid", "attnum", "attislocal", "attinhcount", "attcacheoff", "attidentity", "future"} {
			if string(a[field]) != string(b[field]) {
				t.Fatal("unrequested physical/unknown attribute property changed", field)
			}
		}
		for field, value := range map[string]string{"attisdropped": "true", "atttypid": `"0"`, "attname": `"........pg.dropped.3........"`, "attnotnull": "false", "atthasdef": "false", "attgenerated": `""`, "atthasmissing": "false", "attmissingval": "null", "type_sql": `"-"`, "default_sql": `""`, "collation_sql": `""`, "comment": `""`, "identity_sequence": "null"} {
			if string(b[field]) != value {
				t.Fatal("native removal did not derive the required transition", field)
			}
		}
		if version < 170000 {
			if string(b["attstattarget"]) != "0" || string(b["attacl"]) != string(a["attacl"]) || string(b["attoptions"]) != string(a["attoptions"]) {
				t.Fatal("legacy removal discarded nullable original properties")
			}
		} else {
			for _, field := range []string{"attstattarget", "attacl", "attoptions", "attfdwoptions"} {
				if string(b[field]) != "null" {
					t.Fatal("native removal did not clear nullable fields", field)
				}
			}
		}
	}
}

func TestPostgreSQLDroppedAttributeRejectsIncompleteAndWrongIdentityWithoutPartialOutput(t *testing.T) {
	entry := pgDroppedFieldFixture()
	fields, _ := pgPreservationFields(entry.Data)
	delete(fields, "attcacheoff")
	modern := entry
	modern.Data, _ = json.Marshal(fields)
	if out, err := pgNativeDroppedAttribute(modern, 180000); err != nil || strings.Contains(string(out.Data), "attcacheoff") {
		t.Fatal("PG18 absent cache field was required or invented", err)
	}
	if _, err := pgNativeDroppedAttribute(modern, 170000); err == nil {
		t.Fatal("missing original PG17 cache field accepted")
	}
	for _, sample := range []struct {
		field, value string
	}{
		{"attrelid", `"701"`}, {"attnum", `2`}, {"atttypid", `"0"`}, {"atttypid", `1.5`}, {"attisdropped", `true`}, {"atthasmissing", `null`}, {"attalign", ``}, {"attalign", `"bad"`}, {"attlen", `"private"`}, {"attbyval", `null`},
	} {
		bad := entry
		fields, _ := pgPreservationFields(entry.Data)
		if sample.value == "" {
			delete(fields, sample.field)
		} else {
			fields[sample.field] = json.RawMessage(sample.value)
		}
		bad.Data, _ = json.Marshal(fields)
		out, err := pgNativeDroppedAttribute(bad, 170000)
		if err == nil || out.Data != nil || out.OID != 0 || strings.Contains(err.Error(), "private") {
			t.Fatal("invalid original granted dropped-attribute permission", err)
		}
	}
	for _, version := range []int{0, 130000} {
		if out, err := pgNativeDroppedAttribute(entry, version); err == nil || out.Data != nil {
			t.Fatal("unsupported removal returned partial evidence", err)
		}
	}
}

func TestPostgreSQLDroppedResidualsFollowOriginalStableIDsInsteadOfNamesOrActualState(t *testing.T) {
	if out, err := pgExpectedDroppedAttributes(Snapshot{}, Table{}); err == nil || out != nil {
		t.Fatal("invalid original snapshot published residuals")
	}
	p, _ := pgContextFixture(t)
	for i, e := range p.Entries {
		if e.Kind == "column" && e.OID == 700 && e.SubID == 3 {
			p.Entries[i] = pgDroppedFieldFixture()
		}
	}
	table := Table{Name: "probe", Clauses: []Clause{
		{Kind: "column", Name: "id", ID: "id-stable"}, {Kind: "column", Name: "value", ID: "value-stable"},
		{Kind: "constraint", Name: "probe_pk", ID: "pk-stable"}, {Kind: "constraint", Name: "probe_fk", ID: "fk-stable"},
	}, Definitions: []Definition{{Kind: "index", Name: "probe_idx"}, {Kind: "trigger", Name: "probe_tr"}, {Kind: "trigger", Name: "probe_ct"}}}
	base := Snapshot{Engine: "postgresql", Exists: true, Table: table, Postgres: &p}
	desired := table.Clone()
	desired.Clauses[1].Name = "renamed"
	if out, err := pgExpectedDroppedAttributes(base, desired); err != nil || len(out) != 0 {
		t.Fatal("stable rename was inferred as deletion", err)
	}
	desired = table.Clone()
	desired.Clauses[1].ID = "new-value"
	residuals, err := pgExpectedDroppedAttributes(base, desired)
	if err != nil || len(residuals) != 1 || residuals[0].SubID != 3 {
		t.Fatal("same name granted old physical slot to new identity", err)
	}
	// The exact independently derived residual must appear after removal;
	// loss of an old dropped slot, alignment, unknown property or new residual
	// is still forbidden by the full context comparison.
	desired.Clauses = slices.DeleteFunc(desired.Clauses, func(c Clause) bool { return c.Kind == "column" && c.Name == "value" })
	actual := p.Clone()
	actual.Entries = slices.DeleteFunc(actual.Entries, func(e PostgreSQLCatalogEntry) bool {
		return e.Kind == "column" && e.OID == 700 && e.SubID == 3 || e.Kind == "default" && e.OID == 700
	})
	actual.Entries = append(actual.Entries, residuals[0])
	beforeNames, afterNames := pgPreservationNames(table), pgPreservationNames(desired)
	if err := pgVerifyUnselectedCatalog(p, beforeNames, *actual, afterNames, residuals...); err != nil {
		t.Fatal("independently derived dropped slot rejected", err)
	}
	if err := pgVerifyUnselectedCatalog(p, beforeNames, *actual, afterNames); err == nil {
		t.Fatal("unrequested residual acquired permission")
	}
	for _, field := range []string{"attalign", "attlen", "future"} {
		changed := actual.Clone()
		for i, e := range changed.Entries {
			if e.Kind == "column" && e.OID == 700 && e.SubID == 3 {
				fields, _ := pgPreservationFields(e.Data)
				fields[field] = json.RawMessage(`"private mutation"`)
				changed.Entries[i].Data, _ = json.Marshal(fields)
			}
		}
		if err := pgVerifyUnselectedCatalog(p, beforeNames, *changed, afterNames, residuals...); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("wrong residual property accepted or exposed", err)
		}
	}
}
