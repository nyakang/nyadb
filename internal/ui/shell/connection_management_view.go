package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) openConnectionManagement() {
	for i, t := range s.tabs {
		if t.kind == tabConnections {
			s.activeTab = i
			return
		}
	}
	s.nextTabID++
	s.tabs = append(s.tabs, &tab{id: s.nextTabID, kind: tabConnections, title: "管理连接"})
	s.activeTab = len(s.tabs) - 1
	s.connectionManagerRow = -1
	s.status = "正在管理连接档案"
}

func (s *State) connectionManagementWorkspace(c *ui.Context) {
	p := theme.Colors(c)
	entries := s.navigation.Profiles()
	s.connectionManagerList.Selected = &s.connectionManagerRow
	selected := s.connectionManagerRow >= 0 && s.connectionManagerRow < len(entries)
	connecting := false
	if selected {
		connecting = s.mysqlSessionStateFor(entries[s.connectionManagerRow].ID) == "连接中"
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(3, 7).Height(theme.RowHeight(c, 31)).AlignItems(ui.Center).Gap(6).
			Background(p.Header).Children(func() {
			ui.Text(c, locales.Text("管理连接")).FontSize(theme.Font(c, 12)).Bold()
			ui.Box(c).Grow(1)
			if ui.Button(c, locales.Text("新建连接…")).Clicked() {
				s.openConnectionDialog()
			}
			if ui.Button(c, locales.Text("编辑连接")).Disabled(!selected).Clicked() {
				s.selectProfileByName(entries[s.connectionManagerRow].Name)
				s.startEditProfile()
			}
			if ui.Button(c, locales.Text("复制连接")).Disabled(!selected).Clicked() {
				s.selectProfileByName(entries[s.connectionManagerRow].Name)
				s.duplicateProfile()
			}
			if ui.Button(c, locales.Text("删除连接")).Disabled(!selected || connecting).Clicked() {
				s.selectProfileByName(entries[s.connectionManagerRow].Name)
				s.confirmDeleteProfile()
			}
			if ui.Button(c, locales.Text("刷新")).Clicked() {
				s.loadProfiles()
			}
		})
		if len(entries) == 0 {
			ui.Text(c, locales.Text("暂无连接")).Padding(10).TextColor(p.Muted)
			return
		}
		columns := []ui.TableColumn{{Title: "名称", Width: 220}, {Title: "类型", Width: 125},
			{Title: "组", Width: 160}, {Title: "状态", Width: 125}, {Title: "主机", Width: 200}}
		for i := range columns {
			columns[i].ID = columns[i].Title
			columns[i].Title = locales.Text(columns[i].Title)
		}
		s.connectionManagerList.Key = func(row int) any { return entries[row].ID }
		theme.Table(c, &s.connectionManagerList, columns, len(entries), func(row, col int) {
			item := entries[row]
			value := ""
			switch col {
			case 0:
				value = item.Name
			case 1:
				value = connectionKindName(item.Kind)
			case 2:
				value = item.GroupName
			case 3:
				value = locales.Text(s.mysqlSessionStateFor(item.ID))
			case 4:
				value = item.Host
			}
			ui.Text(c, value).SingleLine().FontSize(theme.Font(c, 12))
		}).Grow(1)
	})
}

// A connection-level object reload discards only the requested profile's
// cached metadata. It schedules a fresh query for the currently selected
// database, while other expanded databases reload lazily.
func (s *State) refreshConnectionObjects(services ui.Services, id int64) {
	if id <= 0 || s.mysqlSessionStateFor(id) != "已连接" {
		return
	}
	s.activateMySQLProfile(id)
	s.selectedProfileID = id
	if s.mysqlSessionProfileID != id {
		return
	}
	database := s.mysqlSelectedDatabase
	s.cancelMySQLObjectQuery()
	for key := range s.mysqlObjectCache {
		delete(s.mysqlObjectCache, key)
	}
	for key := range s.mysqlObjectErrors {
		delete(s.mysqlObjectErrors, key)
	}
	if database != "" {
		s.loadMySQLObjects(services, database, true)
	} else {
		s.refreshMySQLDatabases(services)
	}
	s.status = "正在刷新连接数据库对象"
}
