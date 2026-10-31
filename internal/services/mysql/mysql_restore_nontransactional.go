package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// NontransactionalRestoreOutcome counts acknowledged INSERTs. A partial or
// uncertain outcome never implies that earlier MyISAM/MEMORY writes rolled back.
type NontransactionalRestoreOutcome struct {
	State, RowState        string
	RowsApplied, FailedRow int64
	Error                  error
}

// restoreNontransactionalData owns source and uses the restore operation's
// original physical connection. It never asks database/sql to retry a write.
func restoreNontransactionalData(ctx context.Context, c *sql.Conn, database, table string, source MySQLDataImportRowSource, expected int64, valid func() bool) (out NontransactionalRestoreOutcome) {
	out.State = "not_executed"
	out.RowState = "not_executed"
	if source == nil || c == nil || expected < 0 {
		out.Error = ErrMySQLDataPage
		return out
	}
	defer func() {
		if e := source.Close(); e != nil && out.Error == nil {
			out.Error = e
			if out.RowsApplied > 0 {
				out.State = "partial"
			}
		}
	}()
	target, e := mysqlEditTable(database, table)
	if e != nil {
		out.Error = e
		return out
	}
	allowed := func() bool { return ctx.Err() == nil && (valid == nil || valid()) }
	fail := func(e error, row int64, uncertain bool) NontransactionalRestoreOutcome {
		out.Error = e
		out.FailedRow = row
		out.RowState = "not_executed"
		if uncertain {
			out.State = "uncertain"
			out.RowState = "uncertain"
		} else if out.RowsApplied > 0 {
			out.State = "partial"
		}
		return out
	}
	if !allowed() {
		return fail(errors.Join(ctx.Err(), ErrMySQLNotConnected), 0, false)
	}
	var engine, kind string
	if e = c.QueryRowContext(ctx, "SELECT ENGINE,TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", database, table).Scan(&engine, &kind); e != nil {
		return fail(e, 0, false)
	}
	if kind != "BASE TABLE" || strings.EqualFold(engine, "InnoDB") || strings.EqualFold(engine, "BLACKHOLE") {
		return fail(errors.New("nontransactional restore requires a storage table with a non-InnoDB engine"), 0, false)
	}
	for _, stmt := range []string{"SET NAMES utf8mb4", "SET SESSION time_zone='+00:00'", "SET SESSION autocommit=1"} {
		if !allowed() {
			return fail(errors.Join(ctx.Err(), ErrMySQLNotConnected), 0, false)
		}
		if _, e = c.ExecContext(ctx, stmt); e != nil {
			return fail(e, 0, false)
		}
	}
	columns, e := readMySQLColumnMetadata(ctx, c, database, table, 128)
	if e != nil {
		return fail(e, 0, false)
	}
	for row := int64(1); ; row++ {
		if !allowed() {
			return fail(errors.Join(ctx.Err(), ErrMySQLNotConnected), row, false)
		}
		edit, e := source.Next(ctx)
		if errors.Is(e, io.EOF) {
			if out.RowsApplied != expected {
				return fail(errors.New("backup row count does not match manifest"), row, false)
			}
			out.State = "executed"
			out.RowState = "executed"
			return out
		}
		if e != nil {
			return fail(e, row, false)
		}
		if row > expected {
			return fail(errors.New("backup has more rows than its manifest"), row, false)
		}
		if edit.Kind != "insert" || edit.Original != nil {
			return fail(ErrMySQLDataPage, row, false)
		}
		names, expressions, args, e := mysqlAssignmentsBounded(columns, edit.Values, true, MaxMySQLDataImportBytes)
		if e != nil {
			return fail(e, row, false)
		}
		stmt := "INSERT INTO " + target + " (" + strings.Join(names, ",") + ") VALUES (" + strings.Join(expressions, ",") + ")"
		if len(stmt)+mysqlDataImportArgumentBound(args) > MaxMySQLDataImportBytes {
			return fail(ErrMySQLDataEditLimit, row, false)
		}
		if !allowed() {
			return fail(errors.Join(ctx.Err(), ErrMySQLNotConnected), row, false)
		}
		result, e := c.ExecContext(ctx, stmt, args...)
		if e != nil {
			var server *mysqldriver.MySQLError
			return fail(e, row, !errors.As(e, &server))
		}
		affected, e := result.RowsAffected()
		if e != nil {
			return fail(e, row, true)
		}
		if affected != 1 {
			return fail(errors.New("nontransactional INSERT affected an unexpected number of rows"), row, true)
		}
		out.RowsApplied++
		out.RowState = "executed"
		warnings, e := c.QueryContext(ctx, "SHOW WARNINGS LIMIT 1")
		if e != nil {
			return fail(e, row, true)
		}
		if warnings.Next() {
			var level, message string
			var code int
			e = warnings.Scan(&level, &code, &message)
			closeErr := warnings.Close()
			if e != nil || closeErr != nil {
				return fail(errors.Join(e, closeErr), row, true)
			}
			out.State = "partial"
			out.RowState = "executed"
			out.Error = fmt.Errorf("nontransactional row %d was written but exact value preservation failed (server warning %d); completed rows remain", row, code)
			out.FailedRow = row
			return out
		}
		e = warnings.Err()
		closeErr := warnings.Close()
		if e != nil || closeErr != nil {
			return fail(errors.Join(e, closeErr), row, true)
		}
	}
}
