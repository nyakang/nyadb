package schemaddl

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPostgreSQLRetainedOpaqueChangesRequireTheOriginalRequest(t *testing.T) {
	original := Table{Name: "t", Postgres: &PostgreSQLTableProperties{Comment: "table before", ReplicaIdentity: "d"}, Clauses: []Clause{{ID: "column:4", Kind: "column", Name: "label", Postgres: &PostgreSQLColumnProperties{Comment: "field before"}}}, Definitions: []Definition{{Kind: "index", Name: "idx", Postgres: &PostgreSQLDefinitionProperties{Comment: "index before"}}, {Kind: "trigger", Name: "tr", Postgres: &PostgreSQLDefinitionProperties{Comment: "trigger before"}}}}
	desired := original.Clone()
	desired.Postgres.Comment, desired.Postgres.ReplicaIdentity, desired.Postgres.RowSecurity = "table after", "f", true
	desired.Clauses[0].Postgres.Comment = "field after"
	desired.Definitions[0].Postgres.Comment = "index after"
	desired.Definitions[1].Postgres.Comment = "trigger after"
	for _, sample := range []struct {
		kind, object, attribute, old, next string
	}{
		{"relation", "@table", "", `{"comment":"table before","relreplident":"d","relrowsecurity":false,"relforcerowsecurity":false,"relacl":["reader=r/owner"],"relowner":42}`, `{"comment":"table after","relreplident":"f","relrowsecurity":true,"relforcerowsecurity":false,"relacl":["reader=r/owner"],"relowner":42}`},
		{"column", "@table", "@column:column:4", `{"comment":"field before","attacl":["reader=r/owner"],"attoptions":["n_distinct=0.5"]}`, `{"comment":"field after","attacl":["reader=r/owner"],"attoptions":["n_distinct=0.5"]}`},
		{"index", "@index:idx", "", `{"comment":"index before","indisclustered":true,"indisreplident":true}`, `{"comment":"index after","indisclustered":true,"indisreplident":true}`},
		{"relation", "@index:idx", "", `{"comment":"index before","relowner":42}`, `{"comment":"index after","relowner":42}`},
		{"trigger", "@trigger:tr", "", `{"comment":"trigger before"}`, `{"comment":"trigger after"}`},
	} {
		t.Run(sample.kind+sample.object, func(t *testing.T) {
			a := PostgreSQLGraphRecord{Kind: sample.kind, Object: sample.object, Attribute: sample.attribute, Opaque: json.RawMessage(sample.old)}
			b := a
			b.Opaque = json.RawMessage(sample.next)
			before := Signature(original)
			if err := pgPreservationOpaque(a, b, original, desired); err != nil {
				t.Fatal("explicit comment/property request rejected", err)
			}
			if Signature(original) != before {
				t.Fatal("original request changed")
			}
			if err := pgPreservationOpaque(a, b, original, original); err == nil {
				t.Fatal("a new graph gave permission for an unrequested original property")
			}
			fields, err := pgPreservationFields(b.Opaque)
			if err != nil {
				t.Fatal(err)
			}
			fields["future_property"] = json.RawMessage(`9007199254740993`)
			b.Opaque, _ = json.Marshal(fields)
			if err := pgPreservationOpaque(a, b, original, desired); err == nil || strings.Contains(err.Error(), "9007199254740993") {
				t.Fatal("unknown opaque property authorized or logged", err)
			}
		})
	}
	for _, sample := range []PostgreSQLGraphRecord{
		{Kind: "constraint", Object: "@constraint:ck", Opaque: json.RawMessage(`{"comment":"retain"}`)},
		{Kind: "trigger", Object: "@internal-trigger:fk", Opaque: json.RawMessage(`{"original_tgenabled":"D","original_enabled":"D","comment":"retain"}`)},
		{Kind: "type", Object: "@row-type", Opaque: json.RawMessage(`{"typacl":["reader=U/owner"],"typowner":42,"comment":"retain"}`)},
	} {
		bad := sample
		bad.Opaque = json.RawMessage(`{}`)
		if err := pgPreservationOpaque(sample, bad, original, desired); err == nil {
			t.Fatal("fresh CREATE default overwrote original opaque state", sample.Kind)
		}
	}
}

