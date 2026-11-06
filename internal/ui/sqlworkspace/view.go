package sqlworkspace

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/gridview"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) View(c *ui.Context, t *Document) {
	if t == nil {
		return
	}
	if t.preview {
		s.previewView(c, t)
		return
	}
	p := theme.Colors(c)
	connected := t.profileID != 0 && s.connected(t.profileID)
	_, fileSupported := t.lease.(fileLease)
	if lease, ok := t.lease.(interface{ SupportsFiles() bool }); ok {
		fileSupported = lease.SupportsFiles()
	}
	editorPane := func() {
		ui.Column(c).Fill().Children(func() {
			commands := ui.Row(c).Key("sql-command-row").Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(2, 6).Gap(7).AlignItems(ui.Center).Background(p.Header)
			available := commands.Bounds().W
			if available <= 0 {
				available, _ = c.Size()
			}
			contextBelow := available < 420*max(float32(1), theme.Font(c, 12)/12)
			profile, schema := t.sourceScope()
			if schema == "" {
				schema = locales.Text("未指定数据库")
			}
			name := fmt.Sprintf(locales.Text("连接 #%d"), profile)
			if s.deps.ProfileName != nil {
				name = s.deps.ProfileName(profile)
			}

			drawContext := func() {
				if s.deps.ProfileColor != nil {
					ui.Box(c).Size(3, 14).Background(theme.ProfileColor(c, s.deps.ProfileColor(profile)))
				}
				contextWidth := float32(160)
				if contextBelow {
					contextWidth = max(float32(1), available-12)
				}
				ui.Text(c, name+" · "+schema).MaxWidth(contextWidth).MinWidth(0).Shrink(1).
					TextColor(p.Muted).FontSize(theme.Font(c, 11)).SingleLine().Tooltip(fmt.Sprintf("%s\nCatalog: %s\nSchema: %s", name, t.Namespace().Catalog, t.Namespace().Schema))
			}
			commands.Children(func() {
				ui.Toolbar(c, func() {
					caption := locales.Text("运行当前语句")
					runLabel, formatLabel := locales.Text("运行当前或选中 SQL"), locales.Text("格式化 SQL")
					if t.nativeCommands {
						runLabel = locales.Text("运行 MongoDB 命令")
						formatLabel = locales.Text("格式化 JSON")
					}
					width := available
					if strings.TrimSpace(t.Editor.SelectedText()) != "" {
						caption = locales.Text("运行选中 SQL")
					}
					if width < 760*max(float32(1), theme.Font(c, 12)/12) {
						caption = locales.Text("运行")
					}
					if ui.Button(c, caption).Tooltip(runLabel + " · Ctrl+Enter").Label(runLabel).
						Disabled(!connected || t.loading || t.recoveryNeeded).Height(theme.RowHeight(c, theme.ControlHeight)).Clicked() {
						s.Run(c.Services().Invalidate, t)
					}
					theme.IconButton(c, "", "chevron-down").Label(locales.Text("选择 SQL 执行范围")).Disabled(!connected || t.loading || t.recoveryNeeded).Menu(func(m *ui.Menu) {
						if m.Item(locales.Text("运行当前语句")).Chosen() {
							s.RunCurrent(c.Services().Invalidate, t)
						}
						if m.Item(locales.Text("运行选中 SQL")).Disabled(strings.TrimSpace(t.Editor.SelectedText()) == "").Chosen() {
							s.RunSelection(c.Services().Invalidate, t)
						}
						if m.Item(locales.Text("运行全部 SQL（F5）")).Chosen() {
							s.RunAll(c.Services().Invalidate, t)
						}
					})
					if ui.Button(c, locales.Text("取消")).Disabled(!t.loading || t.cancelRequested).
						Height(theme.RowHeight(c, theme.ControlHeight)).Clicked() {
						s.RequestCancel(t)
					}
					s.sqlLayoutMenu(c, t)
					if ui.Button(c, formatLabel).Clicked() {
						s.Format(t)
					}
					if ui.Button(c, "EXPLAIN").Disabled(t.loading || !connected || t.nativeCommands).Clicked() {
						s.Explain(t)
					}
					if ui.Button(c, locales.Text("运行并导出")).Disabled(t.loading || t.recoveryNeeded || !connected).Clicked() {
						s.RunExport(t)
					}
					if ui.Button(c, locales.Text("执行 SQL 文件")).Disabled(t.loading || t.recoveryNeeded || !connected || !fileSupported).Clicked() {
						s.RunFile(t)
					}
					if ui.Button(c, locales.Text("打开 SQL")).Disabled(t.loading).Clicked() {
						s.OpenFile(t)
					}
					if ui.Button(c, locales.Text("保存查询")).Clicked() {
						s.Save(t, false, nil)
					}
					if ui.Button(c, locales.Text("另存为")).Clicked() {
						s.Save(t, true, nil)
					}
					if ui.Button(c, locales.Text("AI SQL")).Label(locales.Text("打开 AI SQL 助手")).Disabled(!s.AIAvailable() || t.loading).Clicked() {
						s.OpenAI(t)
					}
					if ui.Button(c, locales.Text("SQL 历史")).Disabled(!s.HistoryAvailable()).Clicked() {
						profile := t.profileID
						if t.recoveryNeeded {
							profile = t.recoveryProfile
						}
						s.OpenHistory(profile, s.history.openEntry)
					}
					ui.Button(c, locales.Text("命名查询")).Menu(func(m *ui.Menu) {
						profile := t.profileID
						if t.recoveryNeeded {
							profile = t.recoveryProfile
						}
						for _, n := range s.Named(profile) {
							if m.Item(n.Name).Chosen() {
								s.OpenNamed(t, n)
							}
						}
					})
				}).Label(locales.Text("SQL 执行操作")).Grow(1).MinWidth(0).Padding(0).Gap(5)
				if !contextBelow {
					drawContext()
				}
			})
			if contextBelow {
				ui.Row(c).Height(theme.RowHeight(c, 22)).Padding(2, 6).Background(p.Header).Children(drawContext)
			}
			s.queryDiagnosticView(c, t)
			if t.err != "" {
				ui.Text(c, t.err).Padding(3, 8).FontSize(theme.Font(c, 11)).
					TextColor(p.Error).SingleLine().Tooltip(t.err)
			} else if !connected && !t.recoveryNeeded {
				ui.Text(c, locales.Text("请连接数据库后新建查询")).Padding(3, 8).
					TextColor(p.Muted).FontSize(theme.Font(c, 11))
			}
			if t.recoveryNeeded {
				id := t.recoveryProfile
				canRecover := !t.loading && t.prepareResults == nil &&
					id > 0 && s.runtime() != nil && s.connected(id) && s.runtime().IsConnected(id)
				ui.Row(c).Padding(3, 8).Gap(9).AlignItems(ui.Center).Children(func() {
					ui.Text(c, locales.Text("新会话无法恢复事务或临时状态；不会自动执行 SQL")).
						FontSize(theme.Font(c, 11)).TextColor(p.Muted)
					if s.deps.ConnectProfile != nil && ui.Button(c, locales.Text("恢复原连接")).Disabled(s.connected(id) || !s.profileExists(id) || t.loading).Clicked() {
						s.deps.ConnectProfile(c.Services(), id)
					}
					if ui.Button(c, locales.Text("以新会话继续")).Label(locales.Text("建立新物理会话（不执行现有 SQL）")).
						Disabled(!canRecover).Clicked() {
						s.Recover(t)
					}
				})
				if !s.profileExists(id) {
					ui.Text(c, locales.Text("原连接已不存在，请重新绑定或关闭页签")).Padding(3, 8)
					ui.Button(c, locales.Text("重新绑定连接")).Menu(func(m *ui.Menu) {
						if s.deps.Profiles == nil {
							return
						}
						for _, p := range s.deps.Profiles() {
							if m.Item(p.Name).Chosen() {
								s.Rebind(t, p.ID)
							}
						}
					})
				}
			}
			theme.ApplySQLAppearance(c, t.Editor)
			t.Editor.Label = locales.Text("SQL 编辑器")
			if t.nativeCommands {
				t.Editor.Label = locales.Text("MongoDB 命令编辑器")
			}
			ed := editor.View(c, t.Editor).Grow(1).FillWidth()
			if s.deps.EditFocus != nil {
				ed.FocusBind(s.deps.EditFocus, fmt.Sprintf("sql/editor/%d", t.id))
			}
			handle := ui.Local(ed, "editor-focus", func() ui.Handle { return ui.Handle{} })
			ed.Bind(handle)
			if t.focusEditor {
				ed.Focus()
				t.focusEditor = false
			}
			s.registerEditor(t, ed.Focused(), handle)
		})
	}
	root := ui.Column(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
		switch t.layoutMode {
		case "editor":
			editorPane()
		case "result":
			s.resultGrid(c, t)
		default:
			if t.result == nil && !t.loading && len(t.snapshots) == 0 {
				editorPane()
			} else {
				ui.SplitVerticalPercent(c, &s.SplitPercent, editorPane, func() { s.resultGrid(c, t) }).Grow(1)
			}

		}
	})
	s.sqlKeyboard(c, root, t)
}

