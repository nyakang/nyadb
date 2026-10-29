package shell

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

func (s *State) canChangeMySQLTable(profileID int64) bool {
	return (s.sessions.Manager != nil || s.mysqlTableRunner != nil) &&
		!s.mysqlTablePending && s.sessions.Current().Status == "已连接" && s.sessions.Current().ProfileID == profileID
}

// Shared by the native navigator and central object grid. Unsupported entries
// remain visibly disabled; they never dispatch placeholder callbacks.
func (s *State) mysqlObjectContextMenu(services ui.Services, m *ui.Menu, profileID int64, database, name, kind string) {
	open, design := mysqlObjectActionLabels(kind)
	if m.Item(locales.Text(open)).Shortcut(0, ui.KeyEnter).Chosen() {
		s.openMySQLObject(services, database, name, kind)
	}
	if kind == "表" {
		m.Submenu(locales.Text("通过配置文件打开表"), func(sub *ui.Menu) {
			sub.Item(locales.Text("无配置文件")).Disabled(true)
			sub.Submenu(locales.Text("默认配置文件"), func(defaults *ui.Menu) {
				defaults.Item(locales.Text("快速模式")).Disabled(true)
			}).Disabled(true)
		}).Disabled(true)
	}
	if m.Item(locales.Text(design)).Shortcut(ui.Ctrl, ui.KeyD).Disabled(isMySQLProgram(kind)).Chosen() {
		s.openMySQLStructure(services, database, name)
	}
	if kind != "表" {
		m.Separator()
		if m.Item(locales.Text("刷新")).Shortcut(0, ui.KeyF5).Chosen() {
			s.loadMySQLObjects(services, database, true)
		}
		return
	}

	ready := s.canChangeMySQLTable(profileID)
	begin := func(action mysqlsvc.MySQLTableAction) {
		s.beginMySQLTableChange(profileID, database, name, action)
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
		Disabled(s.sessions.Current().Pending != "" || s.sessions.Current().Status != "已连接").Chosen() {
		s.loadMySQLObjects(services, database, true)
	}
}

func (s *State) beginMySQLTableChange(profileID int64, database, name string, action mysqlsvc.MySQLTableAction) {
	if !s.canChangeMySQLTable(profileID) {
		return
	}
	s.mysqlTableDialog = true
	s.mysqlTableAction = action
	s.mysqlTableProfileID = profileID
	s.mysqlTableDatabase = database
	s.mysqlTableName = name
	s.mysqlTableNewName = ""
	if action == mysqlsvc.MySQLTableRename {
		s.mysqlTableNewName = name
	}
	s.mysqlTableConfirmation = ""
	s.mysqlTableError = ""
}

func (s *State) mysqlTableChangeModal(c *ui.Context) {
	if !s.mysqlTableDialog {
		return
	}
	colors := theme.Colors(c)
	label := mysqlTableActionLabel(s.mysqlTableAction)
	db, name := s.mysqlTableDatabase, s.mysqlTableName
	danger := mysqlTableActionDangerous(s.mysqlTableAction)
	ui.Modal(c, &s.mysqlTableDialog, func() {
		width, _ := c.Size()
		ui.Column(c).Width(min(float32(510), width-40)).Padding(18, 23).
			Background(colors.Header).Gap(12).Children(func() {
			ui.Text(c, locales.Text(label)).Bold().FontSize(theme.Font(c, 15))
			target := "数据库：" + db
			if s.mysqlTableAction != mysqlsvc.MySQLTableCreate {
				target += "    表：" + name
			}
			ui.Text(c, target).
				FontSize(theme.Font(c, 12)).TextColor(colors.Muted)
			if mysqlTableActionNeedsName(s.mysqlTableAction) {
				ui.Text(c, "目标表名称").FontSize(theme.Font(c, 12))
				ui.TextInput(c, &s.mysqlTableNewName).Label("新表名称").Grow(1)
			}
			switch s.mysqlTableAction {
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
				ui.TextInput(c, &s.mysqlTableConfirmation).Label("确认原表名称").Grow(1)
			}
			if s.mysqlTableError != "" {
				ui.Text(c, s.mysqlTableError).TextColor(colors.Error).FontSize(theme.Font(c, 12))
			}
			if s.mysqlTablePending {
				ui.Text(c, "正在执行数据库操作…").FontSize(theme.Font(c, 12))
			}
			valid := !s.mysqlTablePending && s.mysqlTableProfileID == s.sessions.Current().ProfileID &&
				s.sessions.Current().Status == "已连接"
			if mysqlTableActionNeedsName(s.mysqlTableAction) {
				valid = valid && strings.TrimSpace(s.mysqlTableNewName) != "" &&
					(s.mysqlTableAction == mysqlsvc.MySQLTableCreate ||
						s.mysqlTableNewName != name)
			}
			if danger {
				valid = valid && s.mysqlTableConfirmation == name
			}
			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				if ui.Button(c, "取消").Disabled(s.mysqlTablePending).Clicked() {
					s.mysqlTableDialog = false
				}
				if ui.PrimaryButton(c, "确认"+label).Disabled(!valid).Clicked() {
					s.startMySQLTableChange(c.Services())
				}
			})
		})
	})
}

