package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/datainspector"
)

func (s *State) dataInspectorDependencies() datainspector.Dependencies {
	return datainspector.Dependencies{
		Session: func() datainspector.Session {
			w := s.sessions.Current()
			return datainspector.Session{ProfileID: w.ProfileID, Generation: w.Generation, Connected: w.Status == "已连接"}
		},
		InfoLoader: func() datainspector.InfoLoader {
			if s.sessions.Manager == nil {
				return nil
			}
			return s.sessions.Manager.TableInfo
		},
		CountLoader: func() datainspector.CountLoader {
			if s.sessions.Manager == nil {
				return nil
			}
			return s.sessions.Manager.CountTableRows
		},
		ChunkLoader: func() datainspector.ChunkLoader {
			if s.sessions.Manager == nil {
				return nil
			}
			return s.sessions.Manager.ReadMySQLFieldChunk
		},
		Profile: func(id int64) (connections.Profile, bool) {
			for _, p := range s.navigation.Profiles() {
				if p.ID == id {
					return p, true
				}
			}
			return connections.Profile{}, false
		},
		Show: func() { s.inspectorOn = true }, Hide: func() { s.inspectorOn = false },
	}
}
func (s *State) dataInspectorPanel(t *tab) *datainspector.Panel {
	if t == nil {
		return nil
	}
	if t.dataPanel == nil {
		t.dataPanel = s.dataInspector.Open(t.dataProfileID, t.dataDatabase, t.dataTable, func() datainspector.Selection {
			return datainspector.Selection{Page: t.dataPage, Column: t.dataState.Cells.Cursor, Row: t.dataSelectedRow, Hidden: t.dataHiddenColumns, Offset: t.dataOffset, Loading: t.dataLoading, Error: t.dataErr}
		})
	}
	return t.dataPanel
}
func (s *State) showMySQLFieldInspector(t *tab) {
	if t != nil && t.kind == tabMySQLData {
		s.dataInspector.ShowField(s.dataInspectorPanel(t))
	}
}
func (s *State) cancelMySQLChunk(t *tab) {
	if t != nil {
		s.dataInspector.CancelChunk(t.dataPanel)
	}
}
func (s *State) mysqlDataObjectInspector(c *ui.Context, t *tab) {
	s.dataInspector.View(c, s.dataInspectorPanel(t))
}
