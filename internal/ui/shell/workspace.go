package shell

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/datasources"
	"slices"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/dataworkspace"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
	"github.com/nyakang/nyadb/internal/ui/sqlworkspace"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) workspace(c *ui.Context) {
	palette := theme.Colors(c)

	ui.Column(c).Fill().Background(palette.Sidebar).Children(func() {
		ui.Row(c).Height(theme.RowHeight(c, theme.TabHeight)).AlignItems(ui.Center).Padding(0, 3).
			Background(palette.Panel).Gap(3).Children(func() {
			ui.ScrollHorizontal(c).Grow(1).Children(func() {
				tabs := ui.TabsBase(c, &s.activeTab, len(s.tabs))
				frameTabs := append([]*tab(nil), s.tabs...)
				tabs.List.Gap(0).Children(func() {
					for i, doc := range frameTabs {
						index, current := i, doc
						item := tabs.Tab(i).Height(theme.RowHeight(c, theme.TabHeight)).Padding(2, 8).Radius(0).Label(current.title)
						if i == s.activeTab {
							item.Background(palette.ToolbarSelected).BorderWidth(1, 1, 0, 1).BorderColor(palette.Border)
						}
						item.Children(func() {
							ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
								icon := "database"
								if current.kind == tabSQL {
									icon = "query"
								} else if current.kind == tabData {
									icon = "table"
								} else if current.kind == tabStructure {
									icon = "design-table"
								} else if current.kind == tabMySQLData {
									icon = "table"
								} else if current.kind == tabSettings {
									icon = "settings"
								} else if current.kind == tabProgram {
									icon = "function"
								} else if current.kind == tabConnections {
									icon = "connection"
								}
								theme.Icon(c, icon, 14, palette.Blue)
								ui.Text(c, s.workspaceTabCaption(current)).FontSize(theme.Font(c, theme.FontSize)).SingleLine().MaxWidth(220).Tooltip(current.title)
								if current.kind != tabObjects {
									if ui.ButtonBase(c).Label("关闭 "+current.title).Tooltip("关闭 "+current.title).
										Size(16, 18).Children(func() { theme.Icon(c, "close", 10, palette.Muted) }).Clicked() {
										s.closeTab(index)
									}
								}
							})
						})
						item.ContextMenu(func(m *ui.Menu) {
							if m.Item(locales.Text("关闭 ") + current.title).Disabled(index == 0).Chosen() {
								s.closeTab(index)
							}
							if m.Item(locales.Text("关闭其他页签")).Disabled(len(s.tabs) <= 1 || (len(s.tabs) == 2 && index != 0)).Chosen() {
								s.closeOtherTabs(index)
							}
						})
					}
				})
			})
			if ui.ButtonBase(c).Label(locales.Text("新建 SQL 页签")).Tooltip(locales.Text("新建 SQL 页签")).Size(24, 23).
				Children(func() { theme.Icon(c, "plus", 12, palette.Muted) }).Clicked() {
				s.openQuery()
			}
		})
		switch tab := s.currentTab(); tab.kind {
		case tabSQL:
			s.sqlWorkspace.View(c, tab.query)
		case tabSettings:
			s.settingsWorkspace(c)
		case tabData:
			s.dataPreview(c)
		case tabStructure:
			s.objectWorkspace.View(c, tab.object)
		case tabMySQLData:
			s.dataWorkspace.View(c, tab.data)
		case tabProgram:
			s.objectWorkspace.View(c, tab.object)
		case tabConnections:
			s.connectionManagementWorkspace(c)
		default:
			s.objectsWorkspace(c)
		}
	})
}

