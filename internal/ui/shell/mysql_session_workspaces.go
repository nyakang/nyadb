package shell

import (
	"context"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// mysqlSessionWorkspace parks one profile's workbench-only state. The service
// manager already owns concurrent authenticated sessions keyed by ProfileID.
// A parked slot keeps its one-shot result channels and cancellation handles;
// nothing reads those results while a different profile is active.
type mysqlSessionWorkspace struct {
	sessionCancel     context.CancelFunc
	sessionResults    <-chan mysqlSessionResult
	sessionGeneration uint64
	sessionStatus     string
	sessionError      string
	sessionPassword   string
	databases         []string

	selectedDatabase  string
	objectKind        string
	objectCache       map[string][]connections.MySQLDBObject
	objectErrors      map[string]string
	objectPending     string
	objectRequest     uint64
	objectCancel      context.CancelFunc
	objectResults     <-chan mysqlObjectResult
	databaseNodes     map[string]*mysqlDatabaseNode
	objectRow         int
	objectState       ui.ListState
	navigateDatabase  string
	navigateTable     string
	navigateProfileID int64
}

func (s *State) snapshotCurrentMySQLWorkspace() *mysqlSessionWorkspace {
	return &mysqlSessionWorkspace{
		sessionCancel: s.mysqlSessionCancel, sessionResults: s.mysqlSessionResults,
		sessionGeneration: s.mysqlSessionGeneration, sessionStatus: s.mysqlSessionStatus,
		sessionError: s.mysqlSessionError, sessionPassword: s.mysqlSessionPassword,
		databases:        s.mysqlSessionDatabases,
		selectedDatabase: s.mysqlSelectedDatabase, objectKind: s.mysqlObjectKind,
		objectCache: s.mysqlObjectCache, objectErrors: s.mysqlObjectErrors,
		objectPending: s.mysqlObjectPending, objectRequest: s.mysqlObjectRequest,
		objectCancel: s.mysqlObjectCancel, objectResults: s.mysqlObjectResults,
		databaseNodes: s.mysqlDatabaseNodes, objectRow: s.objectRow,
		objectState: s.objectState, navigateDatabase: s.navigateDatabase,
		navigateTable: s.navigateTable, navigateProfileID: s.navigateProfileID,
	}
}

// restoreMySQLWorkspace changes only UI-owned fields. It does not connect,
// disconnect, query a database or close tabs belonging to another profile.
func (s *State) restoreMySQLWorkspace(id int64, slot *mysqlSessionWorkspace) {
	if slot == nil {
		slot = &mysqlSessionWorkspace{
			sessionStatus: "未连接", objectKind: "全部", objectRow: -1,
			objectCache:   make(map[string][]connections.MySQLDBObject),
			objectErrors:  make(map[string]string),
			databaseNodes: make(map[string]*mysqlDatabaseNode),
		}
		id = 0
	}
	s.mysqlSessionProfileID = id
	s.mysqlSessionCancel, s.mysqlSessionResults = slot.sessionCancel, slot.sessionResults
	s.mysqlSessionGeneration, s.mysqlSessionStatus = slot.sessionGeneration, slot.sessionStatus
	s.mysqlSessionError, s.mysqlSessionPassword = slot.sessionError, slot.sessionPassword
	s.mysqlSessionDatabases = slot.databases
	s.mysqlSelectedDatabase, s.mysqlObjectKind = slot.selectedDatabase, slot.objectKind
	s.mysqlObjectCache, s.mysqlObjectErrors = slot.objectCache, slot.objectErrors
	s.mysqlObjectPending, s.mysqlObjectRequest = slot.objectPending, slot.objectRequest
	s.mysqlObjectCancel, s.mysqlObjectResults = slot.objectCancel, slot.objectResults
	s.mysqlDatabaseNodes, s.objectRow, s.objectState = slot.databaseNodes, slot.objectRow, slot.objectState
	s.navigateDatabase, s.navigateTable, s.navigateProfileID = slot.navigateDatabase, slot.navigateTable, slot.navigateProfileID
}

// activateMySQLProfile makes the requested profile's slot current. A profile
// with no session receives an empty view; the previous connection is parked
// and remains live. Only explicit Connect/Disconnect starts network actions.
func (s *State) activateMySQLProfile(id int64) {
	if id <= 0 || id == s.mysqlSessionProfileID {
		return
	}
	// No authenticated session is active; retain the selected profile's
	// in-memory password draft until Connect consumes it. The selection
	// handler clears drafts when moving between two disconnected profiles.
	if s.mysqlSessionProfileID == 0 && s.parkedMySQLWorkspace(id) == nil {
		return
	}
	if s.mysqlWorkspaces == nil {
		s.mysqlWorkspaces = make(map[int64]*mysqlSessionWorkspace)
	}
	if active := s.mysqlSessionProfileID; active > 0 {
		s.mysqlWorkspaces[active] = s.snapshotCurrentMySQLWorkspace()
	}
	slot, ok := s.mysqlWorkspaces[id]
	if ok {
		delete(s.mysqlWorkspaces, id)
		s.restoreMySQLWorkspace(id, slot)
		// A database-list refresh may have completed while this slot was
		// parked. Prune only this profile's stale object caches and tabs.
		if slot.sessionStatus == "已连接" && slot.sessionResults == nil && slot.sessionError == "" {
			s.reconcileMySQLDatabases(slot.databases)
		}
	} else {
		s.restoreMySQLWorkspace(0, nil)
	}
}

func (s *State) parkedMySQLWorkspace(id int64) *mysqlSessionWorkspace {
	if id == 0 || id == s.mysqlSessionProfileID {
		return nil
	}
	return s.mysqlWorkspaces[id]
}

// The active tab owns the identity of its database session. Returning to a tab
// for a parked connection also restores that profile's browser state.
func (s *State) activateMySQLForCurrentTab() {
	t := s.currentTab()
	var id int64
	switch t.kind {
	case tabStructure:
		id = t.structureProfileID
	case tabMySQLData:
		id = t.dataProfileID
	case tabProgram:
		id = t.programProfileID
	case tabSQL:
		id = t.sqlProfileID
	}
	if id == 0 || id == s.mysqlSessionProfileID {
		return
	}
	s.activateMySQLProfile(id)
	s.selectedProfileID = id
	s.selectedGroup = ""
	s.explorerName = profileNode(id)
	s.explorerRow = -1
}

// Connected handshakes finish even when the user has selected another
// profile. Resolve the parked connection's status on the UI thread so the tree
// updates without requiring a second click on the profile.
func (s *State) consumeParkedMySQLSessionResults() {
	for id, slot := range s.mysqlWorkspaces {
		if slot.sessionResults == nil {
			continue
		}
		select {
		case result := <-slot.sessionResults:
			if result.id != id || result.generation != slot.sessionGeneration {
				continue
			}
			slot.sessionResults = nil
			if result.err != nil {
				slot.sessionStatus, slot.sessionError = "连接失败", result.err.Error()
				continue
			}
			slot.sessionStatus = "已连接"
			slot.sessionError = ""
			if result.metadataErr != nil {
				slot.sessionError = "数据库列表读取失败：" + result.metadataErr.Error()
				continue
			}
			slot.databases = result.names
			s.explorerTree.Open.Add(profileNode(id))
		default:
		}
	}
}

// Active and parked status lookups never expose one profile's state as another.
func (s *State) mysqlSessionStateForProfile(id int64) string {
	if id != 0 && id == s.mysqlSessionProfileID && s.mysqlSessionStatus != "" {
		return s.mysqlSessionStatus
	}
	if slot := s.parkedMySQLWorkspace(id); slot != nil && slot.sessionStatus != "" {
		return slot.sessionStatus
	}
	return "未连接"
}

func (s *State) mysqlSessionDatabasesFor(id int64) []string {
	if id != 0 && id == s.mysqlSessionProfileID {
		return s.mysqlSessionDatabases
	}
	if slot := s.parkedMySQLWorkspace(id); slot != nil {
		return slot.databases
	}
	return nil
}

func (s *State) mysqlObjectCacheFor(id int64) map[string][]connections.MySQLDBObject {
	if id != 0 && id == s.mysqlSessionProfileID {
		return s.mysqlObjectCache
	}
	if slot := s.parkedMySQLWorkspace(id); slot != nil {
		return slot.objectCache
	}
	return nil
}

func (s *State) mysqlObjectLoadStateFor(id int64, database string) (bool, string) {
	if id != 0 && id == s.mysqlSessionProfileID {
		return s.mysqlObjectPending == database, s.mysqlObjectErrors[database]
	}
	if slot := s.parkedMySQLWorkspace(id); slot != nil {
		return slot.objectPending == database, slot.objectErrors[database]
	}
	return false, ""
}

// Explicitly close one profile without destroying unrelated workbench slots.
func (s *State) disconnectMySQLProfile(id int64) {
	if id <= 0 {
		return
	}
	if id == s.mysqlSessionProfileID {
		s.disconnectMySQLSession()
		return
	}
	if slot := s.parkedMySQLWorkspace(id); slot != nil {
		if slot.sessionCancel != nil {
			slot.sessionCancel()
		}
		if slot.objectCancel != nil {
			slot.objectCancel()
		}
		delete(s.mysqlWorkspaces, id)
	}
	s.cancelMySQLSQLForProfile(id)
	if s.mysqlSessionManager != nil {
		_ = s.mysqlSessionManager.Disconnect(id)
	}
	s.clearMySQLTabsFor(id)
}

func (s *State) closeAllMySQLConnections() {
	ids := make([]int64, 0, len(s.mysqlWorkspaces))
	for id := range s.mysqlWorkspaces {
		ids = append(ids, id)
	}
	for _, id := range ids {
		s.disconnectMySQLProfile(id)
	}
	if s.mysqlSessionProfileID != 0 {
		s.disconnectMySQLSession()
	}
}
