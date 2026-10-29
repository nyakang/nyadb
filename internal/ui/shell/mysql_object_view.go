package shell

import (
	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
)

func (s *State) selectMySQLDatabase(services ui.Services, database string) {
	s.openMySQLDatabase(services, database, false)
}

func (s *State) openMySQLDatabase(services ui.Services, database string, force bool) {
	s.sessions.SelectDatabase(database)
	if database == "" || s.sessions.Current().Status != "已连接" {
		return
	}
	s.sessions.MarkOpened(s.sessions.Current().ProfileID, database)
	s.navigation.Tree.Open.Add(navigation.ConnectionKey(s.sessions.Current().ProfileID))
	s.navigation.Tree.Open.Add(navigation.DatabaseNode(s.sessions.Current().ProfileID, database).Key())
	s.loadMySQLObjects(services, database, force)
}

func (s *State) filterMySQLObjects(kind string) { s.objectWorkspace.Filter(kind) }
func (s *State) objectFilterChanged(id int64, database, kind string) {
	row := s.objectWorkspace.SelectedRow(id)
	items := s.objectWorkspace.SelectedObjects()
	if row >= 0 && row < len(items) {
		o := items[row]
		s.selectObjectInNavigator(database, o.Name, o.Kind)
		return
	}
	if database != "" {
		node := navigation.DatabaseNode(id, database)
		if kind != "全部" {
			node.Kind, node.Category = "category", kind
		}
		s.revealNavigationNode(node)
	}
}
func (s *State) mysqlObjectsChanged(id int64, database string) {
	if id != s.sessions.Current().ProfileID {
		return
	}
	if database == s.sessions.Current().Database {
		selected := navigation.Parse(s.navigation.Key)
		items := s.objectWorkspace.SelectedObjects()
		if selected.Kind == "object" && selected.Database == database {
			s.objectWorkspace.SelectRow(s.sessions.Current().ProfileID, -1)
			for i, obj := range items {
				if obj.Name == selected.Object && obj.Kind == selected.Category {
					s.objectWorkspace.SelectRow(s.sessions.Current().ProfileID, i)
					break
				}
			}
			if s.objectWorkspace.SelectedRow(s.sessions.Current().ProfileID) < 0 {
				s.navigation.Key = navigation.DatabaseNode(s.sessions.Current().ProfileID, database).Key()
				s.navigation.Row = -1
			}
		} else if s.objectWorkspace.SelectedRow(s.sessions.Current().ProfileID) < 0 && len(items) > 0 {
			s.objectWorkspace.SelectRow(s.sessions.Current().ProfileID, 0)
		}
	}
	s.resolveNavigatorObject()
}

func (s *State) loadMySQLObjects(services ui.Services, database string, force bool) {
	if database != "" && s.sessions.Current().Status == "已连接" && s.sessions.Current().ProfileID > 0 && !s.sessions.DatabaseOpened(s.sessions.Current().ProfileID, database) {
		s.navigation.Tree.Open.Add(navigation.ConnectionKey(s.sessions.Current().ProfileID))
		s.navigation.Tree.Open.Add(navigation.DatabaseNode(s.sessions.Current().ProfileID, database).Key())
	}
	s.sessions.LoadObjects(database, force, services.Invalidate)
}

func (s *State) openMySQLObject(services ui.Services, database, name, kind string) {
	if objectworkspace.Supports(kind) {
		s.openMySQLProgram(database, name, kind)
		return
	}
	s.openMySQLData(services, database, name)
}

// Programs expose the server's read-only definition and metadata. Do not
// execute a stored routine on double-click; such calls could mutate data.
func (s *State) openMySQLProgram(database, name, kind string) {
	id := s.sessions.Current().ProfileID
	if id == 0 || s.sessions.StatusFor(id) != "已连接" {
		s.status = "请先连接 MySQL"
		return
	}
	var item *mysqlsvc.MySQLDBObject
	for i := range s.sessions.Current().Cache[database] {
		obj := &s.sessions.Current().Cache[database][i]
		if obj.Name == name && obj.Kind == kind {
			item = obj
			break
		}
	}
	if item == nil {
		s.status = "对象元数据不可用，请先刷新：" + name
		return
	}
	if t := s.findDocument(tabProgram, id, database, kind, name); t != nil {
		t.object.Update(*item)
		return
	}
	s.nextTabID++
	doc := s.objectWorkspace.OpenProgram(s.nextTabID, id, database, *item)
	if doc == nil {
		return
	}
	s.tabs = append(s.tabs, &tab{id: s.nextTabID, kind: tabProgram, title: name,
		object: doc})
	s.activeTab = len(s.tabs) - 1
	s.status = "已打开" + kind + "定义：" + database + "." + name
}

func (s *State) openMySQLData(services ui.Services, database, table string) {
	w := s.sessions.Current()
	if w.Status != "已连接" || w.ProfileID <= 0 || !validStructureSelection(database, table) {
		return
	}
	kind := s.objectKindFor(w.ProfileID, database, table)
	if t := s.findDocument(tabMySQLData, w.ProfileID, database, kind, table); t != nil {
		s.dataWorkspace.Load(services, t.data, false)
		return
	}
	s.nextTabID++
	t := &tab{id: s.nextTabID, kind: tabMySQLData, title: database + "." + table + " · 数据", data: s.dataWorkspace.Open(s.nextTabID, w.ProfileID, database, table, kind)}
	s.tabs = append(s.tabs, t)
	s.activeTab = len(s.tabs) - 1
	s.dataWorkspace.Load(services, t.data, false)
}

func (s *State) mysqlTableChanged(services ui.Services, r objectworkspace.TableEvent) {
	if r.Error != nil && r.Action != mysqlsvc.MySQLTableCopyData {
		return
	}
	w := s.sessions.Workspace(r.ProfileID)
	if w == nil || w.Generation != r.Generation || w.Status != "已连接" {
		return
	}
	delete(w.Cache, r.Database)
	if w == s.sessions.Current() && w.Database == r.Database {
		s.loadMySQLObjects(services, r.Database, true)
	}
	if r.Error != nil {
		return
	}
	for i := len(s.tabs) - 1; i > 0; i-- {
		t := s.tabs[i]
		matches := t.object != nil && t.object.Kind() == "structure" && t.object.ProfileID() == r.ProfileID && t.object.Database() == r.Database && t.object.Table() == r.Table || t.data != nil && t.data.ProfileID() == r.ProfileID && t.data.Database() == r.Database && t.data.Table() == r.Table
		if !matches {
			continue
		}
		switch r.Action {
		case mysqlsvc.MySQLTableRename, mysqlsvc.MySQLTableDrop:
			s.closeTab(i)
		case mysqlsvc.MySQLTableDeleteRows, mysqlsvc.MySQLTableTruncate:
			if t.data != nil {
				s.dataWorkspace.Refresh(services, t.data)
			}
		}
	}
}
