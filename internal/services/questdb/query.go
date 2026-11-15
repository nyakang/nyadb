package questdb

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type queryLease struct {
	s                    *Session
	doc                  uint64
	mu, ops              sync.Mutex
	ctx                  context.Context
	cancel               context.CancelFunc
	conn                 *pgx.Conn
	released, lost, slot bool
	releasedDone         chan struct{}
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
	l := &queryLease{s: s, doc: doc, ctx: ctx, cancel: cancel, releasedDone: make(chan struct{})}
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
		l.mu.Unlock()
		<-l.releasedDone
		return
	}
	l.released = true
	l.cancel()
	l.mu.Unlock()
	l.ops.Lock()
	closeConnection(l.conn)
	l.conn = nil
	if l.slot {
		<-l.s.sqlSlots
		l.slot = false
	}
	l.ops.Unlock()
	l.s.mu.Lock()
	delete(l.s.leases, l)
	l.s.mu.Unlock()
	close(l.releasedDone)
}
func (l *queryLease) Replace() (contract.QueryLease, error) {
	l.Release()
	return l.s.ReserveQuery(l.doc)
}
func (l *queryLease) connect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.conn != nil {
		if l.conn.IsClosed() {
			return contract.ErrSQLSessionLost
		}
		return nil
	}
	if !l.slot {
		select {
		case l.s.sqlSlots <- struct{}{}:
			l.slot = true
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c, err := l.s.connect(ctx)
	if err != nil {
		// No SQL has been dispatched and no physical session was obtained.
		// Failed or cancelled handshakes release capacity without retiring
		// the reserved document or replaying a request.
		<-l.s.sqlSlots
		l.slot = false
		return safeError(err, l.s.profileSecret())
	}
	l.conn = c
	return nil
}
func (l *queryLease) request(ctx context.Context) (context.Context, func(), error) {
	if err := l.CheckQueryLease(); err != nil {
		return nil, nil, err
	}
	child, done, err := l.s.request(ctx)
	if err != nil {
		return nil, nil, err
	}
	// A separate lease cancellation scope reaches the captured executor.
	leaseChild, cancel := context.WithCancel(child)
	stop := context.AfterFunc(l.ctx, cancel)
	return leaseChild, func() { stop(); cancel(); done() }, nil
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

func (l *queryLease) Execute(ctx context.Context, database, source string) (contract.SQLResult, error) {
	return l.ExecuteStream(ctx, database, source, nil)
}
func (l *queryLease) ExecuteFile(ctx context.Context, database string, reader io.Reader) (contract.SQLResult, error) {
	if reader == nil {
		return contract.SQLResult{}, connections.ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(reader, (8<<20)+1))
	if err != nil {
		return contract.SQLResult{}, err
	}
	if len(raw) > 8<<20 {
		return contract.SQLResult{}, contract.ErrUnsupported
	}
	return l.Execute(ctx, database, string(raw))
}
func (l *queryLease) ExecuteStream(ctx context.Context, database, source string, sink func(int, []contract.Column, []contract.Cell) error) (result contract.SQLResult, err error) {
	l.ops.Lock()
	defer l.ops.Unlock()
	child, done, err := l.request(ctx)
	if err != nil {
		return result, err
	}
	defer done()
	if database != "" && database != "qdb" {
		return result, connections.ErrInvalid
	}
	if len(source) > 8<<20 {
		return result, contract.ErrUnsupported
	}
	parts, err := sqltext.SplitPortable(source)
	if err != nil || len(parts) == 0 {
		return result, connections.ErrInvalid
	}
	if l.s.profile.QuestDBOptions.ReadOnly {
		for _, q := range parts {
			if !readonly(q) {
				return result, contract.ErrUnsupported
			}
		}
	}
	if err = l.connect(child); err != nil {
		return result, err
	}
	start := time.Now()
	defer func() { result.Duration = time.Since(start); result.SetLegacyView() }()
	budget := previewBytes
	for _, q := range parts {
		if err := child.Err(); err != nil {
			return result, err
		}
		set, e := execute(child, l.conn, q, nil, len(result.Sets), sink, &budget)
		step := contract.StatementOutcome{SQL: q, State: "completed", AffectedRows: set.AffectedRows}
		if len(set.Columns) > 0 {
			result.Sets = append(result.Sets, set)
		}
		result.AffectedRows += set.AffectedRows
		if e != nil {
			var server *pgconn.PgError
			step.State = "failed"
			if !errors.As(e, &server) || l.conn.IsClosed() {
				l.mu.Lock()
				l.lost = true
				l.mu.Unlock()
				step.State = "uncertain"
				e = errors.Join(contract.ErrSQLSessionLost, safeError(e, l.s.profileSecret()))
			} else {
				e = safeError(e, l.s.profileSecret())
			}
			step.Error = e.Error()
			err = e
		}
		result.Steps = append(result.Steps, step)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func readonly(source string) bool {
	tokens, err := sqltext.ClickHouseTokens(source)
	if err != nil {
		return false
	}
	first := ""
	for _, t := range tokens {
		if t.Comment || t.Quoted {
			continue
		}
		word := strings.ToUpper(t.Text)
		if first == "" {
			first = word
		}
		switch word {
		case "INSERT", "UPDATE", "DELETE", "CREATE", "ALTER", "DROP", "TRUNCATE", "COPY", "RENAME", "BACKUP", "VACUUM", "REPAIR":
			return false
		}
	}
	return first == "SELECT" || first == "WITH" || first == "SHOW" || first == "EXPLAIN"
}
