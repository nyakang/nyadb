package queries

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

type NamedWorkspace struct {
	ID, Name  string
	Revision  uint64
	Workspace Workspace
}

func (s *Store) ListWorkspaces(ctx context.Context) ([]NamedWorkspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.repository.List(ctx, "named_workspace", 0)
	if err != nil {
		return nil, err
	}
	out := []NamedWorkspace{}
	for _, r := range records {
		var w NamedWorkspace
		if err = s.open(ctx, r, &w); err != nil {
			return nil, err
		}
		if w.ID != r.ID || w.Revision != r.Revision {
			return nil, ErrStaleRevision
		}
		out = append(out, w)
	}
	return out, nil
}
func (s *Store) SaveNamedWorkspace(ctx context.Context, w NamedWorkspace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveNamedWorkspace(ctx, w)
}
func (s *Store) saveNamedWorkspace(ctx context.Context, w NamedWorkspace) error {
	w.Name = strings.TrimSpace(w.Name)
	if !strings.HasPrefix(w.ID, "workspace/") || w.Name == "" || !utf8.ValidString(w.Name) || utf8.RuneCountInString(w.Name) > 80 || w.Revision == 0 {
		return errors.New("invalid workspace name or identity")
	}
	if _, deleted, err := s.record(ctx, "named_workspace_deleted", w.ID); err != nil {
		return err
	} else if deleted {
		return ErrQueryMissing
	}
	records, err := s.repository.List(ctx, "named_workspace", 0)
	if err != nil {
		return err
	}
	expected := w.Revision - 1
	found := false
	for _, r := range records {
		var old NamedWorkspace
		if err = s.open(ctx, r, &old); err != nil {
			return err
		}
		if r.ID == w.ID {
			found = true
			if r.Revision != expected {
				return ErrStaleRevision
			}
		} else if strings.EqualFold(old.Name, w.Name) {
			return errors.New("workspace name already exists; use explicit replace")
		}
	}
	if !found && expected != 0 {
		return ErrStaleRevision
	}
	record, err := s.seal(ctx, Record{ID: w.ID, Kind: "named_workspace", Revision: w.Revision}, w)
	if err != nil {
		return err
	}
	return s.conditional(ctx, record, expected)
}
func (s *Store) RenameWorkspace(ctx context.Context, id string, revision uint64, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, found, err := s.record(ctx, "named_workspace", id)
	if err != nil {
		return err
	}
	if !found {
		return ErrQueryMissing
	}
	if r.Revision != revision {
		return ErrStaleRevision
	}
	var w NamedWorkspace
	if err = s.open(ctx, r, &w); err != nil {
		return err
	}
	w.Name = name
	w.Revision++
	return s.saveNamedWorkspace(ctx, w)
}
func (s *Store) DeleteWorkspace(ctx context.Context, id string, revision uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, found, err := s.record(ctx, "named_workspace", id)
	if err != nil {
		return err
	}
	if !found {
		return ErrQueryMissing
	}
	if r.Revision != revision {
		return ErrStaleRevision
	}
	var w NamedWorkspace
	if err = s.open(ctx, r, &w); err != nil {
		return err
	}
	r, err = s.seal(ctx, Record{ID: id, Kind: "named_workspace_deleted", Revision: revision + 1}, struct{ ID string }{id})
	if err != nil {
		return err
	}
	return s.conditional(ctx, r, revision)
}
