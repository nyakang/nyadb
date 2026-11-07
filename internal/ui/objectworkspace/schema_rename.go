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

type SchemaRenameEvent struct {
	Original              contract.SchemaDesignTarget
	Generation            uint64
	Database, Destination string
	Outcome               contract.SchemaDesignOutcome
}
type schemaRenameReply struct {
	snapshot contract.SchemaDesignSnapshot
	outcome  *contract.SchemaDesignOutcome
	err      error
}
type schemaRenameOperation struct {
	target                                                   contract.SchemaDesignTarget
	generation                                               uint64
	guardAttempt                                             uint64
	database, destination, err                               string
	ops                                                      contract.SchemaDesignOperations
	original                                                 *contract.SchemaDesignSnapshot
	applied                                                  *contract.SchemaRenameRequest
	open, busy, applying, blocked, cancelRequested, guarding bool
	cancel                                                   context.CancelFunc
	results                                                  <-chan schemaRenameReply
}

func (s *State) canNativeRename(profile int64, database, table string) bool {
	if s.CanChangeTable(profile) {
		return false
	}
	if s.tablePending || s.nativeTablePending() {
		return false
	}
	w := s.session()
	ops, target := s.schemaBinding(profile, database, table)
	return w.Connected && w.ProfileID == profile && target.Generation != 0 && s.operationAvailable("rename_table", target) && ops.Snapshot != nil && ops.Rename != nil && ops.CanRename != nil && ops.CanRename(target)
}
func (s *State) schemaRenameValid(x *schemaRenameOperation) bool {
	if x == nil {
		return false
	}
	w := s.session()
	_, target := s.schemaBinding(x.target.Object.ProfileID, x.database, x.target.Object.Name)
	return w.Connected && w.ProfileID == x.target.Object.ProfileID && w.Generation == x.generation && target == x.target
}
func (s *State) beginSchemaRenameOperation(profile int64, database, table string) {
	if !s.canNativeRename(profile, database, table) || s.CanChangeTable(profile) {
		return
	}
	s.closeSchemaRename()
	s.closeSchemaCopy()
	s.closeSchemaDrop()
	s.tableDialog = false
	ops, target := s.schemaBinding(profile, database, table)
	s.schemaRename = &schemaRenameOperation{target: target, generation: s.session().Generation, database: database, destination: table, ops: ops, open: true}
}
func (s *State) closeSchemaRename() {
	if x := s.schemaRename; x != nil && x.cancel != nil {
		x.cancel()
	}
	s.schemaRename = nil
}
func (s *State) readSchemaRename(services ui.Services) {
	x := s.schemaRename
	if x == nil || !x.open || x.busy || !s.schemaRenameValid(x) {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan schemaRenameReply, 1)
	x.cancel, x.results, x.busy, x.err, x.original = cancel, out, true, "", nil
	x.blocked, x.cancelRequested = false, false
	load, target := x.ops.Snapshot, x.target
	go func() {
		defer cancel()
		snapshot, err := load(ctx, target)
		out <- schemaRenameReply{snapshot: snapshot, err: err}
		services.Invalidate()
	}()
}
func (s *State) schemaRenameRequest(x *schemaRenameOperation) (contract.SchemaRenameRequest, string, error) {
	if x == nil || x.original == nil {
		return contract.SchemaRenameRequest{}, "", errors.New("read the original table before renaming")
	}
	r := contract.SchemaRenameRequest{Original: x.original.Clone(), Destination: strings.TrimSpace(x.destination)}
	if r.Original.Schema.Engine != "opengauss" {
		return r, "", contract.ErrUnsupported
	}
	sql, err := schemaddl.PlanOpenGaussRename(r.Original.Schema, r.Destination)
	return r, sql, err
}
func (s *State) applySchemaRename(services ui.Services) {
	x := s.schemaRename
	if x == nil || !x.open || x.busy || x.blocked || x.guarding || !s.schemaRenameValid(x) {
		return
	}
	request, _, err := s.schemaRenameRequest(x)
	if err != nil {
		x.err = err.Error()
		return
	}
	x.guarding = s.deps.BeforeRename != nil
	x.guardAttempt++
	attempt := x.guardAttempt
	next := func(services ui.Services) {
		if s.schemaRename != x || x.guardAttempt != attempt || !x.open || x.busy || x.blocked || !s.schemaRenameValid(x) {
			return
		}
		current, _, err := s.schemaRenameRequest(x)
		if err != nil || schemaddl.Signature(current) != schemaddl.Signature(request) {
			return
		}
		x.guarding = false
		ctx, cancel := context.WithCancel(context.Background())
		out := make(chan schemaRenameReply, 1)
		x.cancel, x.results, x.busy, x.applying, x.cancelRequested, x.err = cancel, out, true, true, false, ""
		captured := request.Clone()
		x.applied = &captured
		run, r := x.ops.Rename, request.Clone()
		complete := s.beginActivity(x.target.Object.ProfileID, x.generation, x.database, x.target.Object.Name, "table", "rename")
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
			out <- schemaRenameReply{outcome: &result}
			services.Invalidate()
		}()
	}
	if s.deps.BeforeRename == nil || !s.deps.BeforeRename(services, x.target.Object.ProfileID, x.database, x.target.Object.Name, next) {
		x.guarding = false
		next(services)
	}
}
func (s *State) consumeSchemaRename(services ui.Services) {
	x := s.schemaRename
	if x == nil {
		return
	}
	if x.guarding && s.deps.TableGuardPending != nil && !s.deps.TableGuardPending(x.target.Object.ProfileID, x.database, x.target.Object.Name) {
		x.guarding = false
		x.guardAttempt++
	}
	if !s.schemaRenameValid(x) {
		if x.cancel != nil {
			x.cancel()
		}
		x.open = false
		if !x.applying {
			s.closeSchemaRename()
			return
		}
	}
	if x.results == nil {
		return
	}
	select {
	case reply := <-x.results:
		x.results, x.busy, x.cancelRequested = nil, false, false
		if x.cancel != nil {
			x.cancel()
			x.cancel = nil
		}
		if reply.outcome != nil {
			out := *reply.outcome
			x.applying = false
			if x.applied == nil {
				x.blocked, x.err = true, "rename receipt has no captured request"
				return
			}
			destination := x.applied.Destination
			if out.Error != nil {
				s.report(locales.Text("重命名表") + ": " + x.database + "." + x.target.Object.Name + " · " + out.Error.Error())
			}
			if s.deps.SchemaRenamed != nil {
				s.deps.SchemaRenamed(services, SchemaRenameEvent{Original: x.target, Generation: x.generation, Database: x.database, Destination: destination, Outcome: out})
			}
			if s.schemaRename != x {
				return
			}
			if !s.schemaRenameValid(x) {
				s.closeSchemaRename()
				return
			}
			x.blocked = true
			expected := x.target
			expected.Object.Name = destination
			if out.State == "committed" && out.Verified && out.Actual != nil && out.Actual.Target == expected && out.Actual.Schema.Exists && out.Actual.Schema.Table.Name == destination && out.Error == nil {
				s.closeSchemaRename()
				s.report(locales.Text("重命名表成功") + ": " + x.database + "." + expected.Object.Name)
				return
			}
			if out.Error != nil {
				x.err = out.Error.Error()
			} else {
				x.err = locales.Text("重命名结果未确认，请重新连接并核对原表与目标表。")
			}
		} else if reply.err != nil {
			x.err = reply.err.Error()
		} else if reply.snapshot.Target != x.target || !reply.snapshot.Schema.Exists || reply.snapshot.Schema.Engine != "opengauss" || reply.snapshot.Schema.Revision == "" || reply.snapshot.Schema.Table.Name != x.target.Object.Name {
			x.err = "original table snapshot is incomplete or unavailable"
		} else {
			snapshot := reply.snapshot.Clone()
			x.original = &snapshot
		}
	default:
	}
}
func (s *State) schemaRenameModal(c *ui.Context) {
	x := s.schemaRename
	if x == nil || !x.open || x.guarding {
		return
	}
	theme.Modal(c, &x.open, func() {
		ui.Column(c).Width(700).MaxWidthPercent(92).Height(420).MaxHeightPercent(92).Gap(8).Children(func() {
			ui.Text(c, locales.Text("重命名表")).Bold()
			ui.Text(c, fmt.Sprintf("#%d · %s / %s · %s", x.target.Object.ProfileID, x.target.Object.Catalog, x.target.Object.Schema, x.target.Object.Name)).SingleLine().Tooltip(x.target.Object.Name)
			ui.TextInput(c, &x.destination).Label(locales.Text("目标表名称")).Disabled(x.busy)
			ui.Text(c, locales.Text("在同一模式中事务重命名，保留原表、索引、序列和依赖身份。"))
			ui.Text(c, locales.Text("成功后关闭原名称下的数据和结构标签；未保存内容须先处理。"))
			if x.err != "" {
				ui.Text(c, x.err).SingleLine().Tooltip(x.err).TextColor(theme.Colors(c).Error)
			}
			if x.busy {
				ui.Text(c, locales.Text("正在等待结构操作回执…"))
			}
			if x.blocked {
				ui.Text(c, locales.Text("重命名结果或结构已变化；重新读取后才能再次确认。"))
			}
			_, raw, err := s.schemaRenameRequest(x)
			if raw != "" {
				raw += ";"
			}
			ui.TextArea(c, &raw).Label(locales.Text("重命名 SQL 预览")).ReadOnly(true).Grow(1).MinHeight(40)
			if err != nil && x.original != nil {
				ui.Text(c, err.Error()).SingleLine().Tooltip(err.Error()).TextColor(theme.Colors(c).Error)
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
				if ui.Button(c, locales.Text("重新读取结构")).Disabled(x.busy).Clicked() {
					s.readSchemaRename(c.Services())
				}
				if ui.PrimaryButton(c, locales.Text("确认重命名表")).Disabled(x.busy || x.blocked || err != nil || !s.schemaRenameValid(x)).Clicked() {
					s.applySchemaRename(c.Services())
				}
			})
		})
	})
	if !x.open {
		if x.busy && x.applying {
			x.open, x.cancelRequested = true, true
			if x.cancel != nil {
				x.cancel()
			}
		} else {
			s.closeSchemaRename()
		}
	}
}
