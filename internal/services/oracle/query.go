package oracle

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"github.com/sijms/go-ora/v2/network"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
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
	conn           *sql.Conn
	released, lost bool
	schema         string
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
	l := &queryLease{s: s, doc: doc, ctx: ctx, cancel: cancel}
	s.leases[l] = true
	return l, nil
}
func (l *queryLease) Release() {
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return
	}
	l.released = true
	l.cancel()
	l.mu.Unlock()
	l.ops.Lock()
	if l.conn != nil {
		_ = l.conn.Raw(func(any) error { return driver.ErrBadConn })
		_ = l.conn.Close()
		l.conn = nil
	}
	l.ops.Unlock()
	l.s.mu.Lock()
	delete(l.s.leases, l)
	l.s.mu.Unlock()
	<-l.s.slots
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
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	return child, func() { stop(); cancel() }, nil
}
func (l *queryLease) connect(ctx context.Context) error {
	if l.conn != nil {
		return nil
	}
	c, err := l.s.sqlDB.Conn(ctx)
	if err != nil {
		return safeError(err)
	}
	if err = c.PingContext(ctx); err != nil {
		_ = c.Close()
		return safeError(err)
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
	parts, err := sqltext.SplitOracle(source)
	if err != nil {
		return result, err
	}
	if len(parts) == 0 {
		return result, connections.ErrInvalid
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
		_ = l.conn.Raw(func(any) error { return driver.ErrBadConn })
		_ = l.conn.Close()
		l.conn = nil
		return errors.Join(contract.ErrSQLSessionLost, safeError(e))
	}
	if schema != "" && l.schema != schema {
		if _, err = l.conn.ExecContext(child, "ALTER SESSION SET CURRENT_SCHEMA="+quote(schema)); err != nil {
			if connectionLost(err) {
				return result, lose(err)
			}
			return result, safeError(err)
		}
		l.schema = schema
	}
	budget, remaining := 8<<20, 1000
	for _, q := range parts {
		step := contract.StatementOutcome{SQL: q, State: "completed"}
		var e error
		if sqltext.OracleQuery(q) {
			var rows *sql.Rows
			rows, e = l.conn.QueryContext(child, q)
			if e == nil {
				for {
					var set contract.ResultSet
					set, e = scanRows(rows, len(result.Sets), sink, &budget, remaining, 64<<10)
					remaining -= len(set.Rows)
					if len(set.Columns) > 0 {
						result.Sets = append(result.Sets, set)
					}
					if e != nil || !rows.NextResultSet() {
						break
					}
				}
				if e == nil {
					e = rows.Err()
				}
				_ = rows.Close()
			}
		} else {
			var executed sql.Result
			executed, e = l.conn.ExecContext(child, q)
			if e == nil {
				step.AffectedRows, _ = executed.RowsAffected()
				result.AffectedRows += step.AffectedRows
			}
		}
		if e != nil {
			step.State = "failed"
			if connectionLost(e) {
				step.State = "uncertain"
				err = lose(e)
			} else {
				err = safeError(e)
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

func connectionLost(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errWireLost) {
		return true
	}
	var ora *network.OracleError
	if errors.As(err, &ora) {
		switch ora.ErrCode {
		case 3113, 3114, 3135, 12537, 12547, 12570, 12571:
			return true
		}
		return false
	}
	// Unknown transport failures cannot prove that the physical session survived.
	return err != nil
}
