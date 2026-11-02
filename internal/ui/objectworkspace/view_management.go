package objectworkspace

import (
	"context"
	"errors"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type ViewEvent struct {
	Request    contract.ViewDesignRequest
	Generation uint64
	Database   string
	Outcome    contract.ViewDesignOutcome
}
type viewReply struct {
	snapshot contract.ViewDesignSnapshot
	outcome  *contract.ViewDesignOutcome
	err      error
}
type viewOperation struct {
	target                                                        contract.SchemaDesignTarget
	generation, guardAttempt                                      uint64
	database, action, name, text, originalText, confirmation, err string
	ops                                                           contract.ViewDesignOperations
	original                                                      *contract.ViewDesignSnapshot
	prepared, applied                                             *contract.ViewDesignRequest
	sql                                                           []string
	open, busy, applying, blocked, guarding, cancelRequested      bool
	cancel                                                        context.CancelFunc
	results                                                       <-chan viewReply
}

func (s *State) viewBinding(id int64, database, name string) (contract.ViewDesignOperations, contract.SchemaDesignTarget) {
	if s.deps.ViewDesignFor == nil || s.deps.ViewTarget == nil {
		return contract.ViewDesignOperations{}, contract.SchemaDesignTarget{}
	}
	return s.deps.ViewDesignFor(id), s.deps.ViewTarget(id, database, name)
}
func (s *State) CanView(id int64, database, name string) bool {
	if s.nativeTablePending() || s.tablePending || s.objectOp != nil || s.viewOp != nil || s.parkedViewOp != nil {
		return false
	}
	w := s.sessionFor(id)
	ops, target := s.viewBinding(id, database, name)
	return w.Connected && w.ProfileID == id && s.session().ProfileID == id && target.Generation != 0 && target.Object.Kind == "view" && ops.CanApply != nil && ops.Snapshot != nil && ops.Apply != nil && ops.CanApply(target)
}
func (s *State) viewValid(x *viewOperation) bool {
	if x == nil {
		return false
	}
	w := s.sessionFor(x.target.Object.ProfileID)
	_, t := s.viewBinding(x.target.Object.ProfileID, x.database, x.target.Object.Name)
	return s.session().ProfileID == x.target.Object.ProfileID && w.Connected && w.Generation == x.generation && t == x.target
}
func (s *State) BeginView(services ui.Services, id int64, database, name, action string) {
	if !s.CanView(id, database, name) || action != "create" && action != "alter" && action != "rename" && action != "drop" {
		return
	}
	ops, target := s.viewBinding(id, database, name)
	x := &viewOperation{target: target, generation: s.sessionFor(id).Generation, database: database, action: action, name: name, ops: ops, open: true}
	if action == "create" {
		x.text, _ = schemaddl.CreateSQLiteView(schemaddl.View{Name: name, Schema: target.Object.Schema, SelectSQL: "SELECT 1 AS value"}, target.Object.Schema)
		x.originalText = x.text
	}
	s.viewOp = x
	s.readView(services)
}
func (s *State) closeView() {
	if x := s.viewOp; x != nil && x.cancel != nil {
		x.cancel()
	}
	s.viewOp = nil
}
func (s *State) parkView() {
	x := s.viewOp
	if x == nil || x.applying {
		return
	}
	if x.cancel != nil {
		x.cancel()
		x.cancel = nil
	}
	x.results, x.busy, x.open, x.guarding = nil, false, false, false
	x.guardAttempt++
	s.parkedViewOp, s.viewOp = x, nil
}
func (s *State) canResumeView() bool {
	x := s.parkedViewOp
	if x == nil || s.viewOp != nil || s.objectOp != nil || s.nativeTablePending() || s.tablePending || s.session().ProfileID != x.target.Object.ProfileID {
		return false
	}
	w := s.sessionFor(x.target.Object.ProfileID)
	if !w.Connected {
		return false
	}
	ops, target := s.viewBinding(x.target.Object.ProfileID, x.database, x.target.Object.Name)
	return target.Generation != 0 && ops.Snapshot != nil && ops.Apply != nil && ops.CanApply != nil && ops.CanApply(target)
}
func (s *State) resumeView() {
	if !s.canResumeView() {
		return
	}
	x := s.parkedViewOp
	w := s.sessionFor(x.target.Object.ProfileID)
	ops, target := s.viewBinding(x.target.Object.ProfileID, x.database, x.target.Object.Name)
	if target != x.target || w.Generation != x.generation {
		x.original, x.prepared, x.applied, x.sql = nil, nil, nil, nil
		x.blocked = false
	}
	x.ops, x.target, x.generation, x.open = ops, target, w.Generation, true
	s.parkedViewOp, s.viewOp = nil, x
}
func (s *State) readView(services ui.Services) {
	x := s.viewOp
	if x == nil || x.busy || x.guarding || !s.viewValid(x) {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan viewReply, 1)
	x.cancel, x.results, x.busy, x.blocked, x.err = cancel, ch, true, false, ""
	x.prepared, x.applied, x.sql = nil, nil, nil
	load, target := x.ops.Snapshot, x.target
	go func() {
		defer cancel()
		out, err := load(ctx, target)
		ch <- viewReply{snapshot: out, err: err}
		services.Invalidate()
	}()
}
func (s *State) viewRequest(x *viewOperation) (contract.ViewDesignRequest, []string, error) {
	if x == nil || x.original == nil {
		return contract.ViewDesignRequest{}, nil, errors.New("read the original view scope before previewing")
	}
	r := contract.ViewDesignRequest{Original: x.original.Clone(), Action: x.action, Desired: x.original.Schema.View.Clone()}
	if x.action == "create" || x.action == "alter" {
		if x.action == "create" && strings.TrimSpace(x.name) != x.target.Object.Name {
			return r, nil, errors.New("creation name differs from captured absence scope")
		}
		v, err := schemaddl.ParseSQLiteView(x.text)
		if err != nil {
			return r, nil, err
		}
		if v.Name != x.target.Object.Name {
			return r, nil, errors.New("view name differs from the captured scope; rename it separately")
		}
		v.Triggers = r.Original.Schema.View.Clone().Triggers
		r.Desired = v
	} else if x.action == "rename" {
		r.Desired.Name = strings.TrimSpace(x.name)
	}
	p, err := schemaddl.PlanSQLiteView(r.Original.Schema, r.Action, r.Desired, x.target.Object.Schema)
	return r, p.SQL, err
}
func (s *State) prepareView() {
	x := s.viewOp
	if x == nil || x.busy || x.blocked || x.guarding || !s.viewValid(x) {
		return
	}
	r, sql, err := s.viewRequest(x)
	if err != nil {
		x.err = err.Error()
		x.prepared, x.sql = nil, nil
		return
	}
	x.prepared, x.sql, x.err = &r, sql, ""
}
func (s *State) applyView(services ui.Services) {
	x := s.viewOp
	if x == nil || x.prepared == nil || x.busy || x.blocked || x.guarding || !s.viewValid(x) || x.action == "drop" && x.confirmation != x.target.Object.Name {
		return
	}
	r, _, err := s.viewRequest(x)
	if err != nil || schemaddl.Signature(r) != schemaddl.Signature(*x.prepared) {
		return
	}
	x.guardAttempt++
	attempt := x.guardAttempt
	next := func(services ui.Services) {
		if s.viewOp != x || x.guardAttempt != attempt || x.busy || x.blocked || !s.viewValid(x) || x.action == "drop" && x.confirmation != x.target.Object.Name {
			return
		}
		current, _, err := s.viewRequest(x)
		if err != nil || schemaddl.Signature(current) != schemaddl.Signature(r) {
			return
		}
		x.guarding = false
		ctx, cancel := context.WithCancel(context.Background())
		ch := make(chan viewReply, 1)
		captured := r.Clone()
		x.applied = &captured
		x.cancel, x.results, x.busy, x.applying, x.cancelRequested, x.err = cancel, ch, true, true, false, ""
		apply, request := x.ops.Apply, r.Clone()
		complete := s.beginActivity(x.target.Object.ProfileID, x.generation, x.database, x.target.Object.Name, "view", x.action)
		go func() {
			defer cancel()
			out := apply(ctx, request)
			activity := managementActivityOutcome(out.Error)
			if out.State == "uncertain" {
				activity.State = "uncertain"
			}
			if out.State != "committed" && out.Error == nil {
				activity.State = "error"
				activity.Error = "view operation was not acknowledged"
			}
			contract.CompleteActivity(complete, activity)
			ch <- viewReply{outcome: &out}
			services.Invalidate()
		}()
	}
	x.guarding = s.deps.BeforeDDL != nil
	if s.deps.BeforeDDL == nil || !s.deps.BeforeDDL(services, x.target.Object.ProfileID, x.database, x.target.Object.Name, next) {
		x.guarding = false
		next(services)
	}
}
func (s *State) consumeView(services ui.Services) {
	x := s.viewOp
	if x == nil {
		return
	}
	if x.guarding && s.deps.TableGuardPending != nil && !s.deps.TableGuardPending(x.target.Object.ProfileID, x.database, x.target.Object.Name) {
		x.guarding = false
		x.guardAttempt++
	}
	if !s.viewValid(x) {
		if x.cancel != nil {
			x.cancel()
		}
		x.open = false
		if !x.applying {
			s.parkView()
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
			x.applying = false
			out := *reply.outcome
			if x.applied == nil {
				x.err, x.blocked = "view receipt has no captured request", true
				return
			}
			r := x.applied.Clone()
			if s.deps.ViewChanged != nil {
				s.deps.ViewChanged(services, ViewEvent{Request: r, Generation: x.generation, Database: x.database, Outcome: out})
			}
			if s.viewOp != x {
				return
			}
			expected := r.Original.Target
			if r.Action != "drop" {
				expected.Object.Name = r.Desired.Name
			}
			if out.State == "committed" && out.Verified && out.Actual != nil && out.Actual.Target == expected && out.Actual.Schema.Exists == (r.Action != "drop") && out.Error == nil {
				s.closeView()
				s.report(locales.Text("视图操作完成") + ": " + x.database + "." + expected.Object.Name)
				return
			}
			x.blocked = true
			if out.Error != nil {
				x.err = out.Error.Error()
			} else {
				x.err = locales.Text("视图操作未确认，请重新连接并核对原对象。")
			}
			if !s.viewValid(x) {
				s.parkView()
			}
		} else if reply.err != nil {
			x.err = reply.err.Error()
		} else if reply.snapshot.Target != x.target || reply.snapshot.Schema.Engine != "sqlite" || reply.snapshot.Schema.Revision == "" || reply.snapshot.Schema.View.Name != x.target.Object.Name || reply.snapshot.Schema.View.Schema != x.target.Object.Schema {
			x.err = "original view snapshot differs from the captured scope"
		} else {
			original := reply.snapshot.Clone()
			if x.action != "create" && (!original.Schema.Exists || original.Schema.DDL == "") {
				x.err = "original view no longer exists"
				return
			}
			if x.action == "create" && original.Schema.Exists {
				x.err = "view creation target already exists"
				return
			}
			if x.action != "create" {
				if x.action != "alter" || x.text == x.originalText {
					x.text = original.Schema.DDL
				}
				x.originalText = original.Schema.DDL
			}
			x.original = &original
		}
	default:
	}
}
func viewActionLabel(action string) string {
	switch action {
	case "create":
		return locales.Text("新建视图")
	case "alter":
		return locales.Text("编辑视图")
	case "rename":
		return locales.Text("重命名视图")
	case "drop":
		return locales.Text("删除视图")
	}
	return ""
}

func (s *State) viewModal(c *ui.Context) {
	x := s.viewOp
	if x == nil || !x.open || x.guarding {
		return
	}
	p := theme.Colors(c)
	ui.Modal(c, &x.open, func() {
		w, h := c.Size()
		ui.Column(c).Width(min(float32(800), w-70)).Height(min(float32(620), h-70)).Padding(16).Gap(8).Background(p.Header).Children(func() {
			ui.Text(c, locales.Text("SQLite 视图定义")+" · "+viewActionLabel(x.action)+" · "+x.database+" / "+x.target.Object.Name).Bold()
			ui.Text(c, locales.Text("在原命名空间事务执行，不覆盖其它对象；依赖或触发器失效时整批回滚。"))
			if x.action == "create" || x.action == "rename" {
				if ui.TextInput(c, &x.name).Label(locales.Text("视图名称")).Disabled(x.busy || x.blocked).Changed() {
					x.prepared, x.sql = nil, nil
					if x.action == "create" {
						x.original = nil
						v, err := schemaddl.ParseSQLiteView(x.text)
						v.Name = strings.TrimSpace(x.name)
						v.Schema = x.target.Object.Schema
						if nameErr := schemaddl.Identifier(v.Name); nameErr == nil {
							x.target.Object.Name = v.Name
						} else {
							err = nameErr
						}
						if err == nil {
							var raw string
							raw, err = schemaddl.CreateSQLiteView(v, x.target.Object.Schema)
							if err == nil {
								x.text = raw
								x.target.Object.Name = v.Name
								x.original = nil
							}
						}
						if err != nil {
							x.err = err.Error()
						}
					}
				}
			}
			if x.err != "" {
				ui.Text(c, x.err).SingleLine().Tooltip(x.err).TextColor(p.Error)
			}
			if x.busy {
				ui.Text(c, locales.Text("等待原视图操作回执…"))
			}
			if ui.TextArea(c, &x.text).Label(locales.Text("完整 CREATE VIEW 定义")).ReadOnly(x.busy || x.blocked || x.action == "rename" || x.action == "drop").Grow(1).Changed() {
				x.prepared, x.sql = nil, nil
			}
			if x.prepared != nil {
				raw := strings.Join(x.sql, ";\n\n") + ";"
				ui.TextArea(c, &raw).Label(locales.Text("视图 SQL 预览")).ReadOnly(true).Height(105).MinHeight(45)
			}
			if x.action == "drop" {
				ui.TextInput(c, &x.confirmation).Label(locales.Text("确认原视图名称")).Disabled(x.busy)
			}
			ui.Row(c).Wrap().Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, locales.Text("保留草稿并关闭")).Disabled(x.busy).Clicked() {
					s.parkView()
				}
				if ui.Button(c, locales.Text("放弃草稿")).Disabled(x.busy).Clicked() {
					s.closeView()
				}
				if ui.Button(c, locales.Text("取消操作")).Disabled(!x.busy || x.cancelRequested).Clicked() {
					x.cancelRequested = true
					if x.cancel != nil {
						x.cancel()
					}
				}
				if ui.Button(c, locales.Text("重新读取原定义")).Disabled(x.busy).Clicked() {
					s.readView(c.Services())
				}
				if ui.Button(c, locales.Text("SQL 预览")).Disabled(x.busy || x.blocked || x.original == nil).Clicked() {
					s.prepareView()
				}
				if ui.PrimaryButton(c, locales.Text("确认执行视图 SQL")).Disabled(x.busy || x.blocked || x.prepared == nil || !s.viewValid(x) || x.action == "drop" && x.confirmation != x.target.Object.Name).Clicked() {
					s.applyView(c.Services())
				}
			})
		})
	})
	if !x.open && s.viewOp == x {
		if x.applying {
			x.open, x.cancelRequested = true, true
			if x.cancel != nil {
				x.cancel()
			}
		} else {
			s.parkView()
		}
	}
}

func (s *State) ViewOpen() bool    { return s.viewOp != nil && s.viewOp.open && !s.viewOp.guarding }
func (s *State) ViewPending() bool { return s.viewOp != nil && s.viewOp.busy }
func (s *State) ViewError() string {
	if s.viewOp != nil {
		return s.viewOp.err
	}
	return ""
}
