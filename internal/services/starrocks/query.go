package starrocks

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	mysqldriver "github.com/go-sql-driver/mysql"
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
	conn           *physical
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
	l.conn.Close()
	l.conn = nil
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
func (l *queryLease) markLost() {
	l.mu.Lock()
	l.lost = true
	l.mu.Unlock()
	l.conn.Close()
	l.conn = nil
}
func (l *queryLease) connect(ctx context.Context) error {
	if l.conn != nil {
		return nil
	}
	f, err := l.s.open(ctx)
	if err != nil {
		l.markLost()
		return err
	}
	l.conn = f
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

func statements(source string) ([]string, error) {
	if len(source) > 8<<20 || !utf8.ValidString(source) || strings.ContainsRune(source, 0) {
		return nil, connections.ErrInvalid
	}
	scanner := sqltext.NewScriptScanner(strings.NewReader(source))
	scanner.MaxStatementBytes = 1 << 20
	var result []string
	for {
		q, err := scanner.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(result) >= 1024 {
			return nil, connections.ErrInvalid
		}
		tokens, err := sqltext.Tokens(q)
		if err != nil {
			return nil, err
		}
		first := ""
		var code []sqltext.Token
		for _, t := range tokens {
			if t.Comment {
				if strings.HasPrefix(t.Text, "/*!") {
					return nil, contract.ErrUnsupported
				}
				continue
			}
			if t.Text != ";" {
				code = append(code, t)
			}
			if first == "" {
				first = strings.ToUpper(t.Text)
			}
			// Keep the scanner's literal rules and native state serialization
			// invariant. SQL transactions and catalog switching need independent verification.
			if first == "SET" {
				u := strings.ToUpper(t.Text)
				if strings.Contains(u, "SQL_MODE") || strings.Contains(u, "BINARY_ENCODING") || strings.Contains(u, "CATALOG") || strings.Contains(u, "ENABLE_PREPARE_STMT") || strings.Contains(u, "ENABLE_SERVER_SIDE_PREPARED_STATEMENT") || strings.Contains(u, "AUTOCOMMIT") {
					return nil, contract.ErrUnsupported
				}
			}
		}
		// USE may also switch catalogs in StarRocks. Keep single-database USE
		// and an explicit default_catalog qualifier within the verified scope.
		if first == "USE" {
			database := code[1:]
			if len(database) == 3 && database[1].Text == "." && (database[0].Text == "default_catalog" || database[0].Text == "`default_catalog`") {
				database = database[2:]
			}
			if len(database) != 1 || database[0].Quoted && !strings.HasPrefix(database[0].Text, "`") || !database[0].Quoted && strings.ContainsAny(database[0].Text, ".@/\\") {
				return nil, contract.ErrUnsupported
			}
		}
		switch first {
		case "BEGIN", "START", "COMMIT", "ROLLBACK", "SAVEPOINT", "RELEASE", "SWITCH", "PREPARE", "EXECUTE", "DEALLOCATE":
			return nil, contract.ErrUnsupported
		}
		result = append(result, q)
	}
	if len(result) == 0 {
		return nil, connections.ErrInvalid
	}
	return result, nil
}
func firstWord(q string) string {
	ts, _ := sqltext.Tokens(q)
	for _, t := range ts {
		if !t.Comment {
			return strings.ToUpper(t.Text)
		}
	}
	return ""
}
func returnsRows(q string) bool {
	switch firstWord(q) {
	case "SELECT", "WITH", "SHOW", "DESCRIBE", "DESC", "EXPLAIN":
		return true
	}
	return false
}

func (l *queryLease) Execute(ctx context.Context, database, source string) (contract.SQLResult, error) {
	return l.ExecuteStream(ctx, database, source, nil)
}
func (l *queryLease) ExecuteFile(ctx context.Context, database string, r io.Reader) (contract.SQLResult, error) {
	if r == nil {
		return contract.SQLResult{}, connections.ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(r, (8<<20)+1))
	if err != nil {
		return contract.SQLResult{}, errors.New("StarRocks SQL file cannot be read")
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
	parts, err := statements(source)
	if err != nil {
		return result, err
	}
	if database == "" {
		database = l.s.profile.Database
	}
	if database != "" && !validName(database) {
		return result, connections.ErrInvalid
	}
	if err = l.connect(child); err != nil {
		return result, err
	}
	start := time.Now()
	defer func() { result.Duration = time.Since(start); result.SetLegacyView() }()
	// Namespace selection uses this original physical lease, preserving user
	// variables and settings when moving between databases on the same FE.
	if database != "" {
		if _, err = l.conn.conn.ExecContext(child, "USE "+quote(database)); err != nil {
			return result, l.operationError(err)
		}
	}
	budget := previewBytes
	for _, q := range parts {
		step := contract.StatementOutcome{SQL: q, State: "completed"}
		if returnsRows(q) {
			var rows *sql.Rows
			var statement *sql.Stmt
			// Native SHOW/DESCRIBE do not support prepared statements; EXPLAIN
			// sends text packets. Choose the protocol before execution, without retries.
			if firstWord(q) != "SELECT" && firstWord(q) != "WITH" {
				rows, err = l.conn.conn.QueryContext(child, q)
			} else {
				statement, err = l.conn.conn.PrepareContext(child, q)
				if err == nil {
					rows, err = statement.QueryContext(child)
				}
			}
			if err == nil {
				var set contract.ResultSet
				set, err = scanRows(rows, len(result.Sets), sink, &budget, nil)
				if closeErr := rows.Close(); err == nil {
					err = closeErr
				}
				result.Sets = append(result.Sets, set)
			}
			if statement != nil {
				if closeErr := statement.Close(); err == nil {
					err = closeErr
				}
			}
		} else {
			var r sql.Result
			r, err = l.conn.conn.ExecContext(child, q)
			if err == nil {
				step.AffectedRows, _ = r.RowsAffected()
				result.AffectedRows += step.AffectedRows
			}
		}
		if err != nil {
			err = l.operationError(err)
			step.State = "failed"
			if errors.Is(err, contract.ErrSQLSessionLost) {
				step.State = "uncertain"
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
func (l *queryLease) operationError(err error) error {
	var server *mysqldriver.MySQLError
	// An acknowledged server rejection leaves the physical session usable.
	if errors.As(err, &server) {
		return safeError(err)
	}
	l.markLost()
	return errors.Join(contract.ErrSQLSessionLost, safeError(err))
}