func (s *State) sqlLayoutMenu(c *ui.Context, t *Document) {
	ui.Button(c, locales.Text("布局")).Menu(func(m *ui.Menu) {
		for _, item := range []struct{ label, mode string }{{"编辑器与结果", ""}, {"只看编辑器", "editor"}, {"只看结果", "result"}} {
			if m.Item(locales.Text(item.label)).Checked(t.layoutMode == item.mode).Chosen() {
				t.layoutMode = item.mode
			}
		}
	})
}
func (s *State) resultGrid(c *ui.Context, owner *Document) {
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(2, 6).Gap(5).Children(func() {
			s.sqlLayoutMenu(c, owner)
			if theme.PageButton(c, locales.Text("实时结果"), owner.snapshotIndex == 0).Clicked() {
				owner.snapshotIndex = 0
			}
			ui.Button(c, locales.Text("结果快照")).Menu(func(m *ui.Menu) {
				if m.Item(locales.Text("固定本次结果")).Disabled(owner.loading || owner.result == nil).Chosen() {
					s.pinResult(owner)
				}
				for i, snap := range owner.snapshots {
					if m.Item(snap.Document.Name() + " · " + snap.Time.Format("15:04:05")).Checked(owner.snapshotIndex == i+1).Chosen() {
						owner.snapshotIndex = i + 1
					}
				}
				if m.Item(locales.Text("移除当前快照")).Disabled(owner.snapshotIndex == 0).Chosen() {
					owner.removeSnapshot(owner.snapshotIndex - 1)
				}
			})
			if owner.snapshotIndex > 0 && owner.snapshotIndex <= len(owner.snapshots) {
				snap := owner.snapshots[owner.snapshotIndex-1]
				ui.Text(c, snap.Document.Name()+" · "+snap.Time.Format("15:04:05")).SingleLine().Tooltip(fmt.Sprintf("%s\n%+v\n%s", snap.Time.Format("2006-01-02 15:04:05"), snap.Namespace, snap.SQL))
			}
			ui.Box(c).Grow(1)
			displayed := owner
			if owner.snapshotIndex > 0 && owner.snapshotIndex <= len(owner.snapshots) {
				displayed = owner.snapshots[owner.snapshotIndex-1].Document
			}
			summary := sqlResultSummary(displayed)
			ui.Text(c, summary).FontSize(theme.Font(c, 11)).TextColor(theme.Colors(c).Muted).SingleLine().MinWidth(0).Tooltip(summary)
		})
		t := owner
		if owner.snapshotIndex > 0 && owner.snapshotIndex <= len(owner.snapshots) {
			t = owner.snapshots[owner.snapshotIndex-1].Document
		}
		s.resultContent(c, t)
	})
}
func sqlResultSummary(t *Document) string {
	if t.cancelRequested {
		return locales.Text("正在等待取消确认…")
	}
	if t.loading {
		return locales.Text("正在执行…")
	}
	if t.result == nil {
		return locales.Text("尚未执行")
	}
	set := selectedResult(t)
	duration := fmt.Sprintf("%d ms", max(0, t.result.Duration.Milliseconds()))
	if len(set.Columns) > 0 {
		return fmt.Sprintf(locales.Text("%d 行 / %d 列 · %s"), len(set.Rows), len(set.Columns), duration)
	}
	return fmt.Sprintf(locales.Text("影响 %d 行 · %s"), set.AffectedRows, duration)
}
func (s *State) resultContent(c *ui.Context, t *Document) {
	s.cachedExportView(c, t)
	if t.err != "" && (t.snapshotOwner != nil || t.layoutMode == "result") {
		ui.Text(c, t.err).TextColor(theme.Colors(c).Error).SingleLine().Tooltip(t.err)
	}
	s.planNavigation(c, t)
	if t.planVisible && t.plan != nil && !t.showOrdinary {
		s.planView(c, t)
		return
	}
	ui.Column(c).Label(locales.Text("查询结果")).Fill().Children(func() {

		if t.result == nil {
			ui.Box(c).Grow(1)
			return
		}
		r := t.result
		set := selectedResult(t)
		sets := r.ResultSets()

		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Gap(5).Children(func() {
			ui.ScrollHorizontal(c).Grow(1).MinWidth(0).Children(func() {
				labels := make([]string, 0, len(sets)+1)
				for i := range sets {
					labels = append(labels, fmt.Sprintf(locales.Text("结果 %d"), i+1))
				}
				labels = append(labels, locales.Text("消息"))
				chosen := t.resultSet
				if t.showMessages {
					chosen = len(sets)
				}
				if theme.Tabs(c, &chosen, labels, false).Changed() {
					if chosen == len(sets) {
						t.showMessages = true
					} else {
						t.selectResult(chosen)
						t.showMessages = false
					}
				}
			})
			if ui.Button(c, locales.Text("复制结果选区")).Clicked() {
				s.copyResult(c, t)
			}
			if ui.Button(c, locales.Text("导出当前结果")).Clicked() {
				s.openCachedExport(t)
			}
		})
		set = selectedResult(t)
		if t.showMessages {
			ui.Scroll(c).Grow(1).Children(func() {
				ui.Column(c).Gap(4).Padding(6).Children(func() {
					for i, step := range r.Steps {
						state := step.State
						switch state {
						case "executed":
							state = locales.Text("已执行")
						case "not_executed":
							state = locales.Text("未执行")
						case "uncertain":
							state = locales.Text("结果不确定")
						}
						text := fmt.Sprintf("%d · %s", i+1, state)
						if step.Error != "" {
							text += " · " + step.Error
						}
						ui.Text(c, text).Tooltip(step.SQL)
					}
				})
			})
			return
		}
		if len(set.Truncation) > 0 {
			ui.Text(c, truncationText(set.Truncation)).SingleLine().Tooltip(truncationText(set.Truncation))
		}
		if len(set.Columns) == 0 {
			ui.Text(c, fmt.Sprintf(locales.Text("影响 %d 行"), set.AffectedRows)).
				Padding(10).FontSize(theme.Font(c, 12))
			ui.Box(c).Grow(1)
			return
		}
		allColumns := make([]ui.TableColumn, len(set.Columns))
		if t.hiddenResultColumns == nil {
			t.hiddenResultColumns = map[string]bool{}
		}
		columns := []ui.TableColumn{}
		sources := []int{}
		for i, column := range set.Columns {
			allColumns[i] = ui.TableColumn{ID: fmt.Sprintf("sql_%d", i), Title: column.Name, Width: 160, Subtitle: column.DatabaseType}
			if !t.hiddenResultColumns[allColumns[i].ID] {
				columns = append(columns, allColumns[i])
				sources = append(sources, i)
			}
		}
		t.grid.Selected = &t.selectedRow
		t.grid.Selection = &t.selection
		t.grid.CellSelect = true
		t.grid.DragSelect = true
		t.grid.HeaderMenu = func(m *ui.Menu, col int) {
			if col >= 0 && col < len(sources) {
				gridview.ColumnMenu(c, m, &t.grid.Columns, t.hiddenResultColumns, allColumns, sources[col], len(set.Rows), func(row, col int) contract.Cell { return set.Rows[row][col] }, c.Services().WriteClipboard)
			}
		}
		t.grid.CellMenu = func(m *ui.Menu, row, col int) {
			if row < 0 || row >= len(set.Rows) || col < 0 || col >= len(sources) {
				return
			}
			if !t.selection.Has(row) {
				t.selection.Clear()
				t.selection.Add(row)
				t.selectedRow = row
				t.grid.Cells.Cursor = allColumns[sources[col]].ID
				t.grid.Cells.Anchor = t.grid.Cells.Cursor
			}
			if m.Item(locales.Text("复制")).Chosen() {
				s.copyResult(c, t)
			}
			if m.Item(locales.Text("查看字段信息")).Chosen() {
				t.selectedRow = row
				t.grid.Cells.Cursor = allColumns[sources[col]].ID
				t.resultFieldOpen = true
			}
			m.Separator()
			gridview.ColumnMenu(c, m, &t.grid.Columns, t.hiddenResultColumns, allColumns, sources[col], len(set.Rows), func(row, col int) contract.Cell { return set.Rows[row][col] }, c.Services().WriteClipboard)
		}
		tablePane := func() {
			table := theme.Table(c, &t.grid, columns, len(set.Rows), func(row, col int) { gridview.Cell(c, set.Rows[row][sources[col]]) }).Grow(1).MinWidth(0)
			s.registerResult(c, t, table)
			table.HandleInput(func(ev ui.InputEvent) bool {
				if ev.Kind != ui.InputCommand {
					return false
				}
				command := ev.Text
				if command == "selectAll" {
					command = "select_all"
				}
				return s.resultEditTarget(t).Execute(c.Services(), command)
			})
			if table.Shortcut(ui.Cmd, ui.KeyC) {
				s.copyResult(c, t)
			}
			if table.Shortcut(ui.Cmd, ui.KeyA) {
				s.resultEditTarget(t).Execute(c.Services(), "select_all")
			}
		}
		fieldPane := func() {
			ui.Column(c).Fill().MinHeight(0).Padding(4).Gap(4).Children(func() {
				col, err := strconv.Atoi(strings.TrimPrefix(t.grid.Cells.Cursor, "sql_"))
				if err != nil {
					col = 0
				}
				title := locales.Text("字段信息")
				if col >= 0 && col < len(set.Columns) {
					title += " · " + set.Columns[col].Name
				}
				ui.Row(c).Children(func() {
					ui.Text(c, title).SingleLine().Bold().Grow(1)
					ui.Button(c, locales.Text("位置")).Menu(func(m *ui.Menu) {
						if m.Item(locales.Text("下方")).Checked(!t.resultFieldRight).Chosen() {
							t.resultFieldRight = false
						}
						if m.Item(locales.Text("右侧")).Checked(t.resultFieldRight).Chosen() {
							t.resultFieldRight = true
						}
					})
					if theme.IconButton(c, "", "close").Label(locales.Text("隐藏字段信息")).Clicked() {
						t.resultFieldOpen = false
					}
				})
				if t.selectedRow >= 0 && t.selectedRow < len(set.Rows) && col >= 0 && col < len(set.Rows[t.selectedRow]) {
					gridview.FieldPreview(c, set.Rows[t.selectedRow][col], &t.resultFieldMode, func(e ui.Element) {
						theme.BindNativeEdit(c, e, fmt.Sprintf("sql/field/%d", t.id), s.deps.EditFocus, s.deps.EditTarget, func() bool { return !t.closed && t.resultFieldOpen }, false, false)
					})
				} else {
					ui.Text(c, locales.Text("选择一个单元格"))
				}
			})
		}
		if t.resultFieldOpen {
			if t.fieldSplit == 0 {
				t.fieldSplit = 65
			}
			if t.resultFieldRight {
				ui.SplitPercent(c, &t.fieldSplit, tablePane, fieldPane).Grow(1)
			} else {
				ui.SplitVerticalPercent(c, &t.fieldSplit, tablePane, fieldPane).Grow(1)
			}
		} else {
			tablePane()
		}

	})
}

