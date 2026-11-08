package objectworkspace

import (
	"encoding/json"
	"github.com/egoist/mygo/ui"
	"sort"
)

type browserPresentation struct {
	Profile                         int64
	Database, Kind, Category, Group string
	Row                             int
	Columns                         ui.TableLayout
	ScrollX, ScrollY                float32
}

func (s *State) WorkspaceSnapshot() json.RawMessage {
	v := []browserPresentation{}
	for id, b := range s.browsers {
		v = append(v, browserPresentation{Profile: id, Database: b.database, Kind: b.Kind, Category: b.toolbarKind, Group: b.groupFilter, Row: b.Row, Columns: b.List.Columns, ScrollX: b.Scroll.X, ScrollY: b.Scroll.Y})
	}
	sort.Slice(v, func(i, j int) bool { return v[i].Profile < v[j].Profile })
	data, _ := json.Marshal(v)
	return data
}
func (s *State) RestoreWorkspace(data json.RawMessage) error {
	var v []browserPresentation
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	for _, p := range v {
		b := s.presentation(p.Profile)
		b.database, b.Kind, b.toolbarKind, b.groupFilter, b.Row = p.Database, p.Kind, p.Category, p.Group, p.Row
		b.List = ui.ListState{Columns: p.Columns}
		b.Scroll.X, b.Scroll.Y = p.ScrollX, p.ScrollY
		copy := p
		b.restored = &copy
	}
	return nil
}
func (s *State) DocumentSnapshot(d *Document) json.RawMessage {
	if d.SettingsMode() {
		return json.RawMessage(`{"Settings":true}`)
	}
	if d != nil && d.tasks != nil {
		return snapshotTasks(d)
	}
	page := 0
	if d.schemaDesign != nil {
		page = d.schemaDesign.page
	} else if d.design != nil {
		page = d.design.page
	}
	b, _ := json.Marshal(struct {
		Page   int
		Design bool
	}{page, d.DesignMode()})
	return b
}
func (s *State) RestoreDocumentPresentation(d *Document, b json.RawMessage) {
	if d.SettingsMode() {
		d.settings.tab = 0
		return
	}
	if d != nil && d.tasks != nil {
		restoreTasks(d, b)
		return
	}
	var v struct {
		Page   int
		Design bool
	}
	if json.Unmarshal(b, &v) != nil {
		return
	}
	if d.schemaDesign != nil {
		d.schemaDesign.page = v.Page
	} else if d.design != nil {
		d.design.page = v.Page
	}
}
