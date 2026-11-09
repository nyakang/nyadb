package graphschema

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func scalar(kind, text string) contract.GraphValue {
	return contract.GraphValue{Kind: kind, Text: text}
}
func list(items ...string) contract.GraphValue {
	v := contract.GraphValue{Kind: "list"}
	for _, s := range items {
		v.Values = append(v.Values, scalar("string", s))
	}
	return v
}
func options(props ...contract.GraphProperty) contract.GraphValue {
	return contract.GraphValue{Kind: "map", Properties: []contract.GraphProperty{{Name: "indexConfig", Value: contract.GraphValue{Kind: "map", Properties: props}}}}
}
func catalog() contract.GraphSchemaCatalog {
	obj := contract.ObjectRef{ProfileID: 42, Catalog: "neo4j", Kind: "graph_schema", Name: "graph_schema"}
	response := func(columns string) contract.GraphResult {
		return contract.GraphResult{Columns: strings.Fields(columns), Attempted: true, Completed: true, Summary: contract.GraphSummary{Database: obj.Catalog}}
	}
	return contract.GraphSchemaCatalog{Object: obj, Indexes: response("id name state populationPercent type entityType labelsOrTypes properties indexProvider owningConstraint lastRead readCount trackedSince options failureMessage createStatement"), Constraints: response("id name type entityType labelsOrTypes properties ownedIndex propertyType options createStatement")}
}
func addDefinition(c *contract.GraphSchemaCatalog, kind, name, owner string) {
	r := &c.Indexes
	if kind == "constraint" {
		r = &c.Constraints
	}
	values := map[string]contract.GraphValue{
		"id": scalar("integer", "1"), "name": scalar("string", name), "state": scalar("string", "ONLINE"), "populationPercent": scalar("float", "100"),
		"type": scalar("string", "RANGE"), "entityType": scalar("string", "NODE"), "labelsOrTypes": list("Person"), "properties": list("name"),
		"indexProvider": scalar("string", "range-1.0"), "owningConstraint": {Kind: "null"}, "ownedIndex": {Kind: "null"}, "propertyType": {Kind: "null"},
		"options": {Kind: "map"}, "createStatement": scalar("string", "CREATE "+strings.ToUpper(kind)+" "+name),
	}
	if owner != "" {
		if kind == "index" {
			values["owningConstraint"] = scalar("string", owner)
		} else {
			values["ownedIndex"] = scalar("string", owner)
		}
	}
	row := make([]contract.GraphValue, len(r.Columns))
	for i, key := range r.Columns {
		v, ok := values[key]
		if !ok {
			v = scalar("null", "")
		}
		row[i] = v
	}
	r.Rows = append(r.Rows, row)
}

func TestCompleteCatalogPreservesDefinitionsRejectsIncompleteAndIgnoresOnlyOperationalFields(t *testing.T) {
	c := catalog()
	addDefinition(&c, "index", "owned", "")
	before, e := Definitions(c)
	if e != nil {
		t.Fatal(e)
	}
	changed := c.Clone()
	for i, key := range changed.Indexes.Columns {
		switch key {
		case "state":
			changed.Indexes.Rows[0][i] = scalar("string", "POPULATING")
		case "readCount":
			changed.Indexes.Rows[0][i] = scalar("integer", "7")
		case "id":
			changed.Indexes.Rows[0][i] = scalar("integer", "9")
		}
	}
	after, e := Definitions(changed)
	if e != nil || !DefinitionEqual(before[0], after[0]) {
		t.Fatal("operational state became a definition revision", e)
	}
	after[0].Options = options(contract.GraphProperty{Name: "x", Value: scalar("integer", "1")})
	if DefinitionEqual(before[0], after[0]) {
		t.Fatal("configuration change ignored")
	}
	for _, mutate := range []func(*contract.GraphSchemaCatalog){
		func(c *contract.GraphSchemaCatalog) { c.Indexes.Truncated = true },
		func(c *contract.GraphSchemaCatalog) { c.Constraints.Completed = false },
		func(c *contract.GraphSchemaCatalog) { c.Indexes.Summary.Database = "other" },
		func(c *contract.GraphSchemaCatalog) { c.Indexes.Columns = c.Indexes.Columns[:12] },
		func(c *contract.GraphSchemaCatalog) { c.Indexes.Columns[1] = "id" },
		func(c *contract.GraphSchemaCatalog) { c.Indexes.Rows[0] = c.Indexes.Rows[0][:2] },
		func(c *contract.GraphSchemaCatalog) { c.Indexes.Rows = append(c.Indexes.Rows, c.Indexes.Rows[0]) },
	} {
		broken := c.Clone()
		mutate(&broken)
		if _, e := Definitions(broken); !errors.Is(e, ErrIncomplete) {
			t.Fatal("incomplete native baseline accepted", e)
		}
	}
	broken := c.Clone()
	broken.Indexes.Rows[0][1].Text = strings.Repeat("x", MaxBytes)
	if _, e := Definitions(broken); !errors.Is(e, ErrBounds) {
		t.Fatal("oversize catalog accepted", e)
	}
}

