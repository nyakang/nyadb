package shell

import (
	"context"
	"errors"
	"slices"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/locales"
)

type initialSQLCleanupState struct {
	store          *queries.Store
	candidates     map[string]bool
	retry, waiting bool
	err            error
}

func profileInitialSQLIDs(p connections.Profile) []string {
	var ids []string
	if p.MySQLAdvanced.InitialSQLID != "" {
		ids = append(ids, p.MySQLAdvanced.InitialSQLID)
	}
	for _, v := range p.Variants {
		if v.MySQLAdvanced.InitialSQLID != "" {
			ids = append(ids, v.MySQLAdvanced.InitialSQLID)
		}
	}
	return ids
}

// Only operation-produced candidates enter this queue. It never scans or
// deletes historical orphans. Failures remain queued for an explicit retry or
// the next successful/failed save or deletion that supplies candidates.
func (s *State) queueInitialSQLCleanup(ids []string) error {
	q := &s.initialSQLCleanup
	if len(ids) == 0 {
		return nil
	}
	if q.candidates == nil {
		q.candidates = map[string]bool{}
	}
	for _, id := range ids {
		if id != "" {
			q.candidates[id] = true
		}
	}
	q.retry = true
	return s.consumeInitialSQLCleanup()
}

func (s *State) RetryInitialSQLCleanup() error {
	s.initialSQLCleanup.retry = true
	err := s.consumeInitialSQLCleanup()
	if err == nil && len(s.initialSQLCleanup.candidates) == 0 {
		s.status = locales.Text("初始 SQL 清理完成")
	}
	return err
}

func (s *State) consumeInitialSQLCleanup() error {
	q := &s.initialSQLCleanup
	if len(q.candidates) == 0 || !q.retry && !q.waiting {
		return q.err
	}
	if s.sessions.HasPendingConnections() {
		q.waiting = true
		return nil
	}
	q.retry, q.waiting = false, false
	var err error
	if q.store == nil || s.profileService == nil {
		err = errors.New("initial SQL cleanup services unavailable")
	} else {
		profiles, listErr := s.profileService.List(context.Background())
		if listErr != nil {
			err = listErr
		} else {
			referenced := map[string]bool{}
			for _, p := range profiles {
				for _, id := range profileInitialSQLIDs(p) {
					referenced[id] = true
				}
			}
			ids := make([]string, 0, len(q.candidates))
			for id := range q.candidates {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			for _, id := range ids {
				if referenced[id] {
					delete(q.candidates, id)
					continue
				}
				if deleteErr := q.store.DeleteInitialSQL(context.Background(), id); deleteErr != nil {
					err = errors.Join(err, deleteErr)
				} else {
					delete(q.candidates, id)
				}
			}
		}
	}
	q.err = err
	if err != nil {
		s.status = locales.Text("初始 SQL 清理失败；记录已保留，可重试") + ": " + err.Error()
	}
	return err
}
