package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) graphSchemaTarget(profile int64, database string) contract.ObjectRef {
	if s.runtime == nil {
		return contract.ObjectRef{}
	}
	return s.runtime.Namespace(profile, database).Object("graph_schema", "graph_schema")
}
func (s *State) canOpenGraphSchema(profile int64, generation uint64, database string) bool {
	w := s.sessions.Workspace(profile)
	return w != nil && generation != 0 && w.Status == "已连接" && w.Generation == generation && s.objectWorkspace.GraphSchemaManagementSupported(s.graphSchemaTarget(profile, database))
}
func (s *State) openGraphSchemaManagement(services ui.Services, profile int64, generation uint64, database string) {
	if !s.canOpenGraphSchema(profile, generation, database) || !s.activateObjectProfile(profile) {
		return
	}
	if old := s.findDocument(tabStructure, profile, database, "图索引与约束管理", "graph_schema"); old != nil {
		if old.placeholder != nil {
			s.materializeRestored(services, old)
		}
		return
	}
	s.nextTabID++
	d := s.objectWorkspace.OpenGraphSchemaManagement(s.nextTabID, s.graphSchemaTarget(profile, database))
	if d == nil {
		return
	}
	s.tabs = append(s.tabs, &tab{id: s.nextTabID, kind: tabStructure, title: locales.Text("图索引与约束管理"), object: d})
	s.activeTab = len(s.tabs) - 1
}
func (s *State) graphSchemaChanged(services ui.Services, event objectworkspace.GraphSchemaEvent) {
	if s.runtime == nil || s.runtime.Generation(event.Object.ProfileID) != event.Generation || event.Object.Catalog == "" {
		return
	}
	database := s.sessions.NamespaceLabel(event.Object.Namespace())
	s.sessions.InvalidateDatabaseObjects(event.Object.ProfileID, event.Generation, database)
}
