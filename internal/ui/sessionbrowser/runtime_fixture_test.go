package sessionbrowser

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"sync/atomic"
)

func newTestState(deps Dependencies) *State {
	s := New(deps)
	m := dbruntime.New()
	m.Register(connections.MySQL, dbruntime.ProviderFunc(func(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
		settings, e := connections.ResolveActiveSettings(p)
		if e != nil {
			return nil, e
		}
		if s.Opener == nil {
			return nil, contract.ErrUnsupported
		}
		if e = s.Opener(ctx, settings, secret); e != nil {
			return nil, e
		}
		return &fixtureSession{s: s, id: p.ID, lister: s.DatabaseLister}, nil
	}))
	s.SetEngineRuntime(EngineRuntime{Open: m.ConnectSession, Connect: m.Connect, Session: m.Session, Disconnect: m.Disconnect, Close: m.Close})
	return s
}

type fixtureSession struct {
	s      *State
	id     int64
	closed atomic.Bool
	lister DatabaseLister
}

func (s *fixtureSession) ProfileID() int64   { return s.id }
func (s *fixtureSession) Generation() uint64 { return 1 }
func (s *fixtureSession) Capabilities() contract.Capabilities {
	return contract.Capabilities{Metadata: true}
}
func (s *fixtureSession) Close() error { s.closed.Store(true); return nil }
func (s *fixtureSession) Alive() bool  { return !s.closed.Load() }
func (s *fixtureSession) Namespace(n string) contract.NamespaceRef {
	return contract.NamespaceRef{ProfileID: s.id, Catalog: n}
}
func (s *fixtureSession) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
	if s.lister == nil {
		return nil, nil
	}
	names, e := s.lister(ctx, s.id)
	out := make([]contract.Namespace, len(names))
	for i, n := range names {
		out[i] = contract.Namespace{Ref: s.Namespace(n), Label: n}
	}
	return out, e
}
func (s *fixtureSession) Browse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	if s.s.ObjectLister == nil {
		return nil, nil
	}
	return s.s.ObjectLister(ctx, s.id, n.Catalog)
}
func (s *fixtureSession) TableMetadata(context.Context, contract.ObjectRef) (*contract.TableStructure, error) {
	return nil, contract.ErrUnsupported
}
