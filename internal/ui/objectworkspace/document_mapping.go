package objectworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) nativeSchemaStructure(d *Document) bool {
	return d != nil && (d.object.Kind == "core" || d.data != nil && d.data.Engine == "Solr" || s.deps.Engine != nil && s.deps.Engine(d.profileID) == "solr")
}

func (s *State) documentStructure(d *Document) bool {
	return d != nil && (s.cassandraStructure(d) || s.nativeSchemaStructure(d) || d.object.Kind == "index" || d.object.Kind == "alias" || d.object.Kind == "data_stream")
}

// Document mappings keep native definitions and field paths. They do not
// inherit relational index, foreign-key, default or nullability presentation.
func (s *State) documentMappingView(c *ui.Context, d *Document) {
	p := theme.Colors(c)
	snapshot := s.sessionFor(d.profileID)
	ready := snapshot.Connected && snapshot.ProfileID == d.profileID && !d.loading
	title, loading, fieldType, empty := "映射（只读）", "正在读取映射…", "映射类型", "没有已加载的映射字段"
	if s.nativeSchemaStructure(d) {
		title, loading, fieldType, empty = "Schema（只读）", "正在读取 Schema…", "Schema 字段类型", "没有已加载的 Schema 字段"
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(3, 8).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, d.table+" · "+locales.Text(title)).Grow(1).SingleLine()
			if ui.Button(c, locales.Text("刷新")).Disabled(!ready).Clicked() {
				s.Load(d, c.Services().Invalidate, true)
			}
			if d.loading && ui.Button(c, locales.Text("取消加载")).Clicked() {
				s.Cancel(d)
			}
			if ui.Button(c, locales.Text("返回")).Label(locales.Text("返回对象列表")).Clicked() && s.deps.Back != nil {
				s.deps.Back()
			}
		})
		names := []string{locales.Text("字段"), locales.Text("原生定义")}
		ui.Row(c).Children(func() {
			for i, name := range names {
				if ui.Button(c, name).Clicked() {
					d.page = i
				}
			}
		})
		if d.loading {
			ui.Text(c, locales.Text(loading)).TextColor(p.Muted)
		}
		if d.err != "" {
			ui.Text(c, d.err).TextColor(p.Error)
		}
		if d.page == 1 {
			structureDefinitionsView(c, d.data)
			return
		}
		cols := []ui.TableColumn{{ID: "name", Title: locales.Text("字段"), Width: 300}, {ID: "type", Title: locales.Text(fieldType), Width: 160}, {ID: "extra", Title: locales.Text("附加属性"), Width: 240}}
		if d.data == nil || len(d.data.Columns) == 0 {
			theme.EmptyTable(c, &d.columns, cols, locales.Text(empty))
			return
		}
		theme.Table(c, &d.columns, cols, len(d.data.Columns), func(row, col int) {
			f := d.data.Columns[row]
			v := f.Name
			if col == 1 {
				v = f.Type
			}
			if col == 2 {
				v = f.Extra
			}
			ui.Text(c, v).SingleLine().Tooltip(v)
		}).Grow(1)
	})
}
