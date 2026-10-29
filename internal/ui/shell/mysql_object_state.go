package shell

import (
	"context"
	"errors"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

type mysqlDatabaseNode struct {
	opened     bool
	open       bool
	tablesOpen bool
	viewsOpen  bool
}
type mysqlObjectResult struct {
	sessionGeneration uint64
	request           uint64
	database          string
	objects           []mysqlsvc.MySQLDBObject
	err               error
}
type mysqlObjectLister func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error)

// The UI thread owns this complete state. All jobs return immutable values
// through one-shot buffered channels and only invalidate the MyGo frame.
func (s *State) resetMySQLObjectBrowser() {
	s.clearMySQLDatabaseNavigation(s.mysqlSessionProfileID)
	s.mysqlObjectRequest++
	if s.mysqlObjectCancel != nil {
		s.mysqlObjectCancel()
	}
	s.mysqlObjectCancel = nil
	s.mysqlObjectResults = nil
	s.mysqlObjectPending = ""
	s.mysqlSelectedDatabase = ""
	s.mysqlObjectKind = "全部"
	s.mysqlObjectCache = make(map[string][]mysqlsvc.MySQLDBObject)
	s.mysqlObjectErrors = make(map[string]string)
	s.mysqlDatabaseNodes = make(map[string]*mysqlDatabaseNode)
	if navigation.Parse(s.navigation.Key).Database != "" {
		s.navigation.Key = navigation.ConnectionKey(s.selectedProfileID)
		s.navigation.Row = -1
	}
}

