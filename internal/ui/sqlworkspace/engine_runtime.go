package sqlworkspace

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqlresult"
	"io"
)

// EngineRuntime uses the same feature contract for every registered engine.
func EngineRuntime(session func(int64) (contract.Session, error)) Runtime {
	return engineRuntime{session}
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
	p, e := contract.Require[contract.QueryProvider](s)
	if e != nil {
		return nil, e
	}
	l, e := p.ReserveQuery(tab)
	if e != nil {
		return nil, e
	}
	return &engineLease{l}, nil
}

type engineLease struct{ contract.QueryLease }

func (l *engineLease) Replace() (Lease, error) {
	x, e := l.QueryLease.Replace()
	if e != nil {
		return nil, e
	}
	return &engineLease{x}, nil
}
func (l *engineLease) ExecuteStream(ctx context.Context, schema, q string, sink sqlresult.Sink) (contract.SQLResult, error) {
	return l.QueryLease.ExecuteStream(ctx, schema, q, sink)
}

func (l *engineLease) SupportsFiles() bool {
	if f, ok := l.QueryLease.(interface{ SupportsFiles() bool }); ok {
		return f.SupportsFiles()
	}
	_, ok := l.QueryLease.(contract.QueryFileLease)
	return ok
}
func (l *engineLease) ExecuteFile(ctx context.Context, db string, r io.Reader) (contract.SQLResult, error) {
	f, ok := l.QueryLease.(contract.QueryFileLease)
	if !ok {
		return contract.SQLResult{}, contract.ErrUnsupported
	}
	return f.ExecuteFile(ctx, db, r)
}

func (l *engineLease) SupportsGraph() bool {
	if v, ok := l.QueryLease.(interface{ SupportsGraph() bool }); ok {
		return v.SupportsGraph()
	}
	_, ok := l.QueryLease.(contract.GraphQueryLease)
	return ok
}
func (l *engineLease) ExecuteGraph(ctx context.Context, r contract.GraphQueryRequest) (contract.GraphResult, error) {
	p, ok := l.QueryLease.(contract.GraphQueryLease)
	if !ok {
		return contract.GraphResult{}, contract.ErrUnsupported
	}
	return p.ExecuteGraph(ctx, r)
}