func TestPostgreSQLRetainedIdentitiesDoNotFollowFreshOIDsOrAttributePositions(t *testing.T) {
	p, names := pgGraphFixture()
	original, err := BuildPostgreSQLGraphBindings(p, names)
	if err != nil {
		t.Fatal(err)
	}
	if err := pgPreservationIdentities(original, original, p.RelationOID); err != nil {
		t.Fatal(err)
	}
	for _, identity := range []string{"@table", "@row-type", "@row-array", "@sequence:@column:id-stable"} {
		after := original
		after.Objects = make(map[PostgreSQLCatalogObject]string, len(original.Objects))
		for object, id := range original.Objects {
			if id == identity {
				object.ObjectOID += 2000
			}
			after.Objects[object] = id
		}
		if err := pgPreservationIdentities(original, after, p.RelationOID); err == nil {
			t.Fatal("native identity was replaced", identity)
		}
	}
	after := original
	after.Attributes = map[int64]map[int]string{p.RelationOID: {1: "@column:value-stable", 3: "@column:id-stable"}}
	if err := pgPreservationIdentities(original, after, p.RelationOID); err == nil {
		t.Fatal("stable column aliases concealed replaced attributes")
	}
}

func TestPostgreSQLRetainedSecurityLabelsUseCatalogClassAndStableAssetIDs(t *testing.T) {
	original := PostgreSQLCatalog{RelationOID: 700, Entries: []PostgreSQLCatalogEntry{
		pgRecord("security_label:1259", 700, 0, `{"labels":[{"provider":"fixture","label":"table"}]}`),
		pgRecord("security_label:1247", 700, 0, `{"labels":[{"provider":"fixture","label":"type"}]}`),
		pgRecord("security_label:1259", 700, 3, `{"labels":[{"provider":"fixture","label":"column"}]}`),
		pgRecord("security_label:2606", 800, 0, `{"labels":[{"provider":"fixture","label":"constraint"}]}`),
	}}
	before := PostgreSQLGraphBindings{Classes: map[string]int64{"pg_class": 1259}, Objects: map[PostgreSQLCatalogObject]string{{1259, 700}: "@table", {1247, 700}: "@row-type", {2606, 800}: "@constraint:retained"}, Attributes: map[int64]map[int]string{700: {3: "@column:value"}}}
	after := PostgreSQLGraphBindings{Classes: before.Classes, Objects: map[PostgreSQLCatalogObject]string{{1259, 700}: "@table", {1247, 700}: "@row-type", {2606, 900}: "@constraint:retained"}, Attributes: before.Attributes}
	actual := *original.Clone()
	actual.Entries[3].OID = 900
	if err := pgPreservationLabels(original, actual, before, after); err != nil {
		t.Fatal("rebuilt constraint label lost its stable identity", err)
	}
	for _, change := range []func(*PostgreSQLCatalog){
		func(c *PostgreSQLCatalog) { c.Entries = c.Entries[:3] },
		func(c *PostgreSQLCatalog) { c.Entries[1].Kind = "security_label:1259" },
		func(c *PostgreSQLCatalog) { c.Entries[2].SubID = 1 },
		func(c *PostgreSQLCatalog) {
			c.Entries[3].Data = json.RawMessage(`{"labels":[{"provider":"fixture","label":"changed"}]}`)
		},
	} {
		bad := *actual.Clone()
		change(&bad)
		if err := pgPreservationLabels(original, bad, before, after); err == nil {
			t.Fatal("label removal, class confusion, attribute change or new text accepted")
		}
	}
}

