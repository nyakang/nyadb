// Package dbruntime owns driver-independent session publication and revocation.
package dbruntime

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type Provider interface {
	Open(context.Context, connections.Profile, string) (contract.Session, error)
}
type ProviderFunc func(context.Context, connections.Profile, string) (contract.Session, error)

func (f ProviderFunc) Open(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
	return f(ctx, p, password)
}

type attempt struct {
	generation uint64
	cancel     context.CancelFunc
}
type Manager struct {
	mu        sync.Mutex
	next      uint64
	closed    bool
	providers map[connections.Kind]Provider
	pending   map[int64]attempt
	sessions  map[int64]*session
}

func New() *Manager {
	return &Manager{providers: map[connections.Kind]Provider{}, pending: map[int64]attempt{}, sessions: map[int64]*session{}}
}
func (m *Manager) Register(kind connections.Kind, p Provider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.providers[kind] = p
}
func (m *Manager) Connect(ctx context.Context, p connections.Profile, password string) error {
	_, err := m.ConnectSession(ctx, p, password)
	return err
}
func (m *Manager) ConnectSession(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
	p.Variants = append([]connections.ConnectionVariant(nil), p.Variants...)
	p, e := connections.NormalizeProfile(p)
	if e != nil {
		return nil, e
	}
	if p.ID <= 0 {
		return nil, connections.ErrInvalid
	}
	m.mu.Lock()
	provider := m.providers[p.Kind]
	if m.closed {
		m.mu.Unlock()
		return nil, contract.ErrRevoked
	}
	if provider == nil {
		m.mu.Unlock()
		return nil, contract.ErrUnsupported
	}
	m.next++
	generation := m.next
	opctx, cancel := context.WithCancel(ctx)
	previous := m.pending[p.ID]
	old := m.sessions[p.ID]
	m.pending[p.ID] = attempt{generation, cancel}
	delete(m.sessions, p.ID)
	m.mu.Unlock()
	if previous.cancel != nil {
		previous.cancel()
	}
	if old != nil {
		_ = old.Close()
	}
	raw, err := provider.Open(opctx, p, password)
	m.mu.Lock()
	current, exists := m.pending[p.ID]
	stale := m.closed || !exists || current.generation != generation || opctx.Err() != nil
	if exists && current.generation == generation {
		delete(m.pending, p.ID)
	}
	if err == nil && raw != nil && raw.ProfileID() != p.ID {
		err = connections.ErrInvalid
	}
	if err == nil && raw == nil {
		err = errors.New("database provider returned no session")
	}
	var published *session
	if !stale && err == nil {
		published = &session{raw: raw, id: p.ID, generation: generation, kind: p.Kind, cancel: cancel}
		m.sessions[p.ID] = published
	}
	m.mu.Unlock()
	if stale || err != nil {
		cancel()
	}
	if stale || err != nil {
		if raw != nil {
			_ = raw.Close()
		}
		if stale && err == nil {
			err = contract.ErrRevoked
		}
	}
	if err != nil && password != "" {
		err = errors.New(strings.ReplaceAll(err.Error(), password, "[hidden]"))
	}
	if err != nil {
		return nil, err
	}
	return published, nil
}
func (m *Manager) Session(id int64) (contract.Session, error) {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil || !s.alive() {
		return nil, contract.ErrRevoked
	}
	return s, nil
}
func (m *Manager) IsConnected(id int64) bool { _, e := m.Session(id); return e == nil }
func (m *Manager) Generation(id int64) uint64 {
	s, e := m.Session(id)
	if e != nil {
		return 0
	}
	return s.Generation()
}
func (m *Manager) Disconnect(id int64) error {
	m.mu.Lock()
	p := m.pending[id]
	s := m.sessions[id]
	delete(m.pending, id)
	delete(m.sessions, id)
	m.next++
	m.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
	}
	if s != nil {
		return s.Close()
	}
	return nil
}
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	pending, all := m.pending, m.sessions
	m.pending = map[int64]attempt{}
	m.sessions = map[int64]*session{}
	m.mu.Unlock()
	for _, p := range pending {
		p.cancel()
	}
	var err error
	for _, s := range all {
		err = errors.Join(err, s.Close())
	}
	return err
}

