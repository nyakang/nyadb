package queries

import (
	"context"
	"errors"
	"strings"
)

type InitialSQL struct {
	ID, Text  string
	ProfileID int64
	Revision  uint64
}

func (s *Store) SaveInitialSQL(ctx context.Context, v InitialSQL) error {
	if v.ID == "" || v.ProfileID <= 0 || v.Revision == 0 || len(v.Text) > 64<<10 || strings.ContainsRune(v.Text, 0) {
		return errors.New("invalid initial SQL document")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists, err := s.record(ctx, "initial_sql", v.ID)
	if err != nil {
		return err
	}
	if exists {
		var original InitialSQL
		if err = s.openInitialSQL(ctx, old, &original); err != nil {
			return err
		}
		if original.ProfileID != v.ProfileID {
			return ErrQueryScope
		}
		if v.Revision != original.Revision+1 {
			return ErrStaleRevision
		}
	} else if v.Revision != 1 {
		return ErrStaleRevision
	}
	r, err := s.seal(ctx, Record{ID: v.ID, Kind: "initial_sql", ProfileID: v.ProfileID, Revision: v.Revision}, v)
	if err != nil {
		return err
	}
	return s.conditional(ctx, r, v.Revision-1)
}
func (s *Store) LoadInitialSQL(ctx context.Context, id string) (InitialSQL, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, exists, e := s.record(ctx, "initial_sql", id)
	if e != nil {
		return InitialSQL{}, e
	}
	if exists {
		var v InitialSQL
		e = s.openInitialSQL(ctx, r, &v)
		return v, e
	}
	return InitialSQL{}, errors.New("encrypted initial SQL document is missing; connection initialization blocked")
}

func (s *Store) openInitialSQL(ctx context.Context, r Record, v *InitialSQL) error {
	if err := s.open(ctx, r, v); err != nil {
		return err
	}
	if r.Kind != "initial_sql" || v.ID != r.ID || v.ProfileID != r.ProfileID || v.Revision != r.Revision {
		return errors.New("initial SQL record identity mismatch; original retained")
	}
	return nil
}

// The host must establish that no profile or pending handshake references id.
// Authenticate before deletion so a wrong key cannot discard retained content.
func (s *Store) DeleteInitialSQL(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, exists, err := s.record(ctx, "initial_sql", id)
	if err != nil || !exists {
		return err
	}
	var original InitialSQL
	if err = s.openInitialSQL(ctx, r, &original); err != nil {
		return err
	}
	if repo, ok := s.repository.(InitialSQLDeleter); ok {
		return repo.DeleteInitialSQL(ctx, id)
	}
	return errors.New("initial SQL cleanup is unavailable; original retained")
}
