package shell

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// A real read-only MySQL page, never the legacy simulated dataPreview.
func (s *State) mysqlDataWorkspace(c *ui.Context, t *tab) {
	ui.Column(c).Fill().Background(theme.App).Children(func() {
		ui.Row(c).Height(35).Padding(4, 9).AlignItems(ui.Center).Gap(9).
			Background(theme.Header).Children(func() {
			theme.Icon(c, "table", 16, theme.Blue)
			ui.Text(c, t.dataDatabase+" / "+t.dataTable).Bold().FontSize(13).SingleLine()
			ui.Text(c, "只读数据").TextColor(theme.Muted).FontSize(11)
			ui.Box(c).Grow(1)
			if ui.Button(c, "返回对象列表").Clicked() {
				s.activeTab = 0
			}
			if ui.Button(c, "刷新数据").Label("刷新表数据：" + t.dataTable).
				Disabled(s.mysqlSessionStatus != "已连接" || t.dataLoading).Clicked() {
				s.resetAndReloadMySQLData(c.Services(), t)
			}
			if _, cell, ok := mysqlSelectedDataCell(t); ui.Button(c, "复制预览").Label("复制所选单元格预览").Disabled(!ok).Clicked() {
				c.WriteClipboard(mysqlDataCopyPreview(cell))
			}
			if t.dataLoading && ui.Button(c, "取消读取数据").Clicked() {
				s.cancelMySQLData(t)
				t.dataErr = "查询已取消"
			}
		})
		if t.dataLoading {
			ui.Row(c).Height(29).Padding(5, 10).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Width(16).Height(16)
				ui.Text(c, "正在读取真实 MySQL 数据…").TextColor(theme.Muted).FontSize(12)
			})
		}
		if t.dataErr != "" {
			ui.Row(c).Padding(8, 10).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "读取数据失败："+t.dataErr).TextColor(theme.Gold).FontSize(12)
				if ui.Button(c, "重试读取数据").Disabled(t.dataLoading || s.mysqlSessionStatus != "已连接").Clicked() {
					s.loadMySQLData(c.Services(), t, true)
				}
			})
		}
		if t.dataPage == nil {
			// Retain the rule editor after an invalid server query.
			if len(t.dataColumns) > 0 {
				names := make([]string, len(t.dataColumns))
				for i, col := range t.dataColumns {
					names[i] = col.ID
				}
				s.mysqlDataRuleEditor(c, t, &connections.MySQLDataPage{Columns: names})
			}
			if !t.dataLoading && t.dataErr == "" {
				ui.Text(c, "没有已加载的数据页").Padding(12).TextColor(theme.Muted)
			}
			ui.Box(c).Grow(1)
			return
		}
		page := t.dataPage
		s.mysqlDataRuleEditor(c, t, page)
		if !page.StableOrder {
			ui.Text(c, "此对象没有完整非空唯一键，OFFSET 分页期间数据变化可能导致行重复或遗漏").
				Padding(5, 10).FontSize(11).TextColor(theme.Gold)
		}
		if len(page.Columns) == 0 {
			ui.Text(c, "此对象没有可见字段").Padding(12).TextColor(theme.Muted)
			ui.Box(c).Grow(1)
		} else {
			visibleIndices, visibleColumns := mysqlVisibleDataColumns(t, page)
			t.dataState.Selected = &t.dataSelectedRow
			t.dataState.Selection = &t.dataSelection
			t.dataState.CellSelect = true
			t.dataState.Sort = &t.dataSort
			t.dataState.HeaderMenu = func(m *ui.Menu, col int) {
				s.mysqlDataHeaderMenu(c, t, page, visibleIndices, m, col)
			}
			t.dataState.CellMenu = func(m *ui.Menu, row, col int) {
				if row >= 0 && row < len(page.Rows) && col >= 0 && col < len(visibleIndices) {
					source := visibleIndices[col]
					if source < len(page.Rows[row]) && m.Item("复制单元格预览").Chosen() {
						c.WriteClipboard(mysqlDataCopyPreview(page.Rows[row][source]))
					}
				}
			}
			ui.Table(c, &t.dataState, visibleColumns, len(page.Rows), func(row, col int) {
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
			s.syncMySQLDataSort(c.Services(), t)
		}
		ui.Row(c).Height(34).Padding(4, 10).Gap(8).AlignItems(ui.Center).
			Background(theme.Header).Children(func() {
			ui.Text(c, fmt.Sprintf("已读取 %d 行 · 第 %d 页 · %s", len(page.Rows), t.dataOffset/int64(t.dataLimit)+1, mysqlDataPageMethod(page))).
				FontSize(12).TextColor(theme.Muted)
			ui.Box(c).Grow(1)
			if ui.Button(c, "上一页").Label("上一页数据").
				Disabled(t.dataLoading || t.dataOffset <= 0).Clicked() {
				offset := t.dataOffset - int64(t.dataLimit)
				if offset < 0 {
					offset = 0
				}
				s.changeMySQLDataPage(c.Services(), t, offset)
			}
			if ui.Button(c, "下一页").Label("下一页数据").
				Disabled(t.dataLoading || !page.HasMore).Clicked() {
				s.changeMySQLDataPage(c.Services(), t, t.dataOffset+int64(t.dataLimit))
			}
		})
	})
}