type session struct {
	cancel     context.CancelFunc
	mu         sync.Mutex
	raw        contract.Session
	id         int64
	generation uint64
	kind       connections.Kind
	closed     bool
}

func (s *session) ProfileID() int64             { return s.id }
func (s *session) Generation() uint64           { return s.generation }
func (s *session) Underlying() contract.Session { return s.raw }
func (s *session) Capabilities() contract.Capabilities {
	descriptor, _ := connections.Describe(s.kind)
	caps := contract.IntersectCapabilities(descriptor.Capabilities, s.raw.Capabilities())
	if _, err := contract.Require[contract.QueryProvider](s.raw); err != nil {
		caps.Query = false
		caps.Explain = false
	}
	if _, err := contract.Require[contract.MetadataProvider](s.raw); err != nil {
		caps.Metadata = false
		caps.Schemas = false
	}
	r := contract.ComposeData(s.raw)
	if p, err := contract.Require[contract.DataProvider](s.raw); err == nil {
		r = p.DataRuntime()
	}
	caps.EditableTables = caps.EditableTables && r.Saver != nil
	caps.Transactions = caps.Transactions && r.Transactions.Begin != nil && r.Transactions.Commit != nil && r.Transactions.Rollback != nil
	caps.Count = caps.Count && r.Count != nil
	caps.FieldChunks = caps.FieldChunks && r.Chunk != nil
	caps.Analysis = caps.Analysis && r.Analyzer != nil
	caps.Import = caps.Import && (r.Importer != nil || r.StreamImporter != nil)
	caps.JSONL = caps.JSONL && r.JSONL != nil
	caps.Export = caps.Export && (r.Exporter != nil || r.JSONL != nil)
	return s.managementCapabilities(caps)
}
func (s *session) alive() bool {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return false
	}
	if v, ok := s.raw.(interface{ Alive() bool }); ok {
		return v.Alive()
	}
	return true
}
func (s *session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	return s.raw.Close()
}
func (m *Manager) Data(id int64) contract.DataRuntime {
	s, e := m.Session(id)
	if e != nil {
		return contract.DataRuntime{}
	}
	r := contract.ComposeData(s)
	// Legacy injectable fixtures can still supply the compatibility aggregate.
	if _, err := contract.Require[contract.DataReadsProvider](s); err != nil {
		if p, err := contract.Require[contract.DataProvider](s); err == nil {
			r = p.DataRuntime()
		}
	}
	r.Capabilities = contract.IntersectCapabilities(r.Capabilities, s.Capabilities())
	return mSessionData(s.(*session), r)
}
func (m *Manager) Metadata(id int64) (contract.MetadataProvider, error) {
	s, e := m.Session(id)
	if e != nil {
		return nil, e
	}
	if !s.Capabilities().Metadata {
		return nil, contract.ErrUnsupported
	}
	return contract.Require[contract.MetadataProvider](s)
}
func (m *Manager) Namespace(id int64, name string) contract.NamespaceRef {
	s, e := m.Session(id)
	if e != nil {
		return contract.NamespaceRef{ProfileID: id}
	}
	if v, e := contract.Require[interface {
		Namespace(string) contract.NamespaceRef
	}](s); e == nil {
		return v.Namespace(name)
	}
	return contract.NamespaceRef{ProfileID: id}
}

// Test opens an isolated provider handle and never publishes or disconnects a saved profile.
func (m *Manager) Test(ctx context.Context, p connections.Profile, password string) error {
	p.Variants = append([]connections.ConnectionVariant(nil), p.Variants...)
	var err error
	p, err = connections.NormalizeProfile(p)
	if err != nil {
		return err
	}
	if p.ID <= 0 {
		p.ID = 1
	}
	m.mu.Lock()
	provider, closed := m.providers[p.Kind], m.closed
	m.mu.Unlock()
	if closed {
		return contract.ErrRevoked
	}
	if provider == nil {
		return contract.ErrUnsupported
	}
	handle, err := provider.Open(ctx, p, password)
	if handle != nil {
		closeErr := handle.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil && password != "" {
		return errors.New(strings.ReplaceAll(err.Error(), password, "[已隐藏]"))
	}
	return err
}
