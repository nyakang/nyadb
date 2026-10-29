package objectworkspace

import (
	"context"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type mysqlTableChangeResult struct {
	profileID  int64
	generation uint64
	database   string
	table      string
	action     mysqlsvc.MySQLTableAction
	newName    string
	err        error
}

func mysqlTableActionLabel(action mysqlsvc.MySQLTableAction) string {
	switch action {
	case mysqlsvc.MySQLTableCreate:
		return "新建表"
	case mysqlsvc.MySQLTableRename:
		return "重命名"
	case mysqlsvc.MySQLTableDrop:
		return "删除表"
	case mysqlsvc.MySQLTableDeleteRows:
		return "清空表"
	case mysqlsvc.MySQLTableTruncate:
		return "截断表"
	case mysqlsvc.MySQLTableCopyStructure:
		return "仅复制表结构"
	case mysqlsvc.MySQLTableCopyData:
		return "复制表结构和数据"
	}
	return "表操作"
}

func mysqlTableActionNeedsName(action mysqlsvc.MySQLTableAction) bool {
	switch action {
	case mysqlsvc.MySQLTableCreate, mysqlsvc.MySQLTableRename,
		mysqlsvc.MySQLTableCopyStructure, mysqlsvc.MySQLTableCopyData:
		return true
	}
	return false
}

func mysqlTableActionDangerous(action mysqlsvc.MySQLTableAction) bool {
	return action == mysqlsvc.MySQLTableDrop ||
		action == mysqlsvc.MySQLTableDeleteRows ||
		action == mysqlsvc.MySQLTableTruncate
}

func (s *State) CanChangeTable(profileID int64) bool {
	return s.runner() != nil &&
		!s.tablePending && s.session().Connected && s.session().ProfileID == profileID
}

// Shared by the native navigator and central object grid. Unsupported entries
// remain visibly disabled; they never dispatch placeholder callbacks.
func (s *State) ContextMenu(services ui.Services, m *ui.Menu, profileID int64, database, name, kind string) {
	open, design := mysqlObjectActionLabels(kind)
	if m.Item(locales.Text(open)).Shortcut(0, ui.KeyEnter).Chosen() {
		s.openObjectFor(services, profileID, database, name, kind)
	}
	if kind == "表" {
		m.Submenu(locales.Text("通过配置文件打开表"), func(sub *ui.Menu) {
			sub.Item(locales.Text("无配置文件")).Disabled(true)
			sub.Submenu(locales.Text("默认配置文件"), func(defaults *ui.Menu) {
				defaults.Item(locales.Text("快速模式")).Disabled(true)
			}).Disabled(true)
		}).Disabled(true)
	}
	if m.Item(locales.Text(design)).Shortcut(ui.Ctrl, ui.KeyD).Disabled(Supports(kind)).Chosen() {
		if s.deps.OpenStructure != nil {
			s.deps.OpenStructure(services, profileID, database, name)
		}
	}
	if kind != "表" {
		m.Separator()
		if m.Item(locales.Text("刷新")).Shortcut(0, ui.KeyF5).Chosen() {
			s.loadObjects(services, database, true)
		}
		return
	}

	ready := s.CanChangeTable(profileID)
	begin := func(action mysqlsvc.MySQLTableAction) {
		s.BeginTableChange(profileID, database, name, action)
	}
	// The additional advanced Navicat functions are intentionally disabled
	// until real services and workflows exist.
	if m.Item(locales.Text("新建表")).Shortcut(ui.Ctrl, ui.KeyN).Disabled(!ready).Chosen() {
		begin(mysqlsvc.MySQLTableCreate)
	}
	m.Item(locales.Text("通过 AI 新建表")).Disabled(true)
	if m.Item(locales.Text("删除表")).Shortcut(0, ui.KeyDelete).Disabled(!ready).Chosen() {
		begin(mysqlsvc.MySQLTableDrop)
	}
	if m.Item(locales.Text("清空表")).Disabled(!ready).Chosen() {
		begin(mysqlsvc.MySQLTableDeleteRows)
	}
	if m.Item(locales.Text("截断表")).Disabled(!ready).Chosen() {
		begin(mysqlsvc.MySQLTableTruncate)
	}
	m.Submenu(locales.Text("复制表"), func(sub *ui.Menu) {
		if sub.Item(locales.Text("结构和数据")).Chosen() {
			begin(mysqlsvc.MySQLTableCopyData)
		}
		if sub.Item(locales.Text("仅结构")).Chosen() {
			begin(mysqlsvc.MySQLTableCopyStructure)
		}
	}).Disabled(!ready)
	m.Item(locales.Text("设置权限")).Disabled(true)
	m.Separator()
	for _, label := range []string{"导入向导…", "导出向导…", "数据字典…", "数据生成…", "数据脱敏…"} {
		m.Item(locales.Text(label)).Disabled(true)
	}
	m.Separator()
	m.Submenu(locales.Text("转储 SQL 文件"), func(sub *ui.Menu) {
		sub.Item(locales.Text("结构和数据…")).Disabled(true)
		sub.Item(locales.Text("仅结构…")).Disabled(true)
	}).Disabled(true)
	m.Submenu(locales.Text("维护"), func(sub *ui.Menu) {
		sub.Item(locales.Text("分析表")).Disabled(true)
		sub.Submenu(locales.Text("检查表"), func(check *ui.Menu) {
			for _, label := range []string{"常规", "快速（Quick）", "快（Fast）", "已更改", "扩展"} {
				check.Item(locales.Text(label)).Disabled(true)
			}
		}).Disabled(true)
		sub.Item(locales.Text("优化表")).Disabled(true)
		sub.Submenu(locales.Text("修复表"), func(repair *ui.Menu) {
			repair.Item(locales.Text("快速（Quick）")).Disabled(true)
			repair.Item(locales.Text("扩展")).Disabled(true)
		}).Disabled(true)
		sub.Item(locales.Text("获取行的总数")).Disabled(true)
	}).Disabled(true)
	for _, label := range []string{"逆向表到模型…", "创建 BI 工作区…"} {
		m.Item(locales.Text(label)).Disabled(true)
	}
	m.Separator()
	m.Submenu(locales.Text("管理组"), func(sub *ui.Menu) {
		sub.Item(locales.Text("新建组")).Disabled(true)
		sub.Item(locales.Text("移动到")).Disabled(true)
		sub.Item(locales.Text("从组中排除")).Disabled(true)
	}).Disabled(true)
	m.Item(locales.Text("复制")).Shortcut(ui.Ctrl, ui.KeyC).Disabled(true)
	m.Item(locales.Text("粘贴")).Shortcut(ui.Ctrl, ui.KeyV).Disabled(true)
	m.Item(locales.Text("共享…")).Disabled(true)
	if m.Item(locales.Text("重命名")).Shortcut(0, ui.KeyF2).Disabled(!ready).Chosen() {
		begin(mysqlsvc.MySQLTableRename)
	}
	m.Separator()
	if m.Item(locales.Text("刷新")).Shortcut(0, ui.KeyF5).
		Disabled(s.currentBrowser().Pending != "" || !s.session().Connected).Chosen() {
		s.loadObjects(services, database, true)
	}
}

func (s *State) BeginTableChange(profileID int64, database, name string, action mysqlsvc.MySQLTableAction) {
	if !s.CanChangeTable(profileID) {
		return
	}
	s.tableDialog = true
	s.tableAction = action
	s.tableProfileID = profileID
	s.tableDatabase = database
	s.tableName = name
	s.tableNewName = ""
	if action == mysqlsvc.MySQLTableRename {
		s.tableNewName = name
	}
	s.tableConfirmation = ""
	s.tableError = ""
}

func (s *State) Modals(c *ui.Context) {
	if !s.tableDialog {
		return
	}
	colors := theme.Colors(c)
	label := mysqlTableActionLabel(s.tableAction)
	db, name := s.tableDatabase, s.tableName
	danger := mysqlTableActionDangerous(s.tableAction)
	ui.Modal(c, &s.tableDialog, func() {
		width, _ := c.Size()
		ui.Column(c).Width(min(float32(510), width-40)).Padding(18, 23).
			Background(colors.Header).Gap(12).Children(func() {
			ui.Text(c, locales.Text(label)).Bold().FontSize(theme.Font(c, 15))
			target := "数据库：" + db
			if s.tableAction != mysqlsvc.MySQLTableCreate {
				target += "    表：" + name
			}
			ui.Text(c, target).
				FontSize(theme.Font(c, 12)).TextColor(colors.Muted)
			if mysqlTableActionNeedsName(s.tableAction) {
				ui.Text(c, "目标表名称").FontSize(theme.Font(c, 12))
				ui.TextInput(c, &s.tableNewName).Label("新表名称").Grow(1)
			}
			switch s.tableAction {
			case mysqlsvc.MySQLTableCreate:
				ui.Text(c, "创建 InnoDB 基础表，初始字段为 id BIGINT AUTO_INCREMENT PRIMARY KEY；完整结构设计尚未接入。").FontSize(theme.Font(c, 12))
			case mysqlsvc.MySQLTableDeleteRows:
				ui.Text(c, "将执行 DELETE FROM，移除全部行；InnoDB 允许在事务中回滚，但本操作不会自动开启事务。").FontSize(theme.Font(c, 12))
			case mysqlsvc.MySQLTableTruncate:
				ui.Text(c, "将执行 TRUNCATE TABLE，通常重置自增值，并发生隐式提交；不能按普通删除回滚。").FontSize(theme.Font(c, 12)).TextColor(colors.Error)
			case mysqlsvc.MySQLTableDrop:
				ui.Text(c, "将执行 DROP TABLE，永久删除表结构和数据。可能发生隐式提交。").FontSize(theme.Font(c, 12)).TextColor(colors.Error)
			case mysqlsvc.MySQLTableCopyData:
				ui.Text(c, "先 CREATE TABLE LIKE，再 INSERT SELECT；两步不是原子操作。复制失败时新表可能保留。").FontSize(theme.Font(c, 12))
			case mysqlsvc.MySQLTableRename:
				ui.Text(c, "将执行 RENAME TABLE，并使原有名称下的已打开数据/结构标签失效。").FontSize(theme.Font(c, 12))
			}
			if danger {
				ui.Text(c, "请输入原表名称以确认此操作：").FontSize(theme.Font(c, 12))
				ui.TextInput(c, &s.tableConfirmation).Label("确认原表名称").Grow(1)
			}
			if s.tableError != "" {
				ui.Text(c, s.tableError).TextColor(colors.Error).FontSize(theme.Font(c, 12))
			}
			if s.tablePending {
				ui.Text(c, "正在执行数据库操作…").FontSize(theme.Font(c, 12))
			}
			valid := !s.tablePending && s.tableProfileID == s.session().ProfileID &&
				s.session().Connected
			if mysqlTableActionNeedsName(s.tableAction) {
				valid = valid && strings.TrimSpace(s.tableNewName) != "" &&
					(s.tableAction == mysqlsvc.MySQLTableCreate ||
						s.tableNewName != name)
			}
			if danger {
				valid = valid && s.tableConfirmation == name
			}
			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				if ui.Button(c, "取消").Disabled(s.tablePending).Clicked() {
					s.tableDialog = false
				}
				if ui.PrimaryButton(c, "确认"+label).Disabled(!valid).Clicked() {
					s.startMySQLTableChange(c.Services())
				}
			})
		})
	})
}

