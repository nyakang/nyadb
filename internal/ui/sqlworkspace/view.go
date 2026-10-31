package sqlworkspace

import (
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
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
	ui.SplitVerticalPercent(c, &s.SplitPercent, func() {
		ui.Column(c).Fill().Children(func() {
			ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(2, 6).Gap(7).
				AlignItems(ui.Center).Background(p.Header).Children(func() {
				ui.Toolbar(c, func() {
					if ui.Button(c, locales.Text("格式化 SQL")).Clicked() {
						s.Format(t)
					}
					if ui.Button(c, "EXPLAIN").Disabled(t.loading || !connected).Clicked() {
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

					if ui.Button(c, locales.Text("运行")).Label(locales.Text("运行当前或选中 SQL")).
						Disabled(!connected || t.loading || t.recoveryNeeded).Height(theme.RowHeight(c, theme.ControlHeight)).Clicked() {
						s.Run(c.Services().Invalidate, t)
					}
					if ui.Button(c, locales.Text("取消")).Disabled(!t.loading || t.cancelRequested).
						Height(theme.RowHeight(c, theme.ControlHeight)).Clicked() {
						s.RequestCancel(t)
					}
				}).Label(locales.Text("SQL 执行操作")).Grow(1).MinWidth(0).Padding(0).Gap(5)
				schema := t.database
				if schema == "" {
					schema = "未指定数据库"
				}
				ui.Text(c, fmt.Sprintf("连接 #%d · %s", t.profileID, schema)).
					TextColor(p.Muted).FontSize(theme.Font(c, 11)).SingleLine()
			})
			if t.err != "" {
				ui.Text(c, t.err).Padding(3, 8).FontSize(theme.Font(c, 11)).
					TextColor(p.Error).SingleLine().Tooltip(t.err)
			} else if !connected {
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
					if ui.Button(c, locales.Text("以新会话继续")).Label(locales.Text("建立新物理会话（不执行现有 SQL）")).
						Disabled(!canRecover).Clicked() {
						s.Recover(t)
					}
				})
			}
			theme.ApplySQLAppearance(c, t.Editor)
			editor.View(c, t.Editor).Grow(1).FillWidth()
		})
	}, func() { s.resultGrid(c, t) }).Grow(1)
}

func (s *State) resultGrid(c *ui.Context, t *Document) {
	p := theme.Colors(c)
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Background(p.Header).
			AlignItems(ui.Center).Padding(2, 7).Gap(8).Children(func() {
			ui.Text(c, locales.Text("查询结果")).FontSize(theme.Font(c, 12)).Bold()
			ui.Box(c).Grow(1)
			message := "尚未执行"
			if t.loading {
				message = "正在执行…"
			}
			if t.cancelRequested {
				message = "正在等待取消确认…"
			}
			if r := t.result; r != nil {
				set := selectedResult(t)
				if len(set.Columns) > 0 {
					message = fmt.Sprintf("%d 行 / %d 列 · %s", len(set.Rows), len(set.Columns), r.Duration)
				} else {
					message = fmt.Sprintf("影响 %d 行 · %s", set.AffectedRows, r.Duration)
				}
			}
			ui.Text(c, message).SingleLine().FontSize(theme.Font(c, 11)).TextColor(p.Muted)
		})
		if t.result == nil {
			ui.Box(c).Grow(1)
			return
		}
		r := t.result
		if len(r.Steps) > 0 {
			ui.Scroll(c).MaxHeight(105).Children(func() {
				ui.Column(c).Gap(3).Children(func() {
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
						ui.Text(c, text).SingleLine().Tooltip(step.SQL)
					}
				})
			})
		}
		set := selectedResult(t)
		sets := r.ResultSets()
		if len(sets) > 1 {
			choices := make([]string, len(sets))
			for i := range choices {
				choices[i] = fmt.Sprintf("结果 %d", i+1)
			}
			selected := choices[t.resultSet]
			if ui.Select(c, &selected, choices).Changed() {
				for i, v := range choices {
					if v == selected {
						t.resultSet = i
						t.selection.Clear()
						t.grid = ui.ListState{}
					}
				}
				set = selectedResult(t)
			}
		}
		ui.Row(c).Gap(6).Children(func() {
			if ui.Button(c, locales.Text("复制结果选区")).Clicked() {
				s.copyResult(c, t)
			}
			ui.Button(c, locales.Text("导出当前结果")).Menu(func(m *ui.Menu) {
				for _, format := range []string{"CSV", "TSV", "JSON"} {
					if m.Item(format).Chosen() {
						s.exportResult(t, format, false)
					}
				}
			})
			if len(set.Truncation) > 0 {
				ui.Text(c, truncationText(set.Truncation))
			}
		})
		if len(set.Columns) == 0 {
			ui.Text(c, fmt.Sprintf("影响 %d 行", set.AffectedRows)).
				Padding(10).FontSize(theme.Font(c, 12))
			ui.Box(c).Grow(1)
			return
		}
		cols := make([]ui.TableColumn, len(set.Columns))
		for i, column := range set.Columns {
			name := column.Name
			cols[i] = ui.TableColumn{ID: fmt.Sprintf("sql_%d", i), Title: name, Width: 160, Subtitle: column.DatabaseType}
		}
		t.grid.Selected = &t.selectedRow
		t.grid.Selection = &t.selection
		t.grid.CellSelect = true
		t.grid.DragSelect = true
		table := theme.Table(c, &t.grid, cols, len(set.Rows), func(row, col int) {
			cell := set.Rows[row][col]
			e := ui.Text(c, cell.Display()).SingleLine().FontSize(theme.Font(c, 12))
			if cell.Kind == contract.Null {
				e.TextColor(p.Muted)
			}
			if cell.Truncated {
				e.Tooltip(locales.Text("单元格预览已截断"))
			}
		}).Grow(1)
		if table.Shortcut(ui.Ctrl, ui.KeyC) {
			s.copyResult(c, t)
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
							s.report(fmt.Sprintf("模拟查询已运行 %d 次（没有访问数据库）", s.demoRuns))
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
