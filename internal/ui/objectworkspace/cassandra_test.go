package objectworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"testing"
)

func TestCassandraStructureNativeRolesAndDefinitions(t *testing.T) {
	s := New(Dependencies{Engine: func(int64) string { return "cassandra" }, SessionFor: func(id int64) Session { return Session{ProfileID: id, Connected: true} }})
	defer s.CloseAll()
	d := s.OpenQualifiedStructure(1, contract.NamespaceRef{ProfileID: 7, Catalog: "MixedCase"}.Object("table", "items"), "表")
	d.data = &contract.TableStructure{Object: d.object, Engine: "Cassandra", Columns: []contract.StructureColumn{{Name: "id", Type: "int", Key: "partition_key", Extra: `{"kind":{"type":"text","value":"partition_key"},"position":{"type":"int","value":"0"}}`}, {Name: "note", Type: "text", Extra: `{"kind":{"type":"text","value":"static"},"position":{"type":"int","value":"-1"}}`}, {Name: "ord", Type: "int", Key: "clustering", Extra: `{"kind":{"type":"text","value":"clustering"},"position":{"type":"int","value":"0"},"clustering_order":{"type":"text","value":"desc"}}`}}, Indexes: []contract.StructureIndex{{Name: "note_idx", Type: "CUSTOM"}}, Definition: `{"id":"native-uuid"}`, Definitions: []contract.Definition{{Name: "note_idx", Kind: "index", SQL: `{"options":{"class_name":"StorageAttachedIndex"}}`}}}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.InspectorView(c, d) }, 1100, 760)
	if !tt.HasText("Cassandra 结构（只读）") || !tt.HasText("partition_key") || !tt.HasText("static") || !tt.HasText("desc") || tt.HasText("外键") || tt.HasText("默认值") || tt.HasText("自动递增") {
		t.Fatal("Cassandra inherited SQL decorations")
	}
	role, pos, order := cassandraColumnAttributes(d.data.Columns[2])
	if role != "clustering" || pos != "0" || order != "desc" {
		t.Fatal("native column attributes")
	}
	d.page = 2
	tt.Frame()
	if !tt.HasText("note_idx") || !tt.HasText("CUSTOM") {
		t.Fatal("native indexes lost")
	}
	d.page = 1
	tt.Frame()
	if !tt.HasText("原生定义") || len(d.data.Definitions) != 1 {
		t.Fatal("native definitions lost")
	}
}
