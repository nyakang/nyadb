package dataworkspace

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// A real bounded MySQL page with independently managed record drafts.
func (s *State) View(c *ui.Context, t *Document) {
	if t == nil || t.closed {
		return
	}
	palette := theme.Colors(c)

	mysqlDataSyncAnalysisGrid(t)
	s.restoreMySQLDataLayout(t)
	t.edit.Hits = nil
	ui.Column(c).FillWidth().Grow(1).MinHeight(0).Background(palette.App).Children(func() {
		page := t.page
		if page == nil {
			page = &mysqlsvc.MySQLDataPage{}
			for _, col := range t.columns {
				page.Columns = append(page.Columns, col.ID)
			}
		}
		s.mysqlDataRuleEditor(c, t, page)
		s.analysisView(c, t)
		ui.Row(c).Grow(1).MinHeight(0).FillWidth().AlignItems(ui.Stretch).Gap(0).Children(func() {
			if t.toolbar.columnsOpen && t.page != nil {
				s.dataColumnsPanel(c, t)
			}
			ui.Column(c).Grow(1).MinWidth(0).MinHeight(0).Children(func() {
				if t.toolbar.dock == "" {
					s.mysqlDataBody(c, t, page)
				} else {
					if t.toolbar.dockHeight == 0 {
						t.toolbar.dockHeight = 62
					}
					if t.toolbar.dockExpanded {
						s.dataDock(c, t)
					} else {
						ui.SplitVerticalPercent(c, &t.toolbar.dockHeight, func() { s.mysqlDataBody(c, t, page) }, func() { s.dataDock(c, t) }).Grow(1).MinHeight(0).FillWidth()
					}
				}
			})
		})
		s.mysqlDataFooter(c, t, page)
	})
	s.trackMySQLDataLayout(c, t)
}

