package shell

import (
	"context"
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
)

// Switch the default for future sessions only; a running connection must be
// disconnected before changing its visible configuration.
func (s *State) setProfileActiveVariant(id int64, name string) {
	if s.profileService == nil || id == 0 {
		return
	}
	if state := s.sessions.StatusFor(id); state == "已连接" || state == "连接中" {
		s.status = "请先断开连接，再切换连接配置文件"
		return
	}
	ctx := context.Background()
	p, err := s.profileService.Get(ctx, id)
	if err != nil {
		s.status = "读取连接配置失败：" + err.Error()
		return
	}
	if p.Kind != connections.MySQL {
		return
	}
	if name != "" {
		valid := false
		for _, variant := range p.Variants {
			if variant.Name == name {
				valid = true
				break
			}
		}
		if !valid {
			s.status = "连接配置已不存在，请刷新"
			return
		}
	}
	if p.ActiveVariant == name {
		return
	}
	p.ActiveVariant = name
	if _, err := s.profileService.Update(ctx, p); err != nil {
		s.status = "切换连接配置失败：" + err.Error()
		return
	}
	s.loadProfiles()
	label := name
	if label == "" {
		label = "默认"
	}
	s.status = "已切换活动连接配置：" + label
}

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

func (s *State) activeTabObject() (int64, string, string, bool) {
	t := s.currentTab()
	switch t.kind {
	case tabMySQLData:
		return t.dataProfileID, t.dataDatabase, t.dataTable, t.dataProfileID != 0 && t.dataDatabase != ""
	case tabStructure:
		return t.design.ProfileID(), t.design.Database(), t.design.Table(), t.design.ProfileID() != 0 && t.design.Database() != ""
	default:
		return 0, "", "", false
	}
}

// resolveNavigatorObject selects the real metadata row after an optional
// asynchronous fetch, without inventing a table in a stale database snapshot.
func (s *State) resolveNavigatorObject() {
	if s.sessions.Current().NavigateDatabase == "" || s.sessions.Current().NavigateDatabase != s.sessions.Current().Database ||
		s.sessions.Current().NavigateProfileID != s.sessions.Current().ProfileID || s.sessions.Current().Status != "已连接" {
		return
	}
	objects, ok := s.sessions.Current().Cache[s.sessions.Current().NavigateDatabase]
	if !ok {
		return
	}
	s.sessions.Current().Kind = "全部"
	for i, obj := range objects {
		if obj.Name == s.sessions.Current().NavigateTable {
			s.sessions.Current().Row = i
			s.revealNavigationNode(navigation.Node{Kind: "object", ProfileID: s.sessions.Current().NavigateProfileID, Database: s.sessions.Current().NavigateDatabase, Category: obj.Kind, Object: obj.Name})
			s.sessions.Current().NavigateDatabase, s.sessions.Current().NavigateTable = "", ""
			s.sessions.Current().NavigateProfileID = 0
			return
		}
	}
	s.sessions.Current().Row = -1
	s.sessions.Current().NavigateDatabase, s.sessions.Current().NavigateTable = "", ""
	s.sessions.Current().NavigateProfileID = 0
	s.status = "已找到数据库，但活动标签对象不在当前元数据中"
}

func (s *State) navigateToActiveTab(services ui.Services) {
	id, database, table, ok := s.activeTabObject()
	if !ok || s.sessions.StatusFor(id) != "已连接" {
		s.status = "当前标签没有可定位的活动 MySQL 对象"
		return
	}
	for _, p := range s.navigation.Profiles() {
		if p.ID != id {
			continue
		}
		s.sessions.Activate(id)
		// Temporarily reveal the target if the sidebar is filtered.
		s.navigation.SuspendFilter()
		s.navigation.Group = ""
		s.selectedProfileID = id
		s.navigation.Key = navigation.ConnectionKey(p.ID)
		s.navigation.Row = -1
		if p.GroupName != "" {
			s.navigation.Tree.Open.Add(navigation.GroupKey(p.GroupName))
		}
		s.navigation.Tree.Open.Add(navigation.ConnectionKey(id))
		s.navigation.Tree.Open.Add(navigation.DatabaseNode(id, database).Key())
		s.sessions.Current().NavigateDatabase, s.sessions.Current().NavigateTable, s.sessions.Current().NavigateProfileID = database, table, id
		s.selectMySQLDatabase(services, database)
		s.resolveNavigatorObject()
		s.status = "已定位活动标签对象：" + database + "." + table
		return
	}
	s.status = "活动标签的连接档案不存在"
}