func (s *State) startMySQLTableChange(services ui.Services) {
	if s.tablePending || !s.tableDialog || s.tableProfileID != s.session().ProfileID ||
		!s.session().Connected {
		return
	}
	action := s.tableAction
	if mysqlTableActionDangerous(action) && s.tableConfirmation != s.tableName {
		return
	}
	if mysqlTableActionNeedsName(action) && (strings.TrimSpace(s.tableNewName) == "" ||
		(action != mysqlsvc.MySQLTableCreate && s.tableNewName == s.tableName)) {
		return
	}
	request := mysqlsvc.MySQLTableChange{
		Action: action, Database: s.tableDatabase,
		Table: s.tableName, NewName: s.tableNewName,
	}
	if action == mysqlsvc.MySQLTableCreate {
		// Creation has no source table. The service creates Table, while NewName
		// is the destination only for rename and copy operations.
		request.Table = s.tableNewName
		request.NewName = ""
	}
	run := s.runner()
	if run == nil {
		s.tableError = "MySQL 会话不可用"
		return
	}

	id := s.tableProfileID
	generation := s.session().Generation
	ch := make(chan mysqlTableChangeResult, 1)
	s.tableResults = ch
	s.tablePending = true
	s.tableError = ""
	ctx, cancel := context.WithCancel(context.Background())
	s.tableCancel = cancel
	go func() {
		err := run(ctx, id, request)
		ch <- mysqlTableChangeResult{profileID: id, generation: generation, database: request.Database,
			table: request.Table, newName: request.NewName, action: request.Action, err: err}
		services.Invalidate()
	}()
}

