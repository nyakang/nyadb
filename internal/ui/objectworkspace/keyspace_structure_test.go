package objectworkspace

import (
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestNativeKeyStructureAndInspectorKeepNativeFieldsWithoutRelationalAttributes(t *testing.T) {
	for _, engine := range []string{"consul", "etcd"} {
		for _, language := range []string{"zh-Hans", "en"} {
			t.Run(engine+"/"+language, func(t *testing.T) {
				i18n.SetLocaleFunc(func() string { return language })
				defer i18n.SetLocaleFunc(nil)
				s := New(Dependencies{Engine: func(int64) string { return engine }, SessionFor: func(id int64) Session { return Session{ProfileID: id, Connected: true} }})
				defer s.CloseAll()
				d := s.OpenStructure(1, 7, "kv", "keys", "键")
				d.data = &contract.TableStructure{Engine: engine, Columns: []contract.StructureColumn{{Name: "key", Type: "text", Key: "PRI"}, {Name: "value", Type: "bytes", Nullable: true}, {Name: "modify_index", Type: "uint64", Generated: true}}}
				tt := ui.NewTester(func(c *ui.Context) {
					theme.Apply(c)
					ui.Row(c).Fill().Children(func() {
						ui.Column(c).Grow(1).Children(func() { s.View(c, d) })
						ui.Column(c).Width(250).Children(func() { s.InspectorView(c, d) })
					})
				}, 1280, 800)
				for _, size := range [][2]int{{1280, 800}, {720, 520}} {
					tt.SetSize(size[0], size[1])
					if err := tt.Click("modify_index"); err != nil {
						t.Fatal(err)
					}
					if field, ok := d.SelectedField(); !ok || field.Name != "modify_index" || !field.Generated {
						t.Fatal("native field selection lost generated identity")
					}
					for _, label := range []string{"允许 NULL", "生成字段", "是", "uint64"} {
						if !tt.HasText(locales.Text(label)) {
							t.Fatal("native field presentation missing", language, size, label)
						}
					}
					for _, label := range []string{"SQL NULL / 未设置", "默认值", "自增", "自动递增", "外键", "索引", "编辑表结构"} {
						if tt.HasText(locales.Text(label)) {
							t.Fatal("native key structure invented relational metadata", language, size, label)
						}
					}
					if rect, ok := tt.Find(locales.Text("返回对象列表")); !ok || rect.X+rect.W > float32(size[0]) {
						t.Fatal("native structure return action left viewport", size, rect)
					}
				}
				if len(d.Data().Columns) != 3 || d.Data().Columns[0].Key != "PRI" {
					t.Fatal("native presentation mutated complete metadata")
				}
			})
		}
	}
}
