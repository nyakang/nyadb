package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/ui/sqlworkspace"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) SetSQLPersistence(store *queries.Store, files sqlworkspace.Files, invalidate func()) {
	s.sqlWorkspace.Invalidate = invalidate
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
	ui.Column(c).Fill().Padding(12).Gap(8).Children(func() {
		ui.Text(c, locales.Text("命名查询"))
		names := s.sqlWorkspace.Named(profile)
		if len(names) == 0 {
			ui.Text(c, locales.Text("尚无已保存的查询"))
		}
		for _, n := range names {
			query := n
			if ui.Button(c, n.Name).Clicked() {
				s.openQuery()
				s.sqlWorkspace.OpenNamed(s.currentTab().query, query)
			}
		}
	})
}
