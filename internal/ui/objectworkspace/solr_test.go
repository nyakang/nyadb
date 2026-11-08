package objectworkspace

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestSolrStructuresKeepNativeSchemaAndDefinitions(t *testing.T) {
	s := New(Dependencies{Engine: func(int64) string { return "solr" }, SessionFor: func(id int64) Session { return Session{ProfileID: id, Connected: true} }})
	defer s.CloseAll()
	for _, kind := range []string{"core", "collection", "alias"} {
		d := s.OpenQualifiedStructure(1, contract.NamespaceRef{ProfileID: 7, Catalog: "observation"}.Object(kind, "records"), kind)
		d.data = &contract.TableStructure{Object: d.object, Engine: "Solr", Columns: []contract.StructureColumn{{Name: "member.id", Type: "string", Extra: `{"stored":true}`}}, Definitions: []contract.Definition{{Name: "member", Kind: "schema", SQL: `{"uniqueKey":"id"}`}, {Name: "member", Kind: "config", SQL: `{"updateRequestProcessorChain":[]}`}}}
		tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.InspectorView(c, d) }, 1100, 760)
		if !tt.HasText("Schema（只读）") || !tt.HasText("member.id") || tt.HasText("外键") || tt.HasText("默认值") {
			t.Fatal("Solr schema inherited relational presentation", kind)
		}
		d.page = 1
		tt.Frame()
		if !tt.HasText("原生定义") || len(d.data.Definitions) != 2 {
			t.Fatal("complete native definitions lost", kind)
		}
		s.Close(d)
	}
}

func TestSolrObjectSearchKeepsNativeKindsAndQualifiedSchemaFields(t *testing.T) {
	ns := contract.Namespace{Ref: contract.NamespaceRef{ProfileID: 7, Catalog: "solr-observation:original"}, Label: "Solr · solrcloud"}
	for _, target := range []struct{ kind, caption string }{{"core", "Core"}, {"collection", "集合"}, {"alias", "别名"}} {
		ref := ns.Ref.Object(target.kind, "records")
		called := false
		result := runObjectSearch(context.Background(), ObjectSearchRuntime{
			Namespaces: func(context.Context) ([]contract.Namespace, error) { return []contract.Namespace{ns}, nil },
			Browse: func(context.Context, contract.NamespaceRef) ([]contract.DBObject, error) {
				return []contract.DBObject{{Ref: ref, Name: ref.Name, Kind: target.caption}}, nil
			},
			TableMetadata: func(_ context.Context, got contract.ObjectRef) (*contract.TableStructure, error) {
				if got != ref {
					t.Fatal("schema search lost qualified native kind", got)
				}
				called = true
				return &contract.TableStructure{Object: ref, Columns: []contract.StructureColumn{{Name: "member.exact_l", Type: "plong"}}}, nil
			},
		}, objectSearchRequest{profile: 7, query: "exact_l", kind: target.caption, fields: true})
		if result.err != nil || !called || len(result.entries) != 1 || result.entries[0].Object.Ref != ref || result.entries[0].Field != "字段" {
			t.Fatal("native schema field search", target.kind, result.err)
		}
	}
}
