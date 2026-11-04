package objectworkspace

import (
	"context"
	"errors"
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type SchemaDropEvent struct {
	Original   contract.SchemaDesignTarget
	Generation uint64 // contextual UI generation, distinct from Original.Generation
	Database   string
	Outcome    contract.SchemaDesignOutcome
}
type schemaDropReply struct {
	snapshot contract.SchemaDesignSnapshot
	outcome  *contract.SchemaDesignOutcome
	err      error
}
type schemaDropOperation struct {
	target                                                   contract.SchemaDesignTarget
	generation                                               uint64
	guardAttempt                                             uint64
	database                                                 string
	ops                                                      contract.SchemaDesignOperations
	original                                                 *contract.SchemaDesignSnapshot
	open, busy, applying, blocked, cancelRequested, guarding bool
	confirmation, err                                        string
	cancel                                                   context.CancelFunc
	results                                                  <-chan schemaDropReply
}

func (s *State) CanDropTable(profile int64, database, table string) bool {
	if s.CanChangeTable(profile) {
		return true
	}
	if s.tablePending || s.nativeTablePending() {
		return false
	}
	w := s.session()
	ops, target := s.schemaBinding(profile, database, table)
	return w.Connected && w.ProfileID == profile && target.Generation != 0 && s.operationAvailable("drop_table", target) && ops.Snapshot != nil && ops.CanDrop != nil && ops.Drop != nil && ops.CanDrop(target)
}
func (s *State) schemaDropValid(x *schemaDropOperation) bool {
	if x == nil {
		return false
	}
	w := s.session()
	_, actual := s.schemaBinding(x.target.Object.ProfileID, x.database, x.target.Object.Name)
	return w.Connected && w.ProfileID == x.target.Object.ProfileID && w.Generation == x.generation && actual == x.target
}
func (s *State) beginSchemaDrop(profile int64, database, table string) {
	if !s.CanDropTable(profile, database, table) || s.CanChangeTable(profile) {
		return
	}
	s.closeSchemaDrop()
	s.closeSchemaCopy()
	ops, target := s.schemaBinding(profile, database, table)
	s.schemaDrop = &schemaDropOperation{target: target, generation: s.session().Generation, database: database, ops: ops, open: true}
}
func (s *State) closeSchemaDrop() {
	if x := s.schemaDrop; x != nil && x.cancel != nil {
		x.cancel()
	}
	s.schemaDrop = nil // terminal close revokes callbacks; workers own their buffered receipt
}
func (s *State) readSchemaDrop(services ui.Services) {
	x := s.schemaDrop
	if x == nil || !x.open || x.busy || !s.schemaDropValid(x) {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan schemaDropReply, 1)
	x.cancel, x.results, x.busy, x.err, x.original = cancel, out, true, "", nil
	x.blocked, x.confirmation = false, ""
	load, target := x.ops.Snapshot, x.target
	go func() {
		defer cancel()
		snapshot, err := load(ctx, target)
		out <- schemaDropReply{snapshot: snapshot, err: err}
		services.Invalidate()
	}()
}
func (s *State) applySchemaDrop(services ui.Services) {
	x := s.schemaDrop
	if x == nil || !x.open || x.busy || x.blocked || x.guarding || x.original == nil || x.confirmation != x.target.Object.Name || !s.schemaDropValid(x) {
		return
	}
	x.guardAttempt++
	attempt := x.guardAttempt
	next := func(services ui.Services) {
		// Leave decisions may run much later or after this modal was replaced.
		if s.schemaDrop != x || x.guardAttempt != attempt || !x.open || x.busy || x.blocked || x.original == nil || x.confirmation != x.target.Object.Name || !s.schemaDropValid(x) {
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		out := make(chan schemaDropReply, 1)
		x.guarding = false
		x.cancel, x.results, x.busy, x.applying, x.cancelRequested, x.err = cancel, out, true, true, false, ""
		request, apply := x.original.Clone(), x.ops.Drop
		complete := s.beginActivity(x.target.Object.ProfileID, x.generation, x.database, x.target.Object.Name, "table", "drop")
		go func() {
			defer cancel()
			result := apply(ctx, request)
			activity := managementActivityOutcome(result.Error)
			if result.State == "uncertain" {
				activity.State = "uncertain"
			}
			if result.State != "committed" && result.Error == nil {
				activity.State = "error"
				activity.Error = "操作未确认提交。"
			}
			contract.CompleteActivity(complete, activity)
			out <- schemaDropReply{outcome: &result}
			services.Invalidate()
		}()
	}
	x.guarding = s.deps.BeforeDrop != nil
	if s.deps.BeforeDrop == nil || !s.deps.BeforeDrop(services, x.target.Object.ProfileID, x.database, x.target.Object.Name, next) {
		x.guarding = false
		next(services)
	}
}
func (s *State) consumeSchemaDrop(services ui.Services) {
	x := s.schemaDrop
	if x == nil {
		return
	}
	if x.guarding && s.deps.TableGuardPending != nil && !s.deps.TableGuardPending(x.target.Object.ProfileID, x.database, x.target.Object.Name) {
		x.guarding = false
		x.guardAttempt++
	}
	if !s.schemaDropValid(x) {
		if x.cancel != nil {
			x.cancel()
		}
		x.open = false
		if !x.applying {
			s.closeSchemaDrop()
			return
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
			out := *reply.outcome
			x.applying = false
			if out.Error != nil {
				s.report(locales.Text("删除表") + ": " + x.database + "." + x.target.Object.Name + " · " + out.Error.Error())
			}
			// Preserve a late mutation receipt for its captured original scope.
			// The host separately checks original published/context generations.
			if s.deps.SchemaDropped != nil {
				s.deps.SchemaDropped(services, SchemaDropEvent{Original: x.target, Generation: x.generation, Database: x.database, Outcome: out})
			}
			if s.schemaDrop != x {
				return
			}
			if !s.schemaDropValid(x) {
				s.closeSchemaDrop()
				return
			}
			x.blocked = true // require an explicit fresh read before any retry
			if out.State == "committed" && out.Verified && out.Actual != nil && !out.Actual.Schema.Exists && out.Actual.Target == x.target && out.Error == nil {
				s.closeSchemaDrop()
				s.report(locales.Text("删除表成功") + ": " + x.database + "." + x.target.Object.Name)
				return
			}
			if out.Error != nil {
				x.err = out.Error.Error()
			} else {
				x.err = locales.Text("删除结果未确认，请重新连接并核对原表。")
			}
		} else if reply.err != nil {
			x.err = reply.err.Error()
		} else if reply.snapshot.Target != x.target || !reply.snapshot.Schema.Exists || (reply.snapshot.Schema.Engine != "sqlite" && reply.snapshot.Schema.Engine != "postgresql") || reply.snapshot.Schema.Revision == "" || reply.snapshot.Schema.Table.Name != x.target.Object.Name {
			x.err = errors.New("original table snapshot is incomplete or unavailable").Error()
		} else {
			snapshot := reply.snapshot.Clone()
			x.original = &snapshot
		}
	default:
	}
}
func (s *State) schemaDropModal(c *ui.Context) {
	x := s.schemaDrop
	if x == nil || !x.open || x.guarding {
		return
	}
	ui.Modal(c, &x.open, func() {
		ui.Column(c).Width(680).MaxWidthPercent(92).Height(430).MaxHeightPercent(90).Padding(18).Gap(10).Children(func() {
			ui.Text(c, locales.Text("删除表")).Bold()
			ui.Text(c, fmt.Sprintf("#%d · %s / %s · %s", x.target.Object.ProfileID, x.target.Object.Catalog, x.target.Object.Schema, x.target.Object.Name)).SingleLine().Tooltip(x.target.Object.Name)
			ui.Text(c, locales.Text("事务删除原表及其索引、触发器和数据；存在外键、视图或触发器依赖时拒绝删除。"))
			if x.err != "" {
				ui.Text(c, x.err).SingleLine().Tooltip(x.err).TextColor(theme.Colors(c).Error)
			}
			if x.busy {
				ui.Text(c, locales.Text("正在等待结构操作回执…"))
			}
			if x.blocked {
				ui.Text(c, locales.Text("删除结果或结构已变化；重新读取后才能再次确认。"))
			}
			raw := ""
			if x.original != nil {
				raw = "DROP TABLE " + schemaddl.Quote(x.target.Object.Schema) + "." + schemaddl.Quote(x.target.Object.Name)
				if x.original.Schema.Engine == "postgresql" {
					raw += " RESTRICT"
				}
				raw += ";"
			}
			ui.TextArea(c, &raw).Label(locales.Text("删除 SQL 预览")).ReadOnly(true).Grow(1).MinHeight(40)
			ui.TextInput(c, &x.confirmation).Label(locales.Text("确认原表名称")).Disabled(x.busy || x.original == nil)
			ui.Row(c).Wrap().Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, locales.Text("重新读取结构")).Disabled(x.busy).Clicked() {
					s.readSchemaDrop(c.Services())
				}
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
				if ui.PrimaryButton(c, locales.Text("确认删除表")).Disabled(x.busy || x.blocked || x.original == nil || x.confirmation != x.target.Object.Name || !s.schemaDropValid(x)).Clicked() {
					s.applySchemaDrop(c.Services())
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
			s.closeSchemaDrop()
		}
	}
}
