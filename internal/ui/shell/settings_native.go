package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
)

// Only the original profile/generation's metadata is invalidated. Search/data
// documents keep their captured settings and require their own explicit reads.
func (s *State) settingsChanged(services ui.Services, event objectworkspace.SettingsEvent) {
	if s.runtime == nil {
		return
	}
	original, err := s.runtime.Session(event.Object.ProfileID)
	if err != nil || original.Generation() != event.Generation {
		return
	}
	if !s.sessions.InvalidateDatabaseObjects(event.Object.ProfileID, event.Generation, event.Object.Catalog) {
		return
	}
	w := s.sessions.Workspace(event.Object.ProfileID)
	if w == s.sessions.Current() && w.Database == event.Object.Catalog {
		s.loadMySQLObjects(services, event.Object.Catalog, true)
	}
}