func (s *State) reconcileMySQLDatabases(names []string) {
	known := make(map[string]bool, len(names))
	for _, name := range names {
		known[name] = true
	}
	if s.mysqlObjectPending != "" && !known[s.mysqlObjectPending] {
		s.cancelMySQLObjectQuery()
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
	for key := range s.navigation.Tree.Open.All() {
		n := navigation.Parse(key)
		if n.ProfileID == s.mysqlSessionProfileID && n.Database != "" && !known[n.Database] {
			s.navigation.Tree.Open.Remove(key)
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
	// An explicitly cancelled or superseded read must not be rescheduled just
	// because its database remains expanded. Its retry/refresh action resumes it.
	if database := s.mysqlObjectPending; database != "" {
		if s.mysqlObjectErrors == nil {
			s.mysqlObjectErrors = make(map[string]string)
		}
		s.mysqlObjectErrors[database] = "加载已取消"
	}
	s.mysqlObjectRequest++
	if s.mysqlObjectCancel != nil {
		s.mysqlObjectCancel()
	}
	s.mysqlObjectCancel = nil
	s.mysqlObjectPending = ""
	s.mysqlObjectResults = nil
}

// Choosing a row changes its context without cancelling an explicitly opened
// sibling's read. That read can still populate its cache without selecting it.
func (s *State) selectMySQLDatabaseContext(database string) {
	if s.mysqlSelectedDatabase != database {
		s.mysqlSelectedDatabase = database
		// A different database is a different list; keep column layout but do
		// not let MyGo restore the previous database's row key or scroll anchor.
		s.objectState = ui.ListState{Columns: s.objectState.Columns}
		s.objectRow = -1
		s.mysqlObjectKind = "全部"
	}
}

func (s *State) selectMySQLDatabase(services ui.Services, database string) {
	s.openMySQLDatabase(services, database, false)
}

func (s *State) openMySQLDatabase(services ui.Services, database string, force bool) {
	s.selectMySQLDatabaseContext(database)
	if database == "" || s.mysqlSessionStatus != "已连接" {
		return
	}
	s.markMySQLDatabaseOpened(s.mysqlSessionProfileID, database)
	s.navigation.Tree.Open.Add(navigation.ConnectionKey(s.mysqlSessionProfileID))
	s.navigation.Tree.Open.Add(navigation.DatabaseNode(s.mysqlSessionProfileID, database).Key())
	s.loadMySQLObjects(services, database, force)
}

// Keep the selected object's qualified identity when it survives a filter.
// If it does not, select the visible category/database instead of leaving an
// inspector on an object that the central list no longer contains.
func (s *State) filterMySQLObjects(kind string) {
	if kind == s.mysqlObjectKind {
		return
	}
	selected := navigation.Parse(s.navigation.Key)
	s.mysqlObjectKind = kind
	s.objectState = ui.ListState{Columns: s.objectState.Columns}
	s.objectRow = -1
	if selected.Kind == "object" && selected.Database == s.mysqlSelectedDatabase &&
		(kind == "全部" || kind == selected.Category) {
		for i, obj := range s.mysqlSelectedObjects() {
			if obj.Name == selected.Object && obj.Kind == selected.Category {
				s.objectRow = i
				return
			}
		}
	}
	if s.mysqlSelectedDatabase != "" {
		node := navigation.DatabaseNode(s.mysqlSessionProfileID, s.mysqlSelectedDatabase)
		if kind != "全部" {
			node.Kind, node.Category = "category", kind
		}
		s.revealNavigationNode(node)
	}
}

func (s *State) loadMySQLObjects(services ui.Services, database string, force bool) {
	// The first metadata query may be scheduled before the database tree has
	// rendered (for example, when restoring an already-opened session).
	if s.mysqlObjectCache == nil {
		s.mysqlObjectCache = make(map[string][]mysqlsvc.MySQLDBObject)
	}
	if s.mysqlObjectErrors == nil {
		s.mysqlObjectErrors = make(map[string]string)
	}
	if database == "" || s.mysqlSessionStatus != "已连接" || s.mysqlSessionProfileID <= 0 {
		return
	}
	// Explicit metadata refreshes can also open a previously unopened database.
	// Preserve a previously opened database's collapsed state on refresh.
	if !s.mysqlDatabaseOpenedFor(s.mysqlSessionProfileID, database) {
		s.markMySQLDatabaseOpened(s.mysqlSessionProfileID, database)
		s.navigation.Tree.Open.Add(navigation.ConnectionKey(s.mysqlSessionProfileID))
		s.navigation.Tree.Open.Add(navigation.DatabaseNode(s.mysqlSessionProfileID, database).Key())
	}
	if _, ok := s.mysqlObjectCache[database]; ok && !force {
		return
	}
	if s.mysqlObjectPending == database && !force {
		return
	}
	if s.mysqlObjectErrors[database] != "" && !force {
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
		lister = s.mysqlSessionManager.ObjectsWithPrograms
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
		if result.database == s.mysqlSelectedDatabase {
			selected := navigation.Parse(s.navigation.Key)
			items := s.mysqlSelectedObjects()
			if selected.Kind == "object" && selected.Database == result.database {
				s.objectRow = -1
				for i, obj := range items {
					if obj.Name == selected.Object && obj.Kind == selected.Category {
						s.objectRow = i
						break
					}
				}
				if s.objectRow < 0 {
					s.navigation.Key = navigation.DatabaseNode(s.mysqlSessionProfileID, result.database).Key()
					s.navigation.Row = -1
				}
			} else if s.objectRow < 0 && len(items) > 0 {
				s.objectRow = 0
			}
		}
		delete(s.mysqlObjectErrors, result.database)
		s.resolveNavigatorObject()
	default:
	}
}

func (s *State) mysqlSelectedObjects() []mysqlsvc.MySQLDBObject {
	all := s.mysqlObjectCache[s.mysqlSelectedDatabase]
	if s.mysqlObjectKind == "" || s.mysqlObjectKind == "全部" {
		return all
	}
	items := make([]mysqlsvc.MySQLDBObject, 0, len(all))
	for _, object := range all {
		if object.Kind == s.mysqlObjectKind {
			items = append(items, object)
		}
	}
	return items
}
