package clickhouse

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
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
	conn           driver.Conn
	database       string
	released, lost bool
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
	ctx, cancel := context.WithCancel(s.ctx)
	l := &queryLease{s: s, doc: doc, ctx: ctx, cancel: cancel, database: s.profile.Database}
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
		_ = l.conn.Close()
		l.conn = nil
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
func (l *queryLease) connect(ctx context.Context, database string) error {
	if database == "" {
		database = l.s.profile.Database
	}
	if !validName(database) {
		return connections.ErrInvalid
	}
	if l.conn != nil && l.database != database {
		_ = l.conn.Close()
		l.conn = nil
	}
	l.database = database
	if l.conn != nil {
		return nil
	}
	l.s.mu.Lock()
	password := l.s.password
	closed := l.s.closed
	l.s.mu.Unlock()
	if closed {
		return contract.ErrRevoked
	}
	c, err := openNative(l.s.profile, password, database, true)
	if err == nil {
		err = c.Ping(ctx)
	}
	if err != nil {
		if c != nil {
			_ = c.Close()
		}
		l.markLost()
		return safeError(err)
	}
	l.conn = c
	return nil
}
func (l *queryLease) markLost() { l.mu.Lock(); l.lost = true; l.mu.Unlock() }
func (l *queryLease) Preconnect(ctx context.Context) error {
	l.ops.Lock()
	defer l.ops.Unlock()
	child, done, err := l.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	return l.connect(child, l.database)
}
func (l *queryLease) Execute(ctx context.Context, database, source string) (contract.SQLResult, error) {
	return l.ExecuteStream(ctx, database, source, nil)
}
func (l *queryLease) ExecuteStream(ctx context.Context, database, source string, sink func(int, []contract.Column, []contract.Cell) error) (result contract.SQLResult, err error) {
	l.ops.Lock()
	defer l.ops.Unlock()
	child, done, err := l.request(ctx)
	if err != nil {
		return result, err
	}
	defer done()
	parts, err := statements(source)
	if err != nil {
		return result, err
	}
	if len(parts) == 0 {
		return result, connections.ErrInvalid
	}
	if err = l.connect(child, database); err != nil {
		return result, err
	}
	start := time.Now()
	defer func() { result.Duration = time.Since(start); result.SetLegacyView() }()
	budget := previewBytes
	for _, q := range parts {
		step := contract.StatementOutcome{SQL: q, State: "completed"}
		if returnsRows(q) {
			var rows driver.Rows
			rows, err = l.conn.Query(child, q)
			if err == nil {
				var set contract.ResultSet
				set, err = scanRows(rows, len(result.Sets), sink, &budget)
				_ = rows.Close()
				result.Sets = append(result.Sets, set)
			}
		} else {
			err = l.conn.Exec(child, q)
		}
		if err != nil {
			// Native errors/cancellation discard the physical connection. Never
			// redial/replay or describe an unacknowledged mutation as rolled back.
			l.markLost()
			err = errors.Join(contract.ErrSQLSessionLost, safeError(err))
			step.State = "uncertain"
			step.Error = err.Error()
		}
		result.Steps = append(result.Steps, step)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func statements(source string) ([]string, error) {
	return sqltext.SplitClickHouse(source)
}
func returnsRows(q string) bool {
	tokens, err := sqltext.ClickHouseTokens(q)
	if err != nil || len(tokens) == 0 {
		return false
	}
	for _, token := range tokens {
		if token.Comment {
			continue
		}
		switch strings.ToUpper(token.Text) {
		case "SELECT", "WITH", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "EXISTS", "CHECK":
			return true
		}
		break
	}
	return false
}
