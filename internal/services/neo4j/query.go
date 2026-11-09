package neo4j

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphresult"
)

type queryLease struct {
	s              *Session
	document       uint64
	ops            sync.Mutex
	mu             sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	client         *Client
	released, lost bool
}

func (s *Session) ReserveQuery(document uint64) (contract.QueryLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked {
		return nil, contract.ErrRevoked
	}
	if document == 0 {
		return nil, ErrInvalid
	}
	if len(s.leases) >= 32 {
		return nil, contract.ErrSQLSessionLimit
	}
	ctx, cancel := context.WithCancel(s.ctx)
	l := &queryLease{s: s, document: document, ctx: ctx, cancel: cancel}
	s.leases[l] = true
	return l, nil
}
func (l *queryLease) Release() {
	l.mu.Lock()
	l.released = true
	l.cancel()
	l.mu.Unlock()
	l.ops.Lock()
	if l.client != nil {
		_ = l.client.Close()
		l.client = nil
	}
	l.ops.Unlock()
	l.s.mu.Lock()
	delete(l.s.leases, l)
	l.s.mu.Unlock()
}
func (l *queryLease) Replace() (contract.QueryLease, error) {
	l.Release()
	return l.s.ReserveQuery(l.document)
}
func (l *queryLease) request(ctx context.Context) (context.Context, func(), error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil, nil, contract.ErrRevoked
	}
	if l.lost {
		return nil, nil, contract.ErrSQLSessionLost
	}
	child, done, e := l.s.request(ctx)
	if e != nil {
		return nil, nil, e
	}
	child, cancel := context.WithCancel(child)
	stop := context.AfterFunc(l.ctx, cancel)
	return child, func() { stop(); cancel(); done() }, nil
}
func (l *queryLease) markLost(e error) error {
	l.mu.Lock()
	l.lost = true
	l.mu.Unlock()
	if l.client != nil {
		e = errors.Join(e, l.client.Close())
	}
	return errors.Join(contract.ErrSQLSessionLost, e)
}
func (l *queryLease) connect(ctx context.Context) error {
	if l.client != nil {
		if !l.client.Alive() {
			return l.markLost(ErrRetired)
		}
		return nil
	}
	c, e := l.s.open(ctx)
	if e != nil {
		if errors.Is(e, contract.ErrRevoked) {
			return l.markLost(e)
		}
		return e
	}
	l.client = c
	return nil
}
func (l *queryLease) Preconnect(ctx context.Context) error {
	l.ops.Lock()
	defer l.ops.Unlock()
	ctx, done, e := l.request(ctx)
	if e != nil {
		return e
	}
	defer done()
	if e = l.connect(ctx); e != nil {
		return e
	}
	_, e = databaseInfo(ctx, l.client, l.s.database)
	if e != nil && (!l.client.Alive() || errors.Is(e, ErrProtocol)) {
		return l.markLost(e)
	}
	return e
}

// ExecuteGraph captures parameters before waiting for this original executor.
// One source is one native auto-commit Run. Cypher is never split as SQL.
func (l *queryLease) ExecuteGraph(ctx context.Context, req contract.GraphQueryRequest) (Response, error) {
	b := valueBudget{limit: 1 << 20}
	_, params, e := b.properties(req.Parameters, false, 0)
	if e != nil {
		return Response{}, e
	}
	req.Parameters = params
	if req.Database == "" {
		req.Database = l.s.database
	}
	if e = l.s.checkNamespace(l.s.Namespace(req.Database)); e != nil {
		return Response{}, e
	}
	l.ops.Lock()
	defer l.ops.Unlock()
	ctx, done, e := l.request(ctx)
	if e != nil {
		return Response{}, e
	}
	defer done()
	if e = l.connect(ctx); e != nil {
		return Response{}, e
	}
	plan, e := l.client.Prepare(req)
	if e != nil {
		return Response{}, e
	}
	r, e := l.client.Execute(ctx, plan)
	if r.Completed && r.Summary.Database != req.Database {
		e = errors.Join(e, ErrProtocol)
		return r, l.markLost(e)
	}
	if r.Uncertain || !l.client.Alive() {
		e = l.markLost(e)
	}
	if r.FailureCode != "" {
		e = &contract.QueryError{Cause: e, Diagnostic: contract.QueryDiagnostic{Code: r.FailureCode, SQL: req.Query, Statement: 1}}
	}
	return r, e
}
func (l *queryLease) Execute(ctx context.Context, database, source string) (contract.SQLResult, error) {
	return l.ExecuteStream(ctx, database, source, nil)
}
func (l *queryLease) ExecuteFile(ctx context.Context, database string, reader io.Reader) (contract.SQLResult, error) {
	if reader == nil {
		return contract.SQLResult{}, ErrInvalid
	}
	raw, e := io.ReadAll(io.LimitReader(reader, (8<<20)+1))
	if e != nil {
		return contract.SQLResult{}, errors.New("Neo4j Cypher file cannot be read")
	}
	if len(raw) > 8<<20 {
		return contract.SQLResult{}, ErrBounds
	}
	return l.Execute(ctx, database, string(raw))
}
func (l *queryLease) ExecuteStream(ctx context.Context, database, source string, sink func(int, []contract.Column, []contract.Cell) error) (result contract.SQLResult, e error) {
	start := time.Now()
	r, e := l.ExecuteGraph(ctx, Request{Query: source, Database: database})
	defer func() { result.Duration = time.Since(start); result.SetLegacyView() }()
	result = graphresult.Project(source, r)
	if e != nil && len(result.Steps) > 0 {
		result.Steps[0].Error = e.Error()
	}
	if sink != nil && e == nil && len(result.Sets) > 0 {
		set := result.Sets[0]
		if e = sink(0, slices.Clone(set.Columns), nil); e != nil {
			return result, e
		}
		for _, row := range set.Rows {
			if e = ctx.Err(); e != nil {
				break
			}
			cells := make([]contract.Cell, len(row))
			for i, c := range row {
				cells[i] = c.Clone()
			}
			if e = sink(0, slices.Clone(set.Columns), cells); e != nil {
				break
			}
		}
		if e != nil && len(result.Steps) > 0 {
			result.Steps[0].Error = e.Error()
		}
	}
	return result, e
}

var _ contract.GraphQueryLease = (*queryLease)(nil)
var _ contract.QueryFileLease = (*queryLease)(nil)
