package objectworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// NativeDefinitionKind selects server metadata that has no relational grid.
func NativeDefinitionKind(kind string) bool {
	return kind == "集合信息" || kind == "集合别名" || kind == "数据库信息" || kind == "设计文档" || kind == "索引信息" || kind == "索引设置" || kind == "索引统计"
}
func (s *State) nativeDefinition(d *Document) bool {
	return d != nil && (d.object.Kind == "points" && d.objectKind == "向量点" || d.object.Kind == "design_document" || (d.object.Kind == "collection" || d.object.Kind == "aliases" || d.object.Kind == "database" || d.object.Kind == "index" || d.object.Kind == "settings" || d.object.Kind == "statistics") && NativeDefinitionKind(d.objectKind))
}

// Native definitions retain full JSON; no SQL fields or editing tools are inferred.
func (s *State) nativeDefinitionView(c *ui.Context, d *Document) {
	p := theme.Colors(c)
	snapshot := s.sessionFor(d.profileID)
	ready := snapshot.Connected && snapshot.ProfileID == d.profileID && !d.loading
	title, jsonLabel := locales.Text("CouchDB 原生定义（只读）"), locales.Text("CouchDB 原生 JSON 定义")
	if d.object.Kind == "index" || d.object.Kind == "settings" || d.object.Kind == "statistics" {
		title, jsonLabel = locales.Text("Meilisearch 原生定义（只读）"), locales.Text("Meilisearch 原生 JSON 定义")
	}
	if d.object.Kind == "collection" || d.object.Kind == "aliases" || d.object.Kind == "points" {
		title, jsonLabel = locales.Text("Qdrant 原生定义（只读）"), locales.Text("Qdrant 原生 JSON 定义")
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(3, 8).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, d.database+" / "+d.table+" · "+title).Grow(1).MinWidth(0).SingleLine()
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
				case "points":
					kind = locales.Text("向量点")
				case "collection":
					kind = locales.Text("集合信息")
				case "aliases":
					kind = locales.Text("集合别名")
				case "index":
					kind = locales.Text("索引信息")
				case "settings":
					kind = locales.Text("索引设置")
				case "statistics":
					kind = locales.Text("索引统计")
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
		ui.TextArea(c, &text).ReadOnly(true).Label(jsonLabel).Fill()
	})
}
