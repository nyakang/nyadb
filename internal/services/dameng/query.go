package dameng

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

type queryLease struct {
	s              *Session
	doc            uint64
	ops, mu        sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	conn           *sql.Conn
	released, lost bool
	releaseDone    chan struct{}
	requests       sync.WaitGroup
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
	select {
	case s.slots <- struct{}{}:
	default:
		return nil, contract.ErrSQLSessionLimit
	}
	ctx, cancel := context.WithCancel(s.ctx)
	l := &queryLease{s: s, doc: doc, ctx: ctx, cancel: cancel, releaseDone: make(chan struct{})}
	s.leases[l] = struct{}{}
	return l, nil
}

func (l *queryLease) Release() {
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		<-l.releaseDone
		return
	}
	l.released = true
	l.cancel()
	l.mu.Unlock()
	l.ops.Lock()
	l.retire()
	l.ops.Unlock()
	l.requests.Wait()
	l.s.mu.Lock()
	delete(l.s.leases, l)
	l.s.mu.Unlock()
	<-l.s.slots
	close(l.releaseDone)
}

// ErrBadConn is confined to explicit disposal of this captured sql.Conn. It
// is never returned from a submitted command, so database/sql cannot replay it.
func (l *queryLease) retire() {
	if l.conn != nil {
		_ = l.conn.Raw(func(any) error { return driver.ErrBadConn })
		_ = l.conn.Close()
		l.conn = nil
	}
}
func (l *queryLease) Replace() (contract.QueryLease, error) {
	l.Release()
	return l.s.ReserveQuery(l.doc)
}
func (l *queryLease) request(ctx context.Context) (context.Context, context.CancelFunc, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || !l.s.Alive() {
		return nil, nil, contract.ErrRevoked
	}
	if l.lost {
		return nil, nil, contract.ErrSQLSessionLost
	}
	l.requests.Add(1)
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	return child, func() { stop(); cancel(); l.requests.Done() }, nil
}
func (l *queryLease) connect(ctx context.Context) error {
	if l.conn != nil {
		return nil
	}
	c, err := l.s.sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	if err = c.PingContext(ctx); err != nil {
		_ = c.Raw(func(any) error { return driver.ErrBadConn })
		return errors.Join(err, c.Close())
	}
	l.conn = c
	return nil
}
func (l *queryLease) Preconnect(ctx context.Context) error {
	l.ops.Lock()
	defer l.ops.Unlock()
	child, done, err := l.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	return l.connect(child)
}
func (l *queryLease) Execute(ctx context.Context, schema, source string) (contract.SQLResult, error) {
	return l.ExecuteStream(ctx, schema, source, nil)
}
func (l *queryLease) ExecuteStream(ctx context.Context, schema, source string, sink func(int, []contract.Column, []contract.Cell) error) (result contract.SQLResult, err error) {
	l.ops.Lock()
	defer l.ops.Unlock()
	child, done, err := l.request(ctx)
	if err != nil {
		return result, err
	}
	defer done()
	if schema != "" && !validName(schema) {
		return result, connections.ErrInvalid
	}
	parts, err := sqltext.SplitDameng(source)
	if err != nil {
		return result, err
	}
	if len(parts) == 0 {
		return result, connections.ErrInvalid
	}
	if err = child.Err(); err != nil {
		return result, err
	}
	if err = l.connect(child); err != nil {
		return result, err
	}
	start := time.Now()
	defer func() { result.Duration = time.Since(start); result.SetLegacyView() }()
	lose := func(e error) error {
		l.mu.Lock()
		l.lost = true
		l.mu.Unlock()
		l.retire()
		return errors.Join(contract.ErrSQLSessionLost, e)
	}
	if schema != "" {
		// User SQL can change CURRENT_SCHEMA. Reapply and read it back for each
		// host request, instead of trusting a cached schema from a prior batch.
		err = l.conn.Raw(func(raw any) error {
			c, ok := raw.(driver.Conn)
			if !ok {
				return contract.ErrUnsupported
			}
			return dpi.InitializeSchema(child, c, schema)
		})
		if err != nil {
			if connectionLost(err) {
				return result, lose(err)
			}
			return result, err
		}
	}
	budget, remaining := 8<<20, 1000
	for i, q := range parts {
		if err = child.Err(); err != nil {
			return result, lose(err)
		}
		step := contract.StatementOutcome{SQL: q, State: "completed"}
		var e error
		if sqltext.DamengQuery(q) {
			var rows *sql.Rows
			rows, e = l.conn.QueryContext(child, q)
			if e == nil {
				var set contract.ResultSet
				set, e = scanRows(rows, len(result.Sets), sink, &budget, remaining)
				remaining -= len(set.Rows)
				result.Sets = append(result.Sets, set)
				e = errors.Join(e, rows.Err(), rows.Close())
			}
		} else {
			var executed sql.Result
			executed, e = l.conn.ExecContext(child, q)
			if e == nil {
				step.AffectedRows, _ = executed.RowsAffected()
				result.AffectedRows += step.AffectedRows
				result.Sets = append(result.Sets, contract.ResultSet{AffectedRows: step.AffectedRows})
			}
		}
		if e != nil {
			step.State = "failed"
			if connectionLost(e) {
				e = lose(e)
			}
			if errors.Is(e, dpi.ErrOutcomeUnknown) {
				step.State = "uncertain"
			}
			var native *dpi.Error
			if errors.As(e, &native) {
				e = &contract.QueryError{Cause: e, Diagnostic: contract.QueryDiagnostic{Code: strconv.FormatInt(int64(native.Code), 10), SQL: q, Statement: i + 1}}
			}
			step.Error = e.Error()
		}
		result.Steps = append(result.Steps, step)
		if e != nil {
			return result, e
		}
	}
	return result, nil
}

func connectionLost(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, dpi.ErrSessionClosed) || errors.Is(err, dpi.ErrOutcomeUnknown) || errors.Is(err, sql.ErrConnDone)
}

func (l *queryLease) ExecuteFile(ctx context.Context, schema string, reader io.Reader) (contract.SQLResult, error) {
	child, done, err := l.request(ctx)
	if err != nil {
		return contract.SQLResult{}, err
	}
	defer done()
	if reader == nil {
		return contract.SQLResult{}, errors.New("SQL file reader is absent")
	}
	data, err := io.ReadAll(io.LimitReader(scriptReader{child, reader}, (8<<20)+1))
	if err != nil {
		return contract.SQLResult{}, err
	}
	return l.Execute(child, schema, strings.TrimPrefix(string(data), "\uFEFF"))
}

type scriptReader struct {
	ctx context.Context
	r   io.Reader
}

func (r scriptReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

var _ contract.QueryLease = (*queryLease)(nil)
var _ contract.QueryFileLease = (*queryLease)(nil)
