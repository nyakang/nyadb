package objectworkspace

import (
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"testing"
)

func TestNativeMappingInspectorDoesNotInventRelationalDefaults(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		s := New(Dependencies{})
		d := s.OpenQualifiedStructure(1, contract.NamespaceRef{ProfileID: 7, Catalog: "uuid"}.Object("index", "records"), "索引")
		d.data = &contract.TableStructure{Object: d.object, Engine: "Elasticsearch", Columns: []contract.StructureColumn{{Name: "nested.n", Type: "long", Nullable: true}}}
		tt := ui.NewTester(func(c *ui.Context) { s.InspectorView(c, d) }, 300, 500)
		if !tt.HasText("nested.n") || !tt.HasText("long") || tt.HasText("SQL NULL / 未设置") || tt.HasText("SQL NULL / not set") || tt.HasText("不是 NULL") || tt.HasText("Not NULL") {
			t.Fatal("mapping inspector fabricated SQL attributes", language)
		}
		s.Close(d)
	}
}
