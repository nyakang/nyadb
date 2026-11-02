package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
)

func (s *State) nativeViewChanged(services ui.Services, event objectworkspace.ViewEvent) {
	out := event.Outcome
	if out.State != "committed" && out.State != "uncertain" {
		return
	}
	original := event.Request.Original.Target
	profile := original.Object.ProfileID
	if session, err := s.runtime.Session(profile); err != nil || session.Generation() != original.Generation {
		return
	}
	if !s.sessions.InvalidateDatabaseObjects(profile, event.Generation, event.Database) {
		return
	}
	expected := original
	if event.Request.Action != "drop" {
		expected.Object.Name = event.Request.Desired.Name
	}
	if out.State == "uncertain" || !out.Verified || out.Actual == nil || out.Actual.Target != expected || out.Actual.Schema.Exists != (event.Request.Action != "drop") || out.Error != nil {
		if session, err := s.runtime.Session(profile); err == nil && session.Generation() == original.Generation {
			_ = session.Close()
		}
		return
	}
	removed := event.Request.Action == "drop" || event.Request.Action == "rename"
	w := s.sessions.Workspace(profile)
	if w == s.sessions.Current() && w.Database == event.Database {
		selected := navigation.Parse(s.navigation.Key)
		if removed && selected.ProfileID == profile && selected.Database == event.Database && selected.Object == original.Object.Name {
			s.navigation.Key = navigation.DatabaseNode(profile, event.Database).Key()
			s.navigation.Row = -1
		}
		s.loadMySQLObjects(services, event.Database, true)
	}
	for i := len(s.tabs) - 1; i > 0; i-- {
		t := s.tabs[i]
		dataMatch := t.data != nil && t.data.Object().Namespace() == original.Object.Namespace() && t.data.Table() == original.Object.Name
		objectMatch := t.object != nil && t.object.Object().Namespace() == original.Object.Namespace() && t.object.Table() == original.Object.Name
		if removed && (dataMatch || objectMatch) {
			s.closeTab(i)
		} else if event.Request.Action == "alter" {
			if dataMatch {
				s.dataWorkspace.Refresh(services, t.data)
			}
			if objectMatch {
				s.objectWorkspace.Load(t.object, services.Invalidate, true)
			}
		}
	}
}
