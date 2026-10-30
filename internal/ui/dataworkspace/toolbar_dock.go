package dataworkspace

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func mysqlDataEditorDockOpen(t *Document) bool {
	return t.toolbar.dock != "" && t.toolbar.dock != "历史" && t.toolbar.dock != "列统计"
}

func (s *State) closeDataDock(t *Document) {
	if t.edit.Editor != nil && t.edit.Editor.Dedicated {
		s.finishMySQLCellEdit(t, true)
	}
	if t.edit.Editor == nil {
		t.toolbar.dock = ""
	}
}

func (s *State) dataColumnsPanel(c *ui.Context, t *Document) {
	palette := theme.Colors(c)
	ui.Column(c).Width(185).MinWidth(120).Background(palette.Panel).BorderWidth(0, 1, 0, 0).BorderColor(palette.Border).Padding(6).Gap(5).Children(func() {
		ui.Text(c, locales.Text("显示或隐藏列")).SingleLine()
		ui.TextInput(c, &t.toolbar.columnSearch).Label(locales.Text("搜索列")).Placeholder(locales.Text("搜索列")).FillWidth()
		ui.Scroll(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
			for _, name := range t.page.Columns {
				if !strings.Contains(strings.ToLower(name), strings.ToLower(t.toolbar.columnSearch)) {
					continue
				}
				visible := !t.hiddenColumns[name]
				ui.Checkbox(c.Key(name), &visible, name).Label(name).FillWidth()
				if visible == t.hiddenColumns[name] {
					func() {
						if visible {
							delete(t.hiddenColumns, name)
						} else {
							mysqlDataHideColumn(t, t.page, name)
						}
					}()
				}
			}
		})
		ui.Button(c, locales.Text("列设置")).Menu(func(m *ui.Menu) { s.mysqlDataColumnsMenu(c, t, t.page, m) }).FillWidth()
	})
}

func (s *State) dataDock(c *ui.Context, t *Document) {
	palette := theme.Colors(c)
	ui.Column(c).Fill().MinHeight(100).Background(palette.App).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(2, 6).Gap(4).AlignItems(ui.Center).Background(palette.Header).Children(func() {
			tabs := []string{"文本", "JSON", "十六进制", "图像", "历史"}
			if t.tools.analysisOpen && t.tools.analysis != nil {
				tabs = append(tabs, "列统计")
			}
			for _, tab := range tabs {
				if dataToolbarToggle(c, tab, "", t.toolbar.dock == tab, false).Clicked() {
					t.toolbar.dock = tab
				}
			}
			ui.Box(c).Grow(1)
			if ui.Button(c, "⌃").Label(locales.Text("展开编辑器")).Clicked() {
				t.toolbar.dockExpanded = !t.toolbar.dockExpanded
			}
			if ui.Button(c, "×").Label(locales.Text("关闭编辑器")).Clicked() {
				s.closeDataDock(t)
			}
		})
		if t.toolbar.dock == "列统计" {
			s.dataAnalysisDock(c, t)
			return
		}
		if t.toolbar.dock == "历史" {
			s.dataHistoryDock(c, t)
			return
		}
		if t.page == nil || t.selectedRow < 0 || t.selectedRow >= len(t.page.Rows) {
			ui.Text(c, locales.Text("选择一个单元格")).Padding(8)
			return
		}
		source := slices.Index(t.page.Columns, t.grid.Cells.Cursor)
		if source < 0 {
			source = 0
		}
		if source >= len(t.page.Rows[t.selectedRow]) {
			return
		}
		// Opening an editor selects its owning cell. Do that only for a single
		// cell so showing the dock never collapses a row or rectangle selection.
		singleCell := !t.grid.Cells.All && t.grid.Cells.Anchor == t.grid.Cells.Cursor &&
			(t.selection.Len() == 0 || t.selection.Len() == 1 && t.selection.Has(t.selectedRow))
		if singleCell && !t.edit.Busy && mysqlDataCanEdit(t, t.selectedRow, source) {
			e := t.edit.Editor
			if e == nil || e.Row != t.selectedRow || e.Field != t.page.Columns[source] {
				s.beginMySQLCellEdit(c.Services(), t, t.selectedRow, source, false)
			}
		}
		e := t.edit.Editor
		text := t.page.Rows[t.selectedRow][source].Text
		editable := singleCell && e != nil && e.Row == t.selectedRow && e.Field == t.page.Columns[source] && e.Dedicated && !t.edit.Busy
		if editable {
			text = e.Text
		}
		if t.toolbar.dock == "图像" {
			raw, err := hex.DecodeString(strings.TrimPrefix(text, "0x"))
			if err == nil {
				config, _, err := image.DecodeConfig(bytes.NewReader(raw))
				if err == nil && config.Width > 0 && config.Height > 0 && int64(config.Width)*int64(config.Height) <= 8<<20 {
					if bitmap, err := ui.DecodeBitmap(raw); err == nil {
						ui.Image(c, bitmap).Grow(1).FillWidth()
						return
					}
				}
			}
			ui.Text(c, locales.Text("此单元格没有可预览的 PNG 或 JPEG 图像")).Padding(8)
			return
		}
		if t.toolbar.dock == "JSON" {
			if editable && e.Original.Binary {
				text = ""
				editable = false
			}
			if ui.Button(c, locales.Text("格式化 JSON")).Disabled(!editable).Clicked() {
				var pretty bytes.Buffer
				if err := json.Indent(&pretty, []byte(text), "", "  "); err != nil {
					t.edit.Err = locales.Text("JSON 格式无效")
				} else {
					e.Text = pretty.String()
					text = e.Text
				}
			}
		}
		if t.toolbar.dock == "十六进制" && editable && !e.Original.Binary {
			text = hex.EncodeToString([]byte(text))
			editable = false
		}
		if editable {
			ui.TextArea(c, &e.Text).Label(locales.Text("单元格编辑器内容")).FillWidth().Grow(1).MinHeight(50).FontSize(theme.Font(c, 12))
		} else {
			ui.TextArea(c, &text).Label(locales.Text("单元格预览")).ReadOnly(true).FillWidth().Grow(1).MinHeight(50).FontSize(theme.Font(c, 12))
		}
		if t.edit.Err != "" {
			ui.Text(c, t.edit.Err).TextColor(palette.Error).SingleLine().Tooltip(t.edit.Err)
		}
	})
}

