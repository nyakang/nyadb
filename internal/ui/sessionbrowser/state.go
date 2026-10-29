// Package sessionbrowser owns UI-thread session workspaces and asynchronous
// database/object metadata jobs. Document and navigation actions stay with its host.
package sessionbrowser

import (
	"context"

	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

type SessionOpener func(context.Context, connections.ActiveSettings, string) error
type DatabaseLister func(context.Context, int64) ([]string, error)
type ObjectLister func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error)

type Dependencies struct {
	ReadSecret       func(int64, string) (string, bool, error)
	Status           func(string)
	Connected        func(int64)
	BrowserReset     func(int64)
	DatabasesChanged func(int64, map[string]bool)
	ObjectsChanged   func(int64, string)
	Disconnected     func(int64)
}

// Workspace contains one profile's session and metadata state. It is owned
// by the UI thread. Background jobs return immutable results through private
// buffered channels; they never retain a Workspace or write its fields.
type Workspace struct {
	ProfileID               int64
	Generation              uint64
	Status, Error, Password string
	Databases               []string
	Database                string
	Cache                   map[string][]mysqlsvc.MySQLDBObject
	Errors                  map[string]string
	Pending                 string
	opened                  map[string]bool
	sessionCancel           context.CancelFunc
	refreshCancel           context.CancelFunc
	sessionResults          <-chan sessionResult
	objectRequest           uint64
	objectCancel            context.CancelFunc
	objectResults           <-chan objectResult
}

func newWorkspace() *Workspace {
	w := &Workspace{Status: "未连接",
		Cache: make(map[string][]mysqlsvc.MySQLDBObject), Errors: make(map[string]string), opened: make(map[string]bool)}
	return w
}

func (w *Workspace) Refreshing() bool      { return w.sessionResults != nil }
func (w *Workspace) ObjectRequest() uint64 { return w.objectRequest }

// State keeps workspace identities stable instead of copying a parked profile's
// fields into one shared browser. Manager and loader functions can be injected;
// SQL/data/structure documents may use the same manager without owning its lifetime.
type State struct {
	Manager        *mysqlsvc.MySQLSessionManager
	Opener         SessionOpener
	DatabaseLister DatabaseLister
	ObjectLister   ObjectLister
	current        *Workspace
	parked         map[int64]*Workspace
	generation     uint64
	deps           Dependencies
}

func New(deps Dependencies) *State {
	return &State{current: newWorkspace(), parked: make(map[int64]*Workspace), deps: deps}
}
func (s *State) Current() *Workspace { return s.current }
func (s *State) Parked(id int64) *Workspace {
	if id == 0 || id == s.current.ProfileID {
		return nil
	}
	return s.parked[id]
}
func (s *State) Workspace(id int64) *Workspace {
	if id != 0 && id == s.current.ProfileID {
		return s.current
	}
	return s.Parked(id)
}
func (s *State) HasSessions() bool { return s.current.ProfileID != 0 || len(s.parked) != 0 }
func (s *State) StatusFor(id int64) string {
	if w := s.Workspace(id); w != nil && w.Status != "" {
		return w.Status
	}
	return "未连接"
}
func (s *State) DatabasesFor(id int64) []string {
	if w := s.Workspace(id); w != nil {
		return w.Databases
	}
	return nil
}
func (s *State) CacheFor(id int64) map[string][]mysqlsvc.MySQLDBObject {
	if w := s.Workspace(id); w != nil {
		return w.Cache
	}
	return nil
}
func (s *State) LoadStateFor(id int64, database string) (bool, string) {
	if w := s.Workspace(id); w != nil {
		return w.Pending == database, w.Errors[database]
	}
	return false, ""
}
func (s *State) DatabaseOpened(id int64, database string) bool {
	w := s.Workspace(id)
	return w != nil && w.opened[database]
}
func (s *State) MarkOpened(id int64, database string) {
	w := s.Workspace(id)
	if w == nil || w.Status != "已连接" || database == "" {
		return
	}
	if w.opened == nil {
		w.opened = make(map[string]bool)
	}
	w.opened[database] = true
}
func (s *State) EnsureManager() *mysqlsvc.MySQLSessionManager {
	if s.Manager == nil {
		s.Manager = mysqlsvc.NewMySQLSessionManager()
	}
	return s.Manager
}
func (s *State) report(message string) {
	if s.deps.Status != nil {
		s.deps.Status(message)
	}
}

// ResetInactive clears a disconnected profile's password draft when selection changes.
func (s *State) ResetInactive() {
	if s.current.ProfileID == 0 {
		s.current = newWorkspace()
	}
}
func (s *State) Activate(id int64) {
	if id <= 0 || id == s.current.ProfileID {
		return
	}
	if s.current.ProfileID == 0 && s.Parked(id) == nil {
		return
	}
	if s.current.ProfileID > 0 {
		s.parked[s.current.ProfileID] = s.current
	}
	if next := s.parked[id]; next != nil {
		delete(s.parked, id)
		s.current = next
		if next.Status == "已连接" && !next.Refreshing() && next.Error == "" {
			s.ReconcileDatabases(next.Databases)
		}
	} else {
		s.current = newWorkspace()
	}
}
func (s *State) bumpGeneration(w *Workspace) {
	s.generation = max(s.generation, w.Generation) + 1
	w.Generation = s.generation
}
