package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
)

func (s *State) guardNamespaceManagement(services ui.Services, documentID uint64, plan contract.TaskMutationPlan, next func(ui.Services)) bool {
	if plan.Namespace == nil {
		return false
	}
	refs := []contract.NamespaceRef{plan.Namespace.Draft.Ref}
	if plan.Namespace.Draft.Destination.Catalog != "" {
		refs = append(refs, plan.Namespace.Draft.Destination)
	}
	matches := func(t *tab) bool {
		if t.id == documentID || t.data == nil && t.object == nil {
			return false
		}
		var ref contract.NamespaceRef
		if t.data != nil {
			ref = t.data.Object().Namespace()
		}
		if t.object != nil {
			ref = t.object.Object().Namespace()
		}
		for _, affected := range refs {
			if affected == ref {
				return true
			}
		}
		return false
	}
	var finish func(ui.Services)
	finish = func(sv ui.Services) {
		if !s.guardTabsLeave(matches, finish) {
			s.removeTabs(matches, func() { s.selectTab(documentID); next(sv) })
		}
	}
	if s.guardTabsLeave(matches, finish) {
		return true
	}
	s.removeTabs(matches, func() { s.selectTab(documentID) })
	return false
}
func (s *State) namespaceChanged(services ui.Services, event objectworkspace.NamespaceEvent) {
	if s.runtime == nil || s.runtime.Generation(event.Object.ProfileID) != event.Generation || event.Result.Namespace == nil {
		return
	}
	n := event.Result.Namespace
	refs := []contract.NamespaceRef{n.Draft.Ref}
	if n.Draft.Destination.Catalog != "" {
		refs = append(refs, n.Draft.Destination)
	}
	for _, ref := range refs {
		s.sessions.InvalidateDatabaseObjects(ref.ProfileID, event.Generation, ref.Catalog)
	}
	if event.Result.NamespaceVerified || event.Result.State == "failed" || event.Result.State == "canceled" {
		s.sessions.RefreshNamespaces(event.Object.ProfileID, event.Generation, services.Invalidate)
	}
}
