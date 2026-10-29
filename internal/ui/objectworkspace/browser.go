package objectworkspace

import (
	"context"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

type BrowserSnapshot struct {
	ProfileID                     int64
	Database, Error, Pending      string
	Connected, Opened, Refreshing bool
	Errors                        map[string]string
	Objects                       []mysqlsvc.MySQLDBObject
}
type browserState struct {
	List        ui.ListState
	Row         int
	Kind        string
	toolbarKind string
	database    string
}
type browserFrame struct {
	*browserState
	BrowserSnapshot
}
type TableRunner func(context.Context, int64, mysqlsvc.MySQLTableChange) error
type TableEvent struct {
	ProfileID                int64
	Generation               uint64
	Database, Table, NewName string
	Action                   mysqlsvc.MySQLTableAction
	Error                    error
}

func (s *State) presentation(profileID int64) *browserState {
	b := s.browsers[profileID]
	if b == nil {
		b = &browserState{Row: -1, Kind: "全部", toolbarKind: "表"}
		s.browsers[profileID] = b
	}
	return b
}
func (s *State) currentBrowser() *browserFrame {
	snapshot := BrowserSnapshot{}
	if s.deps.Browser != nil {
		snapshot = s.deps.Browser()
	}
	b := s.presentation(snapshot.ProfileID)
	if b.database != snapshot.Database {
		b.database = snapshot.Database
		b.Kind = "全部"
		b.Row = -1
		b.List = ui.ListState{Columns: b.List.Columns}
	}
	return &browserFrame{browserState: b, BrowserSnapshot: snapshot}
}
func (s *State) SelectedObjects() []mysqlsvc.MySQLDBObject {
	w := s.currentBrowser()
	if w.Kind == "" || w.Kind == "全部" {
		return w.Objects
	}
	items := []mysqlsvc.MySQLDBObject{}
	for _, o := range w.Objects {
		if o.Kind == w.Kind {
			items = append(items, o)
		}
	}
	return items
}
func (s *State) Filter(kind string) {
	w := s.currentBrowser()
	if w.Kind == kind {
		return
	}
	var selected *mysqlsvc.MySQLDBObject
	items := s.SelectedObjects()
	if w.Row >= 0 && w.Row < len(items) {
		o := items[w.Row]
		selected = &o
	}
	w.Kind = kind
	w.List = ui.ListState{Columns: w.List.Columns}
	w.Row = -1
	if selected != nil {
		for i, o := range s.SelectedObjects() {
			if o.Name == selected.Name && o.Kind == selected.Kind {
				w.Row = i
				break
			}
		}
	}
	if s.deps.FilterChanged != nil {
		s.deps.FilterChanged(w.ProfileID, w.Database, kind)
	}
}
func (s *State) ResetBrowser(id int64) { delete(s.browsers, id) }
func (s *State) selected(db, name, kind string) {
	if s.deps.Selected != nil {
		s.deps.Selected(s.session().ProfileID, db, name, kind)
	}
}
func (s *State) openObject(services ui.Services, db, name, kind string) {
	s.openObjectFor(services, s.session().ProfileID, db, name, kind)
}
func (s *State) openObjectFor(services ui.Services, profileID int64, db, name, kind string) {
	if Supports(kind) {
		if s.deps.OpenProgram != nil {
			s.deps.OpenProgram(profileID, db, name, kind)
		}
	} else if s.deps.OpenData != nil {
		s.deps.OpenData(services, profileID, db, name)
	}
}
func (s *State) openData(services ui.Services, db, name string) {
	if s.deps.OpenData != nil {
		s.deps.OpenData(services, s.session().ProfileID, db, name)
	}
}
func (s *State) openStructure(services ui.Services, db, name string) {
	if s.deps.OpenStructure != nil {
		s.deps.OpenStructure(services, s.session().ProfileID, db, name)
	}
}
func (s *State) openProgram(db, name, kind string) {
	if s.deps.OpenProgram != nil {
		s.deps.OpenProgram(s.session().ProfileID, db, name, kind)
	}
}
func (s *State) loadObjects(services ui.Services, db string, force bool) {
	if s.deps.LoadObjects != nil {
		s.deps.LoadObjects(services, db, force)
	}
}
func (s *State) cancelObjects() {
	if s.deps.CancelObjects != nil {
		s.deps.CancelObjects()
	}
}
func (s *State) refreshDatabases(services ui.Services) {
	if s.deps.RefreshDatabases != nil {
		s.deps.RefreshDatabases(services)
	}
}
func (s *State) runner() TableRunner {
	if s.Runner != nil {
		return s.Runner
	}
	if s.deps.Runner != nil {
		return s.deps.Runner()
	}
	return nil
}
func (s *State) sessionFor(id int64) Session {
	if s.deps.SessionFor != nil {
		return s.deps.SessionFor(id)
	}
	w := s.session()
	if w.ProfileID == id {
		return w
	}
	return Session{}
}
func (s *State) report(message string) {
	if s.deps.Status != nil {
		s.deps.Status(message)
	}
}

func (s *State) syncPresentation(id int64) *browserState {
	if s.deps.Browser != nil && s.deps.Browser().ProfileID == id {
		return s.currentBrowser().browserState
	}
	return s.presentation(id)
}
func (s *State) SelectedRow(id int64) int    { return s.syncPresentation(id).Row }
func (s *State) FilterKind(id int64) string  { return s.syncPresentation(id).Kind }
func (s *State) SelectRow(id int64, row int) { s.syncPresentation(id).Row = row }
func (s *State) SetFilter(id int64, kind string) {
	b := s.syncPresentation(id)
	if b.Kind == kind {
		return
	}
	b.Kind = kind
	b.Row = -1
	b.List = ui.ListState{Columns: b.List.Columns}
}

// Category is the workbench toolbar category, independent of the list filter.
func (s *State) Category(id int64) string { return s.syncPresentation(id).toolbarKind }
func (s *State) ShowCategory(id int64, kind string) {
	s.syncPresentation(id).toolbarKind = kind
	s.SetFilter(id, kind)
	s.SelectRow(id, -1)
}
