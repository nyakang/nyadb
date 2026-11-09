package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/dataworkspace"
	"github.com/nyakang/nyadb/locales"
)

// Graph names are not unique across kinds. Every entry point carries the
// original qualified reference and published generation to this single opener.
func (s *State) openQualifiedData(services ui.Services, generation uint64, database string, object contract.ObjectRef) {
	w := s.sessions.Workspace(object.ProfileID)
	if w == nil || w.Kind != connections.Neo4j || w.Status != "已连接" || w.Generation != generation ||
		object.Namespace() != s.runtime.Namespace(object.ProfileID, database) || object.Name == "" ||
		(object.Kind != "label" && object.Kind != "relationship") || !s.objectCapabilitiesFor(object.ProfileID).Documents {
		return
	}
	kind := "图标签"
	if object.Kind == "relationship" {
		kind = "关系类型"
	}
	found := false
	for _, item := range w.Cache[database] {
		if item.Ref == object && item.Kind == kind && item.Name == object.Name {
			found = true
			break
		}
	}
	if !found || !s.activateObjectProfile(object.ProfileID) {
		return
	}
	if existing := s.findDocument(tabMySQLData, object.ProfileID, database, kind, object.Name); existing != nil {
		s.dataWorkspace.Load(services, existing.data, false)
		return
	}
	s.nextTabID++
	doc := s.dataWorkspace.OpenObject(s.nextTabID, object, s.objectCapabilitiesFor(object.ProfileID))
	if doc == nil {
		return
	}
	s.tabs = append(s.tabs, &tab{id: s.nextTabID, kind: tabMySQLData, title: database + "." + object.Name + " · " + locales.Text(kind) + " · " + locales.Text("数据"), data: doc})
	s.activeTab = len(s.tabs) - 1
	s.dataWorkspace.Load(services, doc, false)
}

func (s *State) reopenDataDocument(services ui.Services, original *dataworkspace.Document) {
	if !s.dataWorkspace.CanReopen(original) {
		return
	}
	object := original.Object()
	if object.Namespace() != s.runtime.Namespace(object.ProfileID, original.Database()) || original.Generation() != s.runtime.Generation(object.ProfileID) {
		return
	}
	for _, t := range s.tabs {
		if t.data != original {
			continue
		}
		s.dataWorkspace.Close(original)
		t.data = s.dataWorkspace.OpenObject(t.id, object, s.objectCapabilitiesFor(object.ProfileID))
		if t.data != nil {
			s.dataWorkspace.Load(services, t.data, false)
		}
		return
	}
}