func (s *State) mysqlDataBody(c *ui.Context, t *Document, page *mysqlsvc.MySQLDataPage) {
	palette := theme.Colors(c)
	ui.Column(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
		if t.loading {
			ui.Row(c).Height(theme.RowHeight(c, theme.ControlHeight)).Padding(2, 6).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Width(16).Height(16)
				ui.Text(c, locales.Text("正在读取真实 MySQL 数据…")).TextColor(palette.Muted).FontSize(theme.Font(c, theme.FontSize)).SingleLine().Grow(1).MinWidth(0)
			})
		}
		if t.err != "" {
			ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(2, 6).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.Text("读取数据失败：")+locales.Text(t.err)).TextColor(palette.Error).FontSize(theme.Font(c, theme.FontSize)).SingleLine().Grow(1).MinWidth(0).Tooltip(locales.Text(t.err))
				if ui.Button(c, locales.Text("重试读取数据")).Height(theme.RowHeight(c, theme.ControlHeight)).Disabled(t.loading || !s.session().Connected).Clicked() {
					s.Load(c.Services(), t, true)
				}
			})
		}
		if t.page == nil {
			theme.EmptyTable(c, &t.grid, t.columns, locales.Text("没有已加载的数据页"))
		} else if len(page.Columns) == 0 {
			theme.EmptyTable(c, &t.grid, nil, locales.Text("此对象没有可见字段"))
		} else {
			if !page.StableOrder {
				ui.Text(c, locales.Text("此对象没有完整非空唯一键，OFFSET 分页期间数据变化可能导致行重复或遗漏")).
					Padding(2, 6).FontSize(theme.Font(c, theme.SmallFontSize)).TextColor(palette.Warning).SingleLine().Tooltip(locales.Text("此对象没有完整非空唯一键，OFFSET 分页期间数据变化可能导致行重复或遗漏"))
			}
			visibleIndices, visibleColumns := mysqlVisibleDataColumns(t, page)
			columns := append([]ui.TableColumn{mysqlDataRowHeaderColumn(t)}, visibleColumns...)
			rowOffset := mysqlDataPrepareAnalysisGrid(t)
			t.grid.Selection = &t.selection
			t.grid.CellSelect = true
			t.grid.DragSelect = true
			t.grid.TableStyle = &ui.TableStyle{
				AlternateRowColor: palette.GridAlternate,
				GridColor:         palette.Border, RowLines: true, ColumnLines: true,
				RowOffset: t.offset + int64(rowOffset),
			}
			t.grid.Sort = &t.sort
			t.grid.HeaderMenu = func(m *ui.Menu, col int) {
				if col > 0 {
					s.mysqlDataHeaderMenu(c, t, page, visibleIndices, m, col-1)
				}
			}
			t.grid.CellMenu = func(m *ui.Menu, row, col int) {
				row -= rowOffset
				source := -1
				if col > 0 && col <= len(visibleIndices) {
					source = visibleIndices[col-1]
				}
				if t.selection.Len() == 1 {
					t.edit.SelectionAnchor = row
				}
				s.mysqlDataCellMenu(c, t, m, row, source)
			}
			if len(page.Rows) == 0 && rowOffset == 0 {
				theme.EmptyTable(c, &t.grid, columns, "")
			} else {
				// Loading/error status rows before the grid must not change
				// its identity or reset the native scroll container.
				grid := theme.Table(c.Key("mysql-data-grid"), &t.grid, columns, len(page.Rows)+rowOffset, func(gridRow, col int) {
					if gridRow < rowOffset {
						s.analysisHeaders(c, t, columns)
						return
					}
					row := gridRow - rowOffset
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
						ui.Text(c, marker+mysqlDataRowHeaderText(t, row)).SingleLine().FontSize(theme.Font(c, theme.SmallFontSize)).TextColor(theme.CellText(c, &t.grid, gridRow, tableCol, palette.Muted))
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
					ui.Box(c).FillWidth().MinWidth(0).Children(func() {
						if cell.Null {
							ui.Text(c, "NULL").TextColor(theme.CellText(c, &t.grid, gridRow, tableCol, palette.GridNull)).FontSize(theme.Font(c, mysqlDataEditFont(t)))
						} else {
							text := ui.Text(c, cell.Text).SingleLine().FontSize(theme.Font(c, mysqlDataEditFont(t)))
							if cell.Truncated {
								text.Tooltip(locales.Text("已截断的预览；可在字段信息中按需读取"))
							}
						}
					})
					// Absolute children span the native cell's padding box, so
					// clicks in its whitespace have the same target as its text.
					box := ui.Box(c).Key("cell-pointer").Absolute().Top(0).Right(0).Bottom(0).Left(0)
					if cell.Truncated {
						box.Tooltip(locales.Text("已截断的预览；可在字段信息中按需读取"))
					}
					control := ui.Local(box, "cell-bounds", func() ui.Handle { return ui.Handle{} })
					box.Bind(control)
					bounds := box.Bounds()
					t.edit.Hits = append(t.edit.Hits, mysqlCellHit{Row: row, Source: col, Bounds: bounds, Control: *control})
					source := col
					services := c.Services()
					box.HandleInput(func(ev ui.InputEvent) bool {
						refreshMySQLDataCellHits(c, t)
						return s.mysqlDataCellPointer(services, t, row, source, control.Bounds(c), ev)
					})

				}).Grow(1)
				mysqlDataSyncAnalysisGrid(t)
				if t.edit.FocusGrid && t.edit.Editor == nil {
					grid.Focus()
					t.edit.FocusGrid = false
				}
				gridControl := ui.Local(grid, "grid-bounds", func() ui.Handle { return ui.Handle{} })
				grid.Bind(gridControl)
				gridServices := c.Services()
				grid.HandleInput(func(ev ui.InputEvent) bool {
					refreshMySQLDataCellHits(c, t)
					return s.mysqlDataGridPointer(gridServices, t, gridControl.Bounds(c), ev)
				})
				if t.edit.Editor == nil && grid.Shortcut(ui.Ctrl, ui.KeyC) {
					s.copyMySQLDataSelection(c, t)
				}
				if t.edit.Editor == nil && grid.Shortcut(ui.Ctrl, ui.KeyV) {
					for source, name := range page.Columns {
						if name == t.grid.Cells.Cursor {
							s.pasteMySQLDataSelection(c, t, t.selectedRow, source)
							break
						}
					}
				}
				if t.edit.Editor == nil && grid.Shortcut(0, ui.KeyF5) && !t.loading {
					s.Refresh(c.Services(), t)
				}
				if t.edit.Editor == nil && grid.Shortcut(0, ui.KeyF2) {
					for source, name := range page.Columns {
						if name == t.grid.Cells.Cursor {
							mysqlDataRevealEditRow(t, t.selectedRow)
							s.beginMySQLCellEdit(c.Services(), t, t.selectedRow, source, false)
							break
						}
					}
				}
				if !t.edit.Batch && !t.edit.Busy && t.edit.Editor == nil {
					for _, d := range t.edit.Drafts {
						if d.Row != t.selectedRow {
							s.saveMySQLData(c.Services(), t)
							break
						}
					}
				}
			}
			s.syncMySQLDataSort(c.Services(), t)
		}
	})
}

