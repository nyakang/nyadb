package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/locales"
)

type workbenchPresentation struct {
	Navigation, Objects       json.RawMessage
	Sidebar, Center, SQLSplit float32
	Inspector                 bool
	SelectedProfile           int64
}

// Validate before any leave guard or disposal. Missing optional presentation
// remains compatible with older snapshots; malformed identities do not.
func validateWorkspace(w queries.Workspace) error {
	if w.SnapshotVersion < 0 || w.SnapshotVersion > 2 {
		return errors.New(locales.Text("工作区版本不支持"))
	}
	docs := make(map[uint64]bool, len(w.Documents))
	for _, d := range w.Documents {
		if d.ID <= 1 || docs[d.ID] {
			return errors.New("invalid or duplicate SQL document ID")
		}
		docs[d.ID] = true
		if len(d.Presentation) > 0 && !json.Valid(d.Presentation) {
			return errors.New("invalid SQL document presentation")
		}
	}
	ids := make(map[uint64]bool, len(w.Tabs))
	for _, t := range w.Tabs {
		if t.ID == 0 || ids[t.ID] || t.ID == 1 && tabKind(t.Kind) != tabObjects {
			return errors.New("invalid or duplicate workspace tab ID")
		}
		ids[t.ID] = true
		switch tabKind(t.Kind) {
		case tabSQL:
			if !docs[t.ID] {
				return errors.New("SQL tab document is missing")
			}
		case tabObjects, tabMySQLData, tabStructure, tabProgram, tabSettings, tabConnections:
		default:
			return errors.New("unsupported workspace tab kind")
		}
		if len(t.Presentation) > 0 && !json.Valid(t.Presentation) {
			return errors.New("invalid workspace tab presentation")
		}
	}
	if len(w.Shell) > 0 && !json.Valid(w.Shell) {
		return errors.New("invalid workbench presentation")
	}
	return nil
}

func (s *State) workspaceSnapshot() queries.Workspace {
	ids := []uint64{}
	for _, t := range s.tabs {
		if t.query != nil {
			ids = append(ids, t.id)
		}
	}
	w := s.sqlWorkspace.Snapshot(s.currentTab().id, ids)
	w.SnapshotVersion = 2
	for _, t := range s.tabs {
		// Native KV request files are explicitly saved; no native execution or
		// unconfirmed receipt is reconstructed by SQL workspace restoration.
		if t.kv != nil {
			continue
		}
		key := t.identity()
		saved := queries.WorkspaceTab{ID: t.id, Kind: uint8(t.kind), Title: t.title, ProfileID: key.profileID, Catalog: key.catalog, Schema: key.schema, Database: key.database, ObjectKind: key.objectKind, ObjectName: key.name, Pinned: t.pinned}
		if t.query != nil {
			n := t.query.Namespace()
			saved.ProfileID, saved.Catalog, saved.Schema = n.ProfileID, n.Catalog, n.Schema
		}
		if t.placeholder != nil {
			saved = *t.placeholder
			saved.Pinned = t.pinned
		} else if t.data != nil {
			saved.Presentation = s.dataWorkspace.WorkspaceSnapshot(t.data)
		} else if t.object != nil {
			saved.Presentation = s.objectWorkspace.DocumentSnapshot(t.object)
		}
		w.Tabs = append(w.Tabs, saved)
	}
	w.Shell, _ = json.Marshal(workbenchPresentation{s.navigation.WorkspaceSnapshot(), s.objectWorkspace.WorkspaceSnapshot(), s.sidebarPercent, s.centerPercent, s.sqlWorkspace.SplitPercent, s.inspectorOn, s.selectedProfileID})
	return w
}

