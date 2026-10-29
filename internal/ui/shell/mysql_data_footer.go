package shell

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

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

func (s *State) cancelMySQLDataRead(t *tab) {
	if t == nil || !t.dataLoading {
		return
	}
	s.cancelMySQLData(t)
	t.dataErr = ""
	t.dataReadInfo.Canceled = true
}

func (s *State) changeMySQLDataPageSize(services ui.Services, t *tab, limit int) {
	if s.guardMySQLDataLeave(t, func(next ui.Services) { s.changeMySQLDataPageSize(next, t, limit) }) {
		return
	}
	if t == nil || t.dataLoading || t.dataLimit == limit ||
		(limit != 50 && limit != 100 && limit != mysqlsvc.MySQLDataMaxPageSize) {
		return
	}
	t.dataLimit = limit
	s.resetAndReloadMySQLData(services, t)
}

func mysqlDataQueryStatus(t *tab) string {
	if t.dataReadInfo.QuerySQL == "" {
		return locales.Text("查询信息不可用")
	}
	if t.dataLoading || t.dataErr != "" || t.dataReadInfo.Canceled || t.dataPage == nil {
		return locales.Text("上次成功查询（参数化）：") + t.dataReadInfo.QuerySQL
	}
	return locales.Text("参数化 SQL：") + t.dataReadInfo.QuerySQL
}

func mysqlDataReadStatus(t *tab) string {
	info := t.dataReadInfo
	if info.CompletedAt.IsZero() {
		return locales.Text("尚无成功读取时间")
	}
	return fmt.Sprintf("%s %s · %d ms", locales.Text("上次成功读取"),
		info.CompletedAt.Local().Format("15:04:05"), max(0, info.Duration.Milliseconds()))
}

