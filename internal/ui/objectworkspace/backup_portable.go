package objectworkspace

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type BackupEvent struct {
	Original   contract.SchemaDesignTarget
	Generation uint64
	Outcome    contract.BackupOutcome
}
type portableBackupPanel struct {
	request                                          contract.BackupRequest
	ops                                              contract.BackupOperations
	generation                                       uint64
	database, confirmation, tableNames, err          string
	open, busy, submitted, cancelRequested, guarding bool
	acknowledgeSessions                              bool
	guardAttempt                                     uint64
	plan                                             *contract.BackupPlan
	outcome                                          *contract.BackupOutcome
	progress                                         contract.BackupProgress
	updates                                          <-chan contract.BackupProgress
	cancel                                           context.CancelFunc
	results                                          <-chan portableBackupReply
}
type portableBackupReply struct {
	plan    contract.BackupPlan
	outcome *contract.BackupOutcome
	err     error
}

func (s *State) beginPortableBackup(profile int64, database, table, action string) bool {
	if s.deps.BackupsFor == nil {
		return false
	}
	ops := s.deps.BackupsFor(profile)
	if ops.Plan == nil {
		return false
	}
	if s.portableBackup != nil && s.portableBackup.busy {
		return true
	}
	_, target := s.schemaBinding(profile, database, table)
	if action != "restore" && action != "backup" {
		return true
	}
	if !s.operationAvailable(action, target) || ops.Availability == nil || !ops.Availability(target, action).Enabled {
		return true
	}
	s.closePortableBackup()
	r := contract.BackupRequest{Target: target, Action: action, Scope: "schema"}
	if table != "" {
		r.Scope = "tables"
		r.Tables = []contract.ObjectRef{target.Object}
	}
	if action == "restore" {
		r.Destination = target.Object.Catalog + "_restored"
	}
	if ops.Engine == "sqlite" {
		r.Scope = "database"
		r.Tables = nil
		r.Destination = ""
		table = ""
	}
	s.portableBackup = &portableBackupPanel{request: r, ops: ops, generation: s.sessionFor(profile).Generation, database: database, tableNames: table, open: true}
	return true
}
func (s *State) closePortableBackup() {
	if x := s.portableBackup; x != nil && x.cancel != nil {
		x.cancel()
	}
	s.portableBackup = nil
}
func (s *State) previewPortableBackup(services ui.Services) {
	x := s.portableBackup
	if x == nil || x.busy || x.submitted {
		return
	}
	request := x.request.Clone()
	request.Tables = nil
	if request.Scope == "tables" {
		for _, name := range strings.Split(x.tableNames, "\n") {
			if name != "" {
				o := request.Target.Object
				o.Name = name
				o.Kind = "table"
				request.Tables = append(request.Tables, o)
			}
		}
	}
	x.request = request.Clone()
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan portableBackupReply, 1)
	x.cancel, x.results, x.busy, x.plan, x.err = cancel, out, true, nil, ""
	files, plan, engine := s.backupFiles, x.ops.Plan, x.ops.Engine
	go func() {
		defer cancel()
		var e error
		if request.Path == "" {
			if request.Action == "restore" && files.Open != nil {
				request.Path, e = files.Open()
			} else if files.Save != nil {
				extension := ".dump"
				if engine == "sqlite" {
					extension = ".sqlite"
				}
				request.Path, e = files.Save(request.Target.Object.Catalog + extension)
			}
		}
		if e == nil && request.Path == "" {
			e = context.Canceled
		}
		var p contract.BackupPlan
		if e == nil {
			p, e = plan(ctx, request)
		}
		out <- portableBackupReply{plan: p, err: e}
		services.Invalidate()
	}()
}
func (s *State) executePortableBackup(services ui.Services) {
	x := s.portableBackup
	if x == nil || !x.ready() {
		return
	}
	x.guardAttempt++
	attempt := x.guardAttempt
	start := func(services ui.Services) {
		if s.portableBackup != x || x.guardAttempt != attempt || !x.ready() {
			return
		}
		x.guarding = false
		ctx, cancel := context.WithCancel(context.Background())
		out := make(chan portableBackupReply, 1)
		updates := make(chan contract.BackupProgress, 1)
		x.cancel, x.results, x.updates, x.busy, x.submitted = cancel, out, updates, true, true
		plan, execute := x.plan.Clone(), x.ops.Execute
		complete := s.beginActivity(x.request.Target.Object.ProfileID, x.generation, x.database, "", x.request.Action, strings.ToUpper(x.request.Action))
		go func() {
			defer cancel()
			result := execute(ctx, plan, func(p contract.BackupProgress) {
				select {
				case updates <- p:
				default:
				}
			})
			activity := managementActivityOutcome(result.Error)
			if result.State == "uncertain" {
				activity.State = "uncertain"
			}
			contract.CompleteActivity(complete, activity)
			out <- portableBackupReply{outcome: &result}
			services.Invalidate()
		}()
	}
	if x.request.Action == "restore" && x.request.Overwrite && s.deps.BeforeRestore != nil {
		x.guarding = true
		if s.deps.BeforeRestore(services, x.plan.Clone(), start) {
			return
		}
	}
	start(services)
}

