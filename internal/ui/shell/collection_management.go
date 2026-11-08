package shell

import (
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) openCollectionManagement(services ui.Services, profile int64) {
	o := contract.ObjectRef{ProfileID: profile, Kind: "collection_management", Name: "collection_management"}
	if !s.objectWorkspace.CollectionManagementSupported(o) || !s.activateObjectProfile(profile) {
		return
	}
	if previous := s.findDocument(tabStructure, profile, "", "集合与别名管理", "collection_management"); previous != nil {
		return
	}
	s.nextTabID++
	d := s.objectWorkspace.OpenCollectionManagement(s.nextTabID, o)
	if d == nil {
		return
	}
	s.tabs = append(s.tabs, &tab{id: s.nextTabID, kind: tabStructure, title: locales.Text("集合与别名管理"), object: d})
	s.activeTab = len(s.tabs) - 1
}
func (s *State) guardCollectionManagement(services ui.Services, documentID uint64, profile int64, generation uint64, plan contract.CollectionManagementPlan, next func(ui.Services)) bool {
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
	affected := slices.Clone(plan.Affected())
	ids := map[uint64]bool{}
	for _, t := range s.tabs {
		if t.id == documentID || t.profileID() != profile {
			continue
		}
		if t.query != nil {
			ids[t.id] = true
			continue
		}
		var ref contract.NamespaceRef
		if t.data != nil {
			ref = t.data.Object().Namespace()
		}
		if t.object != nil {
			ref = t.object.Object().Namespace()
		}
		if slices.Contains(affected, ref) {
			ids[t.id] = true
		}
	}
	matches := func(t *tab) bool {
		return ids[t.id] && s.runtime != nil && s.runtime.Generation(profile) == generation && s.objectWorkspace.CollectionManagementGuardPending(original)
	}
	s.removeTabs(matches, func() { s.selectTab(documentID); next(services) })
	return true
}
func (s *State) collectionsChanged(services ui.Services, event objectworkspace.CollectionManagementEvent) {
	if s.runtime == nil || s.runtime.Generation(event.Object.ProfileID) != event.Generation {
		return
	}
	for _, ref := range event.Affected {
		if ref.ProfileID == event.Object.ProfileID {
			s.sessions.InvalidateDatabaseObjects(ref.ProfileID, event.Generation, ref.Catalog)
		}
	}
	s.sessions.RefreshNamespaces(event.Object.ProfileID, event.Generation, services.Invalidate)
}
