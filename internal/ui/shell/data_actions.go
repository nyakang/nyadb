package shell

import "github.com/egoist/mygo/ui"

func (s *State) openTableDataAction(services ui.Services, id int64, database, table, action string) {
	if !s.activateObjectProfile(id) {
		return
	}
	s.openMySQLData(services, database, table)
	if d := s.currentTab().data; d != nil && d.ProfileID() == id && d.Database() == database && d.Table() == table {
		s.dataWorkspace.RequestAction(services, d, action)
	}
}
func (s *State) openDatabaseImport(id int64, database string) {
	if !s.activateObjectProfile(id) || s.sessions.StatusFor(id) != "已连接" {
		return
	}
	s.nextTabID++
	d := s.dataWorkspace.OpenImport(s.nextTabID, id, database)
	if d != nil {
		s.tabs = append(s.tabs, &tab{id: s.nextTabID, kind: tabMySQLData, title: database + " · 导入新表", data: d})
		s.activeTab = len(s.tabs) - 1
	}
}