func (s *State) consumeMySQLTableChange(services ui.Services) {
	if s.tableResults == nil {
		return
	}
	select {
	case r := <-s.tableResults:
		if s.tableCancel != nil {
			s.tableCancel()
		}
		s.tableCancel = nil
		s.tableResults = nil
		s.tablePending = false
		current := r.profileID == s.session().ProfileID && r.generation == s.session().Generation &&
			s.session().Connected
		parked := s.sessionFor(r.profileID)
		parkedValid := parked.ProfileID == r.profileID && parked.Generation == r.generation && parked.Connected

		if !current && !parkedValid {
			s.tableDialog = false
			s.tableError = ""
			s.report("表操作的原会话已失效；执行结果可能不确定，请重新连接后检查数据库")
			return
		}
		if r.err != nil {
			s.tableDialog = current
			s.tableError = r.err.Error()
			s.report(fmt.Sprintf("%s失败：%v", mysqlTableActionLabel(r.action), r.err))
		} else {
			s.tableDialog = false
			s.tableError = ""
			s.report(mysqlTableActionLabel(r.action) + "成功：" + r.database + "." + r.table)
		}
		if s.deps.Changed != nil {
			s.deps.Changed(services, TableEvent{ProfileID: r.profileID, Generation: r.generation, Database: r.database, Table: r.table, NewName: r.newName, Action: r.action, Error: r.err})
		}

	default:
	}
}

func (s *State) TableDialogOpen() bool                  { return s.tableDialog }
func (s *State) TablePending() bool                     { return s.tablePending }
func (s *State) TableAction() mysqlsvc.MySQLTableAction { return s.tableAction }
func (s *State) TableError() string                     { return s.tableError }

// CancelTableFor requests acknowledgement from the original operation. Keep
// pending set until its executor returns so an uncertain command cannot replay.
func (s *State) CancelTableFor(profileID int64) {
	if s.tableProfileID != profileID {
		return
	}
	if s.tableCancel != nil {
		s.tableCancel()
	}
	if !s.tablePending {
		s.tableDialog = false
	}
}