// Last successful execution survives a pending page, cancellation, and errors.
// SQL parameters and data values are never retained here.
type mysqlDataReadInfo struct {
	QuerySQL    string
	CompletedAt time.Time
	Duration    time.Duration
	Canceled    bool
}

func (info *mysqlDataReadInfo) accept(page *mysqlsvc.MySQLDataPage) {
	info.QuerySQL, info.CompletedAt, info.Duration = page.QuerySQL, page.CompletedAt, page.Duration
	info.Canceled = false
}

func (s *State) cancelMySQLDataRead(t *Document) {
	if t == nil || !t.loading {
		return
	}
	s.Cancel(t)
	t.err = ""
	t.readInfo.Canceled = true
}

func (s *State) changeMySQLDataPageSize(services ui.Services, t *Document, limit int) {
	if s.GuardLeave(t, func(next ui.Services) { s.changeMySQLDataPageSize(next, t, limit) }) {
		return
	}
	if t == nil || t.loading || t.limit == limit ||
		(limit != 50 && limit != 100 && limit != mysqlsvc.MySQLDataMaxPageSize) {
		return
	}
	t.limit = limit
	s.Refresh(services, t)
}

func mysqlDataQueryStatus(t *Document) string {
	if t.readInfo.QuerySQL == "" {
		return locales.Text("查询信息不可用")
	}
	if t.loading || t.err != "" || t.readInfo.Canceled || t.page == nil {
		return locales.Text("上次成功查询（参数化）：") + t.readInfo.QuerySQL
	}
	return locales.Text("参数化 SQL：") + t.readInfo.QuerySQL
}

func mysqlDataReadStatus(t *Document) string {
	info := t.readInfo
	if info.CompletedAt.IsZero() {
		return locales.Text("尚无成功读取时间")
	}
	return fmt.Sprintf("%s %s · %d ms", locales.Text("上次成功读取"),
		info.CompletedAt.Local().Format("15:04:05"), max(0, info.Duration.Milliseconds()))
}

