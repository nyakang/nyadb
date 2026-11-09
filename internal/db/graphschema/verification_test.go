package graphschema

import (
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestSchemaVerificationKeepsObservedIDsDefinitionsAndDesiredStateSeparate(t *testing.T) {
	c := catalog()
	addDefinition(&c, "index", "owned", "")
	before, e := Prepare(c, contract.GraphSchemaDraft{Action: "drop_index", Name: "owned"})
	if e != nil {
		t.Fatal(e)
	}
	changed := c.Clone()
	changed.Indexes.Rows[0][0].Text = "2"
	after, e := Prepare(changed, before.Draft)
	if e != nil {
		t.Fatal(e)
	}
	if PreflightMatches(before, after) {
		t.Fatal("observed replacement ID ignored before dispatch")
	}
	if !DefinitionEqual(before.Before[0], after.Before[0]) {
		t.Fatal("observation ID invented definition version")
	}
	if matched, e := DesiredMatches(before, c); e != nil || matched {
		t.Fatal("present DROP name became observed absence", e)
	}
	if matched, e := DesiredMatches(before, catalog()); e != nil || !matched {
		t.Fatal("explicit DROP absence not recognized", e)
	}
	create := contract.GraphSchemaPreview{Request: contract.GraphSchemaRequest{Database: "neo4j"}, Draft: contract.GraphSchemaDraft{Action: "create_index", Name: "owned", Type: "RANGE", EntityType: "NODE", LabelsOrTypes: []string{"Person"}, Properties: []string{"name"}}}
	if matched, e := DesiredMatches(create, c); e != nil || !matched {
		t.Fatal("original requested definition not recognized", e)
	}
	for i, key := range c.Indexes.Columns {
		if key == "indexProvider" {
			c.Indexes.Rows[0][i].Text = "unknown-provider"
		}
	}
	if matched, e := DesiredMatches(create, c); e != nil || matched {
		t.Fatal("foreign provider accepted as requested native definition", e)
	}
}

func TestSchemaVectorVerificationNativeEnumDefaultsAndStrictDimensions(t *testing.T) {
	c := catalog()
	addDefinition(&c, "index", "owned", "")
	cfg := func(n, similarity string) contract.GraphValue {
		return contract.GraphValue{Kind: "map", Properties: []contract.GraphProperty{{Name: "indexConfig", Value: contract.GraphValue{Kind: "map", Properties: []contract.GraphProperty{{Name: "vector.dimensions", Value: contract.GraphValue{Kind: "integer", Text: n}}, {Name: "vector.similarity_function", Value: contract.GraphValue{Kind: "string", Text: similarity}}}}}}}
	}
	for i, key := range c.Indexes.Columns {
		switch key {
		case "type":
			c.Indexes.Rows[0][i].Text = "VECTOR"
		case "indexProvider":
			c.Indexes.Rows[0][i].Text = "vector-2.0"
		case "options":
			c.Indexes.Rows[0][i] = cfg("3", "COSINE")
		}
	}
	p := contract.GraphSchemaPreview{Request: contract.GraphSchemaRequest{Database: "neo4j"}, Draft: contract.GraphSchemaDraft{Action: "create_index", Name: "owned", Type: "VECTOR", EntityType: "NODE", LabelsOrTypes: []string{"Person"}, Properties: []string{"name"}, Options: cfg("3", "cosine")}}
	if matched, e := DesiredMatches(p, c); e != nil || !matched {
		t.Fatal("native normalized enum rejected", e)
	}
	p.Draft.Options = cfg("4", "cosine")
	if matched, e := DesiredMatches(p, c); e != nil || matched {
		t.Fatal("wrong vector dimension accepted", e)
	}
	p.Draft.Options = cfg("3", "euclidean")
	if matched, e := DesiredMatches(p, c); e != nil || matched {
		t.Fatal("wrong native similarity accepted", e)
	}
}