func (s *State) mysqlDataFooter(c *ui.Context, t *tab, page *mysqlsvc.MySQLDataPage) {
	palette := theme.Colors(c)
	s.consumeMySQLDataExport(t)

	ui.Column(c).FillWidth().Shrink(0).Background(palette.Header).Children(func() {
		if t.dataEdit.Batch && mysqlDataHasDraft(t) {
			ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Shrink(0).Padding(2, 6).Gap(6).AlignItems(ui.Center).Background(ui.Hex("#ffa940")).Children(func() {
				ui.Text(c, fmt.Sprintf(locales.Text("%d 条待处理记录"), mysqlDataPendingRecordCount(t))).FontSize(theme.Font(c, theme.SmallFontSize)).TextColor(ui.Hex("#2b1900")).SingleLine().Grow(1).MinWidth(0)
				ready := !t.dataLoading && !t.dataEdit.Busy && t.dataEdit.Uncertain == nil
				if ui.Button(c, locales.Text("应用更改")).Label(locales.Text("应用待处理记录")).Height(theme.ControlHeight).Disabled(!ready || t.dataProfileID != s.mysqlSessionProfileID || s.mysqlSessionStatus != "已连接").Clicked() {
					s.saveMySQLData(c.Services(), t)
				}
				if ui.Button(c, locales.Text("放弃")).Label(locales.Text("放弃待处理记录")).Height(theme.ControlHeight).Disabled(!ready).Clicked() {
					s.discardMySQLData(t)
				}
			})
		}
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Shrink(0).Padding(2, 6).Gap(4).AlignItems(ui.Center).Children(func() {
			ready := t.dataPage != nil && !t.dataLoading && !t.dataEdit.Busy && t.dataEdit.Uncertain == nil && t.dataProfileID == s.mysqlSessionProfileID && s.mysqlSessionStatus == "已连接"
			dirty := mysqlDataHasDraft(t)
			insertReady := ready && len(page.ColumnMetadata) > 0 && len(page.ColumnMetadata) == len(page.Columns)
			if ui.Button(c, "+").Label(locales.Text("添加记录")).Tooltip(locales.Text("添加记录")).Size(26, theme.ControlHeight).Padding(0).Disabled(!insertReady).Clicked() {
				s.addMySQLDataRecord(c.Services(), t)
			}
			if ui.Button(c, "−").Label(locales.Text("删除记录")).Tooltip(locales.Text("删除记录")).Size(26, theme.ControlHeight).Padding(0).Disabled(!ready || !mysqlDataCanDeleteSelection(t, t.dataSelectedRow)).Clicked() {
				s.deleteMySQLDataRecords(c.Services(), t, t.dataSelectedRow)
			}
			if ui.Button(c, "✓").Label(locales.Text("保存数据更改")).Tooltip(locales.Text("保存数据更改")).Size(26, theme.ControlHeight).Padding(0).Disabled(!ready || !dirty || t.dataEdit.Uncertain != nil).Clicked() {
				s.saveMySQLData(c.Services(), t)
			}
			if ui.Button(c, "×").Label(locales.Text("取消数据更改")).Tooltip(locales.Text("取消数据更改")).Size(26, theme.ControlHeight).Padding(0).Disabled(t.dataLoading || t.dataEdit.Busy || !dirty || t.dataEdit.Uncertain != nil).Clicked() {
				s.discardMySQLData(t)
			}
			if ui.Button(c, "↻").Label(locales.Text("刷新数据并返回首页")).Tooltip(locales.Text("刷新数据并返回首页")).Size(26, theme.ControlHeight).Padding(0).Disabled(!ready).Clicked() {
				s.resetAndReloadMySQLData(c.Services(), t)
			}
			if ui.Button(c, "■").Label(locales.Text("停止数据操作")).Tooltip(locales.Text("停止数据操作")).Size(26, theme.ControlHeight).Padding(0).Disabled(!t.dataLoading && !t.dataEdit.Busy).Clicked() {
				if t.dataEdit.Busy {
					s.stopMySQLDataEdit(t)
				} else {
					s.cancelMySQLDataRead(t)
				}
			}
			ui.Checkbox(c, &t.dataEdit.Batch, locales.Text("批量编辑")).Disabled(dirty || t.dataLoading || t.dataEdit.Busy || t.dataEdit.Uncertain != nil).FontSize(theme.Font(c, theme.SmallFontSize))
			s.mysqlDataEditStatus(c, t)
		})
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Shrink(0).Padding(2, 6).Gap(4).AlignItems(ui.Center).Children(func() {
			status := mysqlDataPageStatus(t)
			if t.dataPage != nil && !t.dataLoading && t.dataErr == "" && !t.dataReadInfo.Canceled &&
				t.dataSelectedRow >= 0 && t.dataSelectedRow < len(page.Rows) {
				status += fmt.Sprintf(" · %s %d", locales.Text("当前记录"), t.dataOffset+int64(t.dataSelectedRow)+1)
			}
			ui.Text(c, status).FontSize(theme.Font(c, theme.SmallFontSize)).
				TextColor(palette.Muted).SingleLine().Grow(1).MinWidth(0).Tooltip(status)
			previousDisabled := t.dataLoading || t.dataEdit.Busy || t.dataEdit.Uncertain != nil || t.dataPage == nil || t.dataOffset <= 0
			if ui.Button(c, "«").Label(locales.Text("首页数据")).Tooltip(locales.Text("首页")).
				Size(26, theme.ControlHeight).Padding(0).Disabled(previousDisabled).Clicked() {
				s.changeMySQLDataPage(c.Services(), t, 0)
			}
			if ui.Button(c, "‹").Label(locales.Text("上一页数据")).Tooltip(locales.Text("上一页")).
				Size(26, theme.ControlHeight).Padding(0).Disabled(previousDisabled).Clicked() {
				s.changeMySQLDataPage(c.Services(), t, max(0, t.dataOffset-int64(t.dataLimit)))
			}
			ui.Text(c, fmt.Sprintf(locales.Text("第 %d 页"), t.dataOffset/int64(max(1, t.dataLimit))+1)).
				Label(locales.Text("当前数据页")).FontSize(theme.Font(c, theme.SmallFontSize)).SingleLine()
			if ui.Button(c, "›").Label(locales.Text("下一页数据")).Tooltip(locales.Text("下一页")).
				Size(26, theme.ControlHeight).Padding(0).Disabled(t.dataLoading || t.dataEdit.Busy || t.dataEdit.Uncertain != nil || t.dataPage == nil || !page.HasMore).Clicked() {
				s.changeMySQLDataPage(c.Services(), t, t.dataOffset+int64(t.dataLimit))
			}
			ui.Button(c, fmt.Sprintf("%d / %s", t.dataLimit, locales.Text("页"))).Label(locales.Text("每页行数")).
				Height(theme.RowHeight(c, theme.ControlHeight)).Padding(2, 4).Disabled(t.dataLoading || t.dataEdit.Busy || t.dataEdit.Uncertain != nil).Menu(func(m *ui.Menu) {
				for _, limit := range []int{50, 100, mysqlsvc.MySQLDataMaxPageSize} {
					if m.Item(fmt.Sprintf("%d %s", limit, locales.Text("行 / 页"))).Checked(t.dataLimit == limit).Chosen() {
						s.changeMySQLDataPageSize(c.Services(), t, limit)
					}
				}
			})

		})
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Shrink(0).Padding(2, 6).Gap(6).AlignItems(ui.Center).
			BorderWidth(1, 0, 0, 0).BorderColor(palette.Border).Children(func() {
			ui.Button(c, "SQL").Label(locales.Text("查询 SQL 操作")).Height(theme.RowHeight(c, theme.ControlHeight)).Padding(2, 4).
				Menu(func(m *ui.Menu) {
					if m.Item(locales.Text("复制参数化 SQL")).Disabled(t.dataReadInfo.QuerySQL == "").Chosen() {
						c.WriteClipboard(t.dataReadInfo.QuerySQL)
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

func mysqlDataPendingRecordCount(t *tab) int {
	count := len(t.dataEdit.Drafts)
	if e := t.dataEdit.Editor; e != nil && e.Text != e.Initial && t.dataEdit.Drafts[e.Key] == nil {
		count++
	}
	return count
}