func (s *State) dataHistoryDock(c *ui.Context, t *Document) {
	ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(4, 8).Gap(12).Children(func() {
		choices := []string{"all", "SELECT", "INSERT", "UPDATE", "DELETE", "errors"}
		if t.tools.historyFilter == "" {
			t.tools.historyFilter = "all"
		}
		before := t.tools.historyFilter
		mysqlDataRuleSelect(c, &t.tools.historyFilter, choices, func(kind string) string {
			if kind == "all" {
				return locales.Text("全部")
			}
			if kind == "errors" {
				return locales.Text("错误")
			}
			return kind
		}).Label(locales.Text("历史类型")).Width(125)
		if before != t.tools.historyFilter {
			t.toolbar.historyRow = -1
		}
		ui.Box(c).Grow(1)
		ui.Button(c, locales.Text("历史操作")).Menu(func(m *ui.Menu) { s.historyMenu(c, t, m) })
	})
	indices := mysqlDataHistoryVisible(t)
	columns := []ui.TableColumn{{ID: "sql", Title: locales.Text("查询"), Width: 450, MinWidth: 140}, {ID: "result", Title: locales.Text("消息"), Width: 190, MinWidth: 90}, {ID: "at", Title: locales.Text("时间戳"), Width: 145, MinWidth: 100}}
	t.toolbar.historyGrid.Selected = &t.toolbar.historyRow
	ui.Table(c, &t.toolbar.historyGrid, columns, len(indices), func(row, col int) {
		e := t.tools.history[indices[row]]
		value := e.SQL
		if col == 1 {
			value = mysqlDataHistoryMessage(e)
		}
		if col == 2 {
			value = e.At.Format(time.DateTime)
		}
		text := ui.Text(c, value).SingleLine().Tooltip(value).FontSize(theme.Font(c, theme.FontSize))
		if e.Failed {
			text.TextColor(theme.Colors(c).Error)
		}
	}).Grow(1).MinHeight(50).FillWidth()
	stats := t.tools.historyStats
	ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(4, 8).Children(func() {
		ui.Text(c, fmt.Sprintf(locales.Text("SELECT：%d · INSERT：%d · UPDATE：%d · DELETE：%d · 错误：%d · 总数：%d"), stats.Select, stats.Insert, stats.Update, stats.Delete, stats.Errors, stats.Total)).SingleLine().FontSize(theme.Font(c, theme.SmallFontSize))
	})
}
