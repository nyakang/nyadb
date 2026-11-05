package objectworkspace

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) dictionaryModal(c *ui.Context) {
	x := s.dictionary
	if x == nil || !x.open {
		return
	}
	theme.Modal(c, &x.open, func() {
		ui.Column(c).Width(1120).MaxWidthPercent(95).Height(700).MaxHeightPercent(92).Gap(8).Children(func() {
			ui.Text(c, locales.Text("数据字典")).Bold()
			scope := fmt.Sprintf("#%d · %s / %s", x.scope.namespace.ProfileID, x.scope.namespace.Catalog, x.scope.namespace.Schema)
			if x.scope.object.Name != "" {
				scope += " · " + x.scope.object.Name
			}
			ui.Text(c, scope).SingleLine().Tooltip(scope)
			ui.Text(c, locales.Text("只读取表和视图的结构；多对象读取不保证同一时刻的快照。")).SingleLine().Tooltip(locales.Text("只读取表和视图的结构；多对象读取不保证同一时刻的快照。")).FontSize(theme.Font(c, 11))
			ui.Row(c).Gap(8).Children(func() {
				if ui.TextInput(c, &x.filter).Label(locales.Text("搜索字典表名")).Grow(1).Changed() {
					x.row = 0
					x.tables = ui.ListState{}
				}
				if ui.Button(c, locales.Text("读取字典")).Disabled(x.busy).Clicked() {
					s.readDictionary(c.Services())
				}
			})
			if x.err != "" {
				ui.Text(c, x.err).SingleLine().Tooltip(x.err).TextColor(theme.Colors(c).Error)
			}
			if x.message != "" {
				ui.Text(c, x.message).SingleLine().Tooltip(x.message)
			}
			if x.busy {
				ui.Row(c).Gap(6).Children(func() { ui.Spinner(c).Width(16).Height(16); ui.Text(c, locales.Text("正在处理字典…")) })
			}
			indices := []int{}
			if x.document != nil {
				for i, t := range x.document.Tables {
					if strings.Contains(strings.ToLower(t.Object.Name), strings.ToLower(x.filter)) {
						indices = append(indices, i)
					}
				}
			}
			if x.row >= len(indices) {
				x.row = len(indices) - 1
			}
			ui.Row(c).Grow(1).MinHeight(80).Gap(8).AlignItems(ui.Stretch).Children(func() {
				x.tables.Selected = &x.row
				theme.Table(c, &x.tables, []ui.TableColumn{{Title: locales.Text("名称"), Width: 170}, {Title: locales.Text("类型"), Width: 65}}, len(indices), func(row, col int) {
					t := x.document.Tables[indices[row]]
					value := t.Object.Name
					if col == 1 {
						value = t.Kind
					}
					ui.Text(c, value).SingleLine().Tooltip(value)
				}).Width(235).MinWidth(120).Label(locales.Text("字典对象列表"))
				ui.Column(c).Grow(1).MinWidth(0).Gap(6).Children(func() {
					if x.document == nil || x.row < 0 || x.row >= len(indices) {
						ui.Text(c, locales.Text("读取字典后选择表或视图。")).TextColor(theme.Colors(c).Muted)
						return
					}
					t := x.document.Tables[indices[x.row]]
					ui.Text(c, t.Object.Name+" · "+t.Engine).Bold().SingleLine().Tooltip(t.Object.Name + " · " + t.Engine)
					if t.Comment != "" {
						ui.Text(c, t.Comment).SingleLine().Tooltip(t.Comment)
					}
					labels := []string{locales.Text("字段"), locales.Text("索引"), locales.Text("外键")}
					tabs := ui.TabsBase(c, &x.page, len(labels))
					tabs.List.Children(func() {
						for i, label := range labels {
							tabs.Tab(i).Padding(4, 10).Children(func() { ui.Text(c, label) })
						}
					})
					l := dictionaryLabels()
					columns := []ui.TableColumn{}
					rows := [][]string{}
					boolean := func(b bool) string {
						if b {
							return l.Yes
						}
						return l.No
					}
					switch x.page {
					case 1:
						columns = []ui.TableColumn{{Title: l.Name, Width: 140}, {Title: l.Kind, Width: 90}, {Title: l.Unique, Width: 60}, {Title: l.Columns, Width: 260}}
						for _, i := range t.Indexes {
							rows = append(rows, []string{i.Name, i.Type, boolean(i.Unique), i.Columns})
						}
					case 2:
						columns = []ui.TableColumn{{Title: l.Name, Width: 140}, {Title: l.Columns, Width: 140}, {Title: l.Reference, Width: 160}, {Title: l.ReferencedColumns, Width: 140}, {Title: l.OnUpdate, Width: 90}, {Title: l.OnDelete, Width: 90}}
						for _, f := range t.ForeignKeys {
							rows = append(rows, []string{f.Name, f.Columns, f.Reference, f.ReferencedColumns, f.OnUpdate, f.OnDelete})
						}
					default:
						columns = []ui.TableColumn{{Title: l.Name, Width: 140}, {Title: l.Type, Width: 140}, {Title: l.Nullable, Width: 80}, {Title: l.Default, Width: 170}, {Title: l.Key, Width: 60}, {Title: l.Generated, Width: 80}, {Title: l.Extra, Width: 140}, {Title: l.Comment, Width: 200}}
						for _, f := range t.Columns {
							value := l.Unreported
							if f.DefaultReported {
								value = f.Default
							}
							rows = append(rows, []string{f.Name, f.Type, boolean(f.Nullable), value, f.Key, boolean(f.Generated), f.Extra, f.Comment})
						}
					}
					theme.Table(c, &x.fields, columns, len(rows), func(row, col int) { v := rows[row][col]; ui.Text(c, v).SingleLine().Tooltip(v) }).Grow(1).MinHeight(50).Label(locales.Text("字典结构预览"))
				})
			})
			ui.Text(c, fmt.Sprintf(locales.Text("字典包含 %d 个对象；导出完整已读字典，搜索仅过滤预览。"), func() int {
				if x.document != nil {
					return len(x.document.Tables)
				}
				return 0
			}())).SingleLine().FontSize(theme.Font(c, 11))
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				format := "Markdown"
				if x.format == "html" {
					format = "HTML"
				}
				if ui.Select(c, &format, []string{"Markdown", "HTML"}).Label(locales.Text("字典导出格式")).Disabled(x.busy).Width(140).Changed() {
					x.format = "markdown"
					if format == "HTML" {
						x.format = "html"
					}
				}
				if ui.PrimaryButton(c, locales.Text("导出字典…")).Disabled(x.busy || x.document == nil || s.dictionarySave == nil).Clicked() {
					s.chooseDictionaryFile(c.Services())
				}
				ui.Spacer(c).Grow(1)
				if ui.Button(c, locales.Text("取消读取")).Disabled(!x.busy).Clicked() {
					s.cancelDictionary()
				}
				if ui.Button(c, locales.Text("关闭")).Clicked() {
					s.closeDictionary()
				}
			})
		})
	})
	if !x.open {
		s.closeDictionary()
	}
}
