package sqlworkspace

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

type executionResult struct {
	request   uint64
	profileID int64
	data      mysqlsvc.MySQLSQLResult
	err       error
}

func (s *State) Run(invalidate func(), t *Document) {
	if t == nil || t.closed || t.preview || t.Editor == nil || t.loading {
		return
	}
	if t.recoveryNeeded {
		t.err = "原 SQL 会话已失效；请确认状态可能丢失后点击「以新会话继续」，不会自动重跑 SQL"
		s.report(t.err)
		return
	}
	if t.profileID == 0 || !s.connected(t.profileID) {
		t.err = "没有可用的 MySQL 连接；请先连接并新建查询"
		s.report(t.err)
		return
	}
	statement := strings.TrimSpace(t.Editor.SelectedText())
	if statement == "" {
		statement = sqlAtCaret(t.Editor.Buffer.String(), t.Editor.Cursor())
	}
	if statement == "" {
		t.err = "请输入 SQL"
		s.report(t.err)
		return
	}
	execute := s.Executor
	if execute == nil {
		if s.runtime() == nil {
			t.err = "MySQL 会话管理器不可用"
			s.report(t.err)
			return
		}
		if t.lease == nil {
			t.err = "查询会话已失效，请新建 SQL 标签后重试"
			s.report(t.err)
			return
		}
		lease := t.lease
		execute = func(ctx context.Context, _ int64, database, statement string) (mysqlsvc.MySQLSQLResult, error) {
			return lease.Execute(ctx, database, statement)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.request++
	request := t.request
	t.cancel = cancel
	t.loading = true
	t.cancelRequested = false
	t.result = nil
	t.err = ""
	profileID, database := t.profileID, t.database
	resultChan := make(chan executionResult, 1)
	t.results = resultChan
	s.report("正在执行 SQL")
	go func() {
		result, err := execute(ctx, profileID, database, statement)
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		resultChan <- executionResult{request: request, profileID: profileID, data: result, err: err}
		if invalidate != nil {
			invalidate()
		}
	}()
}

// RequestCancel keeps the document busy until the driver acknowledges the
// canceled context. In particular, a submitted DML statement might have
// already committed; cancellation is never described as a rollback.
func (s *State) RequestCancel(t *Document) {
	if t == nil || !t.loading || t.cancelRequested {
		return
	}
	t.cancelRequested = true
	t.err = "已请求取消；服务器端更改可能已生效"
	if t.cancel != nil {
		t.cancel()
	}
	if s.active(t) {
		s.report(t.err)
	}
}

// For tab close or disconnect, discard any subsequent result and cancel the
// worker. A buffered channel prevents the worker from blocking on delivery.
func (s *State) dispose(t *Document) {
	if t == nil {
		return
	}
	t.request++
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	if t.prepareCancel != nil {
		t.prepareCancel()
		t.prepareCancel = nil
	}
	t.prepareResults = nil
	t.results = nil
	t.loading = false
	t.cancelRequested = false
	if t.lease != nil {
		t.lease.Release()
		t.lease = nil
	}
}

func (s *State) DisconnectProfile(profileID int64) {
	for _, t := range s.documents {
		if t.profileID == profileID {
			s.dispose(t)
			t.recoveryNeeded = true
			t.recoveryProfile = t.profileID
			t.recoverySchema = t.database
			// Retain the unsaved editor, but sever the old session identity.
			// A later Connect for the same profile must not let this tab
			// silently obtain a new physical connection and lose the old
			// transaction or user-variable state without warning.
			t.profileID = 0
			t.database = ""
			t.result = nil
			t.err = "连接已断开；SQL 文本已保留。请先连接原配置，再以新会话继续；事务、临时表与变量不能恢复"
		}
	}
}

func (s *State) Consume() {
	for _, t := range s.documents {
		if t.prepareResults != nil {
			select {
			case err := <-t.prepareResults:
				t.prepareResults = nil
				if t.prepareCancel != nil {
					t.prepareCancel()
					t.prepareCancel = nil
				}
				if err != nil && !errors.Is(err, context.Canceled) &&
					!errors.Is(err, mysqlsvc.ErrMySQLNotConnected) &&
					!t.loading && t.result == nil && t.err == "" {
					t.err = "SQL 预连接失败：" + err.Error()
					if errors.Is(err, mysqlsvc.ErrMySQLSQLSessionLost) {
						t.recoveryNeeded = true
						t.recoveryProfile = t.profileID
						t.recoverySchema = t.database
					}
				}
			default:
			}
		}
		if t.results == nil {
			continue
		}
		select {
		case r := <-t.results:
			if r.request != t.request || r.profileID != t.profileID {
				continue
			}
			if t.cancel != nil {
				t.cancel()
				t.cancel = nil
			}
			t.results = nil
			t.loading = false
			canceled := t.cancelRequested
			t.cancelRequested = false
			if r.err != nil {
				if canceled || errors.Is(r.err, context.Canceled) {
					t.err = "SQL 执行已停止；服务器端更改可能已生效"
				} else if errors.Is(r.err, mysqlsvc.ErrMySQLSQLSessionLost) {
					t.recoveryNeeded = true
					t.recoveryProfile = t.profileID
					t.recoverySchema = t.database
					t.err = "原查询会话已失效；事务、临时表、会话变量无法恢复，DML 结果请先核对。可选择以新会话继续"
				} else if errors.Is(r.err, mysqlsvc.ErrMySQLSQLSessionLimit) {
					t.err = "SQL 标签会话数已达到上限，请关闭其他查询标签后重试"
				} else {
					t.err = r.err.Error()
				}
				if s.active(t) {
					s.report("SQL 执行失败：" + t.err)
				}
				continue
			}
			result := r.data
			t.result = &result
			t.grid = ui.ListState{}
			if canceled {
				t.err = "取消请求可能未阻止执行；请检查服务器端结果"
			} else {
				t.err = ""
			}
			if s.active(t) {
				if len(result.Columns) > 0 {
					s.report(fmt.Sprintf("SQL 已完成 · %d 行 · %s", len(result.Rows), result.Duration))
				} else {
					s.report(fmt.Sprintf("SQL 已完成 · 影响 %d 行 · %s", result.AffectedRows, result.Duration))
				}
				if canceled {
					s.report(t.err)
				}
			}
		default:
		}
	}
}

// preconnect never blocks the native UI and never executes SQL.
func (s *State) preconnect(t *Document) {
	if t.lease == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	t.prepareCancel, t.prepareResults = cancel, done
	lease, invalidate := t.lease, s.Invalidate
	go func() {
		done <- lease.Preconnect(ctx)
		if invalidate != nil {
			invalidate()
		}
	}()
}

func (s *State) Recover(t *Document) {
	if t == nil || t.closed || !t.recoveryNeeded ||
		t.loading || t.prepareResults != nil {
		return
	}
	id := t.recoveryProfile
	runtime := s.runtime()
	if id <= 0 || runtime == nil || !runtime.IsConnected(id) || !s.connected(id) {
		t.err = "请先连接原 MySQL 配置，再选择以新会话继续"
		s.report(t.err)
		return
	}
	var (
		lease Lease
		err   error
	)
	if t.lease == nil {
		lease, err = runtime.Reserve(id, t.id)
	} else {
		lease, err = t.lease.Replace()
		if errors.Is(err, mysqlsvc.ErrMySQLSQLSessionLost) {
			// The profile was explicitly reconnected since this tab was
			// created; a stale generation may not be used to replace the
			// new profile's leases. The tab ID is unique to this workspace.
			lease, err = runtime.Reserve(id, t.id)
		}
	}
	if err != nil {
		t.err = "新会话创建失败：" + err.Error()
		s.report(t.err)
		return
	}
	if t.lease != nil {
		t.lease.Release()
	}
	t.lease = lease
	t.profileID = id
	t.database = t.recoverySchema
	t.recoveryNeeded = false
	t.recoveryProfile = 0
	t.recoverySchema = ""
	t.result = nil
	t.grid = ui.ListState{}
	t.err = ""
	if s.deps.Recovered != nil {
		s.deps.Recovered(id)
	}
	s.report("已创建新 SQL 会话；未执行任何 SQL，请自行核对事务结果后再运行")
	s.preconnect(t)
}