func TestPostgreSQLRetainedDependencyUnknownPropertiesKeepTheirTypedEdge(t *testing.T) {
	a := PostgreSQLGraphRecord{Kind: "dependency", Object: "@table", Declaration: json.RawMessage(`{"edges":[{"classid":1259,"objid":700,"objsubid":0,"refclassid":2615,"refobjid":10,"refobjsubid":0,"deptype":"n","future":9007199254740993},{"classid":1259,"objid":700,"objsubid":0,"refclassid":2615,"refobjid":11,"refobjsubid":0,"deptype":"n"}]}`)}
	if err := pgPreservationUnknownEdges(a, a); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"edges":[{"classid":1259,"objid":700,"objsubid":0,"refclassid":2615,"refobjid":10,"refobjsubid":0,"deptype":"n"},{"classid":1259,"objid":700,"objsubid":0,"refclassid":2615,"refobjid":11,"refobjsubid":0,"deptype":"n","future":9007199254740993}]}`,
		`{"edges":[{"classid":1259,"objid":700,"objsubid":0,"refclassid":2615,"refobjid":10,"refobjsubid":0,"deptype":"n","future":9007199254740992}]}`,
		`{"edges":null}`,
	} {
		b := a
		b.Declaration = json.RawMessage(raw)
		if err := pgPreservationUnknownEdges(a, b); err == nil || strings.Contains(err.Error(), "900719925474099") {
			t.Fatal("unknown edge metadata moved, rounded, malformed or logged", err)
		}
	}
}

func TestPostgreSQLRetainedSequenceNestedUnknownPropertiesSurviveTypeChanges(t *testing.T) {
	before := json.RawMessage(`{"seqrelid":{"catalog_reference":"sequence"},"seqtypid":21,"seqmin":1,"seqmax":32767,"future":{"exact":9007199254740993}}`)
	after := json.RawMessage(`{"future":{"exact":9007199254740993},"seqrelid":{"catalog_reference":"sequence"},"seqtypid":20,"seqmin":1,"seqmax":32767}`)
	if err := pgPreservationSequenceProperties(before, after); err != nil {
		t.Fatal("sequence type change obscured unknown properties", err)
	}
	for _, raw := range []string{
		`{"seqtypid":20,"future":{"exact":9007199254740992}}`,
		`{"seqtypid":20}`,
		`[]`,
	} {
		if err := pgPreservationSequenceProperties(before, json.RawMessage(raw)); err == nil || strings.Contains(err.Error(), "900719925474099") {
			t.Fatal("unknown nested sequence state overwritten or logged", err)
		}
	}
}

func TestPostgreSQLRetainedDependencyContainerPropertiesFollowTypedSources(t *testing.T) {
	original := PostgreSQLCatalog{Entries: []PostgreSQLCatalogEntry{pgRecord("dependency", 700, 0, `{"future":9007199254740993,"edges":[{"classid":1259,"objid":700},{"classid":2606,"objid":700},{"classid":1255,"objid":700}]}`)}}
	actual := PostgreSQLCatalog{Entries: []PostgreSQLCatalogEntry{
		pgRecord("dependency", 700, 0, `{"future":9007199254740993,"edges":[{"classid":1259,"objid":700}]}`),
		pgRecord("dependency", 900, 0, `{"future":9007199254740993,"edges":[{"classid":2606,"objid":900}]}`),
	}}
	before := PostgreSQLGraphBindings{Objects: map[PostgreSQLCatalogObject]string{{1259, 700}: "@table", {2606, 700}: "@constraint:ck"}}
	after := PostgreSQLGraphBindings{Objects: map[PostgreSQLCatalogObject]string{{1259, 700}: "@table", {2606, 900}: "@constraint:ck"}}
	if err := pgPreservationDependencyContainers(original, actual, before, after); err != nil {
		t.Fatal("retained dependency container failed typed rebuild mapping", err)
	}
	for _, raw := range []string{
		`{"future":9007199254740992,"edges":[{"classid":2606,"objid":900}]}`,
		`{"edges":[{"classid":2606,"objid":900}]}`,
		`{"future":9007199254740993,"edges":[{"classid":1255,"objid":900}]}`,
	} {
		bad := *actual.Clone()
		bad.Entries[1].Data = json.RawMessage(raw)
		if err := pgPreservationDependencyContainers(original, bad, before, after); err == nil || strings.Contains(err.Error(), "900719925474099") {
			t.Fatal("wrapper state lost, rounded, moved across classes or logged", err)
		}
	}
}