func (s *State) startMySQLTableChange(services ui.Services) {
	if s.mysqlTablePending || !s.mysqlTableDialog || s.mysqlTableProfileID != s.sessions.Current().ProfileID ||
		s.sessions.Current().Status != "已连接" {
		return
	}
	action := s.mysqlTableAction
	if mysqlTableActionDangerous(action) && s.mysqlTableConfirmation != s.mysqlTableName {
		return
	}
	if mysqlTableActionNeedsName(action) && (strings.TrimSpace(s.mysqlTableNewName) == "" ||
		(action != mysqlsvc.MySQLTableCreate && s.mysqlTableNewName == s.mysqlTableName)) {
		return
	}
	request := mysqlsvc.MySQLTableChange{
		Action: action, Database: s.mysqlTableDatabase,
		Table: s.mysqlTableName, NewName: s.mysqlTableNewName,
	}
	if action == mysqlsvc.MySQLTableCreate {
		// Creation has no source table. The service creates Table, while NewName
		// is the destination only for rename and copy operations.
		request.Table = s.mysqlTableNewName
		request.NewName = ""
	}
	run := s.mysqlTableRunner
	if run == nil {
		if s.sessions.Manager == nil {
			s.mysqlTableError = "MySQL 会话不可用"
			return
		}
		run = s.sessions.Manager.ChangeTable
	}
	id := s.mysqlTableProfileID
	generation := s.sessions.Current().Generation
	ch := make(chan mysqlTableChangeResult, 1)
	s.mysqlTableResults = ch
	s.mysqlTablePending = true
	s.mysqlTableError = ""
	go func() {
		err := run(context.Background(), id, request)
		ch <- mysqlTableChangeResult{profileID: id, generation: generation, database: request.Database,
			table: request.Table, newName: request.NewName, action: request.Action, err: err}
		services.Invalidate()
	}()
}

func (s *State) consumeMySQLTableChange(services ui.Services) {
	if s.mysqlTableResults == nil {
		return
	}
	select {
	case r := <-s.mysqlTableResults:
		s.mysqlTableResults = nil
		s.mysqlTablePending = false
		current := r.profileID == s.sessions.Current().ProfileID && r.generation == s.sessions.Current().Generation &&
			s.sessions.Current().Status == "已连接"
		parked := s.sessions.Parked(r.profileID)
		parkedValid := parked != nil && parked.Generation == r.generation &&
			parked.Status == "已连接"
		if !current && !parkedValid {
			s.mysqlTableDialog = false
			s.mysqlTableError = ""
			s.status = "表操作的原会话已失效；执行结果可能不确定，请重新连接后检查数据库"
			return
		}
		if r.err != nil {
			s.mysqlTableDialog = current
			s.mysqlTableError = r.err.Error()
			s.status = fmt.Sprintf("%s失败：%v", mysqlTableActionLabel(r.action), r.err)
			// CREATE LIKE may succeed even if the subsequent INSERT fails.
			// Refresh metadata to show any partially created destination table.
			if r.action == mysqlsvc.MySQLTableCopyData {
				if current {
					delete(s.sessions.Current().Cache, r.database)
					if s.sessions.Current().Database == r.database {
						s.loadMySQLObjects(services, r.database, true)
					}
				} else {
					delete(parked.Cache, r.database)
				}
			}
			return
		}
		s.mysqlTableDialog = false
		s.mysqlTableError = ""
		// Never let an old profile's completion update another profile's UI.
		if current {
			delete(s.sessions.Current().Cache, r.database)
			if s.sessions.Current().Database == r.database {
				s.loadMySQLObjects(services, r.database, true)
			}
		} else {
			delete(parked.Cache, r.database)
		}
		for i := len(s.tabs) - 1; i > 0; i-- {
			t := s.tabs[i]
			objectMatches := t.kind == tabStructure && t.design.ProfileID() == r.profileID &&
				t.design.Database() == r.database && t.design.Table() == r.table ||
				t.kind == tabMySQLData && t.dataProfileID == r.profileID &&
					t.dataDatabase == r.database && t.dataTable == r.table
			if !objectMatches {
				continue
			}
			switch r.action {
			case mysqlsvc.MySQLTableRename, mysqlsvc.MySQLTableDrop:
				s.closeTab(i)
			case mysqlsvc.MySQLTableDeleteRows, mysqlsvc.MySQLTableTruncate:
				if t.kind == tabMySQLData {
					t.dataOffset = 0
					t.dataAfter = nil
					t.dataCursorHistory = nil
					s.loadMySQLData(services, t, true)
				}
			}
		}
		s.status = mysqlTableActionLabel(r.action) + "成功：" + r.database + "." + r.table
	default:
	}
}
