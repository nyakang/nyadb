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

type SchemaCopyEvent struct {
	Original              contract.SchemaDesignTarget
	Generation            uint64
	Database, Destination string
	Outcome               contract.SchemaDesignOutcome
}
type schemaCopyReply struct {
	snapshot contract.SchemaDesignSnapshot
	outcome  *contract.SchemaDesignOutcome
	err      error
}
type schemaCopyOperation struct {
	target                                                   contract.SchemaDesignTarget
	generation                                               uint64
	guardAttempt                                             uint64
	database, destination, err                               string
	ops                                                      contract.SchemaDesignOperations
	original                                                 *contract.SchemaDesignSnapshot
	applied                                                  *contract.SchemaCopyRequest
	withData, rebindSelf, preserveRowID                      bool
	open, busy, applying, blocked, cancelRequested, guarding bool
	cancel                                                   context.CancelFunc
	results                                                  <-chan schemaCopyReply
}

func (s *State) nativeTablePending() bool {
	return s.schemaDrop != nil && s.schemaDrop.busy || s.schemaCopy != nil && s.schemaCopy.busy
}
func (s *State) CanCopyTable(profile int64, database, table string) bool {
	if s.CanChangeTable(profile) {
		return true
	}
	if s.tablePending || s.nativeTablePending() {
		return false
	}
	w := s.session()
	ops, target := s.schemaBinding(profile, database, table)
	return w.Connected && w.ProfileID == profile && target.Generation != 0 && ops.Snapshot != nil && ops.Copy != nil && ops.CanCopy != nil && ops.CanCopy(target)
}
func (s *State) schemaCopyValid(x *schemaCopyOperation) bool {
	if x == nil {
		return false
	}
	w := s.session()
	_, target := s.schemaBinding(x.target.Object.ProfileID, x.database, x.target.Object.Name)
	return w.Connected && w.ProfileID == x.target.Object.ProfileID && w.Generation == x.generation && target == x.target
}
func (s *State) beginSchemaCopy(profile int64, database, table string, data bool) {
	if !s.CanCopyTable(profile, database, table) || s.CanChangeTable(profile) {
		return
	}
	s.closeSchemaCopy()
	s.closeSchemaDrop()
	ops, target := s.schemaBinding(profile, database, table)
	s.schemaCopy = &schemaCopyOperation{target: target, generation: s.session().Generation, database: database, destination: s.NextObjectName(profile, table+"_copy"), ops: ops, withData: data, rebindSelf: true, preserveRowID: true, open: true}
}
func (s *State) closeSchemaCopy() {
	if x := s.schemaCopy; x != nil && x.cancel != nil {
		x.cancel()
	}
	s.schemaCopy = nil
}
func (s *State) readSchemaCopy(services ui.Services) {
	x := s.schemaCopy
	if x == nil || !x.open || x.busy || !s.schemaCopyValid(x) {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan schemaCopyReply, 1)
	x.cancel, x.results, x.busy, x.err, x.original = cancel, out, true, "", nil
	x.blocked, x.cancelRequested = false, false
	load, target := x.ops.Snapshot, x.target
	go func() {
		defer cancel()
		snapshot, err := load(ctx, target)
		out <- schemaCopyReply{snapshot: snapshot, err: err}
		services.Invalidate()
	}()
}
func (s *State) schemaCopyRequest(x *schemaCopyOperation) (contract.SchemaCopyRequest, []string, error) {
	if x == nil || x.original == nil {
		return contract.SchemaCopyRequest{}, nil, errors.New("read the original table before copying")
	}
	r := contract.SchemaCopyRequest{Original: x.original.Clone(), Destination: strings.TrimSpace(x.destination), WithData: x.withData, RebindSelfReferences: x.rebindSelf, PreserveRowID: x.preserveRowID}
	table, err := schemaddl.CopySQLite(r.Original.Schema.Table, r.Original.Target.Object.Schema, r.Destination, r.RebindSelfReferences)
	if err != nil {
		return r, nil, err
	}
	sql, err := schemaddl.CopySQLiteSQL(table, r.Original.Target.Object.Schema)
	return r, sql, err
}
func (s *State) applySchemaCopy(services ui.Services) {
	x := s.schemaCopy
	if x == nil || !x.open || x.busy || x.blocked || x.guarding || !s.schemaCopyValid(x) {
		return
	}
	request, _, err := s.schemaCopyRequest(x)
	if err != nil {
		x.err = err.Error()
		return
	}
	x.guarding = s.deps.BeforeCopy != nil
	x.guardAttempt++
	attempt := x.guardAttempt
	next := func(services ui.Services) {
		if s.schemaCopy != x || x.guardAttempt != attempt || !x.open || x.busy || x.blocked || !s.schemaCopyValid(x) {
			return
		}
		current, _, err := s.schemaCopyRequest(x)
		if err != nil || schemaddl.Signature(current) != schemaddl.Signature(request) {
			return
		}
		x.guarding = false
		ctx, cancel := context.WithCancel(context.Background())
		out := make(chan schemaCopyReply, 1)
		x.cancel, x.results, x.busy, x.applying, x.cancelRequested, x.err = cancel, out, true, true, false, ""
		captured := request.Clone()
		x.applied = &captured
		run, r := x.ops.Copy, request.Clone()
		source := "copy_structure"
		if r.WithData {
			source = "copy_data"
		}
		complete := s.beginActivity(x.target.Object.ProfileID, x.generation, x.database, x.target.Object.Name, "table", source)
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
			out <- schemaCopyReply{outcome: &result}
			services.Invalidate()
		}()
	}
	if s.deps.BeforeCopy == nil || !s.deps.BeforeCopy(services, x.target.Object.ProfileID, x.database, x.target.Object.Name, next) {
		x.guarding = false
		next(services)
	}
}
func (s *State) consumeSchemaCopy(services ui.Services) {
	x := s.schemaCopy
	if x == nil {
		return
	}
	if x.guarding && s.deps.TableGuardPending != nil && !s.deps.TableGuardPending(x.target.Object.ProfileID, x.database, x.target.Object.Name) {
		x.guarding = false
		x.guardAttempt++
	}
	if !s.schemaCopyValid(x) {
		if x.cancel != nil {
			x.cancel()
		}
		x.open = false
		if !x.applying {
			s.closeSchemaCopy()
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
				x.blocked, x.err = true, "copy receipt has no captured request"
				return
			}
			destination := x.applied.Destination
			if out.Error != nil {
				s.report(locales.Text("复制表") + ": " + x.database + "." + x.target.Object.Name + " · " + out.Error.Error())
			}
			if s.deps.SchemaCopied != nil {
				s.deps.SchemaCopied(services, SchemaCopyEvent{Original: x.target, Generation: x.generation, Database: x.database, Destination: destination, Outcome: out})
			}
			if s.schemaCopy != x {
				return
			}
			if !s.schemaCopyValid(x) {
				s.closeSchemaCopy()
				return
			}
			x.blocked = true
			expected := x.target
			expected.Object.Name = destination
			if out.State == "committed" && out.Verified && out.Actual != nil && out.Actual.Target == expected && out.Actual.Schema.Exists && out.Error == nil {
				s.closeSchemaCopy()
				s.report(locales.Text("复制表成功") + ": " + x.database + "." + expected.Object.Name)
				return
			}
			if out.Error != nil {
				x.err = out.Error.Error()
			} else {
				x.err = locales.Text("复制结果未确认，请重新连接并核对目标表。")
			}
		} else if reply.err != nil {
			x.err = reply.err.Error()
		} else if reply.snapshot.Target != x.target || !reply.snapshot.Schema.Exists || reply.snapshot.Schema.Engine != "sqlite" || reply.snapshot.Schema.Revision == "" || reply.snapshot.Schema.Table.Name != x.target.Object.Name {
			x.err = "original SQLite table snapshot is incomplete or unavailable"
		} else {
			snapshot := reply.snapshot.Clone()
			x.original = &snapshot
		}
	default:
	}
}
func (s *State) schemaCopyModal(c *ui.Context) {
	x := s.schemaCopy
	if x == nil || !x.open || x.guarding {
		return
	}
	ui.Modal(c, &x.open, func() {
		ui.Column(c).Width(800).MaxWidthPercent(92).Height(620).MaxHeightPercent(92).Padding(18).Gap(8).Children(func() {
			ui.Text(c, locales.Text("复制 SQLite 表")).Bold()
			ui.Text(c, fmt.Sprintf("#%d · %s / %s · %s", x.target.Object.ProfileID, x.target.Object.Catalog, x.target.Object.Schema, x.target.Object.Name)).SingleLine().Tooltip(x.target.Object.Name)
			ui.TextInput(c, &x.destination).Label(locales.Text("目标表名称")).Disabled(x.busy)
			ui.Row(c).Wrap().Gap(12).Children(func() {
				ui.Checkbox(c, &x.withData, locales.Text("复制数据")).Disabled(x.busy)
				ui.Checkbox(c, &x.rebindSelf, locales.Text("自引用外键绑定到新表")).Disabled(x.busy)
				ui.Checkbox(c, &x.preserveRowID, locales.Text("保留隐藏 rowid")).Disabled(x.busy || !x.withData)
			})
			ui.Text(c, locales.Text("在同一数据库或模式中事务复制，不覆盖目标。生成字段重新计算并核验；仅结构复制重置自增状态。"))
			ui.Text(c, locales.Text("触发器在数据复制后创建，正文保留原对象引用；外部外键保持原引用。"))
			if x.withData && !x.preserveRowID {
				ui.Text(c, locales.Text("允许重新分配隐藏 rowid；普通字段和行数仍须完整核验。"))
			}
			if x.err != "" {
				ui.Text(c, x.err).SingleLine().Tooltip(x.err).TextColor(theme.Colors(c).Error)
			}
			if x.busy {
				ui.Text(c, locales.Text("正在等待结构操作回执…"))
			}
			if x.blocked {
				ui.Text(c, locales.Text("复制结果或结构已变化；重新读取后才能再次确认。"))
			}
			_, sql, err := s.schemaCopyRequest(x)
			raw := strings.Join(sql, ";\n\n")
			if raw != "" {
				raw += ";"
			}
			ui.TextArea(c, &raw).Label(locales.Text("复制结构 SQL 预览")).ReadOnly(true).Grow(1).MinHeight(40)
			if err != nil && x.original != nil {
				ui.Text(c, err.Error()).SingleLine().Tooltip(err.Error()).TextColor(theme.Colors(c).Error)
			}
			ui.Row(c).Wrap().Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, locales.Text("重新读取结构")).Disabled(x.busy).Clicked() {
					s.readSchemaCopy(c.Services())
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
				if ui.PrimaryButton(c, locales.Text("确认复制表")).Disabled(x.busy || x.blocked || err != nil || !s.schemaCopyValid(x)).Clicked() {
					s.applySchemaCopy(c.Services())
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
			s.closeSchemaCopy()
		}
	}
}
