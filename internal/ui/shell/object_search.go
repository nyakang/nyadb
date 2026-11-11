package shell

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) objectSearchRuntime(profile int64) (objectworkspace.ObjectSearchRuntime, error) {
	session, err := s.runtime.Session(profile)
	if err != nil {
		return objectworkspace.ObjectSearchRuntime{}, err
	}
	provider, err := contract.Require[contract.MetadataProvider](session)
	if err != nil {
		return objectworkspace.ObjectSearchRuntime{}, err
	}
	snapshot := []contract.Namespace{}
	if w := s.sessions.Workspace(profile); w != nil {
		for _, label := range s.sessions.DatabasesFor(profile) {
			if ref, ok := w.Namespaces[label]; ok {
				snapshot = append(snapshot, contract.Namespace{Ref: ref, Label: label})
			}
		}
	}
	return objectworkspace.ObjectSearchRuntime{Snapshot: snapshot, Namespaces: provider.Namespaces, Browse: provider.Browse, TableMetadata: provider.TableMetadata}, nil
}
func (s *State) openObjectSearch() {
	s.objectWorkspace.OpenObjectSearch(s.sessions.Current().ProfileID)
}

func (s *State) openSearchObject(services ui.Services, profile int64, generation uint64, entry objectworkspace.ObjectSearchEntry) {
	w := s.sessions.Workspace(profile)
	if w == nil || w.Status != "已连接" || w.Generation != generation || entry.Object.Ref.ProfileID != profile || entry.Namespace.Ref != entry.Object.Ref.Namespace() {
		return
	}
	database := entry.Namespace.Label
	if s.runtime.Namespace(profile, database) != entry.Namespace.Ref {
		s.status = "对象命名空间已变化，请重新查找"
		return
	}
	s.sessions.Activate(profile)
	s.selectedProfileID = profile
	s.openMySQLDatabase(services, database, false)
	s.navigation.SuspendFilter()
	s.revealNavigationNode(navigation.Node{Kind: "object", ProfileID: profile, Database: database, Object: entry.Object.Name, Category: entry.Object.Kind})
	object := entry.Object
	if s.sessions.Current().Kind == connections.Neo4j && (object.Ref.Kind == "label" || object.Ref.Kind == "relationship") || s.sessions.Current().Kind.IsNativeKV() && object.Ref.Kind == "keyspace" {
		s.openQualifiedData(services, generation, database, object.Ref)
		return
	}
	if objectworkspace.Supports(object.Kind) {
		if existing := s.findDocument(tabProgram, profile, database, object.Kind, object.Name); existing != nil {
			existing.object.Update(object)
			return
		}
		s.nextTabID++
		doc := s.objectWorkspace.OpenProgram(s.nextTabID, profile, database, object)
		if doc == nil {
			return
		}
		s.tabs = append(s.tabs, &tab{id: s.nextTabID, kind: tabProgram, title: object.Name, object: doc})
		s.activeTab = len(s.tabs) - 1
		return
	}
	if object.Ref.Kind != "table" && object.Ref.Kind != "view" {
		s.status = "此对象暂不支持打开"
		return
	}
	if existing := s.findDocument(tabMySQLData, profile, database, object.Kind, object.Name); existing != nil {
		s.dataWorkspace.Load(services, existing.data, false)
		return
	}
	s.nextTabID++
	doc := s.dataWorkspace.OpenObject(s.nextTabID, object.Ref, s.objectCapabilitiesFor(profile))
	if doc == nil {
		return
	}
	s.tabs = append(s.tabs, &tab{id: s.nextTabID, kind: tabMySQLData, title: fmt.Sprintf(locales.Text("%s.%s · 数据"), database, object.Name), data: doc})
	s.activeTab = len(s.tabs) - 1
	s.dataWorkspace.Load(services, doc, false)
}
