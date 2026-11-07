package sqlworkspace

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

type executionResult struct {
	statement string
	version   uint64
	origin    int
	explain   bool
	plan      *contract.ExecutionPlan
	planError string
	request   uint64
	profileID int64
	data      contract.SQLResult
	err       error
}

func (s *State) Run(invalidate func(), t *Document) {
	if t == nil || t.closed || t.preview || t.Editor == nil || t.loading {
		return
	}
	if t.recoveryNeeded {
		t.err = "原 SQL 会话已失效；请确认状态可能丢失后点击「以新会话继续」，不会自动重跑 SQL"
		if t.nativeCommands {
			t.err = "原请求会话已失效；请核对服务器端结果后建立新会话，不会自动执行现有请求"
		}
		s.report(t.err)
		return
	}
	if t.profileID == 0 || !s.connected(t.profileID) {
		t.err = "没有可用的数据库连接；请先连接并新建查询"
		s.report(t.err)
		return
	}
	statement := strings.TrimSpace(t.Editor.SelectedText())
	if statement == "" {
		statement = s.statementAtCaret(t)
	}
	if statement == "" {
		t.err = "请输入 SQL"
		if t.nativeCommands {
			t.err = t.commandText("请输入 MongoDB 命令", "请输入 Redis 命令", "请输入 Elasticsearch REST 请求", "请输入 Easysearch REST 请求")
		}
		s.report(t.err)
		return
	}
	s.runStatement(invalidate, t, statement)
}
func (s *State) runStatement(invalidate func(), t *Document, statement string) {
	if t == nil || t.closed || t.loading || t.recoveryNeeded || !s.connected(t.profileID) {
		return
	}
	execute := s.Executor
	if execute == nil {
		if s.runtime() == nil {
			t.err = "数据库会话管理器不可用"
			s.report(t.err)
			return
		}
		if t.lease == nil {
			t.err = "查询会话已失效，请新建 SQL 标签后重试"
			if t.nativeCommands {
				t.err = "请求会话已失效，请新建请求标签后重试"
			}
			s.report(t.err)
			return
		}
		lease := t.lease
		execute = func(ctx context.Context, _ int64, database, statement string) (contract.SQLResult, error) {
			return lease.Execute(ctx, database, statement)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.request++
	request := t.request
	t.cancel = cancel
	t.loading = true
	t.cancelRequested = false
	explaining := strings.HasPrefix(strings.ToUpper(strings.TrimSpace(statement)), "EXPLAIN ")
	if explaining {
		if t.showOrdinary {
			t.ordinaryReading = t.captureReading()
		} else if t.explainReading == nil {
			t.ordinaryReading = t.captureReading()
		}
	} else {
		t.ordinaryReading, t.explainReading = nil, nil
		t.plan = nil
		t.planError = ""
	}
	t.showOrdinary = false
	t.planVisible = false
	if t.result == nil && len(t.snapshots) == 0 && t.layoutMode == "" {
		t.focusEditor = true
	}
	t.rememberResultLayouts()
	t.result = nil
	t.err = ""
	profileID, database := t.profileID, t.database
	resultChan := make(chan executionResult, 1)
	t.results = resultChan
	s.report("正在执行 SQL")
	if t.nativeCommands {
		s.report(t.commandText("正在执行 MongoDB 命令", "正在执行 Redis 命令", "正在执行 Elasticsearch REST 请求", "正在执行 Easysearch REST 请求"))
	}
	source := "query"
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(statement)), "EXPLAIN ") {
		source = "explain"
	}
	version, origin := t.Editor.Buffer.Version(), executionOrigin(t, statement)
	var parse func(contract.SQLResult) (*contract.ExecutionPlan, error)
	if s.deps.PlanParser != nil {
		parse = s.deps.PlanParser(profileID)
	}
	history := s.beginHistory(t, source, statement)
	go func() {
		result, err := execute(ctx, profileID, database, statement)
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		completeHistory(history, result, err, true)
		var plan *contract.ExecutionPlan
		planError := ""
		if explaining && err == nil && parse != nil {
			var e error
			plan, e = parse(result)
			if e != nil {
				planError = e.Error()
			}
		}
		resultChan <- executionResult{statement: statement, version: version, origin: origin, explain: explaining, plan: plan, planError: planError, request: request, profileID: profileID, data: result, err: err}
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
	t.plan = nil
	t.ordinaryReading, t.explainReading = nil, nil
	t.metadata.stop()
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
			t.recoveryCatalog, t.recoveryNamespaceSchema = t.catalog, t.namespaceSchema
			// Retain the unsaved editor, but sever the old session identity.
			// A later Connect for the same profile must not let this tab
			// silently obtain a new physical connection and lose the old
			// transaction or user-variable state without warning.
			t.profileID = 0
			t.database = ""
			t.rememberResultLayouts()
			t.result = nil
			t.err = "连接已断开；SQL 文本已保留。请先连接原配置，再以新会话继续；事务、临时表与变量不能恢复"
			if t.nativeCommands {
				t.err = t.commandText("连接已断开；MongoDB 命令已保留。请连接原配置，再以新会话继续；不会自动执行命令", "连接已断开；Redis 命令已保留。请连接原配置，再以新会话继续；不会自动执行命令", "连接已断开；REST 请求已保留。请连接原配置，再以新会话继续；不会自动执行请求", "连接已断开；REST 请求已保留。请连接原配置，再以新会话继续；不会自动执行请求")
			}
		}
	}
}

