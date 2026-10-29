package connections

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Group is a virtual connection folder, not a schema or a network destination.
// Empty folders are persisted independently of their connection profiles.
type Group struct {
	Name string
}

// ValidateGroupName checks a canonical, non-empty virtual folder name.
func ValidateGroupName(name string) error {
	if name == "" || utf8.RuneCountInString(name) > 120 || strings.TrimSpace(name) != name {
		return fmt.Errorf("%w: invalid connection group name", ErrInvalid)
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return fmt.Errorf("%w: invalid connection group character", ErrInvalid)
		}
	}
	return nil
}

type groupRepository interface {
	ListGroups(context.Context) ([]Group, error)
	CreateGroup(context.Context, string) error
	RenameGroup(context.Context, string, string) error
	DeleteGroup(context.Context, string) error
	MoveToGroup(context.Context, int64, string) error
}

func (s *Service) groups() groupRepository {
	groups, _ := s.repository.(groupRepository)
	return groups
}

func (s *Service) ListGroups(ctx context.Context) ([]Group, error) {
	if repo := s.groups(); repo != nil {
		return repo.ListGroups(ctx)
	}
	return nil, errors.New("connection group storage unavailable")
}
func (s *Service) CreateGroup(ctx context.Context, name string) error {
	if repo := s.groups(); repo != nil {
		return repo.CreateGroup(ctx, name)
	}
	return errors.New("connection group storage unavailable")
}
func (s *Service) RenameGroup(ctx context.Context, old, name string) error {
	if repo := s.groups(); repo != nil {
		return repo.RenameGroup(ctx, old, name)
	}
	return errors.New("connection group storage unavailable")
}
func (s *Service) DeleteGroup(ctx context.Context, name string) error {
	if repo := s.groups(); repo != nil {
		return repo.DeleteGroup(ctx, name)
	}
	return errors.New("connection group storage unavailable")
}
func (s *Service) MoveToGroup(ctx context.Context, profileID int64, name string) error {
	if repo := s.groups(); repo != nil {
		return repo.MoveToGroup(ctx, profileID, name)
	}
	return errors.New("connection group storage unavailable")
}