func (s *State) sqlWorkspaceDependencies() sqlworkspace.Dependencies {
	return sqlworkspace.Dependencies{
		FavoriteAsset: s.favoriteQuery, CanFavorite: s.favorites.Available,
		AssetsChanged:    s.sqlAssetsChanged,
		StatementAtCaret: s.sqlStatementAtCaret, FormatSQL: s.sqlFormat,
		ValidateExplain:   s.sqlValidateExplain,
		CompletionCatalog: s.sqlCompletionCatalog,
		Completions: func(id int64, db string) []string {
			var out []string
			for _, o := range s.sessions.CacheFor(id)[db] {
				out = append(out, o.Name)
			}
			for _, t := range s.tabs {
				if t.data != nil && t.data.ProfileID() == id && t.data.Database() == db && t.data.Page() != nil {
					out = append(out, t.data.Page().Columns...)
				}
				if t.object != nil && t.object.ProfileID() == id && t.object.Database() == db && t.object.Data() != nil {
					for _, c := range t.object.Data().Columns {
						out = append(out, c.Name)
					}
				}
			}
			return out
		},
		Runtime:   func() sqlworkspace.Runtime { return s.sqlRuntime() },
		Connected: func(id int64) bool { return s.sessions.StatusFor(id) == "已连接" },
		Active:    func(doc *sqlworkspace.Document) bool { return s.currentTab().query == doc },
		Status:    func(message string) { s.status = message },
		Recovered: func(id int64) { s.sessions.Activate(id); s.selectedProfileID = id },
		Preview:   s.dataPreview,
	}
}

