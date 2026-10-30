package shell

import (
	"context"
	"github.com/nyakang/nyadb/internal/services/datasources"
	"slices"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/connections"
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
		Runtime:   func() sqlworkspace.Runtime { return sqlworkspace.MySQLRuntime(s.sessions.Manager) },
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
			if s.sessions.Manager == nil {
				return nil
			}
			return s.sessions.Manager.TableInfo
		},
		CountLoader: func() dataworkspace.CountLoader {
			if s.sessions.Manager == nil {
				return nil
			}
			return s.sessions.Manager.CountTableRows
		},
		ChunkLoader: func() dataworkspace.ChunkLoader {
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

func (s *State) dataWorkspaceDependencies() dataworkspace.Dependencies {
	return dataworkspace.Dependencies{
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
		Runtime:   func() dataworkspace.Runtime { return dataworkspace.MySQLRuntime(s.sessions.Manager) },
		Inspector: s.dataInspectorDependencies(), Status: func(message string) { s.status = message },
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
}

// All document leave checks pass through this gate. Continuations retain the
// original operation's identities while saves wait for server acknowledgement.
func (s *State) guardTabsLeave(matches func(*tab) bool, next func(ui.Services)) bool {
	if s.mysqlForceClose {
		return false
	}
	for _, t := range s.tabs {
		if matches(t) && (s.dataWorkspace.GuardTransactionLeave(t.data, next) || s.dataWorkspace.GuardLeave(t.data, next)) {
			return true
		}
	}
	return false
}
func (s *State) guardMySQLProfileLeave(id int64, next func(ui.Services)) bool {
	return s.guardTabsLeave(func(t *tab) bool { return id == 0 || t.profileID() == id }, next)
}
func (s *State) RequestWorkbenchClose(next func()) bool {
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
		object: s.objectWorkspace.OpenStructure(s.nextTabID, profileID, database, table, kind)}
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
		Loader: func() objectworkspace.Loader {
			if s.sessions.Manager == nil {
				return nil
			}
			return s.sessions.Manager.TableStructure
		},
		Runner: func() objectworkspace.TableRunner {
			if s.sessions.Manager == nil {
				return nil
			}
			return s.sessions.Manager.ChangeTable
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
	profileID                  int64
	database, objectKind, name string
	view                       tabKind
}

func (t *tab) identity() documentIdentity {
	key := documentIdentity{profileID: t.profileID(), database: t.database(), view: t.kind}
	if t.data != nil {
		key.name, key.objectKind = t.data.Table(), t.data.Kind()
	}
	if t.object != nil {
		key.name, key.objectKind = t.object.Name(), t.object.ObjectKind()
	}
	return key
}
func (s *State) findDocument(view tabKind, profileID int64, database, objectKind, name string) *tab {
	key := documentIdentity{profileID: profileID, database: database, objectKind: objectKind, name: name, view: view}
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
