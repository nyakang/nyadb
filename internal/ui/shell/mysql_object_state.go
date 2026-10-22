package shell

import (
	"context"
	"errors"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type mysqlDatabaseNode struct {
	open       bool
	tablesOpen bool
	viewsOpen  bool
}
type mysqlObjectResult struct {
	sessionGeneration uint64
	request           uint64
	database          string
	objects           []connections.MySQLDBObject
	err               error
}
type mysqlObjectLister func(context.Context, int64, string) ([]connections.MySQLDBObject, error)

// The UI thread owns this complete state. All jobs return immutable values
// through one-shot buffered channels and only invalidate the MyGo frame.
func (s *State) resetMySQLObjectBrowser() {
	s.mysqlObjectRequest++
	if s.mysqlObjectCancel != nil {
		s.mysqlObjectCancel()
	}
	s.mysqlObjectCancel = nil
	s.mysqlObjectResults = nil
	s.mysqlObjectPending = ""
	s.mysqlSelectedDatabase = ""
	s.mysqlObjectKind = "全部"
	s.mysqlObjectCache = make(map[string][]connections.MySQLDBObject)
	s.mysqlObjectErrors = make(map[string]string)
	s.mysqlDatabaseNodes = make(map[string]*mysqlDatabaseNode)
}

func (s *State) reconcileMySQLDatabases(names []string) {
	known := make(map[string]bool, len(names))
	for _, name := range names {
		known[name] = true
	}
	for name := range s.mysqlObjectCache {
		if !known[name] {
			delete(s.mysqlObjectCache, name)
		}
	}
	for name := range s.mysqlObjectErrors {
		if !known[name] {
			delete(s.mysqlObjectErrors, name)
		}
	}
	for name := range s.mysqlDatabaseNodes {
		if !known[name] {
			delete(s.mysqlDatabaseNodes, name)
		}
	}
	s.pruneMySQLStructureTabs(known)
	if !known[s.mysqlSelectedDatabase] {
		s.cancelMySQLObjectQuery()
		s.mysqlSelectedDatabase = ""
		s.mysqlObjectKind = "全部"
	}
}

func (s *State) cancelMySQLObjectQuery() {
	s.mysqlObjectRequest++
	if s.mysqlObjectCancel != nil {
		s.mysqlObjectCancel()
	}
	s.mysqlObjectCancel = nil
	s.mysqlObjectPending = ""
	s.mysqlObjectResults = nil
}

func (s *State) selectMySQLDatabase(services ui.Services, database string) {
	if s.mysqlSelectedDatabase != database {
		s.cancelMySQLObjectQuery()
		s.mysqlSelectedDatabase = database
		s.mysqlObjectKind = "全部"
	}
	if database == "" || s.mysqlSessionStatus != "已连接" {
		return
	}
	s.loadMySQLObjects(services, database, false)
}

func (s *State) loadMySQLObjects(services ui.Services, database string, force bool) {
	// The first metadata query may be scheduled before the database tree has
	// rendered (for example, when restoring an already-opened session).
	if s.mysqlObjectCache == nil {
		s.mysqlObjectCache = make(map[string][]connections.MySQLDBObject)
	}
	if s.mysqlObjectErrors == nil {
		s.mysqlObjectErrors = make(map[string]string)
	}
	if database == "" || s.mysqlSessionStatus != "已连接" || s.mysqlSessionProfileID <= 0 {
		return
	}
	if _, ok := s.mysqlObjectCache[database]; ok && !force {
		return
	}
	if s.mysqlObjectPending == database && !force {
		return
	}
	s.cancelMySQLObjectQuery()
	if force {
		delete(s.mysqlObjectCache, database)
	}
	delete(s.mysqlObjectErrors, database)
	ctx, cancel := context.WithCancel(context.Background())
	s.mysqlObjectCancel = cancel
	s.mysqlObjectRequest++
	request := s.mysqlObjectRequest
	generation := s.mysqlSessionGeneration
	id := s.mysqlSessionProfileID
	s.mysqlObjectPending = database
	results := make(chan mysqlObjectResult, 1)
	s.mysqlObjectResults = results
	lister := s.mysqlObjectLister
	if lister == nil {
		if s.mysqlSessionManager == nil {
			s.cancelMySQLObjectQuery()
			return
		}
		lister = s.mysqlSessionManager.Objects
	}
	go func() {
		objects, err := lister(ctx, id, database)
		// Even if a canceled lister returns a success, it cannot publish
		// into the new selection: the UI checks both request and generation.
		results <- mysqlObjectResult{sessionGeneration: generation, request: request, database: database, objects: objects, err: err}
		services.Invalidate()
	}()
}

func (s *State) consumeMySQLObjects() {
	if s.mysqlObjectResults == nil {
		return
	}
	select {
	case result := <-s.mysqlObjectResults:
		if result.sessionGeneration != s.mysqlSessionGeneration || result.request != s.mysqlObjectRequest ||
			result.database != s.mysqlObjectPending {
			return
		}
		s.mysqlObjectResults = nil
		s.mysqlObjectPending = ""
		if s.mysqlObjectCancel != nil {
			s.mysqlObjectCancel()
		}
		s.mysqlObjectCancel = nil
		if result.err != nil {
			if !errors.Is(result.err, context.Canceled) {
				s.mysqlObjectErrors[result.database] = result.err.Error()
			}
			return
		}
		s.mysqlObjectCache[result.database] = result.objects
		delete(s.mysqlObjectErrors, result.database)
	default:
	}
}

func (s *State) mysqlSelectedObjects() []connections.MySQLDBObject {
	all := s.mysqlObjectCache[s.mysqlSelectedDatabase]
	if s.mysqlObjectKind == "" || s.mysqlObjectKind == "全部" {
		return all
	}
	items := make([]connections.MySQLDBObject, 0, len(all))
	for _, object := range all {
		if object.Kind == s.mysqlObjectKind {
			items = append(items, object)
		}
	}
	return items
}

func (s *State) mysqlDatabaseTree(c *ui.Context) {
	if s.mysqlSessionStateFor(s.selectedProfileID) != "已连接" {
		return
	}
	if s.mysqlDatabaseNodes == nil {
		s.mysqlDatabaseNodes = make(map[string]*mysqlDatabaseNode)
	}
	ui.Column(c).Padding(3, 0).Gap(0).Children(func() {
		ui.Text(c, "数据库对象").FontSize(11).Padding(4, 8)
		ui.Tree(c, func() {
			for _, database := range s.mysqlSessionDatabases {
				db := database
				node := s.mysqlDatabaseNodes[db]
				if node == nil {
					node = &mysqlDatabaseNode{}
					s.mysqlDatabaseNodes[db] = node
				}
				before := node.open
				item := ui.TreeItem(c, db, &node.open, func() {
					if s.mysqlObjectPending == db {
						ui.TreeItem(c, "正在加载…", nil, nil)
					} else if err := s.mysqlObjectErrors[db]; err != "" {
						ui.TreeItem(c, "加载失败（点击右侧刷新）", nil, nil)
					} else if objs, ok := s.mysqlObjectCache[db]; ok {
						for _, kind := range []string{"表", "视图"} {
							k := kind
							open := &node.tablesOpen
							if k == "视图" {
								open = &node.viewsOpen
							}
							category := ui.TreeItem(c, k, open, func() {
								shown := 0
								for _, obj := range objs {
									if obj.Kind != k {
										continue
									}
									if shown >= 100 {
										ui.TreeItem(c, "更多对象请在右侧列表查看…", nil, nil)
										break
									}
									shown++
									leaf := ui.TreeItem(c, obj.Name, nil, nil)
									if leaf.Clicked() {
										s.mysqlSelectedDatabase = db
										s.mysqlObjectKind = k
									}
									if leaf.DoubleClicked() {
										s.openMySQLStructure(c.Services(), db, obj.Name)
									}
									leaf.ContextMenu(func(m *ui.Menu) {
										if m.Item("打开只读结构：" + obj.Name).Chosen() {
											s.openMySQLStructure(c.Services(), db, obj.Name)
										}
									})
								}
							})
							if category.Clicked() {
								s.mysqlSelectedDatabase = db
								s.mysqlObjectKind = k
							}
						}
					}
				})
				if item.Clicked() {
					node.open = true
					s.selectMySQLDatabase(c.Services(), db)
				} else if node.open && !before {
					s.selectMySQLDatabase(c.Services(), db)
				}
			}
		})
	})
}
