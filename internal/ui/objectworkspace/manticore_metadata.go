package objectworkspace

import (
	"encoding/json"
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) manticoreStructure(d *Document) bool {
	if d == nil {
		return false
	}
	if s.deps.Engine != nil && s.deps.Engine(d.profileID) == "manticoresearch" {
		return true
	}
	if d.data != nil {
		for _, definition := range d.data.Definitions {
			if definition.Kind == "manticore_indexes" {
				return true
			}
		}
	}
	return false
}

func manticoreIndexes(data *contract.TableStructure) (contract.NativeMetadataObservation, bool) {
	if data != nil {
		for _, definition := range data.Definitions {
			if definition.Kind == "manticore_indexes" {
				var observation contract.NativeMetadataObservation
				if json.Unmarshal([]byte(definition.SQL), &observation) == nil && observation.Command != "" {
					return observation, true
				}
			}
		}
	}
	return contract.NativeMetadataObservation{}, false
}

// Manticore describes fields and search indexes without relational null/key
// metadata. Render those original observations without synthesizing SQL flags.
func (s *State) manticoreStructureView(c *ui.Context, d *Document) {
	p := theme.Colors(c)
	session := s.sessionFor(d.profileID)
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(3, 8).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, d.database+" / "+d.table+" · "+locales.Text("Manticore 原生结构（只读）")).Grow(1).MinWidth(0).SingleLine()
			if ui.Button(c, locales.Text("刷新")).Label("刷新表结构：" + d.table).Disabled(!session.Connected || session.ProfileID != d.profileID || d.loading).Clicked() {
				s.Load(d, c.Services().Invalidate, true)
			}
			if d.loading && ui.Button(c, locales.Text("取消加载")).Clicked() {
				s.Cancel(d)
			}
			if ui.Button(c, locales.Text("返回")).Label(locales.Text("返回对象列表")).Clicked() && s.deps.Back != nil {
				s.deps.Back()
			}
		})
		ui.Row(c).Gap(4).Children(func() {
			for i, label := range []string{"字段", "索引", "原生定义"} {
				if ui.Button(c, locales.Text(label)).Clicked() {
					d.page = i
				}
			}
		})
		if d.loading {
			ui.Text(c, locales.Text("正在读取表结构…")).TextColor(p.Muted)
		}
		if d.err != "" {
			ui.Text(c, d.err).TextColor(p.Error)
		}
		if d.data == nil {
			ui.Text(c, locales.Text("没有已加载的表结构"))
			return
		}
		switch d.page {
		case 1:
			observation, ok := manticoreIndexes(d.data)
			if !ok {
				ui.Text(c, locales.Text("没有已加载的索引"))
				return
			}
			ui.Text(c, observation.Command).TextColor(p.Muted)
			if observation.ErrorCode != 0 {
				ui.Text(c, locales.Text("服务器拒绝索引查询；这不表示索引为空。")).TextColor(p.Error)
				ui.Text(c, fmt.Sprintf("[%d / %s] %s", observation.ErrorCode, observation.SQLState, observation.Error)).TextColor(p.Error)
				return
			}
			cols := []ui.TableColumn{{ID: "name", Title: locales.Text("名称"), Width: 240}, {ID: "type", Title: locales.Text("类型"), Width: 160}, {ID: "enabled", Title: locales.Text("已启用"), Width: 120}, {ID: "percent", Title: locales.Text("覆盖率"), Width: 120}}
			ui.Text(c, locales.Text("原生搜索索引状态不声明 SQL 主键或唯一约束。")).TextColor(p.Muted)
			if len(observation.Result.Rows) == 0 {
				theme.EmptyTable(c, &d.indexes, cols, locales.Text("服务器返回空索引列表"))
				return
			}
			theme.Table(c, &d.indexes, cols, len(observation.Result.Rows), func(row, col int) {
				value := ""
				if col < len(observation.Result.Rows[row]) {
					value = observation.Result.Rows[row][col].Display()
				}
				ui.Text(c, value).SingleLine().Tooltip(value)
			}).Grow(1)
		case 2:
			ui.ScrollBoth(c).Fill().Children(func() {
				ui.Column(c).Padding(8).Gap(3).Children(func() {
					for _, line := range definitionLines(d.data.Definition) {
						ui.Text(c, line)
					}
				})
			})
		default:
			cols := []ui.TableColumn{{ID: "name", Title: locales.Text("字段"), Width: 240}, {ID: "type", Title: locales.Text("类型"), Width: 180}, {ID: "properties", Title: locales.Text("原生属性"), Width: 360}}
			if len(d.data.Columns) == 0 {
				theme.EmptyTable(c, &d.columns, cols, locales.Text("没有已加载的字段"))
				return
			}
			d.columns.Selected = &d.fieldRow
			theme.Table(c, &d.columns, cols, len(d.data.Columns), func(row, col int) {
				f := d.data.Columns[row]
				value := []string{f.Name, f.Type, f.Extra}[col]
				ui.Text(c, value).SingleLine().Tooltip(value)
			}).Grow(1)
		}
	})
}

func (d *programDocument) manticoreFunctionView(c *ui.Context) {
	p := theme.Colors(c)
	ui.Column(c).Fill().Padding(8).Gap(6).Children(func() {
		ui.Text(c, d.database+" / "+d.name+" · "+locales.Text("Manticore 原生函数元数据（只读）")).SingleLine()
		ui.Text(c, locales.Text("SHOW PLUGINS 返回的函数元数据；服务器未提供函数实现。")).TextColor(p.Muted)
		var observation contract.NativeMetadataObservation
		if json.Unmarshal([]byte(d.data.Definition), &observation) != nil {
			ui.Text(c, locales.Text("没有已加载的原生定义"))
			return
		}
		ui.ScrollBoth(c).Fill().Children(func() {
			ui.Column(c).Gap(5).Children(func() {
				for _, row := range observation.Result.Rows {
					for i, cell := range row {
						if i >= len(observation.Result.Columns) {
							break
						}
						label, value := observation.Result.Columns[i].Name, cell.Display()
						ui.Row(c).Gap(12).Children(func() { ui.Text(c, label).Width(115).TextColor(p.Muted); ui.Text(c, value).Grow(1) })
					}
				}
				raw, _ := json.MarshalIndent(observation, "", "  ")
				for _, line := range definitionLines(string(raw)) {
					ui.Text(c, line)
				}
			})
		})
	})
}
