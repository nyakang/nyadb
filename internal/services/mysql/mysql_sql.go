package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqlresult"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

const (
	MaxSQLResultRows    = 1000
	maxSQLResultBytes   = 8 << 20
	maxSQLCellBytes     = 64 << 10
	maxSQLResultColumns = 512
)

var ErrEmptySQL = errors.New("SQL statement is empty")

// MySQLSQLResult is bounded and detached from driver-owned *sql.Rows.
type MySQLSQLResult = contract.SQLResult

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

	session, queryCtx, cancel, err := m.requestContext(ctx, profileID, 0)
	if err != nil {
		return result, err
	}
	defer cancel()

	conn, err := session.db.Conn(queryCtx)
	if err != nil {
		return result, err
	}
	defer conn.Close()

	return executeSQLOnConn(queryCtx, conn, database, statement)
}

// executeSQLOnConn is shared by request-scoped queries and tab-owned leases.
func executeSQLOnConn(queryCtx context.Context, conn *sql.Conn, database, statement string) (result MySQLSQLResult, execErr error) {
	return executeSQLOnConnWithSink(queryCtx, conn, database, statement, nil)
}
func executeSQLOnConnWithSink(queryCtx context.Context, conn *sql.Conn, database, statement string, sink sqlresult.Sink) (result MySQLSQLResult, execErr error) {
	if database != "" {
		if _, err := conn.ExecContext(queryCtx, "USE "+quoteSQLSchema(database)); err != nil {
			return result, fmt.Errorf("select database %q: %w", database, err)
		}
	}

	start := time.Now()
	defer func() { result.Duration = time.Since(start) }()
	if strings.Contains(statement, ";") || strings.Contains(strings.ToUpper(statement), "DELIMITER") {
		return executeSQLScriptOnConn(queryCtx, conn, strings.NewReader(statement), sink)
	}
	return executeSQLStatementOnConn(queryCtx, conn, statement, sqlresult.Preview, sink)
}

func executeSQLStatementOnConn(queryCtx context.Context, conn *sql.Conn, statement string, limits sqlresult.Limits, sink sqlresult.Sink) (result MySQLSQLResult, execErr error) {

	if !sqlReturnsRows(statement) {
		execResult, err := conn.ExecContext(queryCtx, statement)
		if err != nil {
			return result, err
		}
		if err := queryCtx.Err(); err != nil {
			return result, err
		}
		result.AffectedRows, _ = execResult.RowsAffected()
		result.Sets = []contract.ResultSet{{AffectedRows: result.AffectedRows}}
		if sink != nil {
			if err := sink(0, []contract.Column{}, nil); err != nil {
				return result, err
			}
		}
		return result, nil
	}

	rows, err := conn.QueryContext(queryCtx, statement)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	return sqlresult.Scan(queryCtx, rows, limits, sink)
}

func validSQLSchema(database string) error {
	if len(database) > 256 || strings.ContainsRune(database, 0) {
		return fmt.Errorf("%w: invalid schema", connections.ErrInvalid)
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
	tokens, err := sqltext.Tokens(statement)
	if err != nil {
		return false
	}
	with, leading, depth := false, true, 0
	for _, token := range tokens {
		if token.Comment || token.Quoted {
			continue
		}
		word := strings.ToUpper(token.Text)
		if leading {
			leading = false
			with = word == "WITH"
			if !with {
				switch word {
				case "SELECT", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "TABLE", "VALUES", "CALL":
					return true
				}
				return false
			}
			continue
		}
		if token.Text == "(" {
			depth++
			continue
		}
		if token.Text == ")" {
			depth--
			continue
		}
		if with && depth == 0 {
			switch word {
			case "SELECT", "TABLE", "VALUES":
				return true
			case "UPDATE", "DELETE", "INSERT", "REPLACE":
				return false
			}
		}
	}
	return false
}
