package shell

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type documentMenuScope struct {
	edit       theme.EditTarget
	tab        uint64
	profile    int64
	generation uint64
	tabs       map[uint64]bool
}

func primaryModifier() ui.Modifiers {
	if runtime.GOOS == "darwin" {
		return ui.Super
	}
	return ui.Ctrl
}

// Menus are rebuilt when chosen. Keep the original document/session scope
// instead of letting that rebuild silently retarget an editor or close action.
func (s *State) documentMenuCommands(section string, m *ui.Menu) {
	current, session := s.currentTab(), s.sessions.Current()
	scope := documentMenuScope{edit: s.editTarget, tab: current.id, profile: session.ProfileID, generation: session.Generation, tabs: map[uint64]bool{}}
	for _, t := range s.tabs {
		scope.tabs[t.id] = true
	}
	mod, selected := primaryModifier(), ""
	doc := current.query
	ready := doc != nil && doc.Editor != nil
	item := func(label, command string, enabled bool, mods ui.Modifiers, key ui.Key) {
		entry := m.Item(locales.Text(label))
		chosen := entry.Chosen()
		entry.Disabled(!enabled)
		if key != 0 {
			entry.Shortcut(mods, key)
		}
		if chosen {
			selected = command
		}
	}
	switch section {
	case "文件":
		item("新建连接…", "new_connection", true, 0, 0)
		item("新建查询", "new_query", true, mod, ui.KeyN)
		item("打开 SQL…", "open_sql", !s.sqlWorkspace.PersistenceLoading(), mod, ui.KeyO)
		m.Separator()
		if current.data != nil {
			item("保存数据更改", "save_data", s.dataWorkspace.CanSave(current.data), mod, ui.KeyS)
		} else if current.object != nil {
			item("保存表设计", "save_design", current.object.DesignDirty() && !current.object.DesignBusy() && s.objectWorkspace.CanEditDesign(current.object), mod, ui.KeyS)
		} else {
			item("保存查询", "save", ready, mod, ui.KeyS)
		}
		item("另存为", "save_as", ready, mod|ui.Shift, ui.KeyS)
		m.Separator()
		item("关闭当前页签", "close", s.activeTab != 0, mod, ui.KeyW)
	case "编辑":
		for _, entry := range []struct {
			label, command string
			key            ui.Key
			mods           ui.Modifiers
		}{{"撤销", "undo", ui.KeyZ, mod}, {"重做", "redo", ui.KeyZ, mod | ui.Shift}, {"剪切", "cut", ui.KeyX, mod}, {"复制", "copy", ui.KeyC, mod}, {"粘贴", "paste", ui.KeyV, mod}, {"全选", "select_all", ui.KeyA, mod}, {"查找…", "find", ui.KeyF, mod}, {"替换…", "replace", ui.KeyH, mod}} {
			item(entry.label, entry.command, scope.edit.Available(entry.command), entry.mods, entry.key)
		}
		item("格式化 SQL", "format", ready && !doc.Loading(), mod|ui.Shift, ui.KeyI)
		m.Separator()
		item("编辑连接", "edit_connection", s.profileMode && s.selectedProfileID > 0, 0, 0)
		item("复制连接", "copy_connection", s.profileMode && s.selectedProfileID > 0, 0, 0)
	case "窗口":
		item("命名工作区…", "workspaces", s.workspaces.store != nil, 0, 0)
		item("新建 SQL 页签", "new_query", true, mod, ui.KeyN)
		item("关闭当前页签", "close", s.activeTab != 0, mod, ui.KeyW)
		item("关闭其它页签", "close_others", len(s.tabs) > 1, 0, 0)
		item("关闭所有页签", "close_all", len(s.tabs) > 1, 0, 0)
		m.Separator()
		for _, t := range s.tabs {
			if m.Item(fmt.Sprintf("%s · #%d", s.workspaceTabCaption(t), t.id)).Checked(t.id == current.id).Chosen() {
				selected = fmt.Sprintf("tab/%d", t.id)
			}
		}
	}
	if s.documentMenus == nil {
		s.documentMenus = map[string]documentMenuScope{}
	}
	if selected == "" {
		s.documentMenus[section] = scope
		return
	}
	original, exists := s.documentMenus[section]
	delete(s.documentMenus, section)
	if !exists || original.tab != scope.tab || original.profile != scope.profile || original.generation != scope.generation {
		return
	}
	switch selected {
	case "workspaces":
		s.openWorkspaces()
	case "new_connection":
		s.openConnectionDialog()
	case "new_query":
		s.openQuery()
	case "open_sql":
		if !ready {
			s.openQuery()
			doc = s.currentTab().query
		}
		if doc != nil {
			s.sqlWorkspace.OpenFile(doc)
		}
	case "save":
		s.sqlWorkspace.Save(doc, false, nil)
	case "save_design":
		_ = s.objectWorkspace.PrepareDesign(current.object)
	case "save_data":
		if s.dataWorkspace.CanSave(current.data) {
			s.dataWorkspace.Save(s.editServices, current.data)
		}
	case "save_as":
		s.sqlWorkspace.Save(doc, true, nil)
	case "close":
		s.closeActive()
	case "close_others":
		s.removeTabs(func(t *tab) bool { return original.tabs[t.id] && t.id != original.tab && !t.pinned })
	case "close_all":
		s.removeTabs(func(t *tab) bool { return original.tabs[t.id] && !t.pinned })
	case "edit_connection":
		s.startEditProfile()
	case "copy_connection":
		s.duplicateProfile()
	case "format":
		if ready {
			s.sqlWorkspace.Format(doc)
		}
	default:
		if s.dispatchCapturedEdit(original.edit, selected) {
			return
		}
		if strings.HasPrefix(selected, "tab/") {
			id, _ := strconv.ParseUint(strings.TrimPrefix(selected, "tab/"), 10, 64)
			if original.tabs[id] {
				s.selectTab(id)
			}
		}
	}
}

