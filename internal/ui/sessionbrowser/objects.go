package sessionbrowser

import (
	"context"
	"errors"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

type objectResult struct {
	sessionGeneration, request uint64
	database                   string
	objects                    []mysqlsvc.MySQLDBObject
	err                        error
}

func (s *State) ResetBrowser() {
	w := s.current
	w.objectRequest++
	if w.objectCancel != nil {
		w.objectCancel()
	}
	w.objectCancel, w.objectResults, w.Pending = nil, nil, ""
	w.Database = ""
	w.Cache = make(map[string][]mysqlsvc.MySQLDBObject)
	w.Errors = make(map[string]string)
	w.opened = make(map[string]bool)
	if s.deps.BrowserReset != nil {
		s.deps.BrowserReset(w.ProfileID)
	}
}
func (s *State) ReconcileDatabases(names []string) {
	w := s.current
	known := make(map[string]bool, len(names))
	for _, name := range names {
		known[name] = true
	}
	if w.Pending != "" && !known[w.Pending] {
		s.CancelObjects()
	}
	for name := range w.Cache {
		if !known[name] {
			delete(w.Cache, name)
		}
	}
	for name := range w.Errors {
		if !known[name] {
			delete(w.Errors, name)
		}
	}
	for name := range w.opened {
		if !known[name] {
			delete(w.opened, name)
		}
	}
	if s.deps.DatabasesChanged != nil {
		s.deps.DatabasesChanged(w.ProfileID, known)
	}
	if !known[w.Database] {
		// A selected/removed row is independent of a known sibling's read.
		w.Database = ""
	}
}
func (s *State) CancelObjects() {
	s.cancelObjects(s.current)
}

func (s *State) cancelObjects(w *Workspace) {
	if w.Pending != "" {
		if w.Errors == nil {
			w.Errors = make(map[string]string)
		}
		w.Errors[w.Pending] = "加载已取消"
	}
	w.objectRequest++
	if w.objectCancel != nil {
		w.objectCancel()
	}
	w.objectCancel, w.objectResults, w.Pending = nil, nil, ""
}

// InvalidateDatabaseObjects revokes metadata for the original session after a
// table change. A parked profile's sibling read and database selection survive.
// Invalidation never starts a read; the host decides when to refresh.
func (s *State) InvalidateDatabaseObjects(profileID int64, generation uint64, database string) bool {
	w := s.Workspace(profileID)
	if w == nil || w.Status != "已连接" || w.Generation != generation || database == "" {
		return false
	}
	if w.Pending == database {
		s.cancelObjects(w)
	}
	delete(w.Cache, database)
	delete(w.Errors, database)
	return true
}

// InvalidateProfileObjects clears a connection's object caches while retaining
// its selected/opened databases. Only this session's pending read is revoked.
func (s *State) InvalidateProfileObjects(profileID int64, generation uint64) bool {
	w := s.Workspace(profileID)
	if w == nil || w.Status != "已连接" || w.Generation != generation {
		return false
	}
	s.cancelObjects(w)
	clear(w.Cache)
	clear(w.Errors)
	return true
}

// SelectDatabase changes context without canceling an explicitly opened sibling's read.
func (s *State) SelectDatabase(database string) {
	w := s.current
	if w.Database == database {
		return
	}
	w.Database = database
}

// LoadObjects opens a database once, honors cache/error state and permits an
// explicit force refresh. The host owns disclosure and central-list selection.
func (s *State) LoadObjects(database string, force bool, invalidate func()) {
	w := s.current
	if w.Cache == nil {
		w.Cache = make(map[string][]mysqlsvc.MySQLDBObject)
	}
	if w.Errors == nil {
		w.Errors = make(map[string]string)
	}
	if database == "" || w.Status != "已连接" || w.ProfileID <= 0 {
		return
	}
	s.MarkOpened(w.ProfileID, database)
	if _, ok := w.Cache[database]; ok && !force {
		return
	}
	if w.Pending == database && !force {
		return
	}
	if w.Errors[database] != "" && !force {
		return
	}
	s.CancelObjects()
	if force {
		delete(w.Cache, database)
	}
	delete(w.Errors, database)
	ctx, cancel := context.WithCancel(context.Background())
	w.objectCancel = cancel
	w.objectRequest++
	request, generation, id := w.objectRequest, w.Generation, w.ProfileID
	w.Pending = database
	results := make(chan objectResult, 1)
	w.objectResults = results
	lister := s.ObjectLister
	if lister == nil && w.Kind != "mysql" && s.engines.Session != nil {
		lister = s.engineObjects
	}
	if lister == nil {
		if s.Manager == nil {
			s.CancelObjects()
			return
		}
		lister = s.Manager.ObjectsWithPrograms
	}
	go func() {
		objects, err := lister(ctx, id, database)
		results <- objectResult{sessionGeneration: generation, request: request, database: database, objects: objects, err: err}
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) consumeObjects() {
	w := s.current
	if w.objectResults == nil {
		return
	}
	select {
	case result := <-w.objectResults:
		if result.sessionGeneration != w.Generation || result.request != w.objectRequest || result.database != w.Pending {
			return
		}
		w.objectResults, w.Pending = nil, ""
		if w.objectCancel != nil {
			w.objectCancel()
		}
		w.objectCancel = nil
		if result.err != nil {
			if !errors.Is(result.err, context.Canceled) {
				w.Errors[result.database] = result.err.Error()
			}
			return
		}
		w.Cache[result.database] = result.objects
		delete(w.Errors, result.database)
		if s.deps.ObjectsChanged != nil {
			s.deps.ObjectsChanged(w.ProfileID, result.database)
		}
	default:
	}
}

// LoadExpanded drains explicitly opened, expanded databases in order, with one
// metadata request per active profile. Rendering never retries an error by itself.
func (s *State) LoadExpanded(expanded func(string) bool, invalidate func()) {
	w := s.current
	if w.Status != "已连接" {
		return
	}
	for _, database := range w.Databases {
		if !s.DatabaseOpened(w.ProfileID, database) || !expanded(database) || w.Pending != "" || w.Errors[database] != "" {
			continue
		}
		if _, loaded := w.Cache[database]; !loaded {
			s.LoadObjects(database, false, invalidate)
		}
	}
}
