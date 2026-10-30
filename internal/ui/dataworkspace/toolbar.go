package dataworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type mysqlDataToolbarState struct {
	gridOffset       int
	gridSelected     int
	gridLastSelected int
	columnsOpen      bool
	dock             string
	dockHeight       float32
	dockExpanded     bool
	columnSearch     string
	historyGrid      ui.ListState
	historyRow       int
}

func dataToolbarToggle(c *ui.Context, caption, icon string, active, disabled bool) ui.Element {
	e := mysqlDataToolbarButton(c, caption, locales.Text(caption), icon, disabled)
	if active {
		e.Background(theme.Colors(c).ToolbarSelected)
	}
	return e
}

// Toolbar is a single native command row. MyGo preserves command keyboard
// navigation and moves trailing commands into its overflow menu at any width.
func (s *State) mysqlDataToolbar(c *ui.Context, t *Document) {
	page := t.page
	disabled := t.closed || t.loading || t.edit.Busy || t.transfer.Busy
	var profilesButton, fillButton, copyButton ui.Element
	ui.Toolbar(c, func() {
		profilesButton = mysqlDataToolbarButton(c, "表配置文件 ▾", locales.Text("表配置文件"), "design-table", page == nil).
			Menu(func(m *ui.Menu) { s.profilesMenu(c, t, m) })
		ui.Divider(c).Width(1).Height(18)
		if dataTransactionActive(t) {
			if mysqlDataToolbarButton(c, "提交", locales.Text("提交事务"), "check", disabled || dataTransactionBusy(t)).Clicked() {
				s.commitDataTransaction(c.Services(), t)
			}
			if mysqlDataToolbarButton(c, "回滚", locales.Text("回滚事务"), "history", disabled || dataTransactionBusy(t)).Clicked() {
				s.rollbackDataTransaction(c.Services(), t)
			}
		} else if mysqlDataToolbarButton(c, "开始事务", locales.Text("开始事务"), "database", disabled || dataTransactionBusy(t) || !s.session().Connected || page == nil || t.objectKind != "表").Clicked() {
			s.beginDataTransaction(c.Services(), t)
		}
		ui.Divider(c).Width(1).Height(18)
		if dataToolbarToggle(c, "单元格编辑器", "edit", t.toolbar.dock != "" && t.toolbar.dock != "历史" && t.toolbar.dock != "列统计", page == nil).Clicked() {
			if t.toolbar.dock != "" && t.toolbar.dock != "历史" && t.toolbar.dock != "列统计" {
				s.closeDataDock(t)
			} else {
				t.toolbar.dock = "文本"
			}
		}
		if dataToolbarToggle(c, "筛选 & 排序", "catalog-filter", t.rulesExpanded, false).Clicked() {
			t.rulesExpanded = !t.rulesExpanded
		}
		if dataToolbarToggle(c, "列", "table", t.toolbar.columnsOpen, page == nil).Clicked() {
			t.toolbar.columnsOpen = !t.toolbar.columnsOpen
		}
		if dataToolbarToggle(c, "历史", "history", t.toolbar.dock == "历史", false).Clicked() {
			if t.toolbar.dock == "历史" {
				s.closeDataDock(t)
			} else {
				t.toolbar.dock = "历史"
			}
		}
		ui.Divider(c).Width(1).Height(18)
		fillButton = mysqlDataToolbarButton(c, "AI 自动填充 ▾", locales.Text("AI 自动填充"), "star", page == nil).
			Menu(func(m *ui.Menu) { s.dataFillMenu(c, t, m) })
		if dataToolbarToggle(c, "数据分析", "chart", t.tools.analysisOpen, page == nil || disabled).Clicked() {
			if t.tools.analysisOpen {
				s.closeDataAnalysis(t)
			} else {
				s.openAnalysis(t)
			}
		}
		ui.Divider(c).Width(1).Height(18)
		if mysqlDataToolbarButton(c, "导入", locales.Text("导入"), "import", disabled || dataTransactionActive(t) || dataTransactionBusy(t) || t.transfer.Uncertain || page == nil || t.objectKind != "表").Clicked() {
			s.openDataImport(t)
		}
		if mysqlDataToolbarButton(c, "导出", locales.Text("导出"), "export", disabled || dataTransactionActive(t) || dataTransactionBusy(t) || t.transfer.Uncertain || page == nil).Clicked() {
			s.openDataExport(t)
		}
		ui.Divider(c).Width(1).Height(18)
		if mysqlDataToolbarButton(c, "字段信息", locales.Text("查看字段信息"), "info", page == nil).Clicked() {
			s.ShowField(t)
		}
		if mysqlDataToolbarButton(c, "刷新", locales.Text("刷新表数据：")+t.table, "refresh", !s.session().Connected || disabled).Clicked() {
			s.Refresh(c.Services(), t)
		}
		if t.loading && mysqlDataToolbarButton(c, "取消", locales.Text("取消读取数据"), "close", false).Clicked() {
			s.cancelMySQLDataRead(t)
		}
		selected := mysqlDataHasSelectedPreview(t)
		if mysqlDataToolbarButton(c, "复制", locales.Text("复制所选数据预览"), "copy", !selected).Clicked() {
			s.copyMySQLDataSelection(c, t)
		}
		copyButton = ui.Button(c, "▾").Label(locales.Text("复制为")).Height(theme.RowHeight(c, theme.ControlHeight)).Padding(2, 4).Disabled(!selected).Menu(func(m *ui.Menu) { s.mysqlDataCopyAsMenu(c, t, m) })
		count := len(t.filterRules)
		if count == 0 && t.filterMode != "" {
			count = 1
		}
		if mysqlDataToolbarButton(c, "清除", locales.Text("清除筛选"), "catalog-filter", disabled || count == 0).Clicked() {
			s.clearMySQLDataFilter(c.Services(), t)
		}
	}).Label(locales.Text("数据操作")).Height(theme.RowHeight(c, theme.CommandBarHeight)).FillWidth().MinWidth(0).Padding(0).Gap(2)
	// A native overflow choice requests an earlier button's menu during the
	// Toolbar's own build. Service that request once more before MyGo resolves
	// unhandled menu requests at the end of this frame.
	profilesButton.Menu(func(m *ui.Menu) { s.profilesMenu(c, t, m) })
	fillButton.Menu(func(m *ui.Menu) { s.dataFillMenu(c, t, m) })
	copyButton.Menu(func(m *ui.Menu) { s.mysqlDataCopyAsMenu(c, t, m) })
}