func (s *State) dataInspectorDependencies() dataworkspace.InspectorDependencies {
	return dataworkspace.InspectorDependencies{
		Session: func() dataworkspace.Session {
			w := s.sessions.Current()
			return dataworkspace.Session{ProfileID: w.ProfileID, Generation: w.Generation, Connected: w.Status == "已连接"}
		},
		InfoLoader: func() dataworkspace.InfoLoader {
			return s.runtime.Data(s.sessions.Current().ProfileID).TableInfo
		},
		CountLoader: func() dataworkspace.CountLoader {
			return s.runtime.Data(s.sessions.Current().ProfileID).Count
		},
		ChunkLoader: func() dataworkspace.ChunkLoader {
			return s.runtime.Data(s.sessions.Current().ProfileID).Chunk
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

func (s *State) dataWorkspaceDependencies() dataworkspace.Dependencies {
	return dataworkspace.Dependencies{
		RecordActivity: s.recordActivity,
		ImportTargets: func(id int64, db string) []string {
			var tables []string
			for _, obj := range s.sessions.CacheFor(id)[db] {
				if obj.Kind == "表" {
					tables = append(tables, obj.Name)
				}
			}
			return tables
		},
		Session: func() dataworkspace.Session {
			w := s.sessions.Current()
			return dataworkspace.Session{ProfileID: w.ProfileID, Generation: w.Generation, Connected: w.Status == "已连接"}
		},
		SessionFor: func(id int64) dataworkspace.Session {
			w := s.sessions.Workspace(id)
			return dataworkspace.Session{ProfileID: id, Generation: w.Generation, Connected: w.Status == "已连接"}
		},
		RuntimeFor: func(id int64) dataworkspace.Runtime { return s.runtime.Data(id) },
		Runtime:    func() dataworkspace.Runtime { return s.runtime.Data(s.sessions.Current().ProfileID) },
		Inspector:  s.dataInspectorDependencies(), Status: func(message string) { s.status = message },
		TableImported: func(services ui.Services, event dataworkspace.TableImportEvent) {
			if !s.sessions.InvalidateDatabaseObjects(event.ProfileID, event.Generation, event.Database) {
				return
			}
			w := s.sessions.Workspace(event.ProfileID)
			if w == s.sessions.Current() && w.Database == event.Database {
				s.loadMySQLObjects(services, event.Database, true)
			}
		},
		OpenImportedTable: func(services ui.Services, event dataworkspace.TableImportEvent) {
			w := s.sessions.Current()
			if event.Created && !event.Uncertain && w.Status == "已连接" && w.ProfileID == event.ProfileID && w.Generation == event.Generation {
				s.openMySQLData(services, event.Database, event.Table)
			}
		},
		Activate: func(d *dataworkspace.Document) {
			for i, t := range s.tabs {
				if t.data == d {
					s.activeTab = i
					s.activateMySQLForCurrentTab()
					return
				}
			}
		},
	}
}
func (s *State) SetDataLayoutPreferences(read func(string) (string, error), write func(string, string) error) {
	s.dataWorkspace.SetDataLayoutPreferences(read, write)
	s.objectWorkspace.SetObjectGroupPreferences(read, write)
}

// All document leave checks pass through this gate. Continuations retain the
// original operation's identities while saves wait for server acknowledgement.
func (s *State) guardTabsLeave(matches func(*tab) bool, next func(ui.Services)) bool {
	if s.mysqlForceClose {
		return false
	}
	for _, t := range s.tabs {
		if matches(t) && (s.objectWorkspace.GuardDesignLeave(t.object, next) || s.dataWorkspace.GuardTransactionLeave(t.data, next) || s.dataWorkspace.GuardLeave(t.data, next)) {
			return true
		}
	}
	return false
}
func (s *State) guardMySQLProfileLeave(id int64, next func(ui.Services)) bool {
	return s.guardTabsLeave(func(t *tab) bool { return id == 0 || t.profileID() == id }, next)
}
func (s *State) RequestWorkbenchClose(next func()) bool {
	s.observeSQLDocuments()
	if s.sqlWorkspace.GuardWorkspaceClose(func(_ ui.Services) {
		if !s.RequestWorkbenchClose(next) {
			next()
		}
	}) {
		return true
	}
	return s.guardMySQLProfileLeave(0, func(_ ui.Services) {
		if !s.RequestWorkbenchClose(next) {
			next()
		}
	})
}

func (s *State) openMySQLStructure(services ui.Services, database, table string) {
	profileID := s.sessions.Current().ProfileID
	if s.sessions.Current().Status != "已连接" || profileID <= 0 || !validStructureSelection(database, table) {
		return
	}
	kind := s.objectKindFor(profileID, database, table)
	if t := s.findDocument(tabStructure, profileID, database, kind, table); t != nil {
		s.objectWorkspace.Load(t.object, services.Invalidate, false)
		return
	}
	s.nextTabID++
	t := &tab{id: s.nextTabID, kind: tabStructure, title: database + "." + table,
		object: s.objectWorkspace.OpenQualifiedStructure(s.nextTabID, s.runtime.Namespace(profileID, database).Object(neutralObjectKind(kind), table), kind)}
	s.tabs = append(s.tabs, t)
	s.activeTab = len(s.tabs) - 1
	s.objectWorkspace.Load(t.object, services.Invalidate, false)
}
func validStructureSelection(database, table string) bool { return database != "" && table != "" }

// Removing tabs also revokes their in-flight work. Inactive SQL/mock tabs are
// intentionally preserved so unrelated workspace documents are not lost.
func (s *State) clearMySQLTabsFor(profileID int64) {
	s.removeTabs(func(t *tab) bool {
		return (t.data != nil || t.object != nil) && (profileID == 0 || t.profileID() == profileID)
	})
}
func (s *State) pruneMySQLTabsFor(id int64, known map[string]bool) {
	s.removeTabs(func(t *tab) bool {
		return (t.data != nil || t.object != nil) && t.profileID() == id && !known[t.database()]
	})
}

func (s *State) objectWorkspaceDependencies() objectworkspace.Dependencies {
	return objectworkspace.Dependencies{
		SchemaDesignFor: func(id int64) contract.SchemaDesignOperations {
			if s.runtime == nil {
				return contract.SchemaDesignOperations{}
			}
			return s.runtime.SchemaDesign(id)
		},
		SchemaTarget: func(id int64, database, table string) contract.SchemaDesignTarget {
			if s.runtime == nil {
				return contract.SchemaDesignTarget{}
			}
			return contract.SchemaDesignTarget{Object: s.runtime.Namespace(id, database).Object("table", table), Generation: s.runtime.Generation(id)}
		},
		SchemaDesigned: s.schemaTableDesigned,
		RenameSchema: func(services ui.Services, profile int64, database, table, name string) {
			if !s.activateObjectProfile(profile) {
				return
			}
			s.openMySQLStructure(services, database, table)
			if active := s.currentTab(); active != nil && active.object != nil {
				s.objectWorkspace.BeginSchemaRename(active.object, name, services)
			}
		},
		Favorite: s.favoriteObject, CanFavorite: s.favorites.Available,
		SearchRuntime: s.objectSearchRuntime, OpenSearchObject: s.openSearchObject,
		RecordActivity:    s.recordActivity,
		TableProfiles:     s.savedTableProfiles,
		OpenTableProfile:  s.openSavedTableProfile,
		Capabilities:      s.objectCapabilitiesFor,
		AdministrationFor: func(id int64) mysqlsvc.Administration { return s.runtime.Administration(id) },
		DataAction:        s.openTableDataAction,
		Backup:            s.objectWorkspaceBackup,
		AccountLoader: func(ctx context.Context, id int64) ([]mysqlsvc.MySQLAccount, error) {
			admin := s.runtime.Administration(id)
			if admin == nil {
				return nil, mysqlsvc.ErrMySQLNotConnected
			}
			return admin.Accounts(ctx, id)
		},
		AccountGrants: func(ctx context.Context, id int64, a mysqlsvc.MySQLAccount) ([]string, error) {
			admin := s.runtime.Administration(id)
			if admin == nil {
				return nil, mysqlsvc.ErrMySQLNotConnected
			}
			return admin.AccountGrants(ctx, id, a)
		},
		AccountExecutor: func(ctx context.Context, id int64, generation uint64, c []mysqlsvc.AccountChange) mysqlsvc.DDLOutcome {
			admin := s.runtime.Administration(id)
			if admin == nil {
				return mysqlsvc.DDLOutcome{Error: mysqlsvc.ErrMySQLNotConnected}
			}
			return admin.ChangeAccounts(ctx, id, generation, c)
		},
		Maintenance: func(ctx context.Context, id int64, db, table, action string) ([]mysqlsvc.MaintenanceMessage, error) {
			admin := s.runtime.Administration(id)
			if admin == nil {
				return nil, mysqlsvc.ErrMySQLNotConnected
			}
			return admin.MaintainTable(ctx, id, db, table, action)
		},
		BatchExecutor: func(ctx context.Context, id int64, generation uint64, items []mysqlsvc.ObjectBatchItem) []mysqlsvc.ObjectBatchResult {
			admin := s.runtime.Administration(id)
			if admin == nil {
				return nil
			}
			return admin.ApplyObjectBatch(ctx, id, generation, items)
		},
		ObjectLoader: func(ctx context.Context, id int64, ref mysqlsvc.ObjectIdentity) (mysqlsvc.ObjectDefinition, error) {
			admin := s.runtime.Administration(id)
			if admin == nil {
				return mysqlsvc.ObjectDefinition{}, mysqlsvc.ErrMySQLNotConnected
			}
			return admin.ObjectDefinition(ctx, id, ref)
		},
		ObjectExecutor: func(ctx context.Context, id int64, p mysqlsvc.ObjectPlan) mysqlsvc.ObjectOutcome {
			admin := s.runtime.Administration(id)
			if admin == nil {
				return mysqlsvc.ObjectOutcome{DDLOutcome: mysqlsvc.DDLOutcome{Error: mysqlsvc.ErrMySQLNotConnected}}
			}
			return admin.ApplyObject(ctx, id, p)
		},
		RuntimeGeneration: func(id int64) uint64 {
			admin := s.runtime.Administration(id)
			if admin == nil {
				return 0
			}
			return admin.SessionGeneration(id)
		},
		ObjectChanged: s.mysqlObjectChanged,
		ActivateDesign: func(d *objectworkspace.Document) {
			for i, t := range s.tabs {
				if t.object == d {
					s.activeTab = i
					s.activateMySQLForCurrentTab()
					return
				}
			}
		},
		DesignLoader: func() objectworkspace.DesignLoader {
			admin := s.runtime.Administration(s.sessions.Current().ProfileID)
			if admin == nil {
				return nil
			}
			return admin.TableDesign
		},
		DesignExecutor: func() objectworkspace.DesignExecutor {
			admin := s.runtime.Administration(s.sessions.Current().ProfileID)
			if admin == nil {
				return nil
			}
			return admin.ApplyTableDesign
		},
		DesignGeneration: func(id int64) uint64 {
			admin := s.runtime.Administration(id)
			if admin == nil {
				return 0
			}
			return admin.SessionGeneration(id)
		},
		BeforeDDL: func(services ui.Services, id int64, db, table string, next func(ui.Services)) bool {
			var finish func(ui.Services)
			matches := func(t *tab) bool {
				return t.data != nil && t.data.ProfileID() == id && t.data.Database() == db && (table == "" || t.data.Table() == table)
			}
			finish = func(services ui.Services) {
				if !s.guardTabsLeave(matches, finish) {
					next(services)
				}
			}
			return s.guardTabsLeave(matches, finish)
		},
		Designed: s.mysqlTableDesigned,
		NewTable: func(services ui.Services, id int64, db, name string) {
			if !s.activateObjectProfile(id) {
				return
			}
			s.nextTabID++
			var d *objectworkspace.Document
			binding := s.runtime.SchemaDesign(id)
			if binding.Snapshot != nil && binding.Apply != nil {
				d = s.objectWorkspace.OpenNewSchemaDesign(s.nextTabID, s.runtime.Namespace(id, db).Object("table", name), services)
			} else {
				d = s.objectWorkspace.OpenNewDesign(s.nextTabID, id, db, name)
			}
			if d != nil {
				s.tabs = append(s.tabs, &tab{id: s.nextTabID, kind: tabStructure, title: db + "." + name, object: d})
				s.activeTab = len(s.tabs) - 1
			}
		},
		Session: func() objectworkspace.Session {
			w := s.sessions.Current()
			return objectworkspace.Session{ProfileID: w.ProfileID, Generation: w.Generation, Connected: w.Status == "已连接"}
		},
		SessionFor: func(id int64) objectworkspace.Session {
			w := s.sessions.Workspace(id)
			if w == nil {
				return objectworkspace.Session{}
			}
			return objectworkspace.Session{ProfileID: w.ProfileID, Generation: w.Generation, Connected: w.Status == "已连接"}
		},
		Loader: func() objectworkspace.Loader { return s.runtime.Data(s.sessions.Current().ProfileID).TableMetadata },
		Runner: func() objectworkspace.TableRunner {
			admin := s.runtime.Administration(s.sessions.Current().ProfileID)
			if admin == nil {
				return nil
			}
			return admin.ChangeTable
		},
		Browser: func() objectworkspace.BrowserSnapshot {
			w := s.sessions.Current()
			return objectworkspace.BrowserSnapshot{ProfileID: w.ProfileID, Database: w.Database, Error: w.Error, Pending: w.Pending, Connected: s.sessions.StatusFor(s.selectedProfileID) == "已连接", Opened: s.sessions.DatabaseOpened(w.ProfileID, w.Database), Refreshing: w.Refreshing(), Errors: w.Errors, Objects: w.Cache[w.Database]}
		},
		Back: func() { s.activeTab = 0 }, Status: func(message string) { s.status = message },
		OpenData: func(services ui.Services, id int64, db, table string) {
			if s.activateObjectProfile(id) {
				s.openMySQLData(services, db, table)
			}
		},
		OpenStructure: func(services ui.Services, id int64, db, table string) {
			if s.activateObjectProfile(id) {
				s.openMySQLStructure(services, db, table)
			}
		},
		OpenProgram: func(id int64, db, name, kind string) {
			if s.activateObjectProfile(id) {
				s.openMySQLProgram(db, name, kind)
			}
		},
		Selected: func(id int64, db, name, kind string) {
			if s.activateObjectProfile(id) {
				s.selectObjectInNavigator(db, name, kind)
			}
		},
		FilterChanged: s.objectFilterChanged,
		LoadObjects:   s.loadMySQLObjects, CancelObjects: func() { s.sessions.CancelObjects() },
		RefreshDatabases: func(services ui.Services) { s.sessions.RefreshDatabases(services.Invalidate) },
		Changed:          s.mysqlTableChanged,
	}
}

func (s *State) disposeTab(t *tab) {
	if t == nil {
		return
	}
	s.dataWorkspace.Close(t.data)
	s.objectWorkspace.Close(t.object)
	s.sqlWorkspace.Close(t.query)
}
func (t *tab) profileID() int64 {
	if t.data != nil {
		return t.data.ProfileID()
	}
	if t.object != nil {
		return t.object.ProfileID()
	}
	if t.query != nil {
		return t.query.ProfileID()
	}
	return 0
}
func (t *tab) database() string {
	if t.data != nil {
		return t.data.Database()
	}
	if t.object != nil {
		return t.object.Database()
	}
	if t.query != nil {
		return t.query.Database()
	}
	return ""
}
func (s *State) closeActive() { s.closeTab(s.activeTab) }

// The index belongs to the current frame. Once requested, closing retains the
// document ID and applies its selection/status changes only after all guards.
func (s *State) closeTab(index int) {
	if index <= 0 || index >= len(s.tabs) {
		return
	}
	closing := s.tabs[index]
	wasActive := s.currentTab() == closing
	fallbackID := s.tabs[index-1].id
	if index+1 < len(s.tabs) {
		fallbackID = s.tabs[index+1].id
	}
	s.removeTabs(func(t *tab) bool { return t.id == closing.id }, func() {
		if wasActive {
			s.selectTab(fallbackID)
		}
		s.status = "已关闭页签 " + closing.title
	})
}

// Close other tabs only while the requested survivor still exists. A parked
// save must not turn a removed target into an instruction to close everything.
func (s *State) closeOtherTabs(index int) {
	if index < 0 || index >= len(s.tabs) {
		return
	}
	keep := s.tabs[index]
	s.removeTabs(func(t *tab) bool { return slices.Contains(s.tabs, keep) && t != keep }, func() {
		if s.selectTab(keep.id) {
			s.status = "已关闭其他页签"
		}
	})
}

func (s *State) selectTab(id uint64) bool {
	for i, t := range s.tabs {
		if t.id == id {
			s.activeTab = i
			return true
		}
	}
	return false
}

func (s *State) removeTabs(remove func(*tab) bool, after ...func()) {
	activeID := s.currentTab().id
	// A single continuation checks every matching draft again after each save or
	// discard. Failure/cancellation never runs disposal or completion callbacks.
	var finish func(ui.Services)
	finish = func(_ ui.Services) {
		for _, t := range s.tabs {
			if t.kind != tabObjects && remove(t) && s.sqlWorkspace.GuardLeave(t.query, finish) {
				return
			}
		}
		if s.guardTabsLeave(func(t *tab) bool { return t.kind != tabObjects && remove(t) }, finish) {
			return
		}
		kept := s.tabs[:0]
		for _, t := range s.tabs {
			if t.kind != tabObjects && remove(t) {
				s.disposeTab(t)
			} else {
				kept = append(kept, t)
			}
		}
		clear(s.tabs[len(kept):])
		s.tabs = kept
		s.activeTab = 0
		s.selectTab(activeID)
		for _, next := range after {
			next()
		}
	}
	finish(ui.Services{})
}

// documentIdentity includes both presentation purpose and qualified object type.
// SQL documents are intentionally never reused: each has its own text and lease.
type documentIdentity struct {
	catalog, schema            string
	profileID                  int64
	database, objectKind, name string
	view                       tabKind
}

func (t *tab) identity() documentIdentity {
	key := documentIdentity{profileID: t.profileID(), database: t.database(), catalog: t.database(), view: t.kind}
	var ref contract.ObjectRef
	if t.data != nil {
		ref = t.data.Object()
	}
	if t.object != nil {
		ref = t.object.Object()
	}
	if ref.ProfileID != 0 {
		key.catalog, key.schema = ref.Catalog, ref.Schema
	}
	if t.data != nil {
		key.name, key.objectKind = t.data.Table(), t.data.Kind()
	}
	if t.object != nil {
		key.name, key.objectKind = t.object.Name(), t.object.ObjectKind()
	}
	return key
}
func (s *State) findDocument(view tabKind, profileID int64, database, objectKind, name string) *tab {
	namespace := s.runtime.Namespace(profileID, database)
	if namespace.Catalog == "" && namespace.Schema == "" {
		namespace.Catalog = database
	}
	key := documentIdentity{profileID: profileID, database: database, catalog: namespace.Catalog, schema: namespace.Schema, objectKind: objectKind, name: name, view: view}
	for i, t := range s.tabs {
		if t.identity() == key {
			s.activeTab = i
			s.activateMySQLForCurrentTab()
			return t
		}
	}
	return nil
}
func (s *State) objectKindFor(id int64, database, name string) string {
	for _, obj := range s.sessions.CacheFor(id)[database] {
		if obj.Name == name && (obj.Kind == "表" || obj.Kind == "视图") {
			return obj.Kind
		}
	}
	return "表"
}
func (s *State) activateObjectProfile(id int64) bool {
	if id <= 0 || s.sessions.StatusFor(id) != "已连接" {
		return false
	}
	s.sessions.Activate(id)
	s.selectedProfileID = id
	return true
}

func (s *State) SetDataSources(open func(context.Context, datasources.Config) (datasources.Source, error), capabilities func(context.Context, datasources.Config) []datasources.Capability) {
	s.dataWorkspace.SetDataSources(open, capabilities)
}

func neutralObjectKind(kind string) string {
	if kind == "表" {
		return "table"
	}
	if kind == "视图" {
		return "view"
	}
	return kind
}
