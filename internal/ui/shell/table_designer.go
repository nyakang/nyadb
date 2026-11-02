package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
)

func (s *State) schemaTableCopied(services ui.Services, event objectworkspace.SchemaCopyEvent) {
	out := event.Outcome
	if out.State != "committed" && out.State != "uncertain" {
		return
	}
	profile := event.Original.Object.ProfileID
	if original, err := s.runtime.Session(profile); err != nil || original.Generation() != event.Original.Generation {
		return
	}
	if !s.sessions.InvalidateDatabaseObjects(profile, event.Generation, event.Database) {
		return
	}
	expected := event.Original
	expected.Object.Name = event.Destination
	if out.State == "uncertain" || !out.Verified || out.Actual == nil || out.Actual.Target != expected || !out.Actual.Schema.Exists || out.Error != nil {
		if original, err := s.runtime.Session(profile); err == nil && original.Generation() == event.Original.Generation {
			_ = original.Close()
		}
		return
	}
	w := s.sessions.Workspace(profile)
	if w == s.sessions.Current() && w.Database == event.Database {
		s.loadMySQLObjects(services, event.Database, true)
	}
	// A copy keeps source documents and their leases. A pre-existing destination
	// document receives the same guarded refresh as other table operations.
	for _, t := range s.tabs {
		if t.data != nil && t.data.Object() == expected.Object {
			s.dataWorkspace.Refresh(services, t.data)
		}
	}
}

func (s *State) schemaTableDropped(services ui.Services, event objectworkspace.SchemaDropEvent) {
	out := event.Outcome
	if out.State != "committed" && out.State != "uncertain" {
		return
	}
	profile := event.Original.Object.ProfileID
	if original, err := s.runtime.Session(profile); err != nil || original.Generation() != event.Original.Generation {
		return
	}
	if !s.sessions.InvalidateDatabaseObjects(profile, event.Generation, event.Database) {
		return
	}
	if out.State == "uncertain" || !out.Verified || out.Actual == nil || out.Actual.Target != event.Original || out.Actual.Schema.Exists || out.Error != nil {
		if original, err := s.runtime.Session(profile); err == nil && original.Generation() == event.Original.Generation {
			_ = original.Close()
		}
		return
	}
	w := s.sessions.Workspace(profile)
	if w == s.sessions.Current() && w.Database == event.Database {
		selected := navigation.Parse(s.navigation.Key)
		if selected.ProfileID == profile && selected.Database == event.Database && selected.Object == event.Original.Object.Name {
			s.navigation.Key = navigation.DatabaseNode(profile, event.Database).Key()
			s.navigation.Row = -1
		}
		s.loadMySQLObjects(services, event.Database, true)
	}
	for i := len(s.tabs) - 1; i > 0; i-- {
		t := s.tabs[i]
		if t.data != nil && t.data.Object() == event.Original.Object || t.object != nil && t.object.Object() == event.Original.Object {
			s.closeTab(i)
		}
	}
}

func (s *State) schemaTableDesigned(services ui.Services, event objectworkspace.SchemaDesignEvent) {
	if event.Outcome.State != "committed" && event.Outcome.State != "uncertain" {
		return
	}
	if !s.sessions.InvalidateDatabaseObjects(event.Original.Object.ProfileID, event.Generation, event.Database) {
		return
	}
	profile := event.Original.Object.ProfileID
	if event.Outcome.State == "uncertain" {
		// Revoke only the captured original published handle. Drafts remain in
		// their modules; a replacement connection must never be closed here.
		if original, err := s.runtime.Session(profile); err == nil && original.Generation() == event.Original.Generation {
			_ = original.Close()
		}
		return
	}
	if !event.Outcome.Verified || event.Outcome.Actual == nil {
		return
	}
	actual := event.Outcome.Actual.Target.Object
	w := s.sessions.Workspace(profile)
	if w == s.sessions.Current() && w.Database == event.Database {
		s.loadMySQLObjects(services, event.Database, true)
	}
	for i := len(s.tabs) - 1; i > 0; i-- {
		t := s.tabs[i]
		if t.id == event.DocumentID {
			t.title = event.Database + "." + actual.Name
			continue
		}
		if t.data != nil && t.data.Object() == event.Original.Object {
			if actual.Name != event.Original.Object.Name {
				s.closeTab(i)
			} else {
				s.dataWorkspace.Refresh(services, t.data)
			}
		} else if t.object != nil && t.object.Object() == event.Original.Object && actual.Name != event.Original.Object.Name {
			s.closeTab(i)
		}
	}
}

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
