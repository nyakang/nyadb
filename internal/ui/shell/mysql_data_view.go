package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// A real bounded MySQL page with independently managed record drafts.
func (s *State) mysqlDataWorkspace(c *ui.Context, t *tab) {
	palette := theme.Colors(c)

	s.restoreMySQLDataLayout(t)
	t.dataEdit.Hits = nil
	ui.Column(c).FillWidth().Grow(1).MinHeight(0).Background(palette.App).Children(func() {
		page := t.dataPage
		if page == nil {
			page = &connections.MySQLDataPage{}
			for _, col := range t.dataColumns {
				page.Columns = append(page.Columns, col.ID)
			}
		}
		s.mysqlDataRuleEditor(c, t, page)
		s.mysqlDataEditStatus(c, t)
		if t.dataLoading {
			ui.Row(c).Height(theme.RowHeight(c, theme.ControlHeight)).Padding(2, 6).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Width(16).Height(16)
				ui.Text(c, locales.Text("正在读取真实 MySQL 数据…")).TextColor(palette.Muted).FontSize(theme.Font(c, theme.FontSize)).SingleLine().Grow(1).MinWidth(0)
			})
		}
		if t.dataErr != "" {
			ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(2, 6).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.Text("读取数据失败：")+t.dataErr).TextColor(palette.Error).FontSize(theme.Font(c, theme.FontSize)).SingleLine().Grow(1).MinWidth(0).Tooltip(t.dataErr)
				if ui.Button(c, locales.Text("重试读取数据")).Height(theme.RowHeight(c, theme.ControlHeight)).Disabled(t.dataLoading || s.mysqlSessionStatus != "已连接").Clicked() {
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
					Padding(2, 6).FontSize(theme.Font(c, theme.SmallFontSize)).TextColor(palette.Warning).SingleLine().Tooltip(locales.Text("此对象没有完整非空唯一键，OFFSET 分页期间数据变化可能导致行重复或遗漏"))
			}
			visibleIndices, visibleColumns := mysqlVisibleDataColumns(t, page)
			columns := append([]ui.TableColumn{mysqlDataRowHeaderColumn(t)}, visibleColumns...)
			t.dataState.Selected = &t.dataSelectedRow
			t.dataState.Selection = &t.dataSelection
			t.dataState.CellSelect = true
			t.dataState.DragSelect = true
			t.dataState.TableStyle = &ui.TableStyle{
				AlternateRowColor: palette.GridAlternate,
				GridColor:         palette.Border, RowLines: true, ColumnLines: true,
				RowOffset: t.dataOffset,
			}
			t.dataState.Sort = &t.dataSort
			t.dataState.HeaderMenu = func(m *ui.Menu, col int) {
				if col > 0 {
					s.mysqlDataHeaderMenu(c, t, page, visibleIndices, m, col-1)
				}
			}
			t.dataState.CellMenu = func(m *ui.Menu, row, col int) {
				source := -1
				if col > 0 && col <= len(visibleIndices) {
					source = visibleIndices[col-1]
				}
				if t.dataSelection.Len() == 1 {
					t.dataEdit.SelectionAnchor = row
				}
				s.mysqlDataCellMenu(c, t, m, row, source)
			}
			if len(page.Rows) == 0 {
				emptyTableView(c, &t.dataState, columns, "")
			} else {
				grid := theme.Table(c, &t.dataState, columns, len(page.Rows), func(row, col int) {
					tableCol := col
					if col == 0 {
						marker := ""
						if d := mysqlDataDraftForRow(t, row); d != nil {
							if d.Kind == "delete" {
								marker = "− "
							} else if d.Kind == "insert" {
								marker = "+ "
							} else {
								marker = "* "
							}
						}
						ui.Text(c, marker+mysqlDataRowHeaderText(t, row)).SingleLine().FontSize(theme.Font(c, theme.SmallFontSize)).TextColor(theme.CellText(c, &t.dataState, row, tableCol, palette.Muted))
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
					if s.mysqlDataInlineEditor(c, t, row, col) {
						return
					}
					cell := page.Rows[row][col]
					box := ui.Box(c).FillWidth().MinWidth(0).Children(func() {
						if cell.Null {
							ui.Text(c, "NULL").TextColor(theme.CellText(c, &t.dataState, row, tableCol, palette.GridNull)).FontSize(theme.Font(c, mysqlDataEditFont(t)))
						} else {
							text := ui.Text(c, cell.Text).SingleLine().FontSize(theme.Font(c, mysqlDataEditFont(t)))
							if cell.Truncated {
								text.Tooltip(locales.Text("已截断的预览；可在字段信息中按需读取"))
							}
						}
					})
					bounds := box.Bounds()
					t.dataEdit.Hits = append(t.dataEdit.Hits, mysqlCellHit{Row: row, Source: col, Bounds: bounds})
					source := col
					services := c.Services()
					box.HandleInput(func(ev ui.InputEvent) bool { return s.mysqlDataCellPointer(services, t, row, source, bounds, ev) })

				}).Grow(1)
				if t.dataEdit.FocusGrid && t.dataEdit.Editor == nil {
					grid.Focus()
					t.dataEdit.FocusGrid = false
				}
				gridBounds := grid.Bounds()
				gridServices := c.Services()
				grid.HandleInput(func(ev ui.InputEvent) bool { return s.mysqlDataGridPointer(gridServices, t, gridBounds, ev) })
				if t.dataEdit.Editor == nil && grid.Shortcut(ui.Ctrl, ui.KeyC) {
					s.copyMySQLDataSelection(c, t)
				}
				if t.dataEdit.Editor == nil && grid.Shortcut(ui.Ctrl, ui.KeyV) {
					for source, name := range page.Columns {
						if name == t.dataState.Cells.Cursor {
							s.pasteMySQLDataSelection(c, t, t.dataSelectedRow, source)
							break
						}
					}
				}
				if t.dataEdit.Editor == nil && grid.Shortcut(0, ui.KeyF5) && !t.dataLoading {
					s.resetAndReloadMySQLData(c.Services(), t)
				}
				if t.dataEdit.Editor == nil && grid.Shortcut(0, ui.KeyF2) {
					for source, name := range page.Columns {
						if name == t.dataState.Cells.Cursor {
							s.beginMySQLCellEdit(c.Services(), t, t.dataSelectedRow, source, false)
							break
						}
					}
				}
				if !t.dataEdit.Batch && !t.dataEdit.Busy && t.dataEdit.Editor == nil {
					for _, d := range t.dataEdit.Drafts {
						if d.Row != t.dataSelectedRow {
							s.saveMySQLData(c.Services(), t)
							break
						}
					}
				}
			}
			s.syncMySQLDataSort(c.Services(), t)
		}
		s.mysqlDataFooter(c, t, page)
	})
	s.trackMySQLDataLayout(c, t)
}
