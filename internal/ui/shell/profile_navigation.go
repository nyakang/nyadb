package shell

import (
	"context"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *State) profileVisible(p connections.Profile) bool {
	if s.profileStarredOnly && !p.Starred {
		return false
	}
	if s.profileEngine != "" && p.Kind != s.profileEngine {
		return false
	}
	query := strings.ToLower(strings.TrimSpace(s.search))
	if query == "" || strings.Contains(strings.ToLower(p.Name), query) {
		return true
	}
	return p.GroupName != "" && strings.Contains(strings.ToLower(p.GroupName), query)
}

func (s *State) changeProfileFilter() { s.explorerRow = -1 }

func (s *State) setProfileStarred(id int64, starred bool) {
	if s.profileService == nil || id == 0 {
		return
	}
	if err := s.profileService.SetStarred(context.Background(), id, starred); err != nil {
		s.status = "设置星标失败：" + err.Error()
		return
	}
	s.loadProfiles()
	s.status = "已更新连接星标"
}

func (s *State) collapseProfileNavigation() {
	s.explorerTree.Open.Clear()
	for _, node := range s.mysqlDatabaseNodes {
		node.open = false
		node.tablesOpen = false
		node.viewsOpen = false
	}
	s.explorerRow = -1
	s.status = "连接树已全部折叠"
}

func (s *State) activeTabObject() (int64, string, string, bool) {
	t := s.currentTab()
	switch t.kind {
	case tabMySQLData:
		return t.dataProfileID, t.dataDatabase, t.dataTable, t.dataProfileID != 0 && t.dataDatabase != ""
	case tabStructure:
		return t.structureProfileID, t.structureDatabase, t.structureTable, t.structureProfileID != 0 && t.structureDatabase != ""
	default:
		return 0, "", "", false
	}
}

// resolveNavigatorObject selects the real metadata row after an optional
// asynchronous fetch, without inventing a table in a stale database snapshot.
func (s *State) resolveNavigatorObject() {
	if s.navigateDatabase == "" || s.navigateDatabase != s.mysqlSelectedDatabase ||
		s.navigateProfileID != s.mysqlSessionProfileID || s.mysqlSessionStatus != "已连接" {
		return
	}
	objects, ok := s.mysqlObjectCache[s.navigateDatabase]
	if !ok {
		return
	}
	s.mysqlObjectKind = "全部"
	for i, obj := range objects {
		if obj.Name == s.navigateTable {
			s.objectRow = i
			s.revealNavigationNode(navigationNode{Kind: "object", ProfileID: s.navigateProfileID, Database: s.navigateDatabase, Category: obj.Kind, Object: obj.Name})
			s.navigateDatabase, s.navigateTable = "", ""
			s.navigateProfileID = 0
			return
		}
	}
	s.objectRow = -1
	s.navigateDatabase, s.navigateTable = "", ""
	s.navigateProfileID = 0
	s.status = "已找到数据库，但活动标签对象不在当前元数据中"
}

func (s *State) navigateToActiveTab(services ui.Services) {
	id, database, table, ok := s.activeTabObject()
	if !ok || s.mysqlSessionStateFor(id) != "已连接" {
		s.status = "当前标签没有可定位的活动 MySQL 对象"
		return
	}
	for _, p := range s.profileEntries {
		if p.ID != id {
			continue
		}
		// Temporarily reveal the target if the sidebar is filtered.
		s.profileStarredOnly = false
		s.profileEngine = ""
		s.search = ""
		s.selectedGroup = ""
		s.selectedProfileID = id
		s.explorerName = profileNode(p.ID)
		s.explorerRow = -1
		if p.GroupName != "" {
			s.explorerTree.Open.Add(groupNode(p.GroupName))
		}
		s.explorerTree.Open.Add(profileNode(id))
		s.explorerTree.Open.Add(databaseNode(id, database).key())
		s.navigateDatabase, s.navigateTable, s.navigateProfileID = database, table, id
		s.selectMySQLDatabase(services, database)
		s.resolveNavigatorObject()
		if node := s.mysqlDatabaseNodes[database]; node != nil {
			node.open = true
			node.tablesOpen = true
			node.viewsOpen = true
		}
		s.status = "已定位活动标签对象：" + database + "." + table
		return
	}
	s.status = "活动标签的连接档案不存在"
}
