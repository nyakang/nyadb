package rqlite

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type queryLease struct {
	s              *Session
	doc            uint64
	ops, mu        sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	released, lost bool
	done           chan struct{}
}

func (s *Session) ReserveQuery(doc uint64) (contract.QueryLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, contract.ErrRevoked
	}
	if doc == 0 {
		return nil, connections.ErrInvalid
	}
	if len(s.leases) >= s.config.Options.SQLMaxConnections {
		return nil, errors.New("RQLite SQL document limit reached")
	}
	ctx, cancel := context.WithCancel(context.Background())
	l := &queryLease{s: s, doc: doc, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	s.leases[l] = true
	return l, nil
}

func (l *queryLease) CheckQueryLease() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || !l.s.Alive() {
		return contract.ErrRevoked
	}
	if l.lost {
		return contract.ErrSQLSessionLost
	}
	return nil
}

func (l *queryLease) Release() {
	l.mu.Lock()
	if l.released {
		done := l.done
		l.mu.Unlock()
		<-done
		return
	}
	l.released = true
	l.cancel()
	l.mu.Unlock()
	l.ops.Lock() // A dispatched executor settles its original receipt first.
	l.s.mu.Lock()
	delete(l.s.leases, l)
	l.s.mu.Unlock()
	close(l.done)
	l.ops.Unlock()
}

func (l *queryLease) Replace() (contract.QueryLease, error) {
	l.Release()
	return l.s.ReserveQuery(l.doc)
}

func (l *queryLease) admission(ctx context.Context) (context.Context, func(), error) {
	if err := l.CheckQueryLease(); err != nil {
		return nil, nil, err
	}
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	return child, func() { stop(); cancel() }, nil
}

func (l *queryLease) Preconnect(ctx context.Context) error {
	l.ops.Lock()
	defer l.ops.Unlock()
	child, done, err := l.admission(ctx)
	if err != nil {
		return err
	}
	defer done()
	_, err = l.s.request(child, "/db/query", []any{"SELECT 1"}, false, false)
	return err
}

func (l *queryLease) Execute(ctx context.Context, database, source string) (contract.SQLResult, error) {
	return l.ExecuteStream(ctx, database, source, nil)
}

func (l *queryLease) ExecuteStream(ctx context.Context, database, source string, sink func(int, []contract.Column, []contract.Cell) error) (out contract.SQLResult, err error) {
	l.ops.Lock()
	defer l.ops.Unlock()
	child, done, err := l.admission(ctx)
	if err != nil {
		return out, err
	}
	defer done()
	if database != "" && database != "main" {
		return out, connections.ErrInvalid
	}
	parts, err := splitSQL(source)
	if err != nil {
		return out, err
	}
	stmts := make([]any, len(parts))
	for i, q := range parts {
		stmts[i] = q
	}
	path, possibleWrite := "/db/request", true
	if l.s.config.Options.ReadOnly {
		path, possibleWrite = "/db/query", false
	}
	start := time.Now()
	defer func() { out.Duration = time.Since(start); out.SetLegacyView() }()
	tx := l.s.config.Options.BatchTransaction
	r, requestError := l.s.request(child, path, stmts, tx, possibleWrite)
	out.NativeJSON = r.Raw
	if r.Uncertain || errors.Is(requestError, errIncomplete) || errors.Is(requestError, errResponseLost) {
		l.mu.Lock()
		l.lost = true
		l.mu.Unlock()
		requestError = errors.Join(contract.ErrSQLSessionLost, requestError)
	}
	rolledBack := false
	if tx && !r.Uncertain && !errors.Is(requestError, errIncomplete) {
		for _, n := range r.Results {
			if n.failed() {
				rolledBack = true
			}
		}
	}
	// Build every outcome before running a presentation sink. Sink failure
	// cannot erase an original completed write or invent a rollback.
	for i, q := range parts {
		step := contract.StatementOutcome{SQL: q, State: "completed"}
		if r.Uncertain {
			step.State, step.Error = "uncertain", requestError.Error()
		} else if i >= len(r.Results) {
			step.State = "not_executed"
			if i == 0 && requestError != nil {
				step.State, step.Error = "failed", requestError.Error()
			}
		} else {
			n := r.Results[i]
			if n.failed() {
				step.State, step.Error = "failed", n.safeError().Error()
			} else if rolledBack {
				step.State = "rolled_back"
			} else if n.Affected != "" {
				affected, e := n.Affected.Int64()
				if e != nil || affected < 0 {
					return out, errIncomplete
				}
				step.AffectedRows = affected
				out.AffectedRows += affected
			}
		}
		out.Steps = append(out.Steps, step)
	}
	if r.Uncertain || errors.Is(requestError, errIncomplete) {
		return out, requestError
	}
	budget := 8 << 20
	for _, n := range r.Results {
		if len(n.Columns) == 0 || n.failed() {
			continue
		}
		set := contract.ResultSet{Columns: make([]contract.Column, len(n.Columns))}
		if len(n.Types) != len(n.Columns) {
			return out, errIncomplete
		}
		for j, name := range n.Columns {
			set.Columns[j] = contract.Column{Name: name, DatabaseType: n.Types[j], Nullable: true}
		}
		for j, raw := range n.Values {
			row, e := n.cells(raw)
			if e != nil {
				return out, e
			}
			if sink != nil {
				if e := sink(len(out.Sets), set.Columns, row); e != nil {
					return out, e
				}
			}
			rowBytes := 0
			for _, c := range row {
				rowBytes += len(c.Text) + len(c.Bytes)
			}
			if j < 10000 && rowBytes <= budget && len(set.Truncation) == 0 {
				set.Rows = append(set.Rows, row)
				budget -= rowBytes
			} else if len(set.Truncation) == 0 {
				set.Truncation = []string{"preview_limit"}
			}
		}
		out.Sets = append(out.Sets, set)
	}
	return out, requestError
}

var _ contract.QueryLeaseStatus = (*queryLease)(nil)