func (s *State) Consume() {
	s.consumeHistory()
	for _, t := range s.documents {
		s.consumeCompletionMetadata(t)
		if t.prepareResults != nil {
			select {
			case err := <-t.prepareResults:
				t.prepareResults = nil
				if t.prepareCancel != nil {
					t.prepareCancel()
					t.prepareCancel = nil
				}
				if err != nil && !errors.Is(err, context.Canceled) &&
					!errors.Is(err, contract.ErrRevoked) &&
					!t.loading && t.result == nil && t.err == "" {
					t.err = "SQL 预连接失败：" + err.Error()
					if t.nativeCommands {
						t.err = "请求会话预连接失败：" + err.Error()
					}
					if errors.Is(err, contract.ErrSQLSessionLimit) {
						t.err = "SQL 会话数量已达上限，请关闭其他查询标签后重试"
						if t.nativeCommands {
							t.err = "请求会话数量已达上限，请关闭其他请求标签后重试"
						}
					}
					if errors.Is(err, contract.ErrSQLSessionLost) || errors.Is(err, contract.ErrRevoked) {
						t.recoveryNeeded = true
						t.recoveryProfile = t.profileID
						t.recoverySchema = t.database
						t.recoveryCatalog, t.recoveryNamespaceSchema = t.catalog, t.namespaceSchema
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
			s.acceptQueryDiagnostic(t, r)
			if r.explain {
				if r.err != nil {
					t.planError = r.err.Error()
					t.explainReading = &resultReading{Views: map[int]*resultView{}}
				}
				t.plan, t.planError, t.planVisible = r.plan, r.planError, r.plan != nil
				if r.err != nil {
					t.planError = r.err.Error()
				}
			}
			if r.err != nil {
				lost := errors.Is(r.err, contract.ErrSQLSessionLost) || errors.Is(r.err, contract.ErrRevoked)
				if lost {
					t.recoveryNeeded = true
					t.recoveryProfile = t.profileID
					t.recoverySchema = t.database
					t.recoveryCatalog, t.recoveryNamespaceSchema = t.catalog, t.namespaceSchema
				}
				if len(r.data.Steps) > 0 {
					result := r.data
					t.setResult(&result)
				}
				if canceled || errors.Is(r.err, context.Canceled) {
					t.err = "SQL 执行已停止；服务器端更改可能已生效"
					if t.nativeCommands {
						t.err = "请求执行已停止；服务器端更改可能已生效"
					}
					if lost {
						if t.nativeCommands {
							t.err += "；" + locales.Text("原请求会话已失效；请先核对服务器端结果，再建立新会话。不会自动执行现有请求")
						} else {
							t.err += "；" + locales.Text("原查询会话已失效；事务、临时表、会话变量无法恢复，DML 结果请先核对。可选择以新会话继续")
						}
					}
				} else if lost {
					t.err = "原查询会话已失效；事务、临时表、会话变量无法恢复，DML 结果请先核对。可选择以新会话继续"
					if t.nativeCommands {
						t.err = "原请求会话已失效；请先核对服务器端结果，再建立新会话。不会自动执行现有请求"
					}
				} else if errors.Is(r.err, contract.ErrSQLSessionLimit) {
					t.err = "SQL 标签会话数已达到上限，请关闭其他查询标签后重试"
					if t.nativeCommands {
						t.err = "请求会话数量已达上限，请关闭其他请求标签后重试"
					}
				} else {
					t.err = r.err.Error()
				}
				if s.active(t) {
					if t.nativeCommands {
						s.report("请求执行失败：" + t.err)
					} else {
						s.report("SQL 执行失败：" + t.err)
					}
				}
				continue
			}
			result := r.data
			t.setResult(&result)
			if r.explain {
				t.explainReading = t.captureReading()
			}

			if canceled {
				t.err = "取消请求可能未阻止执行；请检查服务器端结果"
			} else {
				t.err = ""
			}
			if s.active(t) {
				if t.nativeCommands {
					s.report(fmt.Sprintf(locales.Text("原生请求已完成 · %d 条响应 · %s"), len(result.Rows), result.Duration))
				} else if len(result.Columns) > 0 {
					s.report(fmt.Sprintf(locales.Text("SQL 已完成 · %d 行 · %s"), len(result.Rows), result.Duration))
				} else {
					s.report(fmt.Sprintf(locales.Text("SQL 已完成 · 影响 %d 行 · %s"), result.AffectedRows, result.Duration))
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
		if t.nativeCommands {
			t.err = "请先连接原配置，再建立新会话；不会自动执行现有请求"
		}
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
		if errors.Is(err, contract.ErrSQLSessionLost) || errors.Is(err, contract.ErrRevoked) {
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
	t.catalog, t.namespaceSchema = t.recoveryCatalog, t.recoveryNamespaceSchema
	t.recoveryNeeded = false
	t.recoveryProfile = 0
	t.recoverySchema = ""
	t.rememberResultLayouts()
	t.result = nil

	t.err = ""
	if s.deps.Recovered != nil {
		s.deps.Recovered(id)
	}
	if t.nativeCommands {
		s.report(locales.Text("已创建新会话；请求尚未执行"))
	} else {
		s.report("已创建新 SQL 会话；未执行任何 SQL，请自行核对事务结果后再运行")
	}
	s.preconnect(t)
}
