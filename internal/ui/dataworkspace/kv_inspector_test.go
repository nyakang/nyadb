package dataworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"testing"
)

func TestKVFieldInspectorPreservesNativeNullWithoutRelationalDefaults(t *testing.T) {
	selection := &Selection{Row: 0, Column: "session", Page: &contract.DataPage{
		Columns: []string{"session"}, ColumnKinds: []string{"uuid"}, ColumnMetadata: []contract.StructureColumn{{Name: "session", Type: "uuid", Comment: "original native session UUID"}},
		Rows: [][]contract.DataCell{{{Null: true}}},
	}}
	s, p := testInspector(selection)
	p.runtime = &contract.DataRuntime{Capabilities: contract.Capabilities{KeyValues: true}}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.FieldView(c, p) }, 340, 760)
	if !tt.HasText("uuid") || !tt.HasText("original native session UUID") || !tt.HasText("原生 NULL") {
		t.Fatal("native field evidence missing", tt.Texts())
	}
	for _, label := range []string{"SQL NULL / 未设置", "默认值", "自增"} {
		if tt.HasText(label) {
			t.Fatal("native field inherited relational metadata", label)
		}
	}
}
