package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// A source factory must open the same immutable, reviewed rows on each call.
// The executor validates a complete pass before DDL or row writes, then opens
// an independent pass for execution. Readers and callbacks belong to its worker.
type MySQLDataImportRowSource interface {
	Next(context.Context) (MySQLDataEdit, error)
	Close() error
}

type MySQLDataImportStreamRequest struct {
	Generation            uint64
	CanonicalBackupValues bool

	Request           MySQLDataImportRequest
	Rows              int64
	Open              func(context.Context) (MySQLDataImportRowSource, error)
	Record            func(MySQLDataImportRowResult) error
	Progress          func(MySQLDataImportProgress)
	IgnoreForeignKeys bool
	ExtendedInsert    bool
	StatementMaxBytes int
}

type MySQLDataImportProgress struct {
	Processed, Prepared, Skipped, Failed int64
}

type MySQLDataImportStreamResult struct {
	Created, CreationUncertain, Committed, CommitUncertain bool
	Progress                                               MySQLDataImportProgress
	Rows                                                   []MySQLDataImportRowResult // bounded diagnostic sample; full records are journaled
}

// ImportMySQLDataStream uses one physical connection and one row transaction
// for all source rows. Record callbacks receive provisional rows; only the
// final transaction receipt makes prepared records committed. No retry occurs.
func (m *MySQLSessionManager) ImportMySQLDataStream(ctx context.Context, id int64, database, table string, stream MySQLDataImportStreamRequest) (result *MySQLDataImportStreamResult, err error) {
	result = &MySQLDataImportStreamResult{}
	r := stream.Request
	if stream.Open == nil || stream.Rows <= 0 || len(r.Edits) != 0 || stream.StatementMaxBytes < 0 || stream.StatementMaxBytes > MaxMySQLDataImportBytes {
		return result, ErrMySQLDataPage
	}
	if r.Mode == "" {
		r.Mode = "append"
	}
	if r.Mode != "append" && r.Mode != "update" && r.Mode != "upsert" && r.Mode != "insert_missing" && r.Mode != "delete" && r.Mode != "replace" || r.NewTable && r.Mode != "append" || !r.NewTable && len(r.Columns) != 0 || len(r.MatchColumns) > 16 {
		return result, ErrMySQLDataPage
	}
	r.Columns = append([]MySQLDataImportColumn(nil), r.Columns...)
	r.MatchColumns = append([]string(nil), r.MatchColumns...)
	target, err := mysqlEditTable(database, table)
	if err != nil {
		return result, err
	}
	session, qctx, cancel, err := m.requestContext(ctx, id, 0)
	if err != nil {
		return result, err
	}
	defer cancel()
	if stream.Generation != 0 && m.SessionGeneration(id) != stream.Generation {
		return result, ErrMySQLNotConnected
	}
	conn, err := session.db.Conn(qctx)
	if err != nil {
		return result, err
	}
	defer conn.Close()
	if stream.CanonicalBackupValues {
		defer conn.Raw(func(any) error { return driver.ErrBadConn })
		if _, err = conn.ExecContext(qctx, "SET NAMES utf8mb4"); err != nil {
			return result, err
		}
		if _, err = conn.ExecContext(qctx, "SET SESSION time_zone='+00:00'"); err != nil {
			return result, err
		}
	}
	var columns []MySQLStructureColumn
	var createSQL string
	if r.NewTable {
		columns, err = MySQLDataImportColumnMetadata(r.Columns)
		if err == nil {
			createSQL, err = mysqlDataImportCreateSQL(database, table, r.Columns, columns)
		}
	} else {
		columns, err = readMySQLColumnMetadata(qctx, conn, database, table, 128)
	}
	if err != nil {
		return result, err
	}
	// Validate all row identities before any source mutation, independently of
	// ContinueOnError. Other conversion failures can be isolated per row.
	preflight, err := stream.Open(qctx)
	if err != nil {
		return result, err
	}
	valid, count := int64(0), int64(0)
	for {
		edit, readErr := preflight.Next(qctx)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = preflight.Close()
			return result, readErr
		}
		count++
		if count > stream.Rows {
			_ = preflight.Close()
			return result, ErrMySQLDataPage
		}
		check := r
		check.Edits = []MySQLDataEdit{edit}
		if e := ValidateMySQLDataImportRequest(columns, check); e != nil {
			_ = preflight.Close()
			return result, e
		}
		if e := mysqlDataImportValidateRow(columns, edit, r.Mode == "append" || r.Mode == "replace"); e == nil || r.Mode == "delete" {
			valid++
		}
	}
	if closeErr := preflight.Close(); closeErr != nil {
		return result, closeErr
	}
	if count != stream.Rows || valid == 0 {
		return result, ErrMySQLDataPage
	}
	var originalMode string
	var originalFK int
	if err = conn.QueryRowContext(qctx, "SELECT @@SESSION.sql_mode, @@SESSION.foreign_key_checks").Scan(&originalMode, &originalFK); err != nil {
		return result, err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		_, modeErr := conn.ExecContext(cleanup, "SET SESSION sql_mode = ?", originalMode)
		_, fkErr := conn.ExecContext(cleanup, "SET SESSION foreign_key_checks = ?", originalFK)
		if modeErr != nil || fkErr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if _, err = conn.ExecContext(qctx, "SET SESSION sql_mode = ?", originalMode+",STRICT_ALL_TABLES"); err != nil {
		return result, err
	}
	if stream.IgnoreForeignKeys {
		if _, err = conn.ExecContext(qctx, "SET SESSION foreign_key_checks = 0"); err != nil {
			return result, err
		}
	}
	if r.NewTable {
		if _, err = conn.ExecContext(qctx, createSQL); err != nil {
			var serverError *mysqldriver.MySQLError
			result.CreationUncertain = !errors.As(err, &serverError)
			return result, err
		}
		result.Created = true
	}
	tx, err := conn.BeginTx(context.WithoutCancel(qctx), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	defer func() {
		for i := range result.Rows {
			if result.Rows[i].Status == "prepared" {
				if result.Committed {
					result.Rows[i].Status = "committed"
				} else if result.CommitUncertain {
					result.Rows[i].Status = "uncertain"
				} else {
					result.Rows[i].Status = "rolled_back"
				}
			}
		}
	}()
	columns, err = mysqlEditTransactionColumns(qctx, tx, database, table, target)
	if err != nil {
		return result, err
	}
	var key []string
	if mysqlDataImportNeedsKey(r.Mode) {
		keys, e := mysqlEditUniqueKeys(qctx, tx, database, table)
		if e != nil {
			return result, e
		}
		key, err = mysqlDataImportMatchKey(r.MatchColumns, keys)
		if err != nil {
			return result, err
		}
	}
	rows, err := stream.Open(qctx)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	if r.Mode == "replace" {
		if _, err = tx.ExecContext(qctx, "DELETE FROM "+target); err != nil {
			return result, err
		}
	}
	executor := mysqlDataStreamExecutor{ctx: qctx, tx: tx, database: database, table: table, target: target, columns: columns, key: key, request: r, stream: stream, result: result}
	if err = executor.run(rows); err != nil {
		return result, err
	}
	if result.Progress.Processed != stream.Rows {
		return result, ErrMySQLDataPage
	}
	if err = rows.Close(); err != nil {
		return result, err
	}
	if err = qctx.Err(); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		result.CommitUncertain = true
		return result, fmt.Errorf("%w: commit reply unavailable", ErrMySQLDataCommitUncertain)
	}
	result.Committed = true
	return result, nil
}
