package sqlserver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/golang-sql/sqlexp"

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
	parts, err := sqltext.SplitSQLServer(source)
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
	budget, remaining := 8<<20, 1000
	for _, q := range parts {
		step := contract.StatementOutcome{SQL: q, State: "completed"}
		var rows *sql.Rows
		messages := &sqlexp.ReturnMessage{}
		rows, err = l.conn.QueryContext(child, q, messages)
		if err == nil {
			active, hasResult := true, false
			for active && err == nil {
				switch message := messages.Message(child).(type) {
				case sqlexp.MsgNext:
					var set contract.ResultSet
					maxCell := 64 << 10
					if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(source)), "SET SHOWPLAN_XML ON\nGO\n") {
						maxCell = 8 << 20
					}
					set, err = scanRows(rows, len(result.Sets), streamSink(sink), &budget, remaining, maxCell)
					remaining -= len(set.Rows)
					hasResult = len(set.Columns) > 0
					if hasResult {
						result.Sets = append(result.Sets, set)
					}
				case sqlexp.MsgNextResultSet:
					active = rows.NextResultSet()
					hasResult = false
				case sqlexp.MsgRowsAffected:
					if !hasResult {
						step.AffectedRows += message.Count
						result.AffectedRows += message.Count
					}
				case sqlexp.MsgError:
					err = message.Error
				}
			}
			if err == nil {
				err = rows.Err()
			}
			_ = rows.Close()
		}
		if err != nil {
			step.State = "uncertain"
			err = safeError(err)
			// SQL batches can leave partial writes or a live transaction even
			// after an acknowledged attention. Revoke this physical lease and
			// require explicit recovery; never replay a batch.
			l.mu.Lock()
			l.lost = true
			l.mu.Unlock()
			_ = l.conn.Raw(func(any) error { return driver.ErrBadConn })
			_ = l.conn.Close()
			l.conn = nil
			err = errors.Join(contract.ErrSQLSessionLost, err)
			step.Error = err.Error()
		}
		result.Steps = append(result.Steps, step)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}
