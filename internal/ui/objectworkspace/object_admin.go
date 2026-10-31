package objectworkspace

import (
	"context"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
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
	if !s.capabilities(profile).DesignTables || s.deps.AccountLoader == nil || s.deps.AccountExecutor == nil || !s.sessionFor(profile).Connected {
		return
	}
	if s.accounts != nil && s.accounts.busy {
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
	go func() {
		list, e := loader(ctx, profile)
		var gs []string
		if e == nil && account.User != "" && grants != nil {
			gs, e = grants(ctx, profile, account)
		}
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
	if !s.capabilities(profile).DesignTables || s.deps.Maintenance == nil || s.maintenance != nil {
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
	go func() {
		v, e := executor(ctx, profile, database, table, operation)
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
	ui.Modal(c, &a.open, func() {
		w, h := c.Size()
		ui.Column(c).Width(min(float32(760), w-40)).Height(min(float32(660), h-40)).Padding(16).Gap(8).Background(p.Header).Children(func() {
			ui.Text(c, "MySQL 用户、角色与权限").Bold()
			ui.Text(c, "服务器权限和版本决定可执行能力；错误逐项报告。")
			if a.err != "" {
				ui.Text(c, a.err).TextColor(p.Error)
			}
			names := []string{}
			for _, acct := range a.accounts {
				names = append(names, acct.User+"@"+acct.Host)
			}
			selected := a.user + "@" + a.host
			if len(names) > 0 && ui.Select(c, &selected, names).Label("现有账户").Disabled(a.busy).Changed() {
				for _, acct := range a.accounts {
					if acct.User+"@"+acct.Host == selected {
						a.user, a.host = acct.User, acct.Host
					}
				}
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.TextInput(c, &a.user).Label("账户名").Disabled(a.busy).Grow(1)
				ui.TextInput(c, &a.host).Label("账户主机").Disabled(a.busy).Grow(1)
			})
			actions := []string{"grant", "revoke", "create_user", "password", "create_role", "grant_role", "revoke_role", "lock", "unlock", "drop", "drop_role"}
			ui.Select(c, &a.action, actions).Label("账户操作").Disabled(a.busy)
			if a.action == "password" || a.action == "create_user" {
				ui.TextInput(c, &a.password).Label("账户密码").Password().Disabled(a.busy)
			}
			if a.action == "grant" || a.action == "revoke" {
				ui.TextInput(c, &a.privileges).Label("权限（逗号分隔）").Disabled(a.busy)
				ui.Row(c).Gap(8).Children(func() {
					ui.TextInput(c, &a.database).Label("授权数据库（空为全部）").Disabled(a.busy).Grow(1)
					ui.TextInput(c, &a.table).Label("授权表（空为全部）").Disabled(a.busy).Grow(1)
				})
			}
			if a.action == "grant_role" || a.action == "revoke_role" {
				ui.Row(c).Gap(8).Children(func() {
					ui.TextInput(c, &a.roleUser).Label("角色名").Disabled(a.busy).Grow(1)
					ui.TextInput(c, &a.roleHost).Label("角色主机").Disabled(a.busy).Grow(1)
				})
			}
			raw := strings.Join(a.grants, "\n")
			ui.TextArea(c, &raw).Label("当前授权").ReadOnly(true).Grow(1)
			if a.outcome != nil {
				for i, step := range a.outcome.Steps {
					ui.Text(c, fmt.Sprintf("%d · %s", i+1, step.State)).Tooltip(step.SQL)
				}
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "关闭").Disabled(a.busy).Clicked() {
					a.password = ""
					s.accounts = nil
				}
				if ui.Button(c, "取消操作").Disabled(!a.busy || a.cancel == nil).Clicked() {
					a.cancel()
				}
				if ui.Button(c, "读取账户与授权").Disabled(a.busy).Clicked() {
					a.blocked = false
					s.readAccounts(c.Services())
				}
				if ui.PrimaryButton(c, "确认执行账户操作").Disabled(a.busy || a.blocked || a.user == "" || !s.sessionFor(a.profile).Connected || s.sessionFor(a.profile).Generation != a.sessionGeneration).Clicked() {
					a.busy = true
					a.err = ""
					ctx, cancel := context.WithCancel(context.Background())
					a.cancel = cancel
					ch := make(chan mysqlsvc.DDLOutcome, 1)
					a.result = ch
					change := mysqlsvc.AccountChange{Action: a.action, Account: mysqlsvc.MySQLAccount{User: a.user, Host: a.host}, Password: a.password, Privileges: strings.Split(a.privileges, ","), Database: a.database, Table: a.table, Role: mysqlsvc.MySQLAccount{User: a.roleUser, Host: a.roleHost}}
					profile, generation, executor, services := a.profile, a.generation, s.deps.AccountExecutor, c.Services()
					go func() {
						ch <- executor(ctx, profile, generation, []mysqlsvc.AccountChange{change})
						services.Invalidate()
					}()
				}
			})
		})
	})
}