func (s *State) previewView(c *ui.Context, tab *Document) {
	palette := theme.Colors(c)

	ui.SplitVerticalPercent(c, &s.SplitPercent,
		func() {
			ui.Column(c).Fill().Children(func() {
				ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(2, 6).Gap(6).AlignItems(ui.Center).
					Background(palette.Header).Children(func() {
					ui.Toolbar(c, func() {
						if ui.Button(c, locales.Text("运行模拟查询")).Height(theme.RowHeight(c, theme.ControlHeight)).Clicked() {
							s.demoRuns++
							s.report(fmt.Sprintf(locales.Text("模拟查询已运行 %d 次（没有访问数据库）"), s.demoRuns))
						}
						ui.Button(c, locales.Text("取消")).Height(theme.RowHeight(c, theme.ControlHeight)).Disabled(true)
					}).Label(locales.Text("查询操作")).Grow(1).MinWidth(0).Padding(0).Gap(4)
					ui.Text(c, "query.sql").TextColor(palette.Muted).FontSize(theme.Font(c, theme.SmallFontSize)).SingleLine()
				})
				theme.ApplySQLAppearance(c, tab.Editor)
				editor.View(c, tab.Editor).Grow(1).FillWidth()
			})
		},
		func() {
			if s.deps.Preview != nil {
				s.deps.Preview(c)
			}
		},
	).Grow(1)
}
