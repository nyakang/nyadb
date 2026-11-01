package objectworkspace

import (
	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/locales"
)

// A menu is rebuilt after its choice. Keep its opening identity and selection
// separately from the frame that receives that choice.
type browserActionScope struct {
	profile    int64
	database   string
	generation uint64
	browser    *browserState
	objects    []mysqlsvc.ObjectIdentity
}

type browserActions struct {
	scope                                                   browserActionScope
	frame                                                   *browserFrame
	items                                                   []mysqlsvc.MySQLDBObject
	ready, canOpen, canProgram, canCreate, canRefresh       bool
	canBatch, canCopy, canAccounts, canNewObject, canResume bool
}

func (s *State) browserActions() browserActions {
	b := s.currentBrowser()
	a := browserActions{frame: b, items: s.SelectedObjects(), ready: b.Connected && b.Database != "" && b.Opened}
	a.scope = browserActionScope{profile: b.ProfileID, database: b.Database, generation: s.sessionFor(b.ProfileID).Generation,
		browser: b.browserState, objects: append([]mysqlsvc.ObjectIdentity(nil), s.SelectedObjectIdentities()...)}
	selected := a.ready && b.Row >= 0 && b.Row < len(a.items)
	a.canProgram = selected && Supports(a.items[b.Row].Kind)
	a.canOpen = selected && !a.canProgram
	a.canCreate = a.ready && s.CanChangeTable(b.ProfileID) && (b.Kind == "全部" || b.Kind == "表")
	a.canRefresh = b.Connected && (b.Database == "" || a.ready) && !b.Refreshing && b.Pending == ""
	caps := s.capabilities(b.ProfileID)
	a.canBatch = a.ready && caps.DesignTables && s.deps.BatchExecutor != nil && !s.tablePending && (s.batch == nil || !s.batch.busy)
	a.canCopy = a.ready && len(a.scope.objects) != 0
	a.canAccounts = b.Connected && caps.DesignTables && s.deps.AccountLoader != nil && s.deps.AccountExecutor != nil && (s.accounts == nil || !s.accounts.busy)
	a.canNewObject = a.ready && caps.DesignTables && s.deps.ObjectExecutor != nil
	a.canResume = s.objectOp == nil && s.parkedObjectOp != nil && s.parkedObjectOp.profile == b.ProfileID
	return a
}

func (s *State) browserScopeValid(scope browserActionScope, opened bool) bool {
	b := s.currentBrowser()
	return b.ProfileID == scope.profile && b.Database == scope.database && b.browserState == scope.browser &&
		s.sessionFor(scope.profile).Generation == scope.generation && (!opened || b.Connected && b.Opened && b.Database != "")
}

func (s *State) browserMoreMenu(services ui.Services, m *ui.Menu) {
	a := s.browserActions()
	var chosen func(browserActionScope)
	item := func(label string, enabled bool, run func(browserActionScope)) {
		if m.Item(locales.Text(label)).Disabled(!enabled).Chosen() {
			chosen = run
		}
	}
	item("复制所选对象", a.canCopy, func(scope browserActionScope) {
		if raw, err := EncodeObjectClipboardFor(scope.profile, scope.objects); err == nil {
			services.WriteClipboard(raw)
		}
	})
	item("粘贴对象…", a.canBatch, func(scope browserActionScope) { s.pasteObjects(services, scope) })
	item("删除所选对象…", a.canBatch && a.canCopy, func(scope browserActionScope) {
		s.BeginObjectBatch(scope.profile, scope.objects, "drop")
	})
	m.Separator()
	m.Submenu(locales.Text("新建对象"), func(sub *ui.Menu) {
		for _, kind := range []string{"视图", "函数", "存储过程", "事件"} {
			if sub.Item(locales.Text("新建" + kind)).Disabled(!a.canNewObject).Chosen() {
				chosen = func(scope browserActionScope) {
					name := s.NextObjectName(scope.profile, "new_object")
					s.BeginObject(services, scope.profile, mysqlsvc.ObjectIdentity{Database: scope.database, Name: name, Kind: kind}, "create")
				}
			}
		}
	}).Disabled(!a.canNewObject)
	m.Separator()
	m.Submenu(locales.Text("对象分组筛选"), func(sub *ui.Menu) {
		if sub.Item(locales.Text("全部分组")).Radio(a.frame.groupFilter == "").Disabled(!a.ready).Chosen() {
			chosen = func(scope browserActionScope) { s.filterObjectGroup(scope, "") }
		}
		for _, group := range s.ObjectGroups(a.scope.profile, a.scope.database) {
			// Group names are user data and must not be translated.
			if sub.Item(group).Radio(a.frame.groupFilter == group).Disabled(!a.ready).Chosen() {
				chosen = func(scope browserActionScope) { s.filterObjectGroup(scope, group) }
			}
		}
	}).Disabled(!a.ready)
	item("管理组…", a.ready, func(scope browserActionScope) { s.beginObjectGroups(scope) })
	m.Separator()
	item("用户与权限", a.canAccounts, func(scope browserActionScope) { s.OpenAccounts(services, scope.profile, scope.database, "") })
	item("继续对象草稿", a.canResume, func(scope browserActionScope) {
		if s.objectOp == nil && s.parkedObjectOp != nil && s.parkedObjectOp.profile == scope.profile {
			s.ResumeObjectDraft()
		}
	})
	if chosen == nil {
		// This callback runs when opening, or when an obsolete/disabled choice
		// was rejected. Neither path dispatches an operation.
		s.moreScope = &a.scope
		return
	}
	if s.moreScope != nil && s.browserScopeValid(*s.moreScope, false) {
		chosen(*s.moreScope)
	}
	s.moreScope = nil
}

func (s *State) pasteObjects(services ui.Services, scope browserActionScope) {
	clip, err := DecodeObjectClipboard(services.ReadClipboard())
	if err != nil {
		s.report(err.Error())
	} else if clip.ProfileID != scope.profile {
		s.report(locales.Text("对象剪贴板属于另一连接，请显式切换到原连接"))
	} else {
		s.BeginObjectBatch(scope.profile, clip.Objects, "copy")
	}
}

func (s *State) filterObjectGroup(scope browserActionScope, group string) {
	if !s.browserScopeValid(scope, true) {
		return
	}
	b := scope.browser
	b.groupFilter = group
	b.Row = -1
	b.selection.Clear()
	b.renameEditing = false
	b.List = ui.ListState{Columns: b.List.Columns}
}
