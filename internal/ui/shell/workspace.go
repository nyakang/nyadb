package shell

import (
	"context"
	"slices"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/datasources"
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
							item.Background(palette.Selection).TextColor(palette.SelectionText).BorderWidth(1, 1, 0, 1).BorderColor(palette.Border)
							if current.kind == tabObjects {
								item.Background(palette.Header).TextColor(palette.Text)
							}
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
								theme.Icon(c, icon, 14, theme.ProfileColor(c, s.profileColor(current.profileID())))
								ui.Text(c, s.workspaceTabCaption(current)).FontSize(theme.Font(c, theme.FontSize)).SingleLine().MaxWidth(220).Tooltip(s.workspaceTabTooltip(current))
								if current.kind != tabObjects {
									if ui.ButtonBase(c).Label("关闭 "+current.title).Tooltip("关闭 "+current.title).
										Size(16, 18).Children(func() { theme.Icon(c, "close", 10, palette.Muted) }).Clicked() {
										s.closeTabID(current.id)
									}
								}
							})
						})
						if index > 0 {
							item.Drag(tabDrag{Owner: s, ID: current.id})
						}
						if dragged, ok := ui.Drop[tabDrag](item); ok && dragged.Owner == s {
							s.moveTab(dragged.ID, current.id)
						}
						if item.Clicked() {
							s.selectTab(current.id)
						}
						item.ContextMenu(func(m *ui.Menu) {
							if m.Item(locales.Text("关闭 ") + current.title).Disabled(index == 0).Chosen() {
								s.closeTabID(current.id)
							}
							if m.Item(locales.Text("关闭其他页签")).Disabled(len(s.tabs) <= 1 || (len(s.tabs) == 2 && index != 0)).Chosen() {
								s.closeOtherTabs(s.tabIndex(current.id))
							}
							label := "固定页签"
							if current.pinned {
								label = "取消固定"
							}
							if m.Item(locales.Text(label)).Disabled(current.kind == tabObjects).Chosen() {
								s.pinTab(current.id)
							}
							if m.Item(locales.Text("关闭右侧页签")).Chosen() {
								s.closeRight(current.id)
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
		if current := s.currentTab(); current.placeholder != nil {
			s.restoredDocument(c, current)
			return
		}
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
	return sqlworkspace.Dependencies{Namespace: s.sqlNamespace, Profiles: func() []sqlworkspace.ProfileChoice {
		var out []sqlworkspace.ProfileChoice
		for _, p := range s.navigation.Profiles() {
			out = append(out, sqlworkspace.ProfileChoice{ID: p.ID, Name: p.Name})
		}
		return out
	}, ConnectProfile: func(services ui.Services, id int64) {
		s.connectWorkspaceProfile(services, id)
	}, EditFocus: &s.editFocus, BuildExplain: s.sqlBuildExplain, PlanParser: s.sqlPlanParser, CompletionSource: s.sqlCompletionSource, ProfileName: s.profileName, ProfileColor: s.profileColor,
		EditTarget: s.registerEditTarget,
		AIProposal: s.openAIProposal,
		AIDialect: func(id int64) string {
			for _, profile := range s.navigation.Profiles() {
				if profile.ID == id {
					if descriptor, ok := connections.Describe(profile.Kind); ok {
						return descriptor.Dialect
					}
				}
			}
			return "SQL"
		},
		FavoriteAsset: s.favoriteQuery, CanFavorite: s.favorites.Available,
		AssetsChanged:    s.sqlAssetsChanged,
		StatementAtCaret: s.sqlStatementAtCaret, FormatSQL: s.sqlFormat, QueryDialect: s.sqlPlanEngine,
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
	return dataworkspace.Dependencies{NamespaceLabel: func(n contract.NamespaceRef) string { return s.sessions.NamespaceLabel(n) }, EditFocus: &s.editFocus,
		EditTarget:     s.registerEditTarget,
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
		CloseDocument: func(_ ui.Services, d *dataworkspace.Document) {
			for _, t := range s.tabs {
				if t.data == d {
					s.closeTabID(t.id)
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

func (s *State) openMySQLStructure(services ui.Services, database, table string, design ...bool) {
	profileID := s.sessions.Current().ProfileID
	if s.sessions.Current().Status != "已连接" || profileID <= 0 || !validStructureSelection(database, table) {
		return
	}
	kind := s.objectKindFor(profileID, database, table)
	if kind == "任务" {
		s.openTasks(services, profileID, database)
		return
	}
	if t := s.findDocument(tabStructure, profileID, database, kind, table); t != nil {
		if len(design) > 0 && design[0] && s.objectWorkspace.CanEditDesign(t.object) {
			if !t.object.DesignMode() {
				s.objectWorkspace.BeginDesign(t.object, services)
			}
		} else {
			s.objectWorkspace.Load(t.object, services.Invalidate, false)
		}
		return
	}
	s.nextTabID++
	t := &tab{id: s.nextTabID, kind: tabStructure, title: database + "." + table,
		object: s.objectWorkspace.OpenQualifiedStructure(s.nextTabID, s.runtime.Namespace(profileID, database).Object(neutralObjectKind(kind), table), kind)}
	s.tabs = append(s.tabs, t)
	s.activeTab = len(s.tabs) - 1
	if len(design) > 0 && design[0] && s.objectWorkspace.CanEditDesign(t.object) {
		if !t.object.DesignMode() {
			s.objectWorkspace.BeginDesign(t.object, services)
		}
	} else {
		s.objectWorkspace.Load(t.object, services.Invalidate, false)
	}
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
		global := t.object != nil && t.object.Object().Catalog == "" && (t.object.Object().Kind == "tasks" || t.object.KeysMode() || t.object.CollectionManagementMode())
		return (t.data != nil || t.object != nil) && t.profileID() == id && !global && !known[t.database()]
	})
}

func restoreIncludesProfile(plan contract.BackupPlan, profile int64) bool {
	for _, affected := range plan.Affected {
		if profile == affected.ProfileID {
			return true
		}
	}
	return plan.QuiesceFile == "" && profile == plan.Request.Target.Object.ProfileID
}

func (s *State) objectWorkspaceDependencies() objectworkspace.Dependencies {
	return objectworkspace.Dependencies{BeforeNamespace: s.guardNamespaceManagement, NamespaceChanged: s.namespaceChanged, NamespaceLabel: func(n contract.NamespaceRef) string { return s.sessions.NamespaceLabel(n) }, ProfileName: s.profileName, EditFocus: &s.editFocus, EditTarget: s.registerEditTarget, Engine: func(id int64) string {
		for _, p := range s.navigation.Profiles() {
			if p.ID == id {
				return string(p.Kind)
			}
		}
		return "mysql"
	},
		FieldTypes: func(id int64) []string {
			for _, p := range s.navigation.Profiles() {
				if p.ID == id {
					descriptor, _ := connections.Describe(p.Kind)
					return descriptor.FieldTypes()
				}
			}
			return nil
		},
		OpenDesign: func(services ui.Services, id int64, db, table string) {
			if s.activateObjectProfile(id) {
				s.openMySQLStructure(services, db, table, true)
			}
		},
		BackupsFor: func(id int64) contract.BackupOperations {
			if s.runtime == nil {
				return contract.BackupOperations{}
			}
			return s.runtime.Backups(id)
		},
		BeforeRestore: func(services ui.Services, plan contract.BackupPlan, next func(ui.Services)) bool {
			// Re-run the existing leave guards after every asynchronous decision,
			// until every affected data transaction and draft has been settled.
			var guard func(ui.Services)
			matches := func(t *tab) bool {
				return restoreIncludesProfile(plan, t.profileID())
			}
			guard = func(services ui.Services) {
				if !s.guardTabsLeave(matches, guard) {
					next(services)
				}
			}
			return s.guardTabsLeave(matches, guard)
		},
		RestoreGuardPending: func(plan contract.BackupPlan) bool {
			for _, t := range s.tabs {
				if restoreIncludesProfile(plan, t.profileID()) && (t.data != nil && t.data.LeavePending() || t.object != nil && t.object.DesignLeavePending()) {
					return true
				}
			}
			return false
		},
		BackupCompleted: func(services ui.Services, event objectworkspace.BackupEvent) {
			for _, drained := range event.Outcome.Drained {
				if s.runtime != nil && s.runtime.Generation(drained.ProfileID) == 0 {
					s.sessions.Disconnect(drained.ProfileID)
				}
			}
			if s.runtime != nil && event.Outcome.Path == event.Original.Object.Catalog &&
				(event.Outcome.State == "completed" || event.Outcome.State == "uncertain") &&
				s.runtime.Generation(event.Original.Object.ProfileID) == event.Original.Generation {
				// A PG overwrite can change every table/view in the archive. Retire
				// inspected/data snapshots after the leave guards have settled them.
				if s.sessions.InvalidateProfileObjects(event.Original.Object.ProfileID, event.Generation) {
					s.clearMySQLTabsFor(event.Original.Object.ProfileID)
				}
			}
			services.Invalidate()
		},
		MaintenanceFor: func(id int64) contract.MaintenanceOperations {
			if s.runtime == nil {
				return contract.MaintenanceOperations{}
			}
			return s.runtime.Maintenance(id)
		},
		Availability: func(id int64, operation string, target contract.SchemaDesignTarget) contract.Availability {
			if s.runtime == nil {
				return contract.Availability{Reason: "disconnected"}
			}
			return s.runtime.Availability(id, operation, target)
		},
		Maintained: func(services ui.Services, event objectworkspace.MaintenanceEvent) {
			if s.runtime == nil || s.runtime.Generation(event.Original.Object.ProfileID) != event.Original.Generation {
				return
			}
			if event.Outcome.State == "completed" || event.Outcome.State == "uncertain" {
				s.sessions.InvalidateDatabaseObjects(event.Original.Object.ProfileID, event.Generation, event.Database)
				services.Invalidate()
			}
		},
		Namespace: func(id int64, database string) contract.NamespaceRef {
			if s.runtime == nil {
				return contract.NamespaceRef{}
			}
			return s.runtime.Namespace(id, database)
		},
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
		SchemaDropped:  s.schemaTableDropped,
		SchemaCopied:   s.schemaTableCopied,
		SchemaRenamed:  s.schemaTableRenamed,
		BeforeRename:   s.guardSchemaTableRename,
		ViewDesignFor: func(id int64) contract.ViewDesignOperations {
			if s.runtime == nil {
				return contract.ViewDesignOperations{}
			}
			return s.runtime.ViewDesign(id)
		},
		ViewTarget: func(id int64, database, name string) contract.SchemaDesignTarget {
			if s.runtime == nil {
				return contract.SchemaDesignTarget{}
			}
			return contract.SchemaDesignTarget{Object: s.runtime.Namespace(id, database).Object("view", name), Generation: s.runtime.Generation(id)}
		},
		ViewChanged: s.nativeViewChanged,
		TableGuardPending: func(id int64, db, table string) bool {
			for _, t := range s.tabs {
				if t.data != nil && t.data.ProfileID() == id && t.data.Database() == db && t.data.Table() == table && t.data.LeavePending() ||
					t.object != nil && t.object.ProfileID() == id && t.object.Database() == db && t.object.Table() == table && t.object.DesignLeavePending() {
					return true
				}
			}
			return false
		},
		BeforeCopy: func(services ui.Services, id int64, db, table string, next func(ui.Services)) bool {
			matches := func(t *tab) bool {
				return t.data != nil && t.data.ProfileID() == id && t.data.Database() == db && t.data.Table() == table ||
					t.object != nil && t.object.ProfileID() == id && t.object.Database() == db && t.object.Table() == table
			}
			var finish func(ui.Services)
			finish = func(services ui.Services) {
				if !s.guardTabsLeave(matches, finish) {
					next(services)
				}
			}
			return s.guardTabsLeave(matches, finish)
		},
		BeforeDrop: func(services ui.Services, id int64, db, table string, next func(ui.Services)) bool {
			matches := func(t *tab) bool {
				return t.data != nil && t.data.ProfileID() == id && t.data.Database() == db && t.data.Table() == table ||
					t.object != nil && t.object.ProfileID() == id && t.object.Database() == db && t.object.Table() == table
			}
			var finish func(ui.Services)
			finish = func(services ui.Services) {
				if !s.guardTabsLeave(matches, finish) {
					next(services)
				}
			}
			return s.guardTabsLeave(matches, finish)
		},
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
		Loader:      func() objectworkspace.Loader { return s.runtime.Data(s.sessions.Current().ProfileID).TableMetadata },
		MetadataFor: func(id int64) (contract.MetadataProvider, error) { return s.runtime.Metadata(id) },
		SnapshotManagementFor: func(id int64) (contract.SnapshotProvider, error) {
			if s.runtime == nil {
				return nil, contract.ErrUnsupported
			}
			return s.runtime.Snapshots(id)
		},
		BeforeSnapshots:  s.guardSnapshotManagement,
		SnapshotsChanged: s.snapshotsChanged,
		CollectionManagementFor: func(id int64) (contract.CollectionManagementProvider, error) {
			if s.runtime == nil {
				return nil, contract.ErrUnsupported
			}
			return s.runtime.CollectionManagement(id)
		},
		BeforeCollections:  s.guardCollectionManagement,
		CollectionsChanged: s.collectionsChanged,
		SettingsDocumentsFor: func(id int64) (contract.SettingsDocumentsProvider, error) {
			if s.runtime == nil {
				return nil, contract.ErrUnsupported
			}
			return s.runtime.SettingsDocuments(id)
		},
		SettingsChanged: s.settingsChanged,
		KeysDocumentsFor: func(id int64) (contract.KeysDocumentsProvider, error) {
			if s.runtime == nil {
				return nil, contract.ErrUnsupported
			}
			return s.runtime.KeysDocuments(id)
		},
		DocumentActive: func(id uint64) bool { return s.currentTab().id == id },
		TaskDocumentsFor: func(id int64) (contract.TaskDocumentsProvider, error) {
			if s.runtime == nil {
				return nil, contract.ErrUnsupported
			}
			return s.runtime.TaskDocuments(id)
		},
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
	if t.placeholder != nil {
		return t.placeholder.ProfileID
	}
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
	if t.placeholder != nil {
		return t.placeholder.Database
	}
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
	s.removeTabs(func(t *tab) bool { return slices.Contains(s.tabs, keep) && t != keep && !t.pinned }, func() {
		if s.selectTab(keep.id) {
			s.status = "已关闭其他页签"
		}
	})
}

func (s *State) selectTab(id uint64) bool {
	for i, t := range s.tabs {
		if t.id == id {
			s.activeTab = i
			s.tabFocus(t)
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
	if t.placeholder != nil {
		v := t.placeholder
		key.catalog, key.schema, key.objectKind, key.name = v.Catalog, v.Schema, v.ObjectKind, v.ObjectName
		return key
	}
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
		if obj.Name == name && (obj.Kind == "向量点" || obj.Kind == "文档" || obj.Kind == "Core" || obj.Kind == "表" || obj.Kind == "视图" || obj.Kind == "集合" || obj.Kind == "键" || obj.Kind == "索引" || obj.Kind == "别名" || obj.Kind == "数据流" || obj.Kind == "任务" || objectworkspace.NativeDefinitionKind(obj.Kind)) {
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

// Recovered documents start disconnected. Activating an existing profile for
// connection must remain distinct from activating it for object reads.
func (s *State) connectWorkspaceProfile(services ui.Services, id int64) {
	for _, profile := range s.navigation.Profiles() {
		if profile.ID == id {
			s.sessions.Activate(id)
			s.selectedProfileID = id
			s.startMySQLSession(services)
			return
		}
	}
}

func (s *State) SetDataSources(open func(context.Context, datasources.Config) (datasources.Source, error), capabilities func(context.Context, datasources.Config) []datasources.Capability) {
	s.dataWorkspace.SetDataSources(open, capabilities)
}

func neutralObjectKind(kind string) string {
	switch kind {
	case "快照管理":
		return "snapshot_management"
	case "集合信息":
		return "collection"
	case "集合别名":
		return "aliases"
	case "向量点":
		return "points"
	case "API 密钥":
		return "keys"
	case "集合与别名管理":
		return "collection_management"
	case "任务":
		return "tasks"
	case "文档":
		return "documents"
	case "索引信息":
		return "index"
	case "索引设置":
		return "settings"
	case "索引统计":
		return "statistics"
	case "数据库信息":
		return "database"
	case "设计文档":
		return "design_document"
	case "Core":
		return "core"
	case "索引":
		return "index"
	case "别名":
		return "alias"
	case "数据流":
		return "data_stream"
	}
	if kind == "键" {
		return "keyspace"
	}
	if kind == "集合" {
		return "collection"
	}
	if kind == "表" {
		return "table"
	}
	if kind == "视图" {
		return "view"
	}
	return kind
}
