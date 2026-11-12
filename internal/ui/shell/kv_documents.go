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
		HTTPWatchProviderFor: func(id int64) (contract.KVHTTPWatchProvider, error) {
			if s.runtime == nil {
				return nil, contract.ErrRevoked
			}
			return s.runtime.KVHTTPWatch(id)
		},
		EventsProviderFor: func(id int64) (contract.KVEventsProvider, error) {
			if s.runtime == nil {
				return nil, contract.ErrRevoked
			}
			return s.runtime.KVEvents(id)
		},
		RenewalProviderFor: func(id int64) (contract.KVRenewalProvider, error) {
			if s.runtime == nil {
				return nil, contract.ErrRevoked
			}
			return s.runtime.KVRenewal(id)
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
	return s.openKVDocument(profileID, contract.KVRequest{}, nil)
}
func (s *State) OpenKVEvents(read contract.KVKeyRead, history bool) bool {
	request, err := kvworkspace.RequestForKey(read, history)
	if err != nil || s.runtime == nil {
		return false
	}
	p, err := s.runtime.KVEvents(read.Scope.Namespace.ProfileID)
	if err != nil || p == nil || !sameKVDocumentScope(read.Scope, p.KVScope()) {
		return false
	}
	return s.openKVDocument(read.Scope.Namespace.ProfileID, request, &read.Scope)
}
func sameKVDocumentScope(a, b contract.KVScope) bool {
	return a.HTTP == b.HTTP && a.Namespace == b.Namespace && a.Generation == b.Generation && string(a.Prefix) == string(b.Prefix) && a.Header == b.Header && a.Version == b.Version
}
func (s *State) openKVDocument(profileID int64, request contract.KVRequest, expected *contract.KVScope) bool {
	if s.runtime == nil {
		return false
	}
	if _, err := s.runtime.KVRequests(profileID); err != nil {
		return false
	}
	s.nextTabID++
	d := s.kvWorkspace.Open(s.nextTabID, profileID, request)
	if d == nil {
		return false
	}
	if expected != nil && !sameKVDocumentScope(d.Scope(), *expected) {
		s.kvWorkspace.Close(d)
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

func (s *State) SetKVSnapshotOperations(artifacts contract.KVSnapshotArtifacts, files kvworkspace.SnapshotFiles) {
	s.kvWorkspace.SetSnapshotOperations(artifacts, files)
}
