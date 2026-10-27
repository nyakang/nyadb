package connections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	MaxSQLResultRows    = 1000
	maxSQLResultBytes   = 8 << 20
	maxSQLCellBytes     = 64 << 10
	maxSQLResultColumns = 512
)

var ErrEmptySQL = errors.New("SQL statement is empty")

// MySQLSQLResult is bounded and detached from driver-owned *sql.Rows.
type MySQLSQLResult struct {
	Columns      []string
	Rows         [][]string
	AffectedRows int64
	Duration     time.Duration
	Truncated    bool
}

// ExecuteSQL runs user-authored SQL on the session identified by profileID.
// Each execution uses its own reserved connection and optional schema.
// Cancellation does not imply that a previously submitted write is rolled back.
func (m *MySQLSessionManager) ExecuteSQL(ctx context.Context, profileID int64, database, statement string) (result MySQLSQLResult, execErr error) {
	if strings.TrimSpace(statement) == "" {
		return result, ErrEmptySQL
	}
	if err := validSQLSchema(database); err != nil {
		return result, err
	}

	m.mu.Lock()
	session := m.sessions[profileID]
	m.mu.Unlock()
	if session == nil {
		return result, ErrMySQLNotConnected
	}

	queryCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(session.ctx, cancel)
	defer func() { stop(); cancel() }()

	conn, err := session.db.Conn(queryCtx)
	if err != nil {
		return result, err
	}
	defer conn.Close()

	return executeSQLOnConn(queryCtx, conn, database, statement)
}

// executeSQLOnConn is shared by request-scoped queries and tab-owned leases.
func executeSQLOnConn(queryCtx context.Context, conn *sql.Conn, database, statement string) (result MySQLSQLResult, execErr error) {
	if database != "" {
		if _, err := conn.ExecContext(queryCtx, "USE "+quoteSQLSchema(database)); err != nil {
			return result, fmt.Errorf("select database %q: %w", database, err)
		}
	}

	start := time.Now()
	defer func() { result.Duration = time.Since(start) }()

	if !sqlReturnsRows(statement) {
		execResult, err := conn.ExecContext(queryCtx, statement)
		if err != nil {
			return result, err
		}
		if err := queryCtx.Err(); err != nil {
			return result, err
		}
		result.AffectedRows, _ = execResult.RowsAffected()
		return result, nil
	}

	rows, err := conn.QueryContext(queryCtx, statement)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return result, err
	}
	if len(columns) > maxSQLResultColumns {
		return result, errors.New("SQL result has too many columns")
	}
	result.Columns = columns

	values := make([]sql.RawBytes, len(columns))
	dests := make([]any, len(columns))
	for i := range values {
		dests[i] = &values[i]
	}

	totalBytes := 0
	for rows.Next() {
		if len(result.Rows) >= MaxSQLResultRows || totalBytes >= maxSQLResultBytes {
			result.Truncated = true
			break
		}
		if err := rows.Scan(dests...); err != nil {
			return result, err
		}
		record := make([]string, len(columns))
		rowBytes := 0
		overBudget := false
		for i, v := range values {
			if v == nil {
				record[i] = "NULL"
				continue
			}
			if len(v) > maxSQLCellBytes {
				v = v[:maxSQLCellBytes]
				result.Truncated = true
			}
			if rowBytes+len(v)+totalBytes > maxSQLResultBytes {
				overBudget = true
				break
			}
			record[i] = string(v)
			rowBytes += len(v)
		}
		if overBudget {
			result.Truncated = true
			break // never append half a record or exceed the global byte cap
		}
		totalBytes += rowBytes
		result.Rows = append(result.Rows, record)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if err := queryCtx.Err(); err != nil {
		return result, err
	}
	return result, nil
}

func validSQLSchema(database string) error {
	if len(database) > 256 || strings.ContainsRune(database, 0) {
		return fmt.Errorf("%w: invalid schema", ErrInvalid)
	}
	return nil
}

func quoteSQLSchema(database string) string {
	return "`" + strings.ReplaceAll(database, "`", "``") + "`"
}

// SQL with a row-producing leading command uses QueryContext; DML/DDL
// uses ExecContext to expose the affected-row count. Leading comments are
// accepted without stripping or rewriting the submitted statement.
func sqlReturnsRows(statement string) bool {
	s := strings.TrimSpace(statement)
	for {
		switch {
		case strings.HasPrefix(s, "--") && (len(s) == 2 || s[2] == ' ' || s[2] == '\n'):
			index := strings.IndexByte(s, '\n')
			if index < 0 {
				return false
			}
			s = strings.TrimSpace(s[index+1:])
		case strings.HasPrefix(s, "#"):
			index := strings.IndexByte(s, '\n')
			if index < 0 {
				return false
			}
			s = strings.TrimSpace(s[index+1:])
		case strings.HasPrefix(s, "/*"):
			index := strings.Index(s, "*/")
			if index < 0 {
				return false
			}
			s = strings.TrimSpace(s[index+2:])
		default:
			fields := strings.Fields(s)
			if len(fields) == 0 {
				return false
			}
			switch strings.ToUpper(fields[0]) {
			case "SELECT", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "WITH", "TABLE", "VALUES", "CALL":
				return true
			}
			return false
		}
	}
}