func (x *portableBackupPanel) ready() bool {
	return !x.busy && !x.submitted && x.plan != nil && reflect.DeepEqual(x.plan.Request, x.request) &&
		(!x.request.Overwrite || x.confirmation == x.request.Destination) &&
		(x.plan.QuiesceFile == "" || len(x.plan.Affected) == 0 || x.acknowledgeSessions)
}
func (s *State) consumePortableBackup(services ui.Services) {
	x := s.portableBackup
	if x == nil || x.request.Target.Object.ProfileID != s.session().ProfileID {
		return
	}
	if x.guarding && x.plan != nil && s.deps.RestoreGuardPending != nil && !s.deps.RestoreGuardPending(x.plan.Clone()) {
		x.guarding = false
		x.guardAttempt++
	}
	select {
	case p := <-x.updates:
		x.progress = p
	default:
	}
	select {
	case reply := <-x.results:
		x.results, x.updates, x.busy = nil, nil, false
		if x.cancel != nil {
			x.cancel()
			x.cancel = nil
		}
		if reply.outcome != nil {
			x.outcome = reply.outcome
			if reply.outcome.Error != nil {
				x.err = reply.outcome.Error.Error()
			}
			if s.deps.BackupCompleted != nil {
				s.deps.BackupCompleted(services, BackupEvent{Original: x.request.Target, Generation: x.generation, Outcome: *reply.outcome})
			}
		} else if reply.err != nil {
			x.err = reply.err.Error()
		} else {
			w := s.sessionFor(x.request.Target.Object.ProfileID)
			if !w.Connected || w.Generation != x.generation {
				x.err = locales.Text("原连接已失效")
				return
			}
			p := reply.plan.Clone()
			x.plan = &p
			x.request = p.Request.Clone()
		}
	default:
	}
}
func (s *State) portableBackupModal(c *ui.Context) {
	x := s.portableBackup
	if x == nil || x.guarding || x.request.Target.Object.ProfileID != s.session().ProfileID {
		return
	}
	theme.Modal(c, &x.open, func() {
		ui.Column(c).Width(740).MaxWidthPercent(94).Height(650).MaxHeightPercent(94).Gap(8).Children(func() {
			ui.Text(c, locales.Text("备份与恢复")).Bold()
			ui.Text(c, fmt.Sprintf("#%d · %s / %s", x.request.Target.Object.ProfileID, x.request.Target.Object.Catalog, x.request.Target.Object.Schema)).SingleLine()
			ui.Text(c, locales.Text("备份文件路径"))
			ui.TextInput(c, &x.request.Path).Label(locales.Text("备份文件路径")).Disabled(x.busy || x.submitted)
			if x.request.Action == "restore" {
				ui.Text(c, locales.Text("目标数据库或文件"))
				ui.TextInput(c, &x.request.Destination).Label(locales.Text("目标数据库或文件")).Disabled(x.busy || x.submitted)
				ui.Checkbox(c, &x.request.Overwrite, locales.Text("覆盖已有目标")).Disabled(x.busy || x.submitted)
				if x.ops.Engine == "postgresql" {
					ui.Row(c).Gap(8).Children(func() {
						ui.Checkbox(c, &x.request.PreserveOwner, locales.Text("保留所有者")).Disabled(x.busy || x.submitted)
						ui.Checkbox(c, &x.request.PreserveACL, locales.Text("保留权限")).Disabled(x.busy || x.submitted)
					})
				}
				if x.request.Overwrite {
					ui.Text(c, locales.Text("再次输入目标名称以确认覆盖"))
					ui.TextInput(c, &x.confirmation).Label(locales.Text("再次输入目标名称以确认覆盖")).Disabled(x.busy || x.submitted)
					if x.ops.Engine == "sqlite" {
						ui.Checkbox(c, &x.acknowledgeSessions, locales.Text("确认暂停相关 SQL 会话并回滚未提交事务；SQL 正文将保留。")).Disabled(x.busy || x.submitted)
					}
				}
			} else {
				scopes := []string{"database", "schema", "tables"}
				if x.ops.Engine == "sqlite" {
					scopes = []string{"database"}
				}
				ui.Select(c, &x.request.Scope, scopes).Label(locales.Text("备份范围")).Disabled(x.busy || x.submitted)
				if x.request.Scope == "tables" {
					if ui.TextArea(c, &x.tableNames).Label(locales.Text("选定表（每行一个名称）")).Height(55).Disabled(x.busy || x.submitted).Changed() {
						x.plan = nil
					}
				}
			}
			if x.err != "" {
				ui.Text(c, x.err).TextColor(theme.Colors(c).Error).SingleLine().Tooltip(x.err)
			}
			raw := ""
			if x.plan != nil {
				warnings := make([]string, len(x.plan.Warnings))
				for i, warning := range x.plan.Warnings {
					warnings[i] = locales.Text(warning)
				}
				raw = x.plan.Engine + " · " + x.plan.ToolVersion + "\n" + strings.Join(warnings, "\n") + "\n" + strings.Join(x.plan.Objects, "\n")
			}
			if x.outcome != nil {
				raw += "\n" + theme.OutcomeLabel(x.outcome.State) + " · " + x.outcome.Path
				if x.outcome.State == "uncertain" {
					raw += "\n" + locales.Text("恢复结果不确定，禁止重放；请核验数据库。")
				}
			}
			ui.TextArea(c, &raw).Label(locales.Text("备份恢复计划与结果")).ReadOnly(true).Grow(1).MinHeight(40)
			if x.busy {
				ui.Text(c, fmt.Sprintf("%s · %d / %d", x.progress.Phase, x.progress.Completed, x.progress.Total))
			}
			ui.Row(c).Wrap().Gap(8).Justify(ui.End).Shrink(0).Children(func() {
				if ui.Button(c, locales.Text("关闭")).Disabled(x.busy).Clicked() {
					s.closePortableBackup()
				}
				if ui.Button(c, locales.Text("取消操作")).Disabled(!x.busy || x.cancelRequested).Clicked() {
					x.cancelRequested = true
					if x.cancel != nil {
						x.cancel()
					}
				}
				if ui.Button(c, locales.Text("预检查与预览")).Disabled(x.busy || x.submitted).Clicked() {
					s.previewPortableBackup(c.Services())
				}
				if ui.PrimaryButton(c, locales.Text("确认执行")).Disabled(!x.ready()).Clicked() {
					s.executePortableBackup(c.Services())
				}
			})
		})
	})
	if !x.open {
		if x.busy {
			x.open, x.cancelRequested = true, true
			if x.cancel != nil {
				x.cancel()
			}
		} else {
			s.closePortableBackup()
		}
	}
}
