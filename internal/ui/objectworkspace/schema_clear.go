package objectworkspace

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type SchemaClearEvent struct {
	Original   contract.SchemaDesignTarget
	Generation uint64 // contextual UI generation, distinct from Original.Generation
	Database   string
	Outcome    contract.SchemaDesignOutcome
}
type schemaClearReply struct {
	snapshot contract.SchemaDesignSnapshot
	outcome  *contract.SchemaDesignOutcome
	err      error
}
type schemaClearOperation struct {
	completed                                                bool
	owner                                                    *schemaDocumentOwner
	applied                                                  *contract.SchemaClearRequest
	resetIdentity                                            bool
	target                                                   contract.SchemaDesignTarget
	generation                                               uint64
	guardAttempt                                             uint64
	database                                                 string
	ops                                                      contract.SchemaDesignOperations
	original                                                 *contract.SchemaDesignSnapshot
	open, busy, applying, blocked, cancelRequested, guarding bool
	confirmation, err                                        string
	cancel                                                   context.CancelFunc
	results                                                  <-chan schemaClearReply
}

func (s *State) CanClearTable(profile int64, database, table string) bool {
	if s.CanChangeTable(profile) {
		return true
	}
	if s.tablePending || s.nativeTablePending() {
		return false
	}
	w := s.session()
	ops, target := s.schemaBinding(profile, database, table)
	return w.Connected && w.ProfileID == profile && target.Generation != 0 && s.operationAvailable("clear_table", target) && ops.Snapshot != nil && (ops.CanClear != nil && ops.Clear != nil && ops.CanClear(target) || s.deps.Availability != nil && s.capabilities(profile).Objects && s.schemaDocumentsAvailable(profile))
}
func (s *State) schemaClearValid(x *schemaClearOperation) bool {
	if x == nil {
		return false
	}
	w := s.session()
	_, actual := s.schemaBinding(x.target.Object.ProfileID, x.database, x.target.Object.Name)
	return w.Connected && w.ProfileID == x.target.Object.ProfileID && w.Generation == x.generation && actual == x.target
}
func (s *State) beginSchemaClear(profile int64, database, table string) {
	if !s.CanClearTable(profile, database, table) || s.CanChangeTable(profile) {
		return
	}
	s.closeSchemaClear()
	s.closeSchemaDrop()
	s.closeSchemaCopy()
	s.closeSchemaRename()
	ops, target := s.schemaBinding(profile, database, table)
	owner, err := s.reserveSchemaDocument(s.nextSchemaDocumentID("clear"), target)
	if err != nil {
		s.report(schemaDesignError(err))
		return
	}
	if owner != nil {
		ops = owner.lease.Operations()
		if ops.Snapshot == nil || ops.Clear == nil {
			owner.close()
			s.report(schemaDesignError(contract.ErrUnsupported))
			return
		}
	}
	s.schemaClear = &schemaClearOperation{owner: owner, target: target, generation: s.session().Generation, database: database, ops: ops, open: true}
}
func (s *State) closeSchemaClear() {
	x := s.schemaClear
	if x != nil {
		if x.cancel != nil {
			x.cancel()
		}
		x.results, x.original, x.applied = nil, nil, nil
		x.cancel, x.ops = nil, contract.SchemaDesignOperations{}
	}
	s.schemaClear = nil // terminal close revokes callbacks; workers own their buffered receipt
	if x != nil {
		x.owner.close()
	}
}
func (s *State) readSchemaClear(services ui.Services) {
	x := s.schemaClear
	if x == nil || !x.open || x.busy || x.completed || !s.schemaClearValid(x) {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan schemaClearReply, 1)
	x.cancel, x.results, x.busy, x.err = cancel, out, true, ""
	if x.owner == nil || !x.owner.blocked {
		x.original, x.blocked = nil, false
	}
	x.confirmation = ""
	load, target := x.ops.Snapshot, x.target
	go func() {
		defer cancel()
		snapshot, err := load(ctx, target)
		out <- schemaClearReply{snapshot: snapshot, err: err}
		services.Invalidate()
	}()
}
func (s *State) schemaClearRequest(x *schemaClearOperation) (contract.SchemaClearRequest, error) {
	if x == nil || x.original == nil {
		return contract.SchemaClearRequest{}, contract.ErrUnsupported
	}
	r := contract.SchemaClearRequest{Original: x.original.Clone(), ResetIdentity: x.resetIdentity}
	if r.Original.Schema.Engine == "databend" {
		_, err := schemaddl.PlanDatabendClear(r.Original.Schema, r.Original.Target.Object.Schema, r.ResetIdentity)
		return r, err
	}
	if r.ResetIdentity && (r.Original.Schema.Engine != "sqlite" || !schemaddl.SQLiteAutoIncrement(r.Original.Schema.Table)) {
		return r, contract.ErrUnsupported
	}
	return r, nil
}
func (s *State) applySchemaClear(services ui.Services) {
	x := s.schemaClear
	if x == nil || !x.open || x.busy || x.blocked || x.guarding || x.original == nil || x.confirmation != x.target.Object.Name || !s.schemaClearValid(x) {
		return
	}
	request, err := s.schemaClearRequest(x)
	if err != nil {
		x.err = schemaDesignError(err)
		return
	}
	x.guardAttempt++
	attempt := x.guardAttempt
	next := func(services ui.Services) {
		if s.schemaClear != x || x.guardAttempt != attempt || !x.open || x.busy || x.blocked || x.confirmation != x.target.Object.Name || !s.schemaClearValid(x) {
			return
		}
		current, err := s.schemaClearRequest(x)
		if err != nil || schemaddl.Signature(current) != schemaddl.Signature(request) {
			x.guarding, x.blocked = false, true
			x.err = schemaDesignError(schemaddl.ErrConflict)
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		out := make(chan schemaClearReply, 1)
		x.guarding = false
		x.cancel, x.results, x.busy, x.applying, x.cancelRequested, x.err = cancel, out, true, true, false, ""
		captured := request.Clone()
		x.applied = &captured
		run, r := x.ops.Clear, request.Clone()
		complete := s.beginActivity(x.target.Object.ProfileID, x.generation, x.database, x.target.Object.Name, "table", "clear")
		go func() {
			defer cancel()
			result := run(ctx, r)
			activity := managementActivityOutcome(result.Error)
			activity.Rows = result.Rows
			if result.State == "uncertain" {
				activity.State = "uncertain"
			}
			if result.State != "committed" && result.Error == nil {
				activity.State = "error"
				activity.Error = "操作未确认提交。"
			}
			contract.CompleteActivity(complete, activity)
			out <- schemaClearReply{outcome: &result}
			services.Invalidate()
		}()
	}
	x.guarding = s.deps.BeforeClear != nil
	if s.deps.BeforeClear == nil || !s.deps.BeforeClear(services, x.target.Object.ProfileID, x.database, x.target.Object.Name, next) {
		x.guarding = false
		next(services)
	}
}
func (s *State) consumeSchemaClear(services ui.Services) {
	x := s.schemaClear
	if x == nil {
		return
	}
	if x.owner != nil && s.session().ProfileID != x.target.Object.ProfileID {
		return // Park the original result until its own profile is active.
	}
	if x.guarding && s.deps.ClearGuardPending != nil && !s.deps.ClearGuardPending(x.target.Object.ProfileID, x.database) {
		x.guarding = false
		x.guardAttempt++
	}
	if !s.schemaClearValid(x) {
		if x.cancel != nil {
			x.cancel()
		}
		if !x.applying {
			if x.owner != nil && x.owner.last != nil {
				// A lost original session revokes reads and writes, not its receipt.
				x.results, x.cancel, x.busy, x.cancelRequested = nil, nil, false, false
				x.blocked, x.owner.showReceipt = true, true
				return
			}
			s.closeSchemaClear()
			return
		}
		if x.owner == nil {
			x.open = false
		}
	}
	if x.results == nil {
		return
	}
	select {
	case reply := <-x.results:
		x.results, x.busy = nil, false
		x.cancelRequested = false
		if x.cancel != nil {
			x.cancel()
			x.cancel = nil
		}
		if reply.outcome != nil {
			out := reply.outcome.Clone()
			if x.owner != nil {
				expected := contract.SchemaDesignWriteReceipt{Operation: "clear"}
				if x.applied != nil {
					expected.Original = x.applied.Original.Clone()
					expected.ResetIdentity = x.applied.ResetIdentity
					expected.Destination = x.applied.Original.Target.Object.Name
				}
				out = x.owner.settle(expected, out)
			}
			x.applying = false
			if out.Error != nil {
				s.report(locales.Text("清空表") + ": " + x.database + "." + x.target.Object.Name + " · " + out.Error.Error())
			}
			// Preserve a late mutation receipt for its captured original scope.
			// The host separately checks original published/context generations.
			if s.deps.SchemaCleared != nil {
				s.deps.SchemaCleared(services, SchemaClearEvent{Original: x.target, Generation: x.generation, Database: x.database, Outcome: out.Clone()})
			}
			if s.schemaClear != x {
				return
			}
			if !s.schemaClearValid(x) {
				if x.owner == nil || x.owner.last == nil {
					s.closeSchemaClear()
					return
				}
				x.owner.showReceipt = true
			}
			x.blocked = true // require an explicit fresh read before any retry
			if out.State == "committed" && out.Verified && out.Actual != nil && out.Actual.Schema.Exists && out.Actual.Target == x.target && out.Error == nil {
				if x.owner == nil {
					s.closeSchemaClear()
				} else {
					x.completed, x.owner.showReceipt, x.err = true, true, ""
				}
				s.report(locales.Text("清空表成功") + ": " + x.database + "." + x.target.Object.Name)
				return
			}
			if out.Error != nil {
				x.err = out.Error.Error()
			} else {
				x.err = locales.Text("清空结果未确认，请重新连接并核对原表。")
			}
		} else if reply.err != nil {
			x.err = reply.err.Error()
		} else if reply.snapshot.Target != x.target || !reply.snapshot.Schema.Exists || (reply.snapshot.Schema.Engine != "sqlite" && reply.snapshot.Schema.Engine != "postgresql" && reply.snapshot.Schema.Engine != "opengauss" && reply.snapshot.Schema.Engine != "databend") || reply.snapshot.Schema.Engine == "opengauss" && reply.snapshot.Schema.OpenGauss == nil || reply.snapshot.Schema.Revision == "" || reply.snapshot.Schema.Table.Name != x.target.Object.Name {
			x.err = errors.New("original table snapshot is incomplete or unavailable").Error()
		} else {
			snapshot := reply.snapshot.Clone()
			if x.owner == nil || !x.owner.blocked {
				x.original = &snapshot
			} else {
				x.blocked, x.err = true, schemaDesignError(contract.ErrSchemaCommitUncertain)
			}
		}
	default:
	}
}
func (s *State) schemaClearModal(c *ui.Context) {
	x := s.schemaClear
	if x == nil || !x.open || x.guarding {
		return
	}
	databend := x.original != nil && x.original.Schema.Engine == "databend" || x.original == nil && s.deps.Engine != nil && s.deps.Engine(x.target.Object.ProfileID) == "databend"
	gap := float32(10)
	if databend {
		gap = 8
	}
	theme.Modal(c, &x.open, func() {
		ui.Column(c).Width(800).MaxWidthPercent(92).Height(620).MaxHeightPercent(90).Gap(gap).Children(func() {
			ui.Text(c, locales.Text("清空表")).Bold()
			ui.Text(c, fmt.Sprintf("#%d · %s / %s · %s", x.target.Object.ProfileID, x.target.Object.Catalog, x.target.Object.Schema, x.target.Object.Name)).SingleLine().Tooltip(x.target.Object.Name)
			if databend {
				ui.Text(c, locales.Text("使用 DELETE 删除原表全部行并保留结构，核验原表身份和空表结果。检查不锁定并发更改；未核验的原始回执禁止重放，取消不表示回滚。"))
			} else {
				ui.Text(c, locales.Text("事务清空所有行；保留表结构、索引和触发器。"))
				ui.Text(c, locales.Text("删除触发器和外键级联会执行，可能修改同库其他表；任一步失败则回滚。"))
			}
			resetAvailable := x.original != nil && x.original.Schema.Engine == "sqlite" && schemaddl.SQLiteAutoIncrement(x.original.Schema.Table)
			if !databend {
				ui.Checkbox(c, &x.resetIdentity, locales.Text("重置自增值")).Disabled(x.busy || x.completed || !resetAvailable)
				ui.Text(c, locales.Text("不勾选时不主动重置自增值；原触发器的自增行为仍会执行。"))
			}
			if x.err != "" {
				ui.Text(c, x.err).SingleLine().Tooltip(x.err).TextColor(theme.Colors(c).Error)
			}
			if x.busy {
				ui.Text(c, locales.Text("正在等待结构操作回执…"))
			}
			if x.completed {
				ui.Text(c, locales.Text("清空表成功"))
			} else if x.owner != nil && x.owner.blocked {
				ui.Text(c, locales.Text("原始结构写入未核验；读取不会解除写入保护。"))
			} else if x.blocked {
				ui.Text(c, locales.Text("清空结果或结构已变化；重新读取后才能再次确认。"))
			}
			raw := ""
			if x.original != nil {
				raw = "DELETE FROM " + schemaddl.Quote(schemaSQLQualifier(*x.original)) + "." + schemaddl.Quote(x.target.Object.Name)

				raw += ";"
				if x.resetIdentity && resetAvailable {
					raw += "\nDELETE FROM " + schemaddl.Quote(schemaSQLQualifier(*x.original)) + ".sqlite_sequence WHERE name='" + strings.ReplaceAll(x.target.Object.Name, "'", "''") + "';"
				}
			}
			if !s.schemaModalReceiptToggle(c, x.owner) {
				ui.TextArea(c, &raw).Label(locales.Text("清空 SQL 预览")).ReadOnly(true).Grow(1).MinHeight(40)
			}
			if !x.completed {
				ui.Text(c, locales.Text("请输入原表名称以确认此操作："))
				ui.TextInput(c, &x.confirmation).Label(locales.Text("确认原表名称")).Disabled(x.busy || x.original == nil)
			}
			ui.Row(c).Wrap().Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, locales.Text("取消")).Disabled(x.cancelRequested).Clicked() {
					if x.busy && x.applying {
						x.cancelRequested = true
						if x.cancel != nil {
							x.cancel()
						}
					} else {
						x.open = false
					}
				}
				if ui.Button(c, locales.Text("重新读取结构")).Disabled(x.busy || x.completed || !s.schemaClearValid(x)).Clicked() {
					s.readSchemaClear(c.Services())
				}
				if ui.PrimaryButton(c, locales.Text("确认清空表")).Disabled(x.busy || x.blocked || x.original == nil || x.confirmation != x.target.Object.Name || !s.schemaClearValid(x)).Clicked() {
					s.applySchemaClear(c.Services())
				}
			})
		})
	})
	if !x.open {
		if x.busy && x.applying {
			// Escape requests cancellation and keeps ownership until acknowledgement.
			x.open, x.cancelRequested = true, true
			if x.cancel != nil {
				x.cancel()
			}
		} else {
			s.closeSchemaClear()
		}
	}
}