func TestSchemaCreatePreviewsRetainOrderedQuotedIdentifiersTypedOptionsAndRejectUnsupportedSemantics(t *testing.T) {
	c := catalog()
	draft := contract.GraphSchemaDraft{Action: "create_index", Name: "索引`name", Type: "FULLTEXT", EntityType: "RELATIONSHIP", LabelsOrTypes: []string{"A`label", "B"}, Properties: []string{"title", "body"}, Options: options(contract.GraphProperty{Name: "fulltext.analyzer", Value: scalar("string", "standard-no-stop-words")}, contract.GraphProperty{Name: "fulltext.eventually_consistent", Value: scalar("boolean", "true")})}
	p, e := Prepare(c, draft)
	if e != nil || p.Request.Query != "CREATE FULLTEXT INDEX `索引``name` FOR ()-[r:`A``label`|`B`]-() ON EACH [r.`title`, r.`body`] OPTIONS $options" {
		t.Fatal(p.Request.Query, e)
	}
	params, e := Parameters(p.Request)
	if e != nil || params["options"].(map[string]any)["indexConfig"].(map[string]any)["fulltext.eventually_consistent"] != true {
		t.Fatal("boolean setting flattened", e)
	}
	draft.Properties[0] = "other"
	draft.Options.Properties[0].Value.Properties[0].Value.Text = "other"
	if p.Draft.Properties[0] != "title" || p.Request.Parameters[0].Value.Properties[0].Value.Properties[0].Value.Text != "standard-no-stop-words" {
		t.Fatal("preview aliases caller inputs")
	}
	cloned := p.Clone()
	cloned.Request.Parameters[0].Value.Properties[0].Value.Properties[0].Value.Text = "changed"
	if reflect.DeepEqual(cloned, p) {
		t.Fatal("clone was not independent")
	}
	for _, mutate := range []func(*contract.GraphSchemaDraft){
		func(d *contract.GraphSchemaDraft) { d.Name = `unsafe\u0060` },
		func(d *contract.GraphSchemaDraft) { d.Properties[1] = d.Properties[0] },
		func(d *contract.GraphSchemaDraft) { d.Type = "BTREE" },
		func(d *contract.GraphSchemaDraft) { d.Action = "create_constraint"; d.Type = "NODE KEY" },
		func(d *contract.GraphSchemaDraft) { d.Type = "TEXT" },
		func(d *contract.GraphSchemaDraft) { d.Options.Properties[0].Value.Properties[0].Name = "arbitrary" },
		func(d *contract.GraphSchemaDraft) { d.Options.Kind = "" },
	} {
		d := p.Draft.Clone()
		mutate(&d)
		if _, e := Prepare(c, d); e == nil {
			t.Fatal("unsupported/injected draft accepted", d)
		}
	}
	vector := contract.GraphSchemaDraft{Action: "create_index", Name: "v", Type: "VECTOR", EntityType: "NODE", LabelsOrTypes: []string{"N"}, Properties: []string{"v"}, Options: options(contract.GraphProperty{Name: "vector.dimensions", Value: scalar("integer", "3")}, contract.GraphProperty{Name: "vector.similarity_function", Value: scalar("string", "cosine")})}
	if _, e := Prepare(c, vector); e != nil {
		t.Fatal(e)
	}
	vector.Options.Properties[0].Value.Properties[0].Value.Text = "0"
	if _, e := Prepare(c, vector); e == nil {
		t.Fatal("invalid dimensions accepted")
	}
}

