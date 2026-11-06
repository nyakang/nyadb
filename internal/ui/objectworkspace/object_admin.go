package objectworkspace

import (
	"context"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/db/contract"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type accountPanel struct {
	sessionGeneration uint64

	profile                                                                            int64
	generation                                                                         uint64
	open, busy, blocked                                                                bool
	user, host, password, action, privileges, database, table, roleUser, roleHost, err string
	accounts                                                                           []mysqlsvc.MySQLAccount
	grants                                                                             []string
	outcome                                                                            *mysqlsvc.DDLOutcome
	cancel                                                                             context.CancelFunc
	load                                                                               <-chan accountRead
	result                                                                             <-chan mysqlsvc.DDLOutcome
}
type accountRead struct {
	accounts []mysqlsvc.MySQLAccount
	grants   []string
	err      error
}
type maintenanceOperation struct {
	profile                    int64
	generation                 uint64
	database, table, operation string
	result                     <-chan maintenanceRead
	cancel                     context.CancelFunc
}
type maintenanceRead struct {
	messages []mysqlsvc.MaintenanceMessage
	err      error
}

func (s *State) OpenAccounts(services ui.Services, profile int64, database, table string) {
	if !s.CanAccounts(profile) {
		return
	}
	gen := s.sessionFor(profile).Generation
	if s.deps.RuntimeGeneration != nil {
		gen = s.deps.RuntimeGeneration(profile)
	}
	s.accounts = &accountPanel{profile: profile, generation: gen, sessionGeneration: s.sessionFor(profile).Generation, open: true, host: "%", roleHost: "%", database: database, table: table, action: "grant", privileges: "SELECT"}
	s.readAccounts(services)
}
func (s *State) readAccounts(services ui.Services) {
	a := s.accounts
	if a == nil || a.busy {
		return
	}
	a.busy = true
	ctx, cancel := context.WithCancel(context.Background())
	ctx = mysqlsvc.WithMySQLSessionGeneration(ctx, a.generation)
	a.cancel = cancel
	ch := make(chan accountRead, 1)
	a.load = ch
	profile, account, loader, grants := a.profile, mysqlsvc.MySQLAccount{User: a.user, Host: a.host}, s.deps.AccountLoader, s.deps.AccountGrants
	if s.deps.AdministrationFor != nil {
		admin := s.deps.AdministrationFor(profile)
		if admin == nil {
			a.busy = false
			a.err = "原账户会话已失效"
			cancel()
			a.load = nil
			return
		}
		loader, grants = admin.Accounts, admin.AccountGrants
	}
	complete := s.beginActivity(profile, a.sessionGeneration, a.database, a.table, "accounts", "READ ACCOUNTS AND GRANTS")
	go func() {
		list, e := loader(ctx, profile)
		var gs []string
		if e == nil && account.User != "" && grants != nil {
			gs, e = grants(ctx, profile, account)
		}
		contract.CompleteActivity(complete, managementActivityOutcome(e))
		ch <- accountRead{list, gs, e}
		services.Invalidate()
	}()
}
func (s *State) consumeAdmin(services ui.Services) {
	if a := s.accounts; a != nil && a.profile == s.session().ProfileID {
		if a.load != nil {
			select {
			case r := <-a.load:
				a.load = nil
				a.busy = false
				if a.cancel != nil {
					a.cancel()
					a.cancel = nil
				}
				if !s.sessionFor(a.profile).Connected || s.sessionFor(a.profile).Generation != a.sessionGeneration {
					a.err = "原账户会话已失效"
					continueAdminNoop := true
					_ = continueAdminNoop
					return
				}
				a.accounts, a.grants = r.accounts, r.grants
				if r.err != nil {
					a.err = r.err.Error()
				}
			default:
			}
		}
		if a.result != nil {
			select {
			case r := <-a.result:
				a.result = nil
				a.busy = false
				if a.cancel != nil {
					a.cancel()
					a.cancel = nil
				}
				a.password = ""
				a.outcome = &r
				a.blocked = r.State == "uncertain" || r.State == "partial"
				if r.Error != nil {
					a.err = r.Error.Error()
				} else {
					a.err = "权限操作完成；可重新读取授权核验"
				}
			default:
			}
		}
	}
	if m := s.maintenance; m != nil && m.profile == s.session().ProfileID {
		select {
		case r := <-m.result:
			if m.cancel != nil {
				m.cancel()
			}
			s.maintenance = nil
			if r.err != nil {
				s.report("表维护失败：" + r.err.Error())
				return
			}
			for _, message := range r.messages {
				s.report(message.Table + " · " + message.Operation + " · " + message.Type + " · " + message.Message)
			}
		default:
		}
	}
}
func (s *State) BeginMaintenance(services ui.Services, profile int64, database, table, operation string) {
	if s.deps.MaintenanceFor != nil {
		s.beginNeutralMaintenance(services, profile, database, table, operation)
		return
	}
	if !s.canAdministration(profile) || s.deps.Maintenance == nil || s.maintenance != nil {
		return
	}
	session := s.sessionFor(profile)
	if !session.Connected {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	if s.deps.RuntimeGeneration != nil {
		ctx = mysqlsvc.WithMySQLSessionGeneration(ctx, s.deps.RuntimeGeneration(profile))
	}
	ch := make(chan maintenanceRead, 1)
	s.maintenance = &maintenanceOperation{profile: profile, generation: session.Generation, database: database, table: table, operation: operation, result: ch, cancel: cancel}
	executor := s.deps.Maintenance
	if s.deps.AdministrationFor != nil {
		admin := s.deps.AdministrationFor(profile)
		if admin == nil {
			cancel()
			s.maintenance = nil
			return
		}
		executor = admin.MaintainTable
	}
	complete := s.beginActivity(profile, session.Generation, database, table, "maintenance", operation)
	go func() {
		v, e := executor(ctx, profile, database, table, operation)
		contract.CompleteActivity(complete, managementActivityOutcome(e))
		ch <- maintenanceRead{v, e}
		services.Invalidate()
	}()
}
func (s *State) accountModal(c *ui.Context) {
	a := s.accounts
	if a == nil {
		return
	}
	if !a.open {
		if a.busy {
			a.open = true
		} else {
			a.password = ""
			s.accounts = nil
			return
		}
	}
	p := theme.Colors(c)
	theme.Modal(c, &a.open, func() {
		w, h := c.Size()
		ui.Column(c).Width(min(float32(760), w-68)).Height(min(float32(660), h-68)).Gap(8).Children(func() {
			title := locales.Text("MySQL 用户、角色与权限")
			if s.deps.Engine != nil && s.deps.Engine(a.profile) == "tidb" {
				title = strings.Replace(title, "MySQL", "TiDB", 1)
			}
			ui.Text(c, title).Bold()
			ui.Text(c, locales.Text("服务器权限和版本决定可执行能力；错误逐项报告。"))
			if a.err != "" {
				ui.Text(c, a.err).TextColor(p.Error)
			}
			ui.Scroll(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
				theme.Fieldset(c, locales.Text("账户与授权"), func() {
					names := []string{}
					for _, acct := range a.accounts {
						names = append(names, acct.User+"@"+acct.Host)
					}
					selected := a.user + "@" + a.host
					if len(names) > 0 && ui.Select(c, &selected, names).Label(locales.Text("现有账户")).Disabled(a.busy).Changed() {
						for _, acct := range a.accounts {
							if acct.User+"@"+acct.Host == selected {
								a.user, a.host = acct.User, acct.Host
							}
						}
					}
					ui.Row(c).Gap(8).Children(func() {
						theme.TextField(c, &a.user, locales.Text("账户名")).Disabled(a.busy).Grow(1)
						theme.TextField(c, &a.host, locales.Text("账户主机")).Disabled(a.busy).Grow(1)
					})
					actions := []string{"grant", "revoke", "create_user", "password", "create_role", "grant_role", "revoke_role", "lock", "unlock", "drop", "drop_role"}
					theme.Choice(c, &a.action, actions, accountActionLabel).Label(locales.Text("账户操作")).Disabled(a.busy)
					if a.action == "password" || a.action == "create_user" {
						theme.TextField(c, &a.password, locales.Text("账户密码")).Password().Disabled(a.busy)
					}
					if a.action == "grant" || a.action == "revoke" {
						theme.TextField(c, &a.privileges, locales.Text("权限（逗号分隔）")).Disabled(a.busy)
						ui.Row(c).Gap(8).Children(func() {
							theme.TextField(c, &a.database, locales.Text("授权数据库（空为全部）")).Disabled(a.busy).Grow(1)
							theme.TextField(c, &a.table, locales.Text("授权表（空为全部）")).Disabled(a.busy).Grow(1)
						})
					}
					if a.action == "grant_role" || a.action == "revoke_role" {
						ui.Row(c).Gap(8).Children(func() {
							theme.TextField(c, &a.roleUser, locales.Text("角色名")).Disabled(a.busy).Grow(1)
							theme.TextField(c, &a.roleHost, locales.Text("角色主机")).Disabled(a.busy).Grow(1)
						})
					}
					raw := strings.Join(a.grants, "\n")
					ui.TextArea(c, &raw).Label(locales.Text("当前授权")).ReadOnly(true).Height(140).MinHeight(60)
					if a.outcome != nil {
						for i, step := range a.outcome.Steps {
							ui.Text(c, fmt.Sprintf("%d · %s", i+1, theme.OutcomeLabel(step.State))).Tooltip(step.SQL)
						}
					}
				})
			})
			ui.Row(c).Wrap().Gap(8).Justify(ui.End).Shrink(0).Children(func() {
				if ui.Button(c, locales.Text("关闭")).Disabled(a.busy).Clicked() {
					a.password = ""
					s.accounts = nil
				}
				if ui.Button(c, locales.Text("取消操作")).Disabled(!a.busy || a.cancel == nil).Clicked() {
					a.cancel()
				}
				if ui.Button(c, locales.Text("读取账户与授权")).Disabled(a.busy).Clicked() {
					a.blocked = false
					s.readAccounts(c.Services())
				}
				if ui.PrimaryButton(c, locales.Text("确认执行账户操作")).Disabled(a.busy || a.blocked || a.user == "" || !s.sessionFor(a.profile).Connected || s.sessionFor(a.profile).Generation != a.sessionGeneration).Clicked() {
					a.busy = true
					a.err = ""
					ctx, cancel := context.WithCancel(context.Background())
					a.cancel = cancel
					ch := make(chan mysqlsvc.DDLOutcome, 1)
					a.result = ch
					change := mysqlsvc.AccountChange{Action: a.action, Account: mysqlsvc.MySQLAccount{User: a.user, Host: a.host}, Password: a.password, Privileges: strings.Split(a.privileges, ","), Database: a.database, Table: a.table, Role: mysqlsvc.MySQLAccount{User: a.roleUser, Host: a.roleHost}}
					profile, generation, executor, services := a.profile, a.generation, s.deps.AccountExecutor, c.Services()
					if s.deps.AdministrationFor != nil {
						admin := s.deps.AdministrationFor(profile)
						if admin == nil {
							cancel()
							a.busy = false
							a.result = nil
							return
						}
						executor = admin.ChangeAccounts
					}
					complete := s.beginActivity(profile, a.sessionGeneration, change.Database, change.Table, "accounts", change.Action+" · "+change.Account.User+"@"+change.Account.Host)
					go func() {
						outcome := executor(ctx, profile, generation, []mysqlsvc.AccountChange{change})
						contract.CompleteActivity(complete, ddlActivityOutcome(outcome))
						ch <- outcome
						services.Invalidate()
					}()
				}
			})
		})
	})
}

func accountActionLabel(value string) string {
	labels := map[string]string{"grant": "授予权限", "revoke": "撤销权限", "create_user": "创建账户", "password": "修改密码", "create_role": "创建角色", "grant_role": "授予角色", "revoke_role": "撤销角色", "lock": "锁定账户", "unlock": "解锁账户", "drop": "删除账户", "drop_role": "删除角色"}
	if label, ok := labels[value]; ok {
		return locales.Text(label)
	}
	return locales.Text("未知操作")
}
