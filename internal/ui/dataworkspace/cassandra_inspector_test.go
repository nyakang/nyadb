package dataworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"testing"
)

func TestCassandraFieldInspectorUsesNativeRolesAndNull(t *testing.T) {
	selection := &Selection{Row: 0, Column: "id", Page: &contract.DataPage{Columns: []string{"id"}, ColumnKinds: []string{"int"}, ColumnMetadata: []contract.StructureColumn{{Name: "id", Type: "int", Key: "partition_key", Extra: `{"kind":{"type":"text","value":"partition_key"},"position":{"type":"int","value":"0"},"clustering_order":{"type":"text","value":"none"}}`}}, Rows: [][]contract.DataCell{{{Null: true}}}}}
	s, p := testInspector(selection)
	p.info.info = &contract.TableInfo{Engine: "Cassandra"}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.FieldView(c, p) }, 340, 760)
	if !tt.HasText("partition_key") || !tt.HasText("原生 NULL") || tt.HasText("SQL NULL") || tt.HasText("默认值") || tt.HasText("自动递增") {
		t.Fatal("Cassandra field inherited SQL metadata", tt.Texts())
	}
}