func (s *State) mysqlDataFooter(c *ui.Context, t *Document, page *mysqlsvc.MySQLDataPage) {
	palette := theme.Colors(c)
	s.consumeMySQLDataExport(t)

	ui.Column(c).FillWidth().Shrink(0).Background(palette.Header).Children(func() {
		if t.edit.Batch && mysqlDataHasDraft(t) {
			ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Shrink(0).Padding(2, 6).Gap(6).AlignItems(ui.Center).Background(palette.Notice).Children(func() {
				ui.Text(c, fmt.Sprintf(locales.Text("%d 条待处理记录"), mysqlDataPendingRecordCount(t))).FontSize(theme.Font(c, theme.SmallFontSize)).TextColor(palette.NoticeText).SingleLine().Grow(1).MinWidth(0)
				ready := !t.loading && !t.edit.Busy && t.edit.Uncertain == nil && !dataTransactionUnavailable(t) && !t.transfer.Busy && !t.transfer.Uncertain
				if ui.Button(c, locales.Text("应用更改")).Label(locales.Text("应用待处理记录")).Height(theme.ControlHeight).Disabled(!ready || t.profileID != s.session().ProfileID || !s.session().Connected).Clicked() {
					s.saveMySQLData(c.Services(), t)
				}
				if ui.Button(c, locales.Text("放弃")).Label(locales.Text("放弃待处理记录")).Height(theme.ControlHeight).Disabled(!ready).Clicked() {
					s.discardMySQLData(t)
				}
			})
		}
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Shrink(0).Padding(2, 6).Gap(4).AlignItems(ui.Center).Children(func() {
			ready := t.page != nil && !t.loading && !t.edit.Busy && t.edit.Uncertain == nil && !dataTransactionUnavailable(t) && !t.transfer.Busy && !t.transfer.Uncertain && t.profileID == s.session().ProfileID && s.session().Connected
			dirty := mysqlDataHasDraft(t)
			insertReady := ready && len(page.ColumnMetadata) > 0 && len(page.ColumnMetadata) == len(page.Columns)
			if ui.Button(c, "+").Label(locales.Text("添加记录")).Tooltip(locales.Text("添加记录")).Size(26, theme.ControlHeight).Padding(0).Disabled(!insertReady).Clicked() {
				s.addMySQLDataRecord(c.Services(), t)
			}
			if ui.Button(c, "−").Label(locales.Text("删除记录")).Tooltip(locales.Text("删除记录")).Size(26, theme.ControlHeight).Padding(0).Disabled(!ready || !mysqlDataCanDeleteSelection(t, t.selectedRow)).Clicked() {
				s.deleteMySQLDataRecords(c.Services(), t, t.selectedRow)
			}
			if ui.Button(c, "✓").Label(locales.Text("保存数据更改")).Tooltip(locales.Text("保存数据更改")).Size(26, theme.ControlHeight).Padding(0).Disabled(!ready || !dirty || t.edit.Uncertain != nil).Clicked() {
				s.saveMySQLData(c.Services(), t)
			}
			if ui.Button(c, "×").Label(locales.Text("取消数据更改")).Tooltip(locales.Text("取消数据更改")).Size(26, theme.ControlHeight).Padding(0).Disabled(t.loading || t.edit.Busy || !dirty || t.edit.Uncertain != nil).Clicked() {
				s.discardMySQLData(t)
			}
			if ui.Button(c, "↻").Label(locales.Text("刷新数据并返回首页")).Tooltip(locales.Text("刷新数据并返回首页")).Size(26, theme.ControlHeight).Padding(0).Disabled(!ready).Clicked() {
				s.Refresh(c.Services(), t)
			}
			if ui.Button(c, "■").Label(locales.Text("停止数据操作")).Tooltip(locales.Text("停止数据操作")).Size(26, theme.ControlHeight).Padding(0).Disabled(!t.loading && !t.edit.Busy && !dataTransactionBusy(t) && !t.transfer.Busy).Clicked() {
				if t.transfer.Busy {
					s.stopDataTransfer(t)
				} else if dataTransactionBusy(t) {
					s.stopDataTransaction(t)
				} else if t.edit.Busy {
					s.stopMySQLDataEdit(t)
				} else {
					s.cancelMySQLDataRead(t)
				}
			}
			ui.Checkbox(c, &t.edit.Batch, locales.Text("批量编辑")).Disabled(dirty || t.loading || t.edit.Busy || t.edit.Uncertain != nil).FontSize(theme.Font(c, theme.SmallFontSize))
			s.mysqlDataEditStatus(c, t)
		})
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Shrink(0).Padding(2, 6).Gap(4).AlignItems(ui.Center).Children(func() {
			status := mysqlDataPageStatus(t)
			if t.page != nil && !t.loading && t.err == "" && !t.readInfo.Canceled &&
				t.selectedRow >= 0 && t.selectedRow < len(page.Rows) {
				status += fmt.Sprintf(" · %s %d", locales.Text("当前记录"), t.offset+int64(t.selectedRow)+1)
			}
			ui.Text(c, status).FontSize(theme.Font(c, theme.SmallFontSize)).
				TextColor(palette.Muted).SingleLine().Grow(1).MinWidth(0).Tooltip(status)
			previousDisabled := t.loading || t.edit.Busy || t.edit.Uncertain != nil || t.page == nil || t.offset <= 0
			if ui.Button(c, "«").Label(locales.Text("首页数据")).Tooltip(locales.Text("首页")).
				Size(26, theme.ControlHeight).Padding(0).Disabled(previousDisabled).Clicked() {
				s.changeMySQLDataPage(c.Services(), t, 0)
			}
			if ui.Button(c, "‹").Label(locales.Text("上一页数据")).Tooltip(locales.Text("上一页")).
				Size(26, theme.ControlHeight).Padding(0).Disabled(previousDisabled).Clicked() {
				s.changeMySQLDataPage(c.Services(), t, max(0, t.offset-int64(t.limit)))
			}
			ui.Text(c, fmt.Sprintf(locales.Text("第 %d 页"), t.offset/int64(max(1, t.limit))+1)).
				Label(locales.Text("当前数据页")).FontSize(theme.Font(c, theme.SmallFontSize)).SingleLine()
			if ui.Button(c, "›").Label(locales.Text("下一页数据")).Tooltip(locales.Text("下一页")).
				Size(26, theme.ControlHeight).Padding(0).Disabled(t.loading || t.edit.Busy || t.edit.Uncertain != nil || t.page == nil || !page.HasMore).Clicked() {
				s.changeMySQLDataPage(c.Services(), t, t.offset+int64(t.limit))
			}
			ui.Button(c, fmt.Sprintf("%d / %s", t.limit, locales.Text("页"))).Label(locales.Text("每页行数")).
				Height(theme.RowHeight(c, theme.ControlHeight)).Padding(2, 4).Disabled(t.loading || t.edit.Busy || t.edit.Uncertain != nil).Menu(func(m *ui.Menu) {
				for _, limit := range []int{50, 100, mysqlsvc.MySQLDataMaxPageSize} {
					if m.Item(fmt.Sprintf("%d %s", limit, locales.Text("行 / 页"))).Checked(t.limit == limit).Chosen() {
						s.changeMySQLDataPageSize(c.Services(), t, limit)
					}
				}
			})

		})
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Shrink(0).Padding(2, 6).Gap(6).AlignItems(ui.Center).
			BorderWidth(1, 0, 0, 0).BorderColor(palette.Border).Children(func() {
			ui.Button(c, "SQL").Label(locales.Text("查询 SQL 操作")).Height(theme.RowHeight(c, theme.ControlHeight)).Padding(2, 4).
				Menu(func(m *ui.Menu) {
					if m.Item(locales.Text("复制参数化 SQL")).Disabled(t.readInfo.QuerySQL == "").Chosen() {
						c.WriteClipboard(t.readInfo.QuerySQL)
					}
				})
			query := mysqlDataQueryStatus(t)
			ui.Text(c, query).Label(locales.Text("数据查询 SQL")).FontSize(theme.Font(c, theme.SmallFontSize)).TextColor(palette.Muted).
				SingleLine().Grow(1).MinWidth(0).Tooltip(query)
			readStatus := mysqlDataReadStatus(t)
			ui.Text(c, readStatus).Label(locales.Text("数据读取时间")).FontSize(theme.Font(c, theme.SmallFontSize)).
				TextColor(palette.Muted).SingleLine().MaxWidth(210).Tooltip(readStatus)
		})
	})
}

