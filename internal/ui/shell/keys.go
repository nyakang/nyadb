package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) openKeys(services ui.Services, profile int64) {
	target := contract.ObjectRef{ProfileID: profile, Kind: "keys", Name: "keys"}
	if !s.objectWorkspace.KeysScopeSupported(target) || !s.activateObjectProfile(profile) {
		return
	}
	if previous := s.findDocument(tabStructure, profile, "", "API 密钥", "keys"); previous != nil {
		s.objectWorkspace.LoadKeys(previous.object, services.Invalidate, false)
		return
	}
	s.nextTabID++
	d := s.objectWorkspace.OpenKeys(s.nextTabID, target)
	if d == nil {
		return
	}
	s.tabs = append(s.tabs, &tab{id: s.nextTabID, kind: tabStructure, title: "Meilisearch · " + locales.Text("API 密钥"), object: d})
	s.activeTab = len(s.tabs) - 1
	s.objectWorkspace.LoadKeys(d, services.Invalidate, false)
}
