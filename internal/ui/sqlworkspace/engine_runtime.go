package sqlworkspace

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqlresult"
	"sync"
)

// EngineRuntime routes driver-free sessions to the existing SQL editor. The
// profile's fixed catalog is immutable; database is its selected schema.
func EngineRuntime(session func(int64) (contract.Session, error)) Runtime {
	return engineRuntime{session: session}
}

type engineRuntime struct {
	session func(int64) (contract.Session, error)
}

func (r engineRuntime) IsConnected(id int64) bool { _, e := r.session(id); return e == nil }
func (r engineRuntime) Reserve(id int64, tab uint64) (Lease, error) {
	s, e := r.session(id)
	if e != nil {
		return nil, e
	}
	return &engineLease{session: s}, nil
}

type engineLease struct {
	mu       sync.Mutex
	prepare  sync.Mutex
	session  contract.Session
	lease    contract.SQLLease
	released bool
}

func (l *engineLease) Preconnect(ctx context.Context) error {
	l.prepare.Lock()
	defer l.prepare.Unlock()
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return errors.New("SQL lease revoked")
	}
	if l.lease != nil {
		l.mu.Unlock()
		return nil
	}
	session := l.session
	l.mu.Unlock()
	x, e := session.Reserve(ctx)
	if e != nil {
		return e
	}
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		x.Close()
		return errors.New("SQL lease revoked")
	}
	l.lease = x
	l.mu.Unlock()
	return nil
}
func (l *engineLease) current() (contract.SQLLease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.lease == nil {
		return nil, errors.New("SQL lease not prepared or revoked")
	}
	return l.lease, nil
}
func (l *engineLease) Execute(ctx context.Context, schema, sql string) (contract.SQLResult, error) {
	if e := l.Preconnect(ctx); e != nil {
		return contract.SQLResult{}, e
	}
	x, e := l.current()
	if e != nil {
		return contract.SQLResult{}, e
	}
	if e = x.SetSchema(ctx, schema); e != nil {
		return contract.SQLResult{}, e
	}
	return x.Execute(ctx, sql)
}
func (l *engineLease) ExecuteStream(ctx context.Context, schema, sql string, sink sqlresult.Sink) (contract.SQLResult, error) {
	if e := l.Preconnect(ctx); e != nil {
		return contract.SQLResult{}, e
	}
	x, e := l.current()
	if e != nil {
		return contract.SQLResult{}, e
	}
	if e = x.SetSchema(ctx, schema); e != nil {
		return contract.SQLResult{}, e
	}
	return x.Stream(ctx, sql, engineSink{sink})
}
func (l *engineLease) Replace() (Lease, error) {
	l.Release()
	return &engineLease{session: l.session}, nil
}
func (l *engineLease) Release() {
	l.mu.Lock()
	l.released = true
	x := l.lease
	l.lease = nil
	l.mu.Unlock()
	if x != nil {
		x.Close()
	}
}

type engineSink struct{ sqlresult.Sink }

func (s engineSink) BeginResult(i int, c []contract.Column) error { return s.Sink(i, c, nil) }
func (s engineSink) WriteRow(i int, r []contract.Cell) error      { return s.Sink(i, nil, r) }
func (s engineSink) EndResult(i int, n int64) error               { return nil }

// CompositeRuntime keeps the MySQL compatibility runtime while admitting
// genuine sessions supplied through the public contract.
func CompositeRuntime(primary, secondary Runtime) Runtime {
	return compositeRuntime{primary, secondary}
}

type compositeRuntime struct{ primary, secondary Runtime }

func (r compositeRuntime) IsConnected(id int64) bool {
	return r.primary != nil && r.primary.IsConnected(id) || r.secondary != nil && r.secondary.IsConnected(id)
}
func (r compositeRuntime) Reserve(id int64, tab uint64) (Lease, error) {
	if r.primary != nil && r.primary.IsConnected(id) {
		return r.primary.Reserve(id, tab)
	}
	if r.secondary != nil && r.secondary.IsConnected(id) {
		return r.secondary.Reserve(id, tab)
	}
	return nil, errors.New("session disconnected")
}
