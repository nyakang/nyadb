package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/kvworkspace"
)

func (s *State) kvWorkspaceDependencies() kvworkspace.Dependencies {
	return kvworkspace.Dependencies{
		ProviderFor: func(id int64) (contract.KVRequestsProvider, error) {
			if s.runtime == nil {
				return nil, contract.ErrRevoked
			}
			return s.runtime.KVRequests(id)
		},
		ProfileName: s.profileName,
		Active:      func(d *kvworkspace.Document) bool { return s.currentTab().kv == d },
		Activate: func(d *kvworkspace.Document) {
			for _, t := range s.tabs {
				if t.kv == d {
					s.selectTab(t.id)
					s.activateMySQLForCurrentTab()
					return
				}
			}
		},
		ConnectProfile: func(services ui.Services, id int64) { s.connectWorkspaceProfile(services, id) },
		EditFocus:      &s.editFocus, EditTarget: s.registerEditTarget,
		Status: func(text string) { s.status = text },
	}
}

// OpenKVRequests opens a native request document bound to its original profile.
func (s *State) OpenKVRequests(profileID int64) bool {
	if s.runtime == nil {
		return false
	}
	if _, err := s.runtime.KVRequests(profileID); err != nil {
		return false
	}
	s.nextTabID++
	d := s.kvWorkspace.Open(s.nextTabID, profileID, kvworkspace.DefaultRequest())
	if d == nil {
		return false
	}
	t := &tab{id: s.nextTabID, kind: tabKV, title: d.Name(), kv: d}
	s.tabs = append(s.tabs, t)
	s.selectTab(t.id)
	s.activateMySQLForCurrentTab()
	s.kvWorkspace.Preconnect(d)
	return true
}
func (s *State) SetKVFiles(files kvworkspace.Files) { s.kvWorkspace.SetFiles(files) }
