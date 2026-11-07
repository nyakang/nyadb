package trino

import (
	"context"
	"errors"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type Session struct {
	mu         sync.Mutex
	profile    connections.Profile
	password   string
	ctx        context.Context
	cancel     context.CancelFunc
	closed     bool
	work       sync.WaitGroup
	leases     map[*queryLease]bool
	namespaces map[string]contract.NamespaceRef
}

func OpenSession(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
	p, err := connections.NormalizeProfile(p)
	if err != nil {
		return nil, err
	}
	if p.Kind != connections.Trino || p.ID <= 0 {
		return nil, connections.ErrInvalid
	}
	cfg, err := connections.TrinoProfileConfig(p)
	if err != nil {
		return nil, err
	}
	c, err := Open(ctx, cfg, password)
	if err != nil {
		return nil, err
	}
	if err = c.Close(); err != nil {
		return nil, err
	}
	root, cancel := context.WithCancel(context.Background())
	return &Session{profile: p, password: password, ctx: root, cancel: cancel, leases: map[*queryLease]bool{}, namespaces: map[string]contract.NamespaceRef{}}, nil
}
func (s *Session) ProfileID() int64   { return s.profile.ID }
func (s *Session) Generation() uint64 { return 1 }
func (s *Session) Capabilities() contract.Capabilities {
	d, _ := connections.Describe(connections.Trino)
	return d.Capabilities
}
func (s *Session) Alive() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.closed }
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.work.Wait()
		return nil
	}
	s.closed = true
	s.cancel()
	s.password = ""
	leases := make([]*queryLease, 0, len(s.leases))
	for l := range s.leases {
		leases = append(leases, l)
	}
	s.namespaces = nil
	s.mu.Unlock()
	for _, l := range leases {
		l.Release()
	}
	s.work.Wait()
	return nil
}
func (s *Session) request(ctx context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, contract.ErrRevoked
	}
	s.work.Add(1)
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return child, func() { stop(); cancel(); s.work.Done() }, nil
}
func (s *Session) open(ctx context.Context) (*Client, error) {
	s.mu.Lock()
	p, password, closed := s.profile, s.password, s.closed
	s.mu.Unlock()
	if closed {
		return nil, contract.ErrRevoked
	}
	cfg, err := connections.TrinoProfileConfig(p)
	if err != nil {
		return nil, err
	}
	return Open(ctx, cfg, password)
}
func quoteName(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func quoteText(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
func namespaceLabel(catalog, schema string) string {
	return quoteName(catalog) + "." + quoteName(schema)
}
func validName(s string) bool {
	return s != "" && len(s) <= 256 && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}
func qualified(o contract.ObjectRef) string {
	return quoteName(o.Catalog) + "." + quoteName(o.Schema) + "." + quoteName(o.Name)
}
func (s *Session) Namespace(label string) contract.NamespaceRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	if label == "" {
		return contract.NamespaceRef{ProfileID: s.profile.ID, Catalog: s.profile.Database, Schema: s.profile.TrinoOptions.DefaultSchema}
	}
	if n, ok := s.namespaces[label]; ok {
		return n
	}
	return contract.NamespaceRef{ProfileID: s.profile.ID}
}
func (s *Session) checkNamespace(n contract.NamespaceRef) error {
	if !s.Alive() {
		return contract.ErrRevoked
	}
	if n.ProfileID != s.ProfileID() || !validName(n.Catalog) || !validName(n.Schema) {
		return connections.ErrInvalid
	}
	return nil
}
func (s *Session) checkObject(o contract.ObjectRef) error {
	if err := s.checkNamespace(o.Namespace()); err != nil {
		return err
	}
	if !validName(o.Name) {
		return connections.ErrInvalid
	}
	if !strings.EqualFold(o.Kind, "TABLE") && !strings.EqualFold(o.Kind, "VIEW") {
		return contract.ErrUnsupported
	}
	return nil
}
func operationError(err error) error {
	if errors.Is(err, ErrLost) {
		return errors.Join(contract.ErrSQLSessionLost, err)
	}
	return err
}
