package trino

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/queryplan"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type queryLease struct {
	s              *Session
	doc            uint64
	ops            sync.Mutex
	mu             sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	client         *Client
	released, lost bool
	selected       string
}

func (s *Session) ReserveQuery(doc uint64) (contract.QueryLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, contract.ErrRevoked
	}
	if doc == 0 || len(s.leases) >= 64 {
		return nil, connections.ErrInvalid
	}
	ctx, cancel := context.WithCancel(s.ctx)
	l := &queryLease{s: s, doc: doc, ctx: ctx, cancel: cancel}
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
	return l.s.ReserveQuery(l.doc)
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
	child, done, err := l.s.request(ctx)
	if err != nil {
		return nil, nil, err
	}
	// Session revocation is already bound by s.request. Release has its own
	// cancellation scope, including a lease released while the session is alive.
	child, cancel := context.WithCancel(child)
	stop := context.AfterFunc(l.ctx, cancel)
	return child, func() { stop(); cancel(); done() }, nil
}
func (l *queryLease) connect(ctx context.Context) error {
	if l.client != nil {
		return nil
	}
	c, err := l.s.open(ctx)
	if err != nil {
		return err
	}
	l.client = c
	return nil
}
func (l *queryLease) Preconnect(ctx context.Context) error {
	l.ops.Lock()
	defer l.ops.Unlock()
	ctx, done, err := l.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	return l.connect(ctx)
}
func (l *queryLease) Execute(ctx context.Context, namespace, source string) (contract.SQLResult, error) {
	return l.ExecuteStream(ctx, namespace, source, nil)
}
func (l *queryLease) ExecuteFile(ctx context.Context, namespace string, r io.Reader) (contract.SQLResult, error) {
	if r == nil {
		return contract.SQLResult{}, connections.ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(r, (8<<20)+1))
	if err != nil {
		return contract.SQLResult{}, errors.New("Trino SQL file cannot be read")
	}
	return l.Execute(ctx, namespace, string(raw))
}
func (l *queryLease) ExecuteStream(ctx context.Context, namespace, source string, sink func(int, []contract.Column, []contract.Cell) error) (result contract.SQLResult, err error) {
	parts, err := sqltext.SplitTrino(source)
	if err != nil {
		return result, err
	}
	l.ops.Lock()
	defer l.ops.Unlock()
	ctx, done, err := l.request(ctx)
	if err != nil {
		return result, err
	}
	defer done()
	if err = l.connect(ctx); err != nil {
		return result, err
	}
	if namespace != "" && namespace != l.selected {
		n := l.s.Namespace(namespace)
		if err = l.s.checkNamespace(n); err != nil {
			return result, err
		}
		l.client.headers.Set("X-Trino-Catalog", n.Catalog)
		l.client.headers.Set("X-Trino-Schema", n.Schema)
		l.selected = namespace
	}
	start := time.Now()
	defer func() { result.Duration = time.Since(start); result.SetLegacyView() }()
	budget := previewBytes
	for _, q := range parts {
		fullPlan := queryplan.IsTrinoPlanRequest(q)
		step := contract.StatementOutcome{SQL: q, State: "completed"}
		set := contract.ResultSet{}
		reasons := map[string]bool{}
		var native Result
		native, err = l.client.Run(ctx, q, func(cols []Column, row []json.RawMessage) error {
			set.Columns = nativeColumns(cols)
			cells := make([]contract.Cell, len(row))
			for i, v := range row {
				c, e := nativeCell(v, cols[i].Type)
				if e != nil {
					return e
				}
				cells[i] = c
			}
			if sink != nil {
				copyCells := make([]contract.Cell, len(cells))
				for i, c := range cells {
					copyCells[i] = c.Clone()
				}
				if e := sink(len(result.Sets), append([]contract.Column(nil), set.Columns...), copyCells); e != nil {
					return e
				}
			}
			size := len(cells) * 64
			for i, c := range cells {
				// Native JSON EXPLAIN is one complete bounded VARCHAR cell. Keep
				// its original bytes for structural parsing and raw-plan reading.
				if !fullPlan {
					cells[i] = preview(c)
				}
				if cells[i].Truncated {
					reasons["cell"] = true
				}
				size += len(cells[i].Text) + len(cells[i].Bytes)
			}
			if len(set.Rows) >= previewRows {
				reasons["rows"] = true
				return nil
			}
			if size > budget {
				reasons["bytes"] = true
				return nil
			}
			budget -= size
			set.Rows = append(set.Rows, cells)
			return nil
		})
		if len(native.Columns) > 0 {
			set.Columns = nativeColumns(native.Columns)
			for _, k := range []string{"rows", "bytes", "cell"} {
				if reasons[k] {
					set.Truncation = append(set.Truncation, k)
				}
			}
			result.Sets = append(result.Sets, set)
		}
		if native.UpdateCount != nil {
			step.AffectedRows = *native.UpdateCount
			result.AffectedRows += step.AffectedRows
		}
		if err != nil {
			err = operationError(err)
			step.State = "failed"
			if errors.Is(err, contract.ErrSQLSessionLost) {
				step.State = "uncertain"
				l.mu.Lock()
				l.lost = true
				l.mu.Unlock()
			}
			step.Error = err.Error()
		}
		result.Steps = append(result.Steps, step)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}
