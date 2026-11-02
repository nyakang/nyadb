package schemaddl

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPostgreSQLProjectionVectorsMapAttributesWithoutReplacingFlagsOrLiterals(t *testing.T) {
	g := pgGraphProjector{columns: map[int64]map[int]string{700: {1: "id", 4: "value"}}}
	for _, raw := range []string{`"1 0 4"`, `[1,0,4]`, `["1","0","4"]`} {
		r := pgGraphRecord{entry: PostgreSQLCatalogEntry{Kind: "index"}, data: map[string]json.RawMessage{"indkey": json.RawMessage(raw), "indoption": json.RawMessage(`[1,0,4]`), "future_literal": json.RawMessage(`4`)}}
		if err := g.vector(r, "indkey", 700, true); err != nil {
			t.Fatal(err)
		}
		if err := g.vector(r, "indoption", 0, false); err != nil {
			t.Fatal(err)
		}
		if string(r.data["indkey"]) != `[{"attribute_reference":"id"},0,{"attribute_reference":"value"}]` || string(r.data["indoption"]) != `[1,0,4]` || string(r.data["future_literal"]) != `4` {
			t.Fatal("attribute mapping changed a flag or unknown literal")
		}
	}
	for _, raw := range []string{`[1.5]`, `true`, `"{1,4}"`, `[null]`, `[9223372036854775808]`, `[2]`} {
		r := pgGraphRecord{entry: PostgreSQLCatalogEntry{Kind: "index"}, data: map[string]json.RawMessage{"indkey": json.RawMessage(raw)}}
		if err := g.vector(r, "indkey", 700, true); err == nil {
			t.Fatal("invalid/unbound attribute vector accepted", raw)
		}
	}
}

func TestPostgreSQLProjectionOIDReferencesRemainCatalogClassQualified(t *testing.T) {
	g := pgGraphProjector{bindings: PostgreSQLGraphBindings{Classes: map[string]int64{"pg_class": 1259, "pg_type": 1247}}, refs: map[PostgreSQLCatalogObject]string{{1259, 700}: "table", {1247, 700}: "type"}}
	for class, wanted := range map[string]string{"pg_class": "table", "pg_type": "type"} {
		r := pgGraphRecord{entry: PostgreSQLCatalogEntry{Kind: "relation"}, data: map[string]json.RawMessage{"oid": json.RawMessage(`"700"`), "unknown_oid": json.RawMessage(`"700"`)}}
		if err := g.oid(r, "oid", class); err != nil {
			t.Fatal(err)
		}
		if string(r.data["oid"]) != `{"catalog_reference":"`+wanted+`"}` || string(r.data["unknown_oid"]) != `"700"` {
			t.Fatal("OID class or unknown value lost")
		}
	}
}

func TestPostgreSQLProjectionDependencyClassesMultiplicityAndUnknownFieldsRemainEvidence(t *testing.T) {
	g := pgGraphProjector{bindings: PostgreSQLGraphBindings{Classes: map[string]int64{"pg_class": 1259, "pg_type": 1247}, Objects: map[PostgreSQLCatalogObject]string{{1259, 700}: "table", {1247, 700}: "row"}}, refs: map[PostgreSQLCatalogObject]string{}}
	raw := `{"edges":[{"classid":1259,"objid":700,"objsubid":0,"refclassid":2615,"refobjid":100,"refobjsubid":0,"deptype":"n","unknown":9007199254740993},{"classid":1247,"objid":700,"objsubid":0,"refclassid":2615,"refobjid":100,"refobjsubid":0,"deptype":"n"},{"classid":1247,"objid":700,"objsubid":0,"refclassid":2615,"refobjid":100,"refobjsubid":0,"deptype":"n"},{"classid":1255,"objid":700,"objsubid":0,"refclassid":2615,"refobjid":100,"refobjsubid":0,"deptype":"n"}]}`
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatal(err)
	}
	out := map[string][]map[string]json.RawMessage{}
	if err := g.dependencies(pgGraphRecord{entry: PostgreSQLCatalogEntry{Kind: "dependency", OID: 700}, data: fields}, out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || len(out["table"]) != 1 || len(out["row"]) != 2 || string(out["table"][0]["unknown"]) != "9007199254740993" {
		t.Fatal("dependency catalog classes, multiplicity or unknown values lost")
	}
}

func TestPostgreSQLProjectionComparisonRetainsUnknownExactValuesAndRejectsMalformedEvidence(t *testing.T) {
	graph := PostgreSQLDeclarationGraph{Scope: PostgreSQLCatalog{ServerVersion: 170000, DatabaseOID: 12, NamespaceOID: 14, NativeByteOrder: "little", Database: "db", Namespace: "schema"}, Records: []PostgreSQLGraphRecord{{Kind: "relation", Object: "@table", Declaration: json.RawMessage(`{"known":1,"future":{"b":9007199254740993,"a":true}}`), Opaque: json.RawMessage(`{"relacl":["owner=arwd/owner"]}`), Physical: json.RawMessage(`{"relfilenode":123}`)}}}
	pretty := graph.Clone()
	pretty.Records[0].Declaration = json.RawMessage(`{ "future": {"a":true,"b":9007199254740993}, "known": 1 }`)
	if err := ComparePostgreSQLDeclarationGraphs(graph, pretty); err != nil {
		t.Fatal("JSON key/space changes altered evidence", err)
	}
	changed := graph.Clone()
	changed.Records[0].Declaration = json.RawMessage(`{"known":1,"future":{"b":9007199254740992,"a":true}}`)
	err := ComparePostgreSQLDeclarationGraphs(graph, changed)
	if err == nil || strings.Contains(err.Error(), "900719925474099") {
		t.Fatal("exact unknown number lost or logged", err)
	}
	for _, raw := range []string{`null`, `{}`, `[]`, `not JSON`, string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'})} {
		changed := graph.Clone()
		changed.Records[0].Declaration = json.RawMessage(raw)
		if err := ComparePostgreSQLDeclarationGraphs(changed, changed); err == nil {
			t.Fatal("malformed matching graph accepted", raw)
		}
	}
	for _, change := range []func(*PostgreSQLDeclarationGraph){func(g *PostgreSQLDeclarationGraph) { g.Scope.DatabaseOID++ }, func(g *PostgreSQLDeclarationGraph) { g.Records = append(g.Records, g.Records[0]) }, func(g *PostgreSQLDeclarationGraph) { g.Records[0].Object = "other" }} {
		changed := graph.Clone()
		change(&changed)
		if err := ComparePostgreSQLDeclarationGraphs(graph, changed); err == nil {
			t.Fatal("wrong/duplicate scope or identity accepted")
		}
	}
	copy := graph.Clone()
	copy.Records[0].Declaration[0], copy.Records[0].Opaque[0], copy.Records[0].Physical[0] = '!', '!', '!'
	if graph.Records[0].Declaration[0] != '{' || graph.Records[0].Opaque[0] != '{' || graph.Records[0].Physical[0] != '{' {
		t.Fatal("projection clone shared buffers")
	}
}
