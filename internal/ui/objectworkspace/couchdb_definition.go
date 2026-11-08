package objectworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// NativeDefinitionKind selects server metadata that has no relational grid.
func NativeDefinitionKind(kind string) bool {
	return kind == "数据库信息" || kind == "设计文档"
}
func (s *State) couchdbDefinition(d *Document) bool {
	return d != nil && (d.object.Kind == "design_document" || d.object.Kind == "database" && NativeDefinitionKind(d.objectKind))
}

// CouchDB definitions retain native JSON; no SQL fields or editing tools are inferred.
func (s *State) couchdbDefinitionView(c *ui.Context, d *Document) {
	p := theme.Colors(c)
	snapshot := s.sessionFor(d.profileID)
	ready := snapshot.Connected && snapshot.ProfileID == d.profileID && !d.loading
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(3, 8).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, d.database+" / "+d.table+" · "+locales.Text("CouchDB 原生定义（只读）")).Grow(1).MinWidth(0).SingleLine()
			if ui.Button(c, locales.Text("刷新")).Label(locales.Text("刷新原生定义")).Disabled(!ready).Clicked() {
				s.Load(d, c.Services().Invalidate, true)
			}
			if d.loading && ui.Button(c, locales.Text("取消加载")).Clicked() {
				s.Cancel(d)
			}
			if ui.Button(c, locales.Text("返回")).Label(locales.Text("返回对象列表")).Clicked() && s.deps.Back != nil {
				s.deps.Back()
			}
		})
		if d.loading {
			ui.Text(c, locales.Text("正在读取原生定义…")).TextColor(p.Muted)
		}
		if d.err != "" {
			ui.Text(c, d.err).TextColor(p.Error)
		}
		if d.data == nil {
			ui.Text(c, locales.Text("没有已加载的原生定义"))
			return
		}
		definitions := d.data.Definitions
		text := d.data.Definition
		if len(definitions) > 0 {
			labels := make([]string, len(definitions))
			for i, v := range definitions {
				kind := v.Kind
				switch kind {
				case "database":
					kind = locales.Text("数据库信息")
				case "design_document":
					kind = locales.Text("设计文档")
				case "view":
					kind = locales.Text("视图")
				}
				labels[i] = kind + " · " + v.Name
			}
			selected := 0
			for i, label := range labels {
				if label == d.nativeDefinitionChoice {
					selected = i
				}
			}
			d.nativeDefinitionChoice = labels[selected]
			ui.Select(c, &d.nativeDefinitionChoice, labels).Label(locales.Text("选择原生定义")).FillWidth()
			for i, label := range labels {
				if label == d.nativeDefinitionChoice {
					text = definitions[i].SQL
					break
				}
			}
		}
		ui.TextArea(c, &text).ReadOnly(true).Label(locales.Text("CouchDB 原生 JSON 定义")).Fill()
	})
}
