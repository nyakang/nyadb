package shell

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type mysqlSQLExecutor func(context.Context, int64, string, string) (mysqlsvc.MySQLSQLResult, error)

type mysqlSQLResult struct {
	request   uint64
	profileID int64
	data      mysqlsvc.MySQLSQLResult
	err       error
}

// Background workers never change MyGo UI state; the frame consumes bounded
// results after verifying the request number and owning profile.
func (s *State) runMySQLSQL(services ui.Services, t *tab) {
	if !s.profileMode || t == nil || t.kind != tabSQL || t.editor == nil || t.sqlLoading {
		return
	}
	if t.sqlRecoveryNeeded {
		t.sqlError = "原 SQL 会话已失效；请确认状态可能丢失后点击「以新会话继续」，不会自动重跑 SQL"
		s.status = t.sqlError
		return
	}
	if t.sqlProfileID == 0 || s.sessions.StatusFor(t.sqlProfileID) != "已连接" {
		t.sqlError = "没有可用的 MySQL 连接；请先连接并新建查询"
		s.status = t.sqlError
		return
	}
	statement := strings.TrimSpace(t.editor.SelectedText())
	if statement == "" {
		statement = sqlAtCaret(t.editor.Buffer.String(), t.editor.Cursor())
	}
	if statement == "" {
		t.sqlError = "请输入 SQL"
		s.status = t.sqlError
		return
	}
	execute := s.mysqlSQLExecutor
	if execute == nil {
		if s.sessions.Manager == nil {
			t.sqlError = "MySQL 会话管理器不可用"
			s.status = t.sqlError
			return
		}
		if t.sqlLease.IsZero() {
			t.sqlError = "查询会话已失效，请新建 SQL 标签后重试"
			s.status = t.sqlError
			return
		}
		manager, lease := s.sessions.Manager, t.sqlLease
		// The SQL tab retains its physical MySQL session across executions.
		execute = func(ctx context.Context, profileID int64, database, statement string) (mysqlsvc.MySQLSQLResult, error) {
			return manager.ExecuteSQLTabToken(ctx, lease, database, statement)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.sqlRequest++
	request := t.sqlRequest
	t.sqlCancel = cancel
	t.sqlLoading = true
	t.sqlCancelRequested = false
	t.sqlResult = nil
	t.sqlError = ""
	profileID, database := t.sqlProfileID, t.sqlDatabase
	resultChan := make(chan mysqlSQLResult, 1)
	t.sqlResults = resultChan
	s.status = "正在执行 SQL"
	go func() {
		result, err := execute(ctx, profileID, database, statement)
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		resultChan <- mysqlSQLResult{request: request, profileID: profileID, data: result, err: err}
		services.Invalidate()
	}()
}

// requestCancelMySQLSQL keeps the tab busy until the driver acknowledges the
// canceled context. In particular, a submitted DML statement might have
// already committed; cancellation is never described as a rollback.
func (s *State) requestCancelMySQLSQL(t *tab) {
	if t == nil || !t.sqlLoading || t.sqlCancelRequested {
		return
	}
	t.sqlCancelRequested = true
	t.sqlError = "已请求取消；服务器端更改可能已生效"
	if t.sqlCancel != nil {
		t.sqlCancel()
	}
	if s.currentTab() == t {
		s.status = t.sqlError
	}
}

// For tab close or disconnect, discard any subsequent result and cancel the
// worker. A buffered channel prevents the worker from blocking on delivery.
func (s *State) cancelMySQLSQL(t *tab) {
	if t == nil {
		return
	}
	t.sqlRequest++
	if t.sqlCancel != nil {
		t.sqlCancel()
		t.sqlCancel = nil
	}
	if t.sqlPrepareCancel != nil {
		t.sqlPrepareCancel()
		t.sqlPrepareCancel = nil
	}
	t.sqlPrepareResults = nil
	t.sqlResults = nil
	t.sqlLoading = false
	t.sqlCancelRequested = false
	if t.kind == tabSQL && s.sessions.Manager != nil {
		s.sessions.Manager.ReleaseSQLTabToken(t.sqlLease)
	}
	t.sqlLease = mysqlsvc.MySQLSQLTabToken{}
}
func (s *State) cancelMySQLSQLForProfile(profileID int64) {
	for _, t := range s.tabs {
		if t.kind == tabSQL && t.sqlProfileID == profileID {
			s.cancelMySQLSQL(t)
			t.sqlRecoveryNeeded = true
			t.sqlRecoveryProfile = t.sqlProfileID
			t.sqlRecoverySchema = t.sqlDatabase
			// Retain the unsaved editor, but sever the old session identity.
			// A later Connect for the same profile must not let this tab
			// silently obtain a new physical connection and lose the old
			// transaction or user-variable state without warning.
			t.sqlProfileID = 0
			t.sqlDatabase = ""
			t.sqlResult = nil
			t.sqlError = "连接已断开；SQL 文本已保留。请先连接原配置，再以新会话继续；事务、临时表与变量不能恢复"
		}
	}
}
func (s *State) consumeMySQLSQLResults() {
	for _, t := range s.tabs {
		if t.kind != tabSQL {
			continue
		}
		if t.sqlPrepareResults != nil {
			select {
			case err := <-t.sqlPrepareResults:
				t.sqlPrepareResults = nil
				if t.sqlPrepareCancel != nil {
					t.sqlPrepareCancel()
					t.sqlPrepareCancel = nil
				}
				if err != nil && !errors.Is(err, context.Canceled) &&
					!errors.Is(err, mysqlsvc.ErrMySQLNotConnected) &&
					!t.sqlLoading && t.sqlResult == nil && t.sqlError == "" {
					t.sqlError = "SQL 预连接失败：" + err.Error()
					if errors.Is(err, mysqlsvc.ErrMySQLSQLSessionLost) {
						t.sqlRecoveryNeeded = true
						t.sqlRecoveryProfile = t.sqlProfileID
						t.sqlRecoverySchema = t.sqlDatabase
					}
				}
			default:
			}
		}
		if t.sqlResults == nil {
			continue
		}
		select {
		case r := <-t.sqlResults:
			if r.request != t.sqlRequest || r.profileID != t.sqlProfileID {
				continue
			}
			if t.sqlCancel != nil {
				t.sqlCancel()
				t.sqlCancel = nil
			}
			t.sqlResults = nil
			t.sqlLoading = false
			canceled := t.sqlCancelRequested
			t.sqlCancelRequested = false
			if r.err != nil {
				if canceled || errors.Is(r.err, context.Canceled) {
					t.sqlError = "SQL 执行已停止；服务器端更改可能已生效"
				} else if errors.Is(r.err, mysqlsvc.ErrMySQLSQLSessionLost) {
					t.sqlRecoveryNeeded = true
					t.sqlRecoveryProfile = t.sqlProfileID
					t.sqlRecoverySchema = t.sqlDatabase
					t.sqlError = "原查询会话已失效；事务、临时表、会话变量无法恢复，DML 结果请先核对。可选择以新会话继续"
				} else if errors.Is(r.err, mysqlsvc.ErrMySQLSQLSessionLimit) {
					t.sqlError = "SQL 标签会话数已达到上限，请关闭其他查询标签后重试"
				} else {
					t.sqlError = r.err.Error()
				}
				if s.currentTab() == t {
					s.status = "SQL 执行失败：" + t.sqlError
				}
				continue
			}
			result := r.data
			t.sqlResult = &result
			t.sqlGrid = ui.ListState{}
			if canceled {
				t.sqlError = "取消请求可能未阻止执行；请检查服务器端结果"
			} else {
				t.sqlError = ""
			}
			if s.currentTab() == t {
				if len(result.Columns) > 0 {
					s.status = fmt.Sprintf("SQL 已完成 · %d 行 · %s", len(result.Rows), result.Duration)
				} else {
					s.status = fmt.Sprintf("SQL 已完成 · 影响 %d 行 · %s", result.AffectedRows, result.Duration)
				}
				if canceled {
					s.status = t.sqlError
				}
			}
		default:
		}
	}
}

func (s *State) mysqlQueryWorkspace(c *ui.Context, t *tab) {
	p := theme.Colors(c)
	connected := t.sqlProfileID != 0 && s.sessions.StatusFor(t.sqlProfileID) == "已连接"
	ui.SplitVerticalPercent(c, &s.editorPercent, func() {
		ui.Column(c).Fill().Children(func() {
			ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Padding(2, 6).Gap(7).
				AlignItems(ui.Center).Background(p.Header).Children(func() {
				ui.Toolbar(c, func() {
					if ui.Button(c, locales.Text("运行")).Label("运行当前或选中 SQL").
						Disabled(!connected || t.sqlLoading || t.sqlRecoveryNeeded).Height(theme.RowHeight(c, theme.ControlHeight)).Clicked() {
						s.runMySQLSQL(c.Services(), t)
					}
					if ui.Button(c, locales.Text("取消")).Disabled(!t.sqlLoading || t.sqlCancelRequested).
						Height(theme.RowHeight(c, theme.ControlHeight)).Clicked() {
						s.requestCancelMySQLSQL(t)
					}
				}).Label("SQL 执行操作").Grow(1).MinWidth(0).Padding(0).Gap(5)
				schema := t.sqlDatabase
				if schema == "" {
					schema = "未指定数据库"
				}
				ui.Text(c, fmt.Sprintf("连接 #%d · %s", t.sqlProfileID, schema)).
					TextColor(p.Muted).FontSize(theme.Font(c, 11)).SingleLine()
			})
			if t.sqlError != "" {
				ui.Text(c, t.sqlError).Padding(3, 8).FontSize(theme.Font(c, 11)).
					TextColor(p.Error).SingleLine().Tooltip(t.sqlError)
			} else if !connected {
				ui.Text(c, locales.Text("请连接 MySQL 后新建查询")).Padding(3, 8).
					TextColor(p.Muted).FontSize(theme.Font(c, 11))
			}
			if t.sqlRecoveryNeeded {
				id := t.sqlRecoveryProfile
				canRecover := !t.sqlLoading && t.sqlPrepareResults == nil &&
					id > 0 && s.sessions.Manager != nil &&
					s.sessions.StatusFor(id) == "已连接" && s.sessions.Manager.IsConnected(id)
				ui.Row(c).Padding(3, 8).Gap(9).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "新会话无法恢复事务或临时状态；不会自动执行 SQL").
						FontSize(theme.Font(c, 11)).TextColor(p.Muted)
					if ui.Button(c, "以新会话继续").Label("建立新物理会话（不执行现有 SQL）").
						Disabled(!canRecover).Clicked() {
						s.recoverMySQLSQLTab(t)
					}
				})
			}
			t.editor.Theme = theme.SQLThemeFor(c)
			editor.View(c, t.editor).Grow(1).FillWidth()
		})
	}, func() { s.mysqlSQLResultGrid(c, t) }).Grow(1)
}

func (s *State) mysqlSQLResultGrid(c *ui.Context, t *tab) {
	p := theme.Colors(c)
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, theme.CommandBarHeight)).Background(p.Header).
			AlignItems(ui.Center).Padding(2, 7).Gap(8).Children(func() {
			ui.Text(c, locales.Text("查询结果")).FontSize(theme.Font(c, 12)).Bold()
			ui.Box(c).Grow(1)
			message := "尚未执行"
			if t.sqlLoading {
				message = "正在执行…"
			}
			if t.sqlCancelRequested {
				message = "正在等待取消确认…"
			}
			if r := t.sqlResult; r != nil {
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
		if t.sqlResult == nil {
			ui.Box(c).Grow(1)
			return
		}
		r := t.sqlResult
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
		theme.Table(c, &t.sqlGrid, cols, len(r.Rows), func(row, col int) {
			ui.Text(c, r.Rows[row][col]).SingleLine().FontSize(theme.Font(c, 12))
		}).Grow(1)
	})
}
