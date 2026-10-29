package sqlworkspace

import (
	"fmt"

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
	ui.SplitVerticalPercent(c, &s.SplitPercent, func() {
		ui.Column(c).Fill().Children(func() {
			ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(2, 6).Gap(7).
				AlignItems(ui.Center).Background(p.Header).Children(func() {
				ui.Toolbar(c, func() {
					if ui.Button(c, locales.Text("运行")).Label("运行当前或选中 SQL").
						Disabled(!connected || t.loading || t.recoveryNeeded).Height(theme.RowHeight(c, theme.ControlHeight)).Clicked() {
						s.Run(c.Services().Invalidate, t)
					}
					if ui.Button(c, locales.Text("取消")).Disabled(!t.loading || t.cancelRequested).
						Height(theme.RowHeight(c, theme.ControlHeight)).Clicked() {
						s.RequestCancel(t)
					}
				}).Label("SQL 执行操作").Grow(1).MinWidth(0).Padding(0).Gap(5)
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
				ui.Text(c, locales.Text("请连接 MySQL 后新建查询")).Padding(3, 8).
					TextColor(p.Muted).FontSize(theme.Font(c, 11))
			}
			if t.recoveryNeeded {
				id := t.recoveryProfile
				canRecover := !t.loading && t.prepareResults == nil &&
					id > 0 && s.runtime() != nil && s.connected(id) && s.runtime().IsConnected(id)
				ui.Row(c).Padding(3, 8).Gap(9).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "新会话无法恢复事务或临时状态；不会自动执行 SQL").
						FontSize(theme.Font(c, 11)).TextColor(p.Muted)
					if ui.Button(c, "以新会话继续").Label("建立新物理会话（不执行现有 SQL）").
						Disabled(!canRecover).Clicked() {
						s.Recover(t)
					}
				})
			}
			t.Editor.Theme = theme.SQLThemeFor(c)
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
				if len(r.Columns) > 0 {
					message = fmt.Sprintf("%d 行 / %d 列 · %s", len(r.Rows), len(r.Columns), r.Duration)
				} else {
					message = fmt.Sprintf("影响 %d 行 · %s", r.AffectedRows, r.Duration)
				}
				if r.Truncated {
					message += " · 已截断（限制 1000 行 / 8 MiB）"
				}
			}
			ui.Text(c, message).SingleLine().FontSize(theme.Font(c, 11)).TextColor(p.Muted)
		})
		if t.result == nil {
			ui.Box(c).Grow(1)
			return
		}
		r := t.result
		if len(r.Columns) == 0 {
			ui.Text(c, fmt.Sprintf("执行成功 · 影响 %d 行", r.AffectedRows)).
				Padding(10).FontSize(theme.Font(c, 12))
			ui.Box(c).Grow(1)
			return
		}
		cols := make([]ui.TableColumn, len(r.Columns))
		for i, name := range r.Columns {
			cols[i] = ui.TableColumn{ID: fmt.Sprintf("sql_%d", i), Title: name, Width: 160}
		}
		theme.Table(c, &t.grid, cols, len(r.Rows), func(row, col int) {
			ui.Text(c, r.Rows[row][col]).SingleLine().FontSize(theme.Font(c, 12))
		}).Grow(1)
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
				tab.Editor.Theme = theme.SQLThemeFor(c)
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