func (s *State) registerEditTarget(target theme.EditTarget) {
	owner := s.currentTab().id
	valid := target.Valid
	target.Valid = func() bool {
		return s.currentTab().id == owner && !s.connectionDialog && valid != nil && valid() && s.editFocus == target.ID
	}
	s.editTarget = target
}

func (s *State) SetEditMenuHost(open func([]theme.EditMenuEntry)) { s.editMenuHost = open }
func (s *State) openEditMenu() {
	if s.editMenuHost == nil {
		return
	}
	target := s.editTarget
	s.editFocus = target.ID
	if s.connectionDialog {
		target = s.connectionEditTarget
		s.connectionEditFocus = target.ID
	}
	if target.Handle != nil {
		target.Handle.Focus()
	}
	entries := []theme.EditMenuEntry{}
	services := s.editServices
	for _, entry := range []struct{ label, command string }{{"撤销", "undo"}, {"重做", "redo"}, {"剪切", "cut"}, {"复制", "copy"}, {"粘贴", "paste"}, {"全选", "select_all"}, {"查找…", "find"}, {"替换…", "replace"}} {
		command := entry.command
		entries = append(entries, theme.EditMenuEntry{Label: locales.Text(entry.label), Command: command, Enabled: target.Available(command), Native: target.Native, Run: func() { s.dispatchCapturedEdit(target, command); services.Invalidate() }})
	}
	current := s.currentTab()
	owner := current.id
	entries = append(entries, theme.EditMenuEntry{Label: locales.Text("格式化 SQL"), Command: "format", Enabled: current.query != nil && !current.query.Loading(), Run: func() {
		if s.currentTab().id == owner {
			s.sqlWorkspace.Format(current.query)
			services.Invalidate()
		}
	}}, theme.EditMenuEntry{Label: locales.Text("编辑连接"), Enabled: s.selectedProfileID > 0, Run: s.startEditProfile}, theme.EditMenuEntry{Label: locales.Text("复制连接"), Enabled: s.selectedProfileID > 0, Run: s.duplicateProfile})
	s.editMenuHost(entries)
}

func (s *State) workbenchKeyboard(c *ui.Context) {
	if s.sqlWorkspace.AIVisible() {
		return
	}
	mod := primaryModifier()
	if current := s.currentTab(); current.data != nil {
		data := current.data
		c.OnShortcut(mod, ui.KeyS, func() {
			if s.currentTab().data == data && s.dataWorkspace.CanSave(data) {
				s.dataWorkspace.Save(c.Services(), data)
			}
		})
	}
	if current := s.currentTab(); current.object != nil {
		object := current.object
		c.OnShortcut(mod, ui.KeyS, func() {
			if s.currentTab().object == object && !object.DesignBusy() {
				_ = s.objectWorkspace.PrepareDesign(object)
			}
		})
	}
	c.OnShortcut(mod|ui.Shift, ui.KeyF, func() { s.openObjectSearch() })
	c.OnShortcut(mod, ui.KeyL, func() { s.sqlWorkspace.OpenHistory(0, s.openHistoryQuery) })
	c.OnShortcut(mod, ui.KeyN, func() { s.openQuery() })
	c.OnShortcut(ui.Ctrl, ui.KeyTab, func() { s.cycleTabs(false) })
	c.OnShortcut(ui.Ctrl|ui.Shift, ui.KeyTab, func() { s.cycleTabs(true) })
	c.OnShortcut(mod, ui.KeyW, func() { s.closeActive() })
	c.OnShortcut(mod, ui.KeyO, func() {
		doc := s.currentTab().query
		if doc == nil {
			s.openQuery()
			doc = s.currentTab().query
		}
		if doc != nil {
			s.sqlWorkspace.OpenFile(doc)
		}
	})
}

func (s *State) dispatchCapturedEdit(target theme.EditTarget, command string) bool {
	if s.connectionDialog {
		if s.connectionEditTarget.ID != target.ID {
			return false
		}
		s.connectionEditFocus = target.ID
	} else {
		if s.editTarget.ID != target.ID {
			return false
		}
		s.editFocus = target.ID
	}
	if target.Handle != nil {
		target.Handle.Focus()
	}
	return target.Execute(s.editServices, command)
}
