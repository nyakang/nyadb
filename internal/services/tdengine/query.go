package tdengine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

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
	conn                 *wire
	released, lost, slot bool
	database             string
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
	l := &queryLease{s: s, doc: doc, ctx: ctx, cancel: cancel, database: s.profile.Database, releasedDone: make(chan struct{})}
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
	if l.conn != nil {
		l.conn.close()
		l.conn = nil
	}
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
func (l *queryLease) request(ctx context.Context) (context.Context, func(), error) {
	if err := l.CheckQueryLease(); err != nil {
		return nil, nil, err
	}
	child, done, err := l.s.request(ctx)
	if err != nil {
		return nil, nil, err
	}
	leaseChild, cancel := context.WithCancel(child)
	stop := context.AfterFunc(l.ctx, cancel)
	return leaseChild, func() { stop(); cancel(); done() }, nil
}
func (l *queryLease) connect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.conn != nil {
		return nil
	}
	select {
	case l.s.sqlSlots <- struct{}{}:
		l.slot = true
	case <-ctx.Done():
		return ctx.Err()
	}
	w, err := l.s.connect(ctx)
	if err != nil {
		<-l.s.sqlSlots
		l.slot = false
		return safeError(err, l.s.secret())
	}
	l.conn = w
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
func (l *queryLease) Execute(ctx context.Context, db, sql string) (contract.SQLResult, error) {
	return l.ExecuteStream(ctx, db, sql, nil)
}
func (l *queryLease) ExecuteFile(ctx context.Context, db string, r io.Reader) (contract.SQLResult, error) {
	if r == nil {
		return contract.SQLResult{}, connections.ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(r, (8<<20)+1))
	if err != nil {
		return contract.SQLResult{}, err
	}
	if len(raw) > 8<<20 {
		return contract.SQLResult{}, contract.ErrUnsupported
	}
	return l.Execute(ctx, db, string(raw))
}
func (l *queryLease) ExecuteStream(ctx context.Context, db, source string, sink func(int, []contract.Column, []contract.Cell) error) (result contract.SQLResult, err error) {
	l.ops.Lock()
	defer l.ops.Unlock()
	child, done, err := l.request(ctx)
	if err != nil {
		return result, err
	}
	defer done()
	if db != "" && !validName(db) || len(source) > 8<<20 {
		return result, connections.ErrInvalid
	}
	parts, e := sqltext.SplitClickHouse(source)
	if e != nil || len(parts) == 0 {
		return result, connections.ErrInvalid
	}
	if l.s.profile.TDengineOptions.ReadOnly {
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
	receipts := []json.RawMessage{}
	defer func() {
		result.Duration = time.Since(start)
		result.SetLegacyView()
		if len(receipts) > 0 {
			raw, _ := json.Marshal(receipts)
			result.NativeJSON = string(raw)
		}
	}()
	if db != "" {
		_, receipt, e := l.conn.query(child, "USE "+quote(db), nil, nil)
		if len(receipt) > 0 {
			receipts = append(receipts, receipt)
		}
		if e != nil {
			return result, l.settle(e)
		}
		l.database = db
	}
	budget := 16 << 20
	for _, q := range parts {
		if err = child.Err(); err != nil {
			return result, err
		}
		index := len(result.Sets)
		var stream func([]contract.Column, []contract.Cell) error
		if sink != nil {
			stream = func(cols []contract.Column, row []contract.Cell) error { return sink(index, cols, row) }
		}
		set, receipt, e := l.conn.query(child, q, stream, &budget)
		if len(receipt) > 0 {
			receipts = append(receipts, receipt)
		}
		if len(set.Columns) > 0 {
			result.Sets = append(result.Sets, set)
		}
		result.AffectedRows += set.AffectedRows
		step := contract.StatementOutcome{SQL: q, State: "completed", AffectedRows: set.AffectedRows}
		if e != nil {
			step.State = "failed"
			if !isServerError(e) {
				step.State = "uncertain"
			}
			err = l.settle(e)
			step.Error = err.Error()
		}
		result.Steps = append(result.Steps, step)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}
func (l *queryLease) settle(err error) error {
	if isServerError(err) {
		return safeError(err, l.s.secret())
	}
	l.mu.Lock()
	l.lost = true
	l.mu.Unlock()
	if l.conn != nil {
		l.conn.close()
	}
	return errors.Join(contract.ErrSQLSessionLost, safeError(err, l.s.secret()))
}

func readonly(source string) bool {
	tokens, err := sqltext.ClickHouseTokens(source)
	if err != nil {
		return false
	}
	first := ""
	for _, token := range tokens {
		if token.Quoted || token.Comment {
			continue
		}
		word := strings.ToUpper(token.Text)
		if first == "" {
			first = word
		}
		switch word {
		case "CREATE":
			if first != "SHOW" {
				return false
			}
		case "INSERT", "UPDATE", "DELETE", "DROP", "ALTER", "TRUNCATE", "GRANT", "REVOKE", "USE", "KILL", "SET", "RESET", "BEGIN", "COMMIT", "ROLLBACK", "IMPORT", "EXPORT", "RESTORE", "INTO":
			return false
		}
	}
	return first == "SELECT" || first == "SHOW" || first == "DESCRIBE" || first == "DESC" || first == "WITH"
}