func TestSchemaDropsCaptureBackingDefinitionsAndExplicitNonAtomicBoundary(t *testing.T) {
	c := catalog()
	addDefinition(&c, "index", "backing", "unique")
	addDefinition(&c, "constraint", "unique", "backing")
	if _, e := Prepare(c, contract.GraphSchemaDraft{Action: "drop_index", Name: "backing"}); !errors.Is(e, contract.ErrGraphSchemaBackingIndex) {
		t.Fatal(e)
	}
	p, e := Prepare(c, contract.GraphSchemaDraft{Action: "drop_constraint", Name: "unique"})
	if e != nil || !p.NonAtomicDrop || len(p.Before) != 2 || p.Request.Query != "DROP CONSTRAINT `unique`" {
		t.Fatal("constraint effect omitted or invented CAS", e)
	}
	c.Indexes.Rows = nil
	if _, e := Prepare(c, contract.GraphSchemaDraft{Action: "drop_constraint", Name: "unique"}); !errors.Is(e, contract.ErrGraphSchemaChanged) {
		t.Fatal("unpaired sequential baseline accepted", e)
	}
	if _, e := Prepare(c, contract.GraphSchemaDraft{Action: "create_index", Name: "unique"}); !errors.Is(e, contract.ErrGraphSchemaExists) {
		t.Fatal("constraint name collision ignored", e)
	}
	if _, e := Prepare(c, contract.GraphSchemaDraft{Action: "drop_constraint", Name: "absent"}); !errors.Is(e, contract.ErrGraphSchemaChanged) {
		t.Fatal("absent drop became IF EXISTS", e)
	}
}

func TestSchemaBoundsRejectCyclesBeforeCloningAndKeepLookupNullsAndMapOrder(t *testing.T) {
	c := catalog()
	addDefinition(&c, "index", "lookup", "")
	for i, key := range c.Indexes.Columns {
		switch key {
		case "type":
			c.Indexes.Rows[0][i] = scalar("string", "LOOKUP")
		case "labelsOrTypes", "properties":
			c.Indexes.Rows[0][i] = scalar("null", "")
		}
	}
	definitions, e := Definitions(c)
	if e != nil || !definitions[0].LabelsOrTypesNull || !definitions[0].PropertiesNull {
		t.Fatal("native lookup nulls lost", e)
	}
	a, b := definitions[0].Clone(), definitions[0].Clone()
	a.Options = contract.GraphValue{Kind: "map", Properties: []contract.GraphProperty{{Name: "a", Value: scalar("integer", "1")}, {Name: "b", Value: scalar("integer", "2")}}}
	b.Options = contract.GraphValue{Kind: "map", Properties: []contract.GraphProperty{a.Options.Properties[1], a.Options.Properties[0]}}
	if !DefinitionEqual(a, b) {
		t.Fatal("native map ordering became definition change")
	}
	b.PropertiesNull = false
	if DefinitionEqual(a, b) {
		t.Fatal("native null and empty list became equivalent")
	}
	cycle := contract.GraphValue{Kind: "map", Properties: make([]contract.GraphProperty, 1)}
	cycle.Properties[0] = contract.GraphProperty{Name: "cycle", Value: cycle}
	d := contract.GraphSchemaDraft{Options: cycle}
	if _, e := Prepare(c, d); !errors.Is(e, ErrBounds) {
		t.Fatal("cyclic draft was marshaled or cloned", e)
	}
	c.Indexes.Rows[0][0] = cycle
	if _, e := Definitions(c); !errors.Is(e, ErrBounds) {
		t.Fatal("cyclic catalog was marshaled or cloned", e)
	}
}
