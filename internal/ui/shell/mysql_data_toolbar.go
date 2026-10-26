package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func mysqlDataToolbarButton(c *ui.Context, caption, label, icon string, disabled bool) ui.Element {
	return ui.ButtonBase(c).Label(label).Tooltip(label).Disabled(disabled).
		Height(theme.ControlHeight).Padding(2, 5).Radius(theme.ControlRadius).
		Children(func() {
			ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
				theme.Icon(c, icon, theme.SmallIconSize, theme.Blue)
				ui.Text(c, locales.Text(caption)).SingleLine().FontSize(theme.FontSize)
			})
		})
}

func (s *State) mysqlDataCompactActions(c *ui.Context, t *tab) {
	page := t.dataPage
	mysqlDataToolbarButton(c, "列", locales.Text("列设置"), "table", page == nil).
		Menu(func(m *ui.Menu) {
			if page != nil {
				s.mysqlDataColumnsMenu(c, t, page, m)
			}
		})
	if mysqlDataToolbarButton(c, "字段信息", locales.Text("查看字段信息"), "info", page == nil).Clicked() {
		s.showMySQLFieldInspector(t)
	}
	ui.Divider(c).Width(1).Height(18)
	if mysqlDataToolbarButton(c, "刷新", locales.Text("刷新表数据：")+t.dataTable, "refresh", s.mysqlSessionStatus != "已连接" || t.dataLoading).Clicked() {
		s.resetAndReloadMySQLData(c.Services(), t)
	}
	if t.dataLoading && mysqlDataToolbarButton(c, "取消", locales.Text("取消读取数据"), "close", false).Clicked() {
		s.cancelMySQLDataRead(t)
	}
	_, cell, selected := mysqlSelectedDataCell(t)
	if mysqlDataToolbarButton(c, "复制", locales.Text("复制所选单元格预览"), "copy", !selected).Clicked() {
		c.WriteClipboard(mysqlDataCopyPreview(cell))
	}
	count := len(t.dataFilterRules)
	if count == 0 && t.dataFilterMode != "" {
		count = 1
	}
	if mysqlDataToolbarButton(c, "清除", locales.Text("清除筛选"), "catalog-filter", t.dataLoading || count == 0).Clicked() {
		s.clearMySQLDataFilter(c.Services(), t)
	}
}
