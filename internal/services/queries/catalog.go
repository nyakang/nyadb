package queries

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var ErrQueryMissing = errors.New("saved query is missing or deleted")
var ErrQueryScope = errors.New("saved query identity does not match its original profile")

// ConditionalRepository is implemented by persistent storage. The fallback is
// for injected single-Store repositories; all such calls hold Store.mu.
type ConditionalRepository interface {
	PutConditional(context.Context, Record, uint64) error
}

type Group struct{ ID, Name string }
type Catalog struct {
	ProfileID  int64
	Revision   uint64
	Groups     []Group
	Membership map[string]string
}

func (s *Store) record(ctx context.Context, kind, id string) (Record, bool, error) {
	if repo, ok := s.repository.(RecordLookup); ok {
		return repo.GetRecord(ctx, kind, id)
	}
	rs, err := s.repository.List(ctx, kind, 0)
	if err != nil {
		return Record{}, false, err
	}
	for _, r := range rs {
		if r.ID == id {
			return r, true, nil
		}
	}
	return Record{}, false, nil
}
func (s *Store) queryRecord(ctx context.Context, id string) (Record, error) {
	if id == "" || id == "workspace" {
		return Record{}, ErrQueryMissing
	}
	r, ok, e := s.record(ctx, "query", id)
	if e != nil {
		return r, e
	}
	if !ok {
		return r, ErrQueryMissing
	}
	return r, nil
}
func (s *Store) conditional(ctx context.Context, r Record, expected uint64) error {
	if repo, ok := s.repository.(ConditionalRepository); ok {
		return repo.PutConditional(ctx, r, expected)
	}
	return s.repository.Put(ctx, r)
}
func (s *Store) saveNamed(ctx context.Context, n Named) error {
	n.Name = strings.TrimSpace(n.Name)
	if n.ID == "" || n.ID == "workspace" || n.Name == "" || n.Revision == 0 {
		return errors.New("invalid named query")
	}
	if _, deleted, e := s.record(ctx, "query_deleted", n.ID); e != nil {
		return e
	} else if deleted {
		return ErrQueryMissing
	}
	old, exists, e := s.record(ctx, "query", n.ID)
	if e != nil {
		return e
	}
	expected := n.Revision - 1
	if exists && old.ProfileID != n.ProfileID {
		return ErrQueryScope
	}
	if exists {
		var original Named
		if e = s.open(ctx, old, &original); e != nil {
			return e
		}
	}
	if (!exists && expected != 0) || (exists && old.Revision != expected) {
		return ErrStaleRevision
	}
	r, e := s.seal(ctx, Record{ID: n.ID, Kind: "query", ProfileID: n.ProfileID, Revision: n.Revision}, n)
	if e != nil {
		return e
	}
	return s.conditional(ctx, r, expected)
}
func (s *Store) RenameNamed(ctx context.Context, profile int64, id string, revision uint64, name string) (Named, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, e := s.queryRecord(ctx, id)
	if e != nil {
		return Named{}, e
	}
	if r.ProfileID != profile {
		return Named{}, ErrQueryScope
	}
	if r.Revision != revision {
		return Named{}, ErrStaleRevision
	}
	var n Named
	if e = s.open(ctx, r, &n); e != nil {
		return n, e
	}
	n.Name = strings.TrimSpace(name)
	n.Revision++
	return n, s.saveNamed(ctx, n)
}
func (s *Store) DeleteNamedRevision(ctx context.Context, profile int64, id string, revision uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteNamed(ctx, profile, id, revision)
}
func (s *Store) deleteNamed(ctx context.Context, profile int64, id string, revision uint64) error {
	r, e := s.queryRecord(ctx, id)
	if e != nil {
		return e
	}
	if r.ProfileID != profile {
		return ErrQueryScope
	}
	if r.Revision != revision {
		return ErrStaleRevision
	}
	// Authenticate the original before replacing it; missing keys never remove it.
	var original Named
	if e = s.open(ctx, r, &original); e != nil {
		return e
	}
	r, e = s.seal(ctx, Record{ID: id, Kind: "query_deleted", ProfileID: profile, Revision: revision + 1}, struct{ ID string }{id})
	if e != nil {
		return e
	}
	return s.conditional(ctx, r, revision)
}
func (s *Store) ListCatalogs(ctx context.Context) ([]Catalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, e := s.repository.List(ctx, "query_catalog", 0)
	if e != nil {
		return nil, e
	}
	var out []Catalog
	for _, r := range rs {
		var c Catalog
		if e = s.open(ctx, r, &c); e != nil {
			return nil, e
		}
		if e = validCatalog(c); e != nil {
			return nil, e
		}
		if c.ProfileID != r.ProfileID || c.Revision != r.Revision {
			return nil, ErrQueryScope
		}
		out = append(out, c)
	}
	return out, nil
}
func validCatalog(c Catalog) error {
	if c.ProfileID < 0 || c.Revision == 0 {
		return errors.New("invalid query group catalog")
	}
	ids, names := map[string]bool{}, map[string]bool{}
	for _, g := range c.Groups {
		if g.ID == "" || strings.TrimSpace(g.Name) == "" || g.Name != strings.TrimSpace(g.Name) || ids[g.ID] || names[g.Name] {
			return errors.New("query group names must be nonempty and unique")
		}
		ids[g.ID], names[g.Name] = true, true
	}
	for id, group := range c.Membership {
		if id == "" || !ids[group] {
			return errors.New("invalid query group membership")
		}
	}
	return nil
}
func (s *Store) SaveCatalog(ctx context.Context, c Catalog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := validCatalog(c); e != nil {
		return e
	}
	id := fmt.Sprintf("query-catalog/%d", c.ProfileID)
	old, exists, e := s.record(ctx, "query_catalog", id)
	if e != nil {
		return e
	}
	expected := c.Revision - 1
	if exists {
		var original Catalog
		if e = s.open(ctx, old, &original); e != nil {
			return e
		}
	}
	if (!exists && expected != 0) || (exists && old.Revision != expected) {
		return ErrStaleRevision
	}
	for id := range c.Membership {
		q, ok, e := s.record(ctx, "query", id)
		if e != nil {
			return e
		}
		if !ok {
			delete(c.Membership, id)
			continue
		}
		if q.ProfileID != c.ProfileID {
			return ErrQueryScope
		}
	}
	r, e := s.seal(ctx, Record{ID: id, Kind: "query_catalog", ProfileID: c.ProfileID, Revision: c.Revision}, c)
	if e != nil {
		return e
	}
	return s.conditional(ctx, r, expected)
}