func (s *State) selectNavigationNode(services ui.Services, key string) {
	if navigation.IsGroup(key) {
		s.selectGroup(navigation.GroupName(key))
		return
	}
	n := navigation.Parse(key)
	if n.ProfileID == 0 || n.Kind == "status" {
		return
	}
	for _, p := range s.navigation.Profiles() {
		if p.ID == n.ProfileID {
			s.selectProfileByName(p.Name)
			break
		}
	}
	s.navigation.Key = key
	s.navigation.Row = s.navigation.SelectionRow(s.navigation.Roots())
	if n.Database == "" {
		s.sessions.CancelObjects()
		s.sessions.Current().Database = ""
		s.sessions.Current().Kind = "全部"
		s.sessions.Current().Row = -1
		return
	}
	if s.sessions.StatusFor(n.ProfileID) != "已连接" {
		return
	}
	if n.Kind == "database" {
		s.sessions.SelectDatabase(n.Database)
	} else {
		s.selectMySQLDatabase(services, n.Database)
	}
	s.sessions.Current().Kind = "全部"
	if n.Category != "" {
		s.sessions.Current().Kind = n.Category
	}
	s.sessions.Current().Row = -1
	if n.Kind == "database" && !s.sessions.DatabaseOpened(n.ProfileID, n.Database) {
		return
	}
	if n.Kind == "category" && !navigation.SupportedCategory(n.Category) {
		s.status = n.Category + "：此类对象尚未提供元数据浏览"
		return
	}
	if len(s.sessions.SelectedObjects()) > 0 {
		s.sessions.Current().Row = 0
	}
	if n.Kind == "object" {
		for i, obj := range s.sessions.SelectedObjects() {
			if obj.Name == n.Object {
				s.sessions.Current().Row = i
				break
			}
		}
	}
}

func (s *State) revealNavigationNode(n navigation.Node) {
	s.navigation.Tree.Open.Add(navigation.ConnectionKey(n.ProfileID))
	if n.Database != "" {
		s.sessions.MarkOpened(n.ProfileID, n.Database)
		s.navigation.Tree.Open.Add(navigation.DatabaseNode(n.ProfileID, n.Database).Key())
	}
	if n.Category != "" {
		category := n
		category.Kind, category.Object = "category", ""
		s.navigation.Tree.Open.Add(category.Key())
	}
	s.navigation.Key, s.navigation.Row = n.Key(), -1
}

// Opening a database is the only tree expansion that schedules metadata. A
// failed read stays failed until an explicit retry, avoiding frame-driven loops.
func (s *State) syncNavigationExpansion(services ui.Services) {
	s.sessions.LoadExpanded(func(database string) bool {
		id := s.sessions.Current().ProfileID
		return s.navigation.Tree.Open.Has(navigation.ConnectionKey(id)) && s.navigation.Tree.Open.Has(navigation.DatabaseNode(id, database).Key())
	}, services.Invalidate)
}

func (s *State) submitNavigation(services ui.Services, n navigation.Node) {
	if n.Kind == "database" {
		s.openMySQLDatabase(services, n.Database, false)
	}
	if n.Kind == "object" {
		s.openMySQLObject(services, n.Database, n.Object, n.Category)
	}
	if n.Kind == "connection" {
		if s.sessions.StatusFor(n.ProfileID) != "已连接" {
			s.startMySQLSession(services)
		} else {
			s.navigation.Tree.Open.Add(n.Key())
		}
	}
}

func (s *State) navigationDescription(n navigation.Node) string {
	if n.Kind == "database" {
		return locales.Text("数据库")
	}
	if n.Kind == "category" {
		if !navigation.SupportedCategory(n.Category) {
			return locales.Text(n.Category) + "（尚未支持）"
		}
		return fmt.Sprintf("%s (%d)", locales.Text(n.Category), len(s.sessions.SelectedObjects()))
	}
	return locales.Text(n.Category)
}

func (s *State) selectObjectInNavigator(database, name, kind string) {
	n := navigation.Node{Kind: "object", ProfileID: s.sessions.Current().ProfileID, Database: database, Category: kind, Object: name}
	s.revealNavigationNode(n)
}
