package shell

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// A real read-only MySQL page, never the legacy simulated dataPreview.
func (s *State) mysqlDataWorkspace(c *ui.Context, t *tab) {
	ui.Column(c).Fill().Background(theme.App).Children(func() {
		page := t.dataPage
		if page == nil {
			page = &connections.MySQLDataPage{}
			for _, col := range t.dataColumns {
				page.Columns = append(page.Columns, col.ID)
			}
		}
		s.mysqlDataRuleEditor(c, t, page)
		if t.dataLoading {
			ui.Row(c).Height(theme.ControlHeight).Padding(2, 6).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Width(16).Height(16)
				ui.Text(c, locales.Text("正在读取真实 MySQL 数据…")).TextColor(theme.Muted).FontSize(theme.FontSize).SingleLine().Grow(1).MinWidth(0)
			})
		}
		if t.dataErr != "" {
			ui.Row(c).Height(theme.CommandBarHeight).Padding(2, 6).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.Text("读取数据失败：")+t.dataErr).TextColor(theme.Gold).FontSize(theme.FontSize).SingleLine().Grow(1).MinWidth(0).Tooltip(t.dataErr)
				if ui.Button(c, locales.Text("重试读取数据")).Height(theme.ControlHeight).Disabled(t.dataLoading || s.mysqlSessionStatus != "已连接").Clicked() {
					s.loadMySQLData(c.Services(), t, true)
				}
			})
		}
		if t.dataPage == nil {
			emptyTableView(c, &t.dataState, t.dataColumns, locales.Text("没有已加载的数据页"))
		} else if len(page.Columns) == 0 {
			emptyTableView(c, &t.dataState, nil, locales.Text("此对象没有可见字段"))
		} else {
			if !page.StableOrder {
				ui.Text(c, locales.Text("此对象没有完整非空唯一键，OFFSET 分页期间数据变化可能导致行重复或遗漏")).
					Padding(2, 6).FontSize(theme.SmallFontSize).TextColor(theme.Gold).SingleLine().Tooltip(locales.Text("此对象没有完整非空唯一键，OFFSET 分页期间数据变化可能导致行重复或遗漏"))
			}
			visibleIndices, visibleColumns := mysqlVisibleDataColumns(t, page)
			columns := append([]ui.TableColumn{{ID: "\x1fnyadb:row", Title: "#", Width: 42, MinWidth: 42, Fixed: true, RowHeader: true}}, visibleColumns...)
			t.dataState.Selected = &t.dataSelectedRow
			t.dataState.Selection = &t.dataSelection
			t.dataState.CellSelect = true
			t.dataState.Sort = &t.dataSort
			t.dataState.HeaderMenu = func(m *ui.Menu, col int) {
				if col > 0 {
					s.mysqlDataHeaderMenu(c, t, page, visibleIndices, m, col-1)
				}
			}
			t.dataState.CellMenu = func(m *ui.Menu, row, col int) {
				col--
				if row >= 0 && row < len(page.Rows) && col >= 0 && col < len(visibleIndices) {
					source := visibleIndices[col]
					if source < len(page.Rows[row]) && m.Item(locales.Text("复制单元格预览")).Chosen() {
						c.WriteClipboard(mysqlDataCopyPreview(page.Rows[row][source]))
					}
				}
			}
			if len(page.Rows) == 0 {
				emptyTableView(c, &t.dataState, columns, "")
			} else {
				ui.Table(c, &t.dataState, columns, len(page.Rows), func(row, col int) {
					if col == 0 {
						ui.Text(c, fmt.Sprint(t.dataOffset+int64(row)+1)).FontSize(theme.SmallFontSize).TextColor(theme.Muted)
						return
					}
					col--
					if col < 0 || col >= len(visibleIndices) {
						return
					}
					col = visibleIndices[col]
					if row >= len(page.Rows) || col >= len(page.Rows[row]) {
						return
					}
					cell := page.Rows[row][col]
					if cell.Null {
						ui.Text(c, "NULL").TextColor(theme.Muted).FontSize(12)
						return
					}
					value := cell.Text
					if value == "" {
						value = "\"\""
					}
					if cell.Truncated {
						value += " …"
					}
					ui.Text(c, value).SingleLine().FontSize(12)
				}).Grow(1)
			}
			s.syncMySQLDataSort(c.Services(), t)
		}
		ui.Row(c).Height(theme.CommandBarHeight).Padding(2, 6).Gap(6).AlignItems(ui.Center).
			Background(theme.Header).Children(func() {
			ui.Text(c, mysqlDataPageStatus(t)).
				FontSize(theme.SmallFontSize).TextColor(theme.Muted).SingleLine().Grow(1).MinWidth(0)
			if ui.Button(c, locales.Text("上一页")).Label(locales.Text("上一页数据")).
				Height(theme.ControlHeight).Disabled(t.dataLoading || t.dataPage == nil || t.dataOffset <= 0).Clicked() {
				offset := t.dataOffset - int64(t.dataLimit)
				if offset < 0 {
					offset = 0
				}
				s.changeMySQLDataPage(c.Services(), t, offset)
			}
			if ui.Button(c, locales.Text("下一页")).Label(locales.Text("下一页数据")).
				Height(theme.ControlHeight).Disabled(t.dataLoading || t.dataPage == nil || !page.HasMore).Clicked() {
				s.changeMySQLDataPage(c.Services(), t, t.dataOffset+int64(t.dataLimit))
			}
		})
	})
}