// Restoration creates presentation placeholders only. No lease, metadata request,
// SQL execution, data drafts or design drafts are reconstructed here.
func (s *State) restoreWorkspace(w queries.Workspace, merge bool) {
	if err := validateWorkspace(w); err != nil {
		s.status = err.Error()
		return
	}
	active := s.currentTab().id
	docs := map[uint64]queries.Document{}
	for _, d := range w.Documents {
		docs[d.ID] = d
	}
	saved := w.Tabs
	if w.SnapshotVersion < 2 {
		for _, d := range w.Documents {
			saved = append(saved, queries.WorkspaceTab{ID: d.ID, Kind: uint8(tabSQL)})
		}
	}
	for _, v := range saved {
		s.nextTabID = max(s.nextTabID, v.ID)
	}
	for _, v := range saved {
		if existing := s.tabIndex(v.ID); existing >= 0 && merge && v.ID > 1 {
			old := s.tabs[existing]
			if old.query != nil && tabKind(v.Kind) == tabSQL {
				snapshot, ok := docs[v.ID]
				if ok && old.query.Editor.Buffer.String() != snapshot.Text {
					s.nextTabID++
					snapshot.ID = s.nextTabID
					docs[snapshot.ID] = snapshot
					v.ID = snapshot.ID
				}
			}
		}
		if v.ID <= 1 || s.tabIndex(v.ID) >= 0 {
			continue
		}
		t := &tab{id: v.ID, kind: tabKind(v.Kind), title: v.Title, pinned: v.Pinned}
		if t.kind == tabSQL {
			d, ok := docs[v.ID]
			if !ok {
				continue
			}
			t.query = s.sqlWorkspace.RestoreDocument(d)
			t.title = t.query.Name()
		} else if t.kind == tabMySQLData || t.kind == tabStructure || t.kind == tabProgram {
			copy := v
			t.placeholder = &copy
		} else if t.kind != tabSettings && t.kind != tabConnections {
			continue
		}
		s.tabs = append(s.tabs, t)
		s.nextTabID = max(s.nextTabID, v.ID)
	}
	// Fixed tabs occupy one continuous region after the permanent object page.
	pinned, normal := []*tab{s.tabs[0]}, []*tab{}
	for _, t := range s.tabs[1:] {
		if t.pinned {
			pinned = append(pinned, t)
		} else {
			normal = append(normal, t)
		}
	}
	s.tabs = append(pinned, normal...)
	if !merge {
		p := workbenchPresentation{Inspector: s.inspectorOn}
		if json.Unmarshal(w.Shell, &p) == nil {
			if p.Sidebar > 0 && p.Sidebar < 100 {
				s.sidebarPercent = p.Sidebar
			}
			if p.Center > 0 && p.Center < 100 {
				s.centerPercent = p.Center
			}
			if p.SQLSplit > 0 && p.SQLSplit < 100 {
				s.sqlWorkspace.SplitPercent = p.SQLSplit
			}
			s.inspectorOn = p.Inspector
			s.selectedProfileID = p.SelectedProfile
			_ = s.navigation.RestoreWorkspace(p.Navigation)
			_ = s.objectWorkspace.RestoreWorkspace(p.Objects)
		}
		s.selectTab(w.ActiveID)
		// Tab activation may reveal its connection; the saved navigation selection
		// and disclosure remain an independent part of the restored scene.
		var scene workbenchPresentation
		if json.Unmarshal(w.Shell, &scene) == nil {
			_ = s.navigation.RestoreWorkspace(scene.Navigation)
		}
	} else {
		s.selectTab(active)
	}
}
func (s *State) restoredDocument(c *ui.Context, t *tab) {
	v := t.placeholder
	ui.Column(c).Fill().Center().Gap(12).Padding(20).Children(func() {
		ui.Text(c, t.title).Bold()
		ui.Text(c, s.workspaceTabTooltip(t))
		exists := false
		for _, p := range s.navigation.Profiles() {
			exists = exists || p.ID == v.ProfileID
		}
		message := "恢复原连接后，点击读取对象继续工作"
		if !exists {
			message = "原连接已不存在，请重新绑定或关闭页签"
		}
		ui.Text(c, locales.Text(message))
		if exists {
			if ui.Button(c, locales.Text("恢复原连接")).Disabled(s.sessions.StatusFor(v.ProfileID) == "已连接" || s.sessions.StatusFor(v.ProfileID) == "连接中").Clicked() {
				s.connectWorkspaceProfile(c.Services(), v.ProfileID)
			}
			if ui.PrimaryButton(c, locales.Text("读取对象")).Disabled(s.sessions.StatusFor(v.ProfileID) != "已连接").Clicked() {
				s.materializeRestored(c.Services(), t)
			}
		}
		ui.Button(c, locales.Text("重新绑定连接")).Menu(func(m *ui.Menu) {
			for _, p := range s.navigation.Profiles() {
				if m.Item(p.Name).Chosen() {
					v.ProfileID = p.ID
					v.Catalog = ""
					v.Schema = v.Database
				}
			}
		})
		if ui.Button(c, locales.Text("关闭当前页签")).Clicked() {
			s.closeTabID(t.id)
		}
	})
}
func (s *State) materializeRestored(services ui.Services, t *tab) {
	if t.placeholder == nil {
		return
	}
	v := *t.placeholder
	if !s.activateObjectProfile(v.ProfileID) || s.sessions.StatusFor(v.ProfileID) != "已连接" {
		return
	}
	o := contract.ObjectRef{ProfileID: v.ProfileID, Catalog: v.Catalog, Schema: v.Schema, Kind: neutralObjectKind(v.ObjectKind), Name: v.ObjectName}
	if o.Schema == "" && o.Catalog == "" {
		o = s.runtime.Namespace(v.ProfileID, v.Database).Object(neutralObjectKind(v.ObjectKind), v.ObjectName)
	}
	if t.kind == tabMySQLData {
		t.data = s.dataWorkspace.OpenObject(t.id, o, s.objectCapabilitiesFor(v.ProfileID))
		if t.data == nil {
			return
		}
		if len(v.Presentation) > 0 {
			if err := s.dataWorkspace.RestoreWorkspace(t.data, v.Presentation); err != nil {
				s.status = fmt.Sprint(err)
			}
		}
		s.dataWorkspace.Load(services, t.data, false)
	} else if t.kind == tabStructure {
		t.object = s.objectWorkspace.OpenQualifiedStructure(t.id, o, v.ObjectKind)
		if t.object == nil {
			s.status = locales.Text("当前引擎不支持此操作")
			return
		}
		if o.Kind == "tasks" || o.Kind == "keys" {
			s.objectWorkspace.RestoreDocumentPresentation(t.object, v.Presentation)
			if len(v.Presentation) == 0 {
				s.objectWorkspace.Load(t.object, services.Invalidate, false)
			}
		} else {
			var p struct{ Design bool }
			_ = json.Unmarshal(v.Presentation, &p)
			if p.Design && s.objectWorkspace.CanEditDesign(t.object) {
				s.objectWorkspace.BeginDesign(t.object, services)
			} else {
				s.objectWorkspace.Load(t.object, services.Invalidate, false)
			}
			s.objectWorkspace.RestoreDocumentPresentation(t.object, v.Presentation)
		}
	} else {
		w := s.sessions.Workspace(v.ProfileID)
		objects, loaded := w.Cache[v.Database]
		if !loaded {
			s.selectMySQLDatabase(services, v.Database)
			s.status = locales.Text("正在读取对象，请稍后重试")
			return
		}
		found := false
		for _, object := range objects {
			if object.Name == v.ObjectName && object.Kind == v.ObjectKind {
				t.object = s.objectWorkspace.OpenProgram(t.id, v.ProfileID, v.Database, object)
				found = true
				break
			}
		}
		if !found {
			s.status = locales.Text("原对象已不存在，请重新绑定或关闭页签")
			return
		}
	}
	t.placeholder = nil
}
