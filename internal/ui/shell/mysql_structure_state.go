package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/structureviewer"
)

func (s *State) structureViewerDependencies() structureviewer.Dependencies {
	return structureviewer.Dependencies{
		Session: func() structureviewer.Session {
			w := s.sessions.Current()
			return structureviewer.Session{ProfileID: w.ProfileID, Generation: w.Generation, Connected: w.Status == "已连接"}
		},
		Loader: func() structureviewer.Loader {
			if s.sessions.Manager == nil {
				return nil
			}
			return s.sessions.Manager.TableStructure
		},
		Back: func() { s.activeTab = 0 },
	}
}
func (s *State) openMySQLStructure(services ui.Services, database, table string) {
	profileID := s.sessions.Current().ProfileID
	if s.sessions.Current().Status != "已连接" || profileID <= 0 || !validStructureSelection(database, table) {
		return
	}
	for i, t := range s.tabs {
		if t.kind == tabStructure && t.design.ProfileID() == profileID && t.design.Database() == database && t.design.Table() == table {
			s.activeTab = i
			s.structureViewer.Load(t.design, services.Invalidate, false)
			return
		}
	}
	s.nextTabID++
	t := &tab{id: s.nextTabID, kind: tabStructure, title: database + "." + table,
		design: s.structureViewer.Open(s.nextTabID, profileID, database, table)}
	s.tabs = append(s.tabs, t)
	s.activeTab = len(s.tabs) - 1
	s.structureViewer.Load(t.design, services.Invalidate, false)
}
func validStructureSelection(database, table string) bool { return database != "" && table != "" }

// Removing tabs also revokes their in-flight work. Inactive SQL/mock tabs are
// intentionally preserved so unrelated workspace documents are not lost.
func (s *State) clearMySQLStructureTabs() { s.clearMySQLTabsFor(0) }

func (s *State) clearMySQLTabsFor(profileID int64) {
	activeID := s.currentTab().id
	kept := s.tabs[:0]
	for _, t := range s.tabs {
		if (t.kind == tabStructure && (profileID == 0 || t.design.ProfileID() == profileID)) ||
			(t.kind == tabMySQLData && (profileID == 0 || t.dataProfileID == profileID)) ||
			(t.kind == tabProgram && (profileID == 0 || t.program.ProfileID() == profileID)) {
			if t.kind == tabStructure {
				s.structureViewer.Close(t.design)
			} else if t.kind == tabMySQLData {
				s.disposeMySQLDataTab(t)
			} else if t.kind == tabProgram {
				t.program.Close()
			}
			continue
		}
		kept = append(kept, t)
	}
	s.tabs = kept
	s.activeTab = 0
	for i, t := range kept {
		if t.id == activeID {
			s.activeTab = i
			break
		}
	}
}

func (s *State) pruneMySQLStructureTabs(known map[string]bool) {
	activeID := s.currentTab().id
	kept := s.tabs[:0]
	for _, t := range s.tabs {
		if (t.kind == tabStructure && t.design.ProfileID() == s.sessions.Current().ProfileID && !known[t.design.Database()]) ||
			(t.kind == tabMySQLData && t.dataProfileID == s.sessions.Current().ProfileID && !known[t.dataDatabase]) ||
			(t.kind == tabProgram && t.program.ProfileID() == s.sessions.Current().ProfileID && !known[t.program.Database()]) {
			if t.kind == tabStructure {
				s.structureViewer.Close(t.design)
			} else if t.kind == tabMySQLData {
				s.disposeMySQLDataTab(t)
			} else if t.kind == tabProgram {
				t.program.Close()
			}
			continue
		}
		kept = append(kept, t)
	}
	s.tabs = kept
	s.activeTab = 0
	for i, t := range kept {
		if t.id == activeID {
			s.activeTab = i
			break
		}
	}
}
