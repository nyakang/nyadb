package shell

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"sync/atomic"
)

// Mock tests use the same provider publication path as real connections.
func installRuntimeFixture(s *State) {
	s.runtime.Register(connections.MySQL, dbruntime.ProviderFunc(func(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
		open := s.sessions.Opener
		if open == nil {
			return mysqlsvc.OpenSession(ctx, p, secret, s.mysqlTemplate)
		}
		settings, e := connections.ResolveActiveSettings(p)
		if e != nil {
			return nil, e
		}
		if e = open(ctx, settings, secret); e != nil {
			return nil, e
		}
		return &runtimeFixtureSession{owner: s, id: p.ID, lister: s.sessions.DatabaseLister}, nil
	}))
}

type runtimeFixtureSession struct {
	owner  *State
	id     int64
	closed atomic.Bool
	lister func(context.Context, int64) ([]string, error)
}

func (s *runtimeFixtureSession) ProfileID() int64   { return s.id }
func (s *runtimeFixtureSession) Generation() uint64 { return 1 }
func (s *runtimeFixtureSession) Capabilities() contract.Capabilities {
	d, _ := connections.Describe(connections.MySQL)
	return d.Capabilities
}
func (s *runtimeFixtureSession) Alive() bool  { return !s.closed.Load() }
func (s *runtimeFixtureSession) Close() error { s.closed.Store(true); return nil }
func (s *runtimeFixtureSession) Namespace(n string) contract.NamespaceRef {
	return contract.NamespaceRef{ProfileID: s.id, Catalog: n}
}
func (s *runtimeFixtureSession) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
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
func (s *runtimeFixtureSession) Browse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	if s.owner.sessions.ObjectLister == nil {
		return nil, nil
	}
	return s.owner.sessions.ObjectLister(ctx, s.id, n.Catalog)
}
func (s *runtimeFixtureSession) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	if s.owner.objectWorkspace.Loader == nil {
		return nil, contract.ErrUnsupported
	}
	return s.owner.objectWorkspace.Loader(ctx, s.id, o.Catalog, o.Name)
}
func (s *runtimeFixtureSession) DataRuntime() contract.DataRuntime {
	saver := s.owner.dataWorkspace.Saver
	return contract.DataRuntime{Capabilities: s.Capabilities(), Saver: func(ctx context.Context, id int64, db, table string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
		if saver == nil {
			return nil, contract.ErrUnsupported
		}
		return saver(ctx, id, db, table, edits)
	}}
}

func publishRuntimeFixture(t interface {
	Helper()
	Fatal(...any)
}, s *State) {
	t.Helper()
	id := s.sessions.Current().ProfileID
	if id <= 0 {
		return
	}
	if !s.runtime.IsConnected(id) {
		s.runtime.Register(connections.MySQL, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) {
			return &runtimeFixtureSession{owner: s, id: id, lister: s.sessions.DatabaseLister}, nil
		}))
		if e := s.runtime.Connect(context.Background(), connections.Profile{ID: id, Name: "Workbench fixture", Kind: connections.MySQL, Host: "localhost", Port: 3306}, ""); e != nil {
			t.Fatal(e)
		}
		installRuntimeFixture(s)
	}
	s.sessions.Current().Kind = connections.MySQL
	s.sessions.Current().Generation = s.runtime.Generation(id)
}

type fixtureAdministration struct{ mysqlsvc.Administration }

func (a fixtureAdministration) SessionGeneration(int64) uint64 { return 1 }
func (s *runtimeFixtureSession) Administration() mysqlsvc.Administration {
	return fixtureAdministration{mysqlsvc.NewMySQLSessionManager()}
}

// Reserve a lazy, successful fixture lease so SQL UI tests do not start with
// unsupported-provider errors. Tests inject their captured executor separately.
func (s *runtimeFixtureSession) ReserveQuery(uint64) (contract.QueryLease, error) {
	if !s.Alive() {
		return nil, contract.ErrRevoked
	}
	return &runtimeFixtureQuery{session: s}, nil
}

type runtimeFixtureQuery struct {
	session *runtimeFixtureSession
	closed  atomic.Bool
}

func (l *runtimeFixtureQuery) Preconnect(ctx context.Context) error {
	if l.closed.Load() || !l.session.Alive() {
		return contract.ErrRevoked
	}
	return ctx.Err()
}
func (l *runtimeFixtureQuery) Execute(ctx context.Context, _, _ string) (contract.SQLResult, error) {
	if err := l.Preconnect(ctx); err != nil {
		return contract.SQLResult{}, err
	}
	return contract.SQLResult{}, contract.ErrUnsupported
}
func (l *runtimeFixtureQuery) ExecuteStream(ctx context.Context, ns, sql string, _ func(int, []contract.Column, []contract.Cell) error) (contract.SQLResult, error) {
	return l.Execute(ctx, ns, sql)
}
func (l *runtimeFixtureQuery) Replace() (contract.QueryLease, error) {
	if err := l.Preconnect(context.Background()); err != nil {
		return nil, err
	}
	l.Release()
	return &runtimeFixtureQuery{session: l.session}, nil
}
func (l *runtimeFixtureQuery) Release() { l.closed.Store(true) }

func runtimeNamespaceNames(ctx context.Context, s *State, id int64) ([]string, error) {
	p, e := s.runtime.Metadata(id)
	if e != nil {
		return nil, e
	}
	ns, e := p.Namespaces(ctx)
	var names []string
	for _, n := range ns {
		names = append(names, n.Label)
	}
	return names, e
}
func (s *State) engineSession(id int64) (contract.RelationalSession, error) {
	raw, e := s.runtime.Session(id)
	if e != nil {
		return nil, e
	}
	return contract.Require[contract.RelationalSession](raw)
}
