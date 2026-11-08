package objectworkspace

import (
	"encoding/json"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) cassandraStructure(d *Document) bool {
	return d != nil && (d.data != nil && d.data.Engine == "Cassandra" || s.deps.Engine != nil && s.deps.Engine(d.profileID) == "cassandra")
}

// Read only the native typed metadata snapshot, without interpreting it as SQL.
func cassandraColumnAttributes(f contract.StructureColumn) (role, position, order string) {
	var attrs map[string]struct {
		Value *string `json:"value"`
	}
	role, position, order = f.Key, "", ""
	if json.Unmarshal([]byte(f.Extra), &attrs) == nil {
		if v := attrs["kind"].Value; v != nil {
			role = *v
		}
		if v := attrs["position"].Value; v != nil && *v != "-1" {
			position = *v
		}
		if v := attrs["clustering_order"].Value; v != nil && *v != "none" {
			order = *v
		}
	}
	return
}

func (s *State) cassandraStructureView(c *ui.Context, d *Document) {
	p := theme.Colors(c)
	snapshot := s.sessionFor(d.profileID)
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(3, 8).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, d.table+" · "+locales.Text("Cassandra 结构（只读）")).Grow(1).SingleLine()
			if ui.Button(c, locales.Text("刷新")).Disabled(!snapshot.Connected || snapshot.ProfileID != d.profileID || d.loading).Clicked() {
				s.Load(d, c.Services().Invalidate, true)
			}
			if d.loading && ui.Button(c, locales.Text("取消加载")).Clicked() {
				s.Cancel(d)
			}
			if ui.Button(c, locales.Text("返回")).Label(locales.Text("返回对象列表")).Clicked() && s.deps.Back != nil {
				s.deps.Back()
			}
		})
		ui.Row(c).Children(func() {
			for i, name := range []string{"字段", "原生定义", "索引"} {
				if ui.Button(c, locales.Text(name)).Clicked() {
					d.page = i
				}
			}
		})
		if d.loading {
			ui.Text(c, locales.Text("正在读取 Cassandra 结构…")).TextColor(p.Muted)
		}
		if d.err != "" {
			ui.Text(c, d.err).TextColor(p.Error)
		}
		if d.page == 1 {
			structureDefinitionsView(c, d.data)
			return
		}
		if d.page == 2 {
			cols := []ui.TableColumn{{ID: "index", Title: locales.Text("名称"), Width: 300}, {ID: "kind", Title: locales.Text("类型"), Width: 180}}
			if d.data == nil || len(d.data.Indexes) == 0 {
				theme.EmptyTable(c, &d.indexes, cols, locales.Text("没有已加载的索引"))
				return
			}
			theme.Table(c, &d.indexes, cols, len(d.data.Indexes), func(row, col int) {
				v := d.data.Indexes[row].Name
				if col == 1 {
					v = d.data.Indexes[row].Type
				}
				ui.Text(c, v).SingleLine().Tooltip(v)
			}).Grow(1)
			return
		}
		cols := []ui.TableColumn{{ID: "name", Title: locales.Text("字段"), Width: 200}, {ID: "type", Title: locales.Text("类型"), Width: 220}, {ID: "role", Title: locales.Text("列角色"), Width: 150}, {ID: "position", Title: locales.Text("键位置"), Width: 80}, {ID: "order", Title: locales.Text("聚簇顺序"), Width: 100}}
		if d.data == nil || len(d.data.Columns) == 0 {
			theme.EmptyTable(c, &d.columns, cols, locales.Text("没有已加载的字段"))
			return
		}
		d.columns.Selected = &d.fieldRow
		theme.Table(c, &d.columns, cols, len(d.data.Columns), func(row, col int) {
			f := d.data.Columns[row]
			role, pos, order := cassandraColumnAttributes(f)
			v := []string{f.Name, f.Type, role, pos, order}[col]
			ui.Text(c, v).SingleLine().Tooltip(v)
		}).Grow(1)
	})
}