func mysqlDataPendingRecordCount(t *Document) int {
	count := len(t.edit.Drafts)
	if e := t.edit.Editor; e != nil && e.Text != e.Initial && t.edit.Drafts[e.Key] == nil {
		count++
	}
	return count
}

func mysqlDataPageMethod(page *mysqlsvc.MySQLDataPage) string {
	if page.KeysetAvailable {
		return "Keyset"
	}
	return "OFFSET"
}

func mysqlDataToolbarButton(c *ui.Context, caption, label, icon string, disabled bool) ui.Element {
	palette := theme.Colors(c)

	return ui.ButtonBase(c).Label(label).Tooltip(label).Disabled(disabled).
		Height(theme.RowHeight(c, theme.ControlHeight)).Padding(2, 5).Radius(theme.ControlRadius).
		Children(func() {
			ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
				theme.Icon(c, icon, theme.SmallIconSize, palette.Blue)
				ui.Text(c, locales.Text(caption)).SingleLine().FontSize(theme.Font(c, theme.FontSize))
			})
		})
}

func (s *State) mysqlDataCompactActions(c *ui.Context, t *Document) {
	page := t.page
	mysqlDataToolbarButton(c, "列", locales.Text("列设置"), "table", page == nil).
		Menu(func(m *ui.Menu) {
			if page != nil {
				s.mysqlDataColumnsMenu(c, t, page, m)
			}
		})
	if mysqlDataToolbarButton(c, "字段信息", locales.Text("查看字段信息"), "info", page == nil).Clicked() {
		s.ShowField(t)
	}
	ui.Divider(c).Width(1).Height(18)
	if mysqlDataToolbarButton(c, "刷新", locales.Text("刷新表数据：")+t.table, "refresh", !s.session().Connected || t.loading).Clicked() {
		s.Refresh(c.Services(), t)
	}
	if t.loading && mysqlDataToolbarButton(c, "取消", locales.Text("取消读取数据"), "close", false).Clicked() {
		s.cancelMySQLDataRead(t)
	}
	selected := mysqlDataHasSelectedPreview(t)
	if mysqlDataToolbarButton(c, "复制", locales.Text("复制所选数据预览"), "copy", !selected).Clicked() {
		s.copyMySQLDataSelection(c, t)
	}
	ui.Button(c, "▾").Label(locales.Text("复制为")).Tooltip(locales.Text("复制为")).
		Height(theme.RowHeight(c, theme.ControlHeight)).Padding(2, 4).Disabled(!selected).
		Menu(func(m *ui.Menu) { s.mysqlDataCopyAsMenu(c, t, m) })
	count := len(t.filterRules)
	if count == 0 && t.filterMode != "" {
		count = 1
	}
	if mysqlDataToolbarButton(c, "清除", locales.Text("清除筛选"), "catalog-filter", t.loading || count == 0).Clicked() {
		s.clearMySQLDataFilter(c.Services(), t)
	}
}

// mysqlDataPageStatus describes only settled read results. A pending request or
// failed query must never display a fabricated row count or successful page.
func mysqlDataPageStatus(t *Document) string {
	if t == nil {
		return ""
	}
	limit := max(1, t.limit)
	page := t.offset/int64(limit) + 1
	if t.loading {
		return fmt.Sprintf(locales.Text("正在读取第 %d 页…"), page)
	}
	if t.readInfo.Canceled {
		return fmt.Sprintf(locales.Text("读取已取消 · 第 %d 页"), page)
	}
	if t.err != "" {
		return fmt.Sprintf(locales.Text("读取失败 · 第 %d 页"), page)
	}
	if t.page == nil {
		return locales.Text("尚未读取数据")
	}
	count := len(t.page.Rows)
	if count == 0 {
		return fmt.Sprintf(locales.Text("已读取 0 行 · 第 %d 页"), page)
	}
	first := t.offset + 1
	last := first + int64(count) - 1
	return fmt.Sprintf(locales.Text("已读取 %d 行 · %d–%d · 第 %d 页"), count, first, last, page)
}
