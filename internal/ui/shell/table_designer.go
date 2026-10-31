package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
)

func (s *State) mysqlTableDesigned(services ui.Services, event objectworkspace.DesignEvent) {
	if event.Outcome.State == "not_executed" {
		return
	}
	if !s.sessions.InvalidateDatabaseObjects(event.ProfileID, event.Generation, event.Database) {
		return
	}
	w := s.sessions.Workspace(event.ProfileID)
	if w == s.sessions.Current() && w.Database == event.Database {
		s.loadMySQLObjects(services, event.Database, true)
	}
	for _, t := range s.tabs {
		if t.data != nil && t.data.ProfileID() == event.ProfileID && t.data.Database() == event.Database && t.data.Table() == event.Table {
			s.dataWorkspace.Refresh(services, t.data)
		}
	}
}
