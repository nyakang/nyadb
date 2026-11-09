package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) SetSnapshotFiles(files objectworkspace.SnapshotFiles) {
	s.objectWorkspace.SetSnapshotFiles(files)
}

func (s *State) openSnapshotManagement(services ui.Services, profile int64) {
	o := contract.ObjectRef{ProfileID: profile, Kind: "snapshot_management", Name: "snapshot_management"}
	if !s.objectWorkspace.SnapshotManagementSupported(o) || !s.activateObjectProfile(profile) {
		return
	}
	if s.findDocument(tabStructure, profile, "", "快照管理", "snapshot_management") != nil {
		return
	}
	s.nextTabID++
	d := s.objectWorkspace.OpenSnapshotManagement(s.nextTabID, o)
	if d == nil {
		return
	}
	s.tabs = append(s.tabs, &tab{id: s.nextTabID, kind: tabStructure, title: locales.Text("快照管理"), object: d})
	s.activeTab = len(s.tabs) - 1
}

func (s *State) guardSnapshotManagement(services ui.Services, documentID uint64, profile int64, generation uint64, plan contract.SnapshotPlan, next func(ui.Services)) bool {
	if plan.Action() == "download" || plan.Action() == "stream" {
		return false
	}
	var original *objectworkspace.Document
	for _, t := range s.tabs {
		if t.id == documentID {
			original = t.object
			break
		}
	}
	if original == nil {
		return true
	}
	ids := map[uint64]bool{}
	for _, t := range s.tabs {
		if t.id == documentID || t.profileID() != profile {
			continue
		}
		if t.query != nil {
			ids[t.id] = true
			continue
		}
		if !plan.RestoresData() {
			continue
		}
		var ref contract.NamespaceRef
		if t.data != nil {
			ref = t.data.Object().Namespace()
		}
		if t.object != nil {
			ref = t.object.Object().Namespace()
		}
		if ref == plan.Target().Collection {
			ids[t.id] = true
		}
	}
	matches := func(t *tab) bool {
		return ids[t.id] && s.runtime != nil && s.runtime.Generation(profile) == generation && s.objectWorkspace.SnapshotManagementGuardPending(original)
	}
	s.removeTabs(matches, func() { s.selectTab(documentID); next(services) })
	return true
}

func (s *State) snapshotsChanged(services ui.Services, event objectworkspace.SnapshotManagementEvent) {
	if s.runtime == nil || s.runtime.Generation(event.Object.ProfileID) != event.Generation {
		return
	}
	for _, ref := range event.Affected {
		if ref.ProfileID == event.Object.ProfileID {
			s.sessions.InvalidateDatabaseObjects(ref.ProfileID, event.Generation, ref.Catalog)
		}
	}
}
