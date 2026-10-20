package connections

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Repository is the persistence boundary for non-secret connection profiles.
type Repository interface {
	Create(context.Context, Profile) (Profile, error)
	Get(context.Context, int64) (Profile, error)
	List(context.Context) ([]Profile, error)
	Update(context.Context, Profile) (Profile, error)
	Delete(context.Context, int64) error
}

type Service struct{ repository Repository }

func NewService(repository Repository) *Service { return &Service{repository: repository} }

func (s *Service) List(ctx context.Context) ([]Profile, error) { return s.repository.List(ctx) }
func (s *Service) Get(ctx context.Context, id int64) (Profile, error) {
	return s.repository.Get(ctx, id)
}
func (s *Service) Create(ctx context.Context, p Profile) (Profile, error) {
	return s.repository.Create(ctx, p)
}
func (s *Service) Update(ctx context.Context, p Profile) (Profile, error) {
	return s.repository.Update(ctx, p)
}
func (s *Service) Delete(ctx context.Context, id int64) error { return s.repository.Delete(ctx, id) }

// Duplicate clones only connection metadata and intentionally never credentials.
func (s *Service) Duplicate(ctx context.Context, id int64, name string) (Profile, error) {
	p, err := s.repository.Get(ctx, id)
	if err != nil {
		return Profile{}, err
	}
	p.ID = 0
	p.Name = strings.TrimSpace(name)
	p.CreatedAt = p.CreatedAt.UTC()
	p.UpdatedAt = p.UpdatedAt.UTC()
	if p.Name == "" {
		return Profile{}, fmt.Errorf("%w: duplicate name required", ErrInvalid)
	}
	return s.repository.Create(ctx, p)
}

// Defaults are UI-independent initial values, not connection attempts.
func Defaults(kind Kind) (Profile, error) {
	switch kind {
	case MySQL:
		return Profile{Kind: MySQL, Host: "localhost", Port: 3306, Username: "root"}, nil
	case PostgreSQL:
		return Profile{Kind: PostgreSQL, Host: "localhost", Port: 5432, Database: "postgres", Username: "postgres"}, nil
	case SQLite:
		return Profile{Kind: SQLite}, nil
	default:
		return Profile{}, errors.Join(ErrInvalid, fmt.Errorf("unsupported connection type"))
	}
}
