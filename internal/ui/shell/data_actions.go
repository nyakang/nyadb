package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
)

func (s *State) savedTableProfiles(id int64, db, table string) ([]objectworkspace.TableProfile, error) {
	_, e := s.runtime.Session(id)
	if e != nil {
		return nil, e
	}
	profiles, e := s.dataWorkspace.SavedProfiles(s.runtime.Namespace(id, db).Object("table", table))
	if e != nil {
		return nil, e
	}
	var out []objectworkspace.TableProfile
	for _, p := range profiles {
		out = append(out, objectworkspace.TableProfile{Name: p.Name, Value: p.Value})
	}
	return out, nil
}
func (s *State) openSavedTableProfile(services ui.Services, id int64, db, table, value string) {
	if !s.activateObjectProfile(id) {
		return
	}
	s.openMySQLData(services, db, table)
	if d := s.currentTab().data; d != nil && d.ProfileID() == id && d.Database() == db && d.Table() == table {
		s.dataWorkspace.RequestProfile(services, d, value)
	}
}

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
	if s.objectCapabilitiesFor(id).GraphExchange {
		s.openGraphExchange(ui.Services{}, id, database, "import")
		return
	}
	// This entry point creates a new relational table. Document imports run
	// from an existing index's bound data document instead.
	if s.objectCapabilitiesFor(id).Documents || !s.activateObjectProfile(id) || s.sessions.StatusFor(id) != "已连接" {
		return
	}
	s.nextTabID++
	d := s.dataWorkspace.OpenImport(s.nextTabID, id, database)
	if d != nil {
		s.tabs = append(s.tabs, &tab{id: s.nextTabID, kind: tabMySQLData, title: database + " · 导入新表", data: d})
		s.activeTab = len(s.tabs) - 1
	}
}
