package objectworkspace

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestNeo4jGraphDefinitionKeepsQualifiedReadOnlySnapshot(t *testing.T) {
	n := contract.NamespaceRef{ProfileID: 7, Catalog: "neo4j"}
	data := contract.DBObject{Ref: n.Object("label", "Person"), Name: "Person", Kind: "图标签", Engine: "Neo4j", Definition: `{"Columns":["label"],"Rows":[[{"Kind":"string","Text":"Person"}]]}`}
	d := newProgram(7, "neo4j", data)
	if d == nil {
		t.Fatal("native graph definition rejected")
	}
	defer d.Close()
	for _, foreign := range []contract.DBObject{{Name: "Person", Kind: "图标签", Engine: "Neo4j"}, {Ref: n.Object("table", "Person"), Name: "Person", Kind: "图标签", Engine: "Neo4j"}} {
		if newProgram(7, "neo4j", foreign) != nil {
			t.Fatal("unqualified or relational identity accepted")
		}
	}
	changed := data
	changed.Ref.Catalog = "other"
	if d.Update(changed) {
		t.Fatal("definition update crossed database")
	}
	tt := ui.NewTester(d.View, 1280, 800)
	if !tt.HasText("Person") || tt.HasText("SQL SECURITY") {
		t.Fatal("native metadata shown as SQL routine")
	}
	if d.Data().Ref != data.Ref || d.Data().Definition != data.Definition {
		t.Fatal("read-only snapshot narrowed native metadata")
	}
}
