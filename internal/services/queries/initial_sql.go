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
	return s.put(ctx, Record{ID: v.ID, Kind: "initial_sql", ProfileID: v.ProfileID, Revision: v.Revision}, v)
}
func (s *Store) LoadInitialSQL(ctx context.Context, id string) (InitialSQL, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, e := s.repository.List(ctx, "initial_sql", 0)
	if e != nil {
		return InitialSQL{}, e
	}
	for _, r := range rs {
		if r.ID == id {
			var v InitialSQL
			e = s.open(ctx, r, &v)
			return v, e
		}
	}
	return InitialSQL{}, errors.New("encrypted initial SQL document is missing; connection initialization blocked")
}
