package objectworkspace

import (
	"context"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type MaintenanceEvent struct {
	Original   contract.SchemaDesignTarget
	Generation uint64
	Database   string
	Outcome    contract.MaintenanceOutcome
}
type maintenanceReply struct {
	plan    contract.MaintenancePlan
	outcome *contract.MaintenanceOutcome
	err     error
}
type maintenancePanel struct {
	request                                contract.MaintenanceRequest
	generation                             uint64
	database                               string
	ops                                    contract.MaintenanceOperations
	plan                                   *contract.MaintenancePlan
	outcome                                *contract.MaintenanceOutcome
	open, busy, submitted, cancelRequested bool
	err                                    string
	cancel                                 context.CancelFunc
	results                                <-chan maintenanceReply
	updates                                <-chan contract.MaintenanceProgress
	progress                               contract.MaintenanceProgress
}

func (s *State) maintenanceActions(profile int64, database, table string) []contract.MaintenanceAction {
	if !s.capabilities(profile).Maintenance || !s.sessionFor(profile).Connected {
		return nil
	}
	if s.deps.MaintenanceFor != nil {
		_, target := s.schemaBinding(profile, database, table)
		if !s.operationAvailable("maintenance", target) {
			return nil
		}
		ops := s.deps.MaintenanceFor(profile)
		if ops.Actions != nil && ops.Plan != nil && ops.Execute != nil {
			return ops.Actions(target)
		}
		return nil
	}
	if s.deps.Maintenance == nil || !s.canAdministration(profile) {
		return nil
	}
	return []contract.MaintenanceAction{{ID: "ANALYZE", Label: "分析表"}, {ID: "CHECK", Label: "检查表"}, {ID: "OPTIMIZE", Label: "优化表"}, {ID: "REPAIR", Label: "修复表"}}
}
func (s *State) beginNeutralMaintenance(services ui.Services, profile int64, database, table, action string) {
	if s.maintenancePanel != nil || s.maintenance != nil {
		return
	}
	found := false
	for _, a := range s.maintenanceActions(profile, database, table) {
		if a.ID == action {
			found = true
		}
	}
	if !found {
		return
	}
	_, target := s.schemaBinding(profile, database, table)
	x := &maintenancePanel{request: contract.MaintenanceRequest{Target: target, Action: action}, generation: s.sessionFor(profile).Generation, database: database, ops: s.deps.MaintenanceFor(profile), open: true, busy: true}
	s.maintenancePanel = x
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan maintenanceReply, 1)
	x.cancel, x.results = cancel, out
	request, plan := x.request, x.ops.Plan
	go func() {
		defer cancel()
		p, err := plan(ctx, request)
		out <- maintenanceReply{plan: p, err: err}
		services.Invalidate()
	}()
}
func (s *State) maintenanceValid(x *maintenancePanel) bool {
	w := s.sessionFor(x.request.Target.Object.ProfileID)
	_, target := s.schemaBinding(x.request.Target.Object.ProfileID, x.database, x.request.Target.Object.Name)
	return w.Connected && w.Generation == x.generation && target == x.request.Target
}
func (s *State) executeMaintenance(services ui.Services) {
	x := s.maintenancePanel
	if x == nil || x.busy || x.submitted || x.plan == nil || !s.maintenanceValid(x) {
		return
	}
	x.busy, x.submitted = true, true
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan maintenanceReply, 1)
	x.cancel, x.results = cancel, out
	updates := make(chan contract.MaintenanceProgress, 1)
	x.updates = updates
	plan, execute := *x.plan, x.ops.ExecuteWithProgress
	complete := s.beginActivity(x.request.Target.Object.ProfileID, x.generation, x.database, x.request.Target.Object.Name, "maintenance", plan.SQL)
	go func() {
		defer cancel()
		result := execute(ctx, plan, func(p contract.MaintenanceProgress) {
			select {
			case updates <- p:
			default:
			}
			services.Invalidate()
		})
		activity := managementActivityOutcome(result.Error)
		if result.State == "uncertain" {
			activity.State = "uncertain"
		}
		contract.CompleteActivity(complete, activity)
		out <- maintenanceReply{outcome: &result}
		services.Invalidate()
	}()
}
func (s *State) consumeMaintenance(services ui.Services) {
	x := s.maintenancePanel
	if x == nil {
		return
	}
	if !s.maintenanceValid(x) && x.cancel != nil {
		x.cancel()
	}
	// Completion stays owned by its original profile while another is active.
	if x.request.Target.Object.ProfileID != s.session().ProfileID {
		return
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
			if s.deps.Maintained != nil {
				s.deps.Maintained(services, MaintenanceEvent{Original: x.request.Target, Generation: x.generation, Database: x.database, Outcome: *reply.outcome})
			}
			if reply.outcome.Error != nil {
				x.err = reply.outcome.Error.Error()
			}
		} else if !s.maintenanceValid(x) {
			x.err = locales.Text("原维护会话已失效")
		} else if reply.err != nil {
			x.err = reply.err.Error()
		} else if reply.plan.Request != x.request {
			x.err = locales.Text("原维护会话已失效")
		} else {
			x.plan = &reply.plan
		}
	default:
	}
}
func (s *State) closeMaintenance() {
	if x := s.maintenancePanel; x != nil && x.cancel != nil {
		x.cancel()
	}
	s.maintenancePanel = nil
}
func (s *State) maintenanceModal(c *ui.Context) {
	x := s.maintenancePanel
	if x == nil || x.request.Target.Object.ProfileID != s.session().ProfileID {
		return
	}
	ui.Modal(c, &x.open, func() {
		ui.Column(c).Width(680).MaxWidthPercent(92).Height(450).MaxHeightPercent(90).Padding(18).Gap(10).Children(func() {
			ui.Text(c, locales.Text("维护")).Bold()
			o := x.request.Target.Object
			ui.Text(c, fmt.Sprintf("#%d · %s / %s · %s", o.ProfileID, o.Catalog, o.Schema, o.Name)).SingleLine()
			if x.busy {
				ui.Text(c, locales.Text("正在等待维护回执…"))
			}
			if x.err != "" {
				ui.Text(c, x.err).SingleLine().Tooltip(x.err).TextColor(theme.Colors(c).Error)
			}
			raw := ""
			if x.plan != nil {
				raw = x.plan.SQL
				if x.plan.Transactional {
					ui.Text(c, locales.Text("此操作在独立事务中执行。"))
				} else {
					ui.Text(c, locales.Text("此操作不保证回滚；取消后请核对原目标。"))
				}
			}
			if x.outcome != nil {
				var lines []string
				lines = append(lines, x.outcome.State)
				for _, m := range x.outcome.Messages {
					lines = append(lines, m.Table+" · "+m.Operation+" · "+m.Message)
				}
				raw += "\n\n" + strings.Join(lines, "\n")
			}
			ui.TextArea(c, &raw).Label(locales.Text("SQL 预览与维护结果")).ReadOnly(true).Grow(1).MinHeight(40)
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, locales.Text("关闭")).Disabled(x.busy).Clicked() {
					s.closeMaintenance()
				}
				if ui.Button(c, locales.Text("取消操作")).Disabled(!x.busy || x.cancelRequested).Clicked() {
					x.cancelRequested = true
					if x.cancel != nil {
						x.cancel()
					}
				}
				if ui.PrimaryButton(c, locales.Text("确认执行维护")).Disabled(x.busy || x.submitted || x.plan == nil || !s.maintenanceValid(x)).Clicked() {
					s.executeMaintenance(c.Services())
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
			s.closeMaintenance()
		}
	}
}
