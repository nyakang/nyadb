package shell

import (
	"context"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type mysqlTableChangeResult struct {
	profileID  int64
	generation uint64
	database   string
	table      string
	action     connections.MySQLTableAction
	newName    string
	err        error
}

func mysqlTableActionLabel(action connections.MySQLTableAction) string {
	switch action {
	case connections.MySQLTableCreate:
		return "新建表"
	case connections.MySQLTableRename:
		return "重命名"
	case connections.MySQLTableDrop:
		return "删除表"
	case connections.MySQLTableDeleteRows:
		return "清空表"
	case connections.MySQLTableTruncate:
		return "截断表"
	case connections.MySQLTableCopyStructure:
		return "仅复制表结构"
	case connections.MySQLTableCopyData:
		return "复制表结构和数据"
	}
	return "表操作"
}

func mysqlTableActionNeedsName(action connections.MySQLTableAction) bool {
	switch action {
	case connections.MySQLTableCreate, connections.MySQLTableRename,
		connections.MySQLTableCopyStructure, connections.MySQLTableCopyData:
		return true
	}
	return false
}

func mysqlTableActionDangerous(action connections.MySQLTableAction) bool {
	return action == connections.MySQLTableDrop ||
		action == connections.MySQLTableDeleteRows ||
		action == connections.MySQLTableTruncate
}

// Shared by the native navigator and central object grid. Unsupported entries
// remain visibly disabled; they never dispatch placeholder callbacks.
func (s *State) mysqlObjectContextMenu(services ui.Services, m *ui.Menu, profileID int64, database, name, kind string) {
	open, design := mysqlObjectActionLabels(kind)
	if m.Item(locales.Text(open)).Shortcut(0, ui.KeyEnter).Chosen() {
		s.openMySQLObject(services, database, name, kind)
	}
	if kind == "表" {
		m.Item(locales.Text("通过配置文件打开表")).Disabled(true)
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

	ready := s.mysqlSessionManager != nil || s.mysqlTableRunner != nil
	ready = ready && !s.mysqlTablePending && s.mysqlSessionStatus == "已连接" &&
		s.mysqlSessionProfileID == profileID
	begin := func(action connections.MySQLTableAction) {
		s.beginMySQLTableChange(profileID, database, name, action)
	}
	// The additional advanced Navicat functions are intentionally disabled
	// until real services and workflows exist.
	if m.Item(locales.Text("新建表")).Disabled(!ready).Chosen() {
		begin(connections.MySQLTableCreate)
	}
	m.Item(locales.Text("通过 AI 新建表")).Disabled(true)
	if m.Item(locales.Text("删除表")).Disabled(!ready).Chosen() {
		begin(connections.MySQLTableDrop)
	}
	if m.Item(locales.Text("清空表")).Disabled(!ready).Chosen() {
		begin(connections.MySQLTableDeleteRows)
	}
	if m.Item(locales.Text("截断表")).Disabled(!ready).Chosen() {
		begin(connections.MySQLTableTruncate)
	}
	m.Submenu(locales.Text("复制表"), func(sub *ui.Menu) {
		if sub.Item(locales.Text("仅复制表结构")).Chosen() {
			begin(connections.MySQLTableCopyStructure)
		}
		if sub.Item(locales.Text("复制表结构和数据")).Chosen() {
			begin(connections.MySQLTableCopyData)
		}
	}).Disabled(!ready)
	m.Item(locales.Text("设置权限")).Disabled(true)
	m.Separator()
	for _, label := range []string{"导入向导…", "导出向导…", "数据字典…", "数据生成…", "数据脱敏…"} {
		m.Item(locales.Text(label)).Disabled(true)
	}
	m.Separator()
	for _, label := range []string{"转储 SQL 文件", "维护", "逆向表到模型…", "创建 BI 工作区…"} {
		m.Item(locales.Text(label)).Disabled(true)
	}
	m.Separator()
	for _, label := range []string{"管理组", "复制", "共享…"} {
		m.Item(locales.Text(label)).Disabled(true)
	}
	if m.Item(locales.Text("重命名")).Shortcut(0, ui.KeyF2).Disabled(!ready).Chosen() {
		begin(connections.MySQLTableRename)
	}
	m.Item(locales.Text("创建打开表快捷方式…")).Disabled(true)
	m.Separator()
	if m.Item(locales.Text("刷新")).Shortcut(0, ui.KeyF5).
		Disabled(s.mysqlObjectPending != "" || s.mysqlSessionStatus != "已连接").Chosen() {
		s.loadMySQLObjects(services, database, true)
	}
}

func (s *State) beginMySQLTableChange(profileID int64, database, name string, action connections.MySQLTableAction) {
	if s.mysqlTablePending || s.mysqlSessionProfileID != profileID ||
		s.mysqlSessionStatus != "已连接" {
		return
	}
	s.mysqlTableDialog = true
	s.mysqlTableAction = action
	s.mysqlTableProfileID = profileID
	s.mysqlTableDatabase = database
	s.mysqlTableName = name
	s.mysqlTableNewName = ""
	if action == connections.MySQLTableRename {
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
			ui.Text(c, "数据库："+db+"    表："+name).
				FontSize(theme.Font(c, 12)).TextColor(colors.Muted)
			if mysqlTableActionNeedsName(s.mysqlTableAction) {
				ui.Text(c, "目标表名称").FontSize(theme.Font(c, 12))
				ui.TextInput(c, &s.mysqlTableNewName).Label("新表名称").Grow(1)
			}
			switch s.mysqlTableAction {
			case connections.MySQLTableCreate:
				ui.Text(c, "创建 InnoDB 基础表，初始字段为 id BIGINT AUTO_INCREMENT PRIMARY KEY；完整结构设计尚未接入。").FontSize(theme.Font(c, 12))
			case connections.MySQLTableDeleteRows:
				ui.Text(c, "将执行 DELETE FROM，移除全部行；InnoDB 允许在事务中回滚，但本操作不会自动开启事务。").FontSize(theme.Font(c, 12))
			case connections.MySQLTableTruncate:
				ui.Text(c, "将执行 TRUNCATE TABLE，通常重置自增值，并发生隐式提交；不能按普通删除回滚。").FontSize(theme.Font(c, 12)).TextColor(colors.Error)
			case connections.MySQLTableDrop:
				ui.Text(c, "将执行 DROP TABLE，永久删除表结构和数据。可能发生隐式提交。").FontSize(theme.Font(c, 12)).TextColor(colors.Error)
			case connections.MySQLTableCopyData:
				ui.Text(c, "先 CREATE TABLE LIKE，再 INSERT SELECT；两步不是原子操作。复制失败时新表可能保留。").FontSize(theme.Font(c, 12))
			case connections.MySQLTableRename:
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
			valid := !s.mysqlTablePending && s.mysqlTableProfileID == s.mysqlSessionProfileID &&
				s.mysqlSessionStatus == "已连接"
			if mysqlTableActionNeedsName(s.mysqlTableAction) {
				valid = valid && strings.TrimSpace(s.mysqlTableNewName) != "" &&
					(s.mysqlTableAction == connections.MySQLTableCreate ||
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
	if s.mysqlTablePending || !s.mysqlTableDialog || s.mysqlTableProfileID != s.mysqlSessionProfileID ||
		s.mysqlSessionStatus != "已连接" {
		return
	}
	action := s.mysqlTableAction
	if mysqlTableActionDangerous(action) && s.mysqlTableConfirmation != s.mysqlTableName {
		return
	}
	if mysqlTableActionNeedsName(action) && (strings.TrimSpace(s.mysqlTableNewName) == "" ||
		(action != connections.MySQLTableCreate && s.mysqlTableNewName == s.mysqlTableName)) {
		return
	}
	request := connections.MySQLTableChange{
		Action: action, Database: s.mysqlTableDatabase,
		Table: s.mysqlTableName, NewName: s.mysqlTableNewName,
	}
	run := s.mysqlTableRunner
	if run == nil {
		if s.mysqlSessionManager == nil {
			s.mysqlTableError = "MySQL 会话不可用"
			return
		}
		run = s.mysqlSessionManager.ChangeTable
	}
	id := s.mysqlTableProfileID
	generation := s.mysqlSessionGeneration
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
		current := r.profileID == s.mysqlSessionProfileID && r.generation == s.mysqlSessionGeneration &&
			s.mysqlSessionStatus == "已连接"
		parked := s.parkedMySQLWorkspace(r.profileID)
		parkedValid := parked != nil && parked.sessionGeneration == r.generation &&
			parked.sessionStatus == "已连接"
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
			if r.action == connections.MySQLTableCopyData {
				if current {
					delete(s.mysqlObjectCache, r.database)
					if s.mysqlSelectedDatabase == r.database {
						s.loadMySQLObjects(services, r.database, true)
					}
				} else {
					delete(parked.objectCache, r.database)
				}
			}
			return
		}
		s.mysqlTableDialog = false
		s.mysqlTableError = ""
		// Never let an old profile's completion update another profile's UI.
		if current {
			delete(s.mysqlObjectCache, r.database)
			if s.mysqlSelectedDatabase == r.database {
				s.loadMySQLObjects(services, r.database, true)
			}
		} else {
			delete(parked.objectCache, r.database)
		}
		for i := len(s.tabs) - 1; i > 0; i-- {
			t := s.tabs[i]
			objectMatches := t.kind == tabStructure && t.structureProfileID == r.profileID &&
				t.structureDatabase == r.database && t.structureTable == r.table ||
				t.kind == tabMySQLData && t.dataProfileID == r.profileID &&
					t.dataDatabase == r.database && t.dataTable == r.table
			if !objectMatches {
				continue
			}
			switch r.action {
			case connections.MySQLTableRename, connections.MySQLTableDrop:
				s.closeTab(i)
			case connections.MySQLTableDeleteRows, connections.MySQLTableTruncate:
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
