package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/sqlworkspace"
)

func (s *State) SetSQLPersistence(store *queries.Store, files sqlworkspace.Files, invalidate func()) {
	s.sqlWorkspace.Invalidate = invalidate
	s.sqlWorkspace.SetHistoryOpener(s.openHistoryQuery)
	s.sqlWorkspace.SetPersistence(store, files, func(w queries.Workspace) {
		for _, snapshot := range w.Documents {
			if snapshot.ID <= 1 {
				continue
			}
			doc := s.sqlWorkspace.RestoreDocument(snapshot)
			s.tabs = append(s.tabs, &tab{id: snapshot.ID, kind: tabSQL, title: doc.Name(), query: doc})
			s.nextTabID = max(s.nextTabID, snapshot.ID)
		}
		s.selectTab(w.ActiveID)
	})
}
func (s *State) openHistoryQuery(entry queries.HistoryEntry) {
	s.nextTabID++
	profile, database := entry.ProfileID, entry.Database
	connected := s.sessions.StatusFor(profile) == "已连接"
	if !connected {
		profile, database = 0, ""
	}
	var d *sqlworkspace.Document
	if !connected {
		d = s.sqlWorkspace.RestoreDocument(queries.Document{ID: s.nextTabID, Text: entry.SQL, ProfileID: entry.ProfileID, Database: entry.Database})
	} else {
		d = s.sqlWorkspace.Open(s.nextTabID, entry.SQL, profile, database, false)
	}
	s.tabs = append(s.tabs, &tab{id: s.nextTabID, kind: tabSQL, title: d.Name(), query: d})
	s.selectTab(s.nextTabID)
	s.sqlWorkspace.Prepare(d)
}
func (s *State) observeSQLDocuments() {
	ids := make([]uint64, 0, len(s.tabs))
	for _, t := range s.tabs {
		if t.query != nil {
			ids = append(ids, t.id)
		}
	}
	s.sqlWorkspace.Observe(s.currentTab().id, ids)
}
func (s *State) namedQueriesView(c *ui.Context, profile int64) {
	s.sqlWorkspace.AssetsView(c, profile, s.openNamedQuery)
}

func (s *State) openNamedQuery(query queries.Named) {
	if s.sqlWorkspace.PersistenceLoading() {
		return
	}
	for _, t := range s.tabs {
		if t.query != nil && t.query.QueryID() == query.ID {
			s.selectTab(t.id)
			return
		}
	}
	// Construct directly in the original scope; openQuery would reserve the current scope first.
	s.nextTabID++
	profile, database := query.ProfileID, query.Database
	if s.sessions.StatusFor(profile) != "已连接" {
		profile, database = 0, ""
	}
	d := s.sqlWorkspace.Open(s.nextTabID, "", profile, database, false)
	s.sqlWorkspace.OpenNamed(d, query)
	t := &tab{id: s.nextTabID, kind: tabSQL, title: d.Name(), query: d}
	s.tabs = append(s.tabs, t)
	s.selectTab(t.id)
	s.sqlWorkspace.Prepare(d)
}
func (s *State) sqlAssetsChanged(profile int64, id string, deleted bool) {
	n := navigation.Parse(s.navigation.Key)
	if n.ProfileID != profile || n.Category != "查询" {
		return
	}
	if n.Kind == "object" {
		if deleted && n.Object == id {
			n.Kind, n.Object = "category", ""
		}
		n.GroupID = s.sqlWorkspace.AssetGroup(profile, n.Object)
	}
	if n.Kind == "query_group" && s.sqlWorkspace.AssetGroupName(profile, n.GroupID) == "" {
		s.navigation.Tree.Open.Remove(n.Key())
		n.Kind, n.GroupID = "category", ""
	}
	s.navigation.Key = n.Key()
	s.navigation.Row = -1
}

func (s *State) recordActivity(request contract.ActivityRequest) func(contract.ActivityResult) {
	if s.sqlWorkspace == nil {
		return nil
	}
	return s.sqlWorkspace.BeginActivity(request)
}
