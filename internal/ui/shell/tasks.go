package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) openTasks(services ui.Services, profile int64, index string) {
	target := contract.ObjectRef{ProfileID: profile, Catalog: index, Kind: "tasks", Name: "tasks"}
	if !s.objectWorkspace.TaskScopeSupported(target) || !s.activateObjectProfile(profile) {
		return
	}
	if previous := s.findDocument(tabStructure, profile, index, "任务", "tasks"); previous != nil {
		s.objectWorkspace.LoadTasks(previous.object, services.Invalidate, false)
		return
	}
	s.nextTabID++
	d := s.objectWorkspace.OpenTasks(s.nextTabID, target)
	if d == nil {
		return
	}
	title := index
	if title == "" {
		title = locales.Text("全部索引")
	}
	s.tabs = append(s.tabs, &tab{id: s.nextTabID, kind: tabStructure, title: title + " · " + locales.Text("任务"), object: d})
	s.activeTab = len(s.tabs) - 1
	s.objectWorkspace.LoadTasks(d, services.Invalidate, false)
}
