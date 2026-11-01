package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/mysqlspec"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

const (
	MaxMySQLDataImportRows  = 10000
	MaxMySQLDataImportBytes = 16 << 20
)

// An import row always describes mapped input values, not a forged existing
// row snapshot. Update identities are resolved and locked by the executor.
type MySQLDataImportRequest = contract.DataImportRequest

type MySQLDataImportRowResult = contract.DataImportRowResult

type MySQLDataImportResult = contract.DataImportResult

func mysqlDataImportErrorSummary(err error) string {
	var serverError *mysqldriver.MySQLError
	if errors.As(err, &serverError) {
		return fmt.Sprintf("MySQL error %d", serverError.Number)
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "import canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "import timed out"
	case errors.Is(err, ErrMySQLDataReadOnly):
		return "a complete non-null unique key and an InnoDB table are required"
	case errors.Is(err, ErrMySQLDataEditLimit):
		return "row exceeds the complete-value limit"
	case errors.Is(err, ErrMySQLDataPage):
		return "invalid mapped value or field"
	default:
		return "database operation failed"
	}
}

func mysqlDataImportOwnedRequest(request MySQLDataImportRequest) (MySQLDataImportRequest, error) {
	return mysqlspec.OwnedImportRequest(request)
}

func mysqlDataImportNeedsKey(mode string) bool { return mysqlspec.ImportNeedsKey(mode) }

// ValidateMySQLDataImportRequest is the shared, bounded review preflight. It
// cannot establish that a chosen field set is a real unique index; that check
// is repeated under the target's transaction metadata lock by the executor.
func ValidateMySQLDataImportRequest(metadata []MySQLStructureColumn, request MySQLDataImportRequest) error {
	return mysqlspec.ValidateImportRequest(metadata, request)
}

func mysqlDataImportValidateRow(columns []MySQLStructureColumn, edit MySQLDataEdit, insert bool) error {
	return mysqlspec.ValidateImportRow(columns, edit, insert)
}

// Resolve the chosen field set to the metadata index's actual order. Nullable,
// prefix and expression indexes are excluded by the same identity policy used
// by ordinary data editing.
func mysqlDataImportMatchKey(chosen []string, keys [][]string) ([]string, error) {
	return mysqlspec.ImportMatchKey(chosen, keys)
}

func mysqlDataImportRef(database, table string, key []string, edit MySQLDataEdit) (MySQLRowRef, error) {
	return mysqlspec.ImportRowRef(database, table, key, edit)
}

// ImportMySQLData executes a reviewed mapped import on a single owned physical
// connection. Upsert checks only the explicitly selected key; a collision on
// another unique index cannot silently update an unrelated record. Each failed
// row can be isolated with a savepoint, but a lost transaction or canceled
// executor always aborts the batch. An uncertain commit is never replayed.
func (m *MySQLSessionManager) ImportMySQLData(ctx context.Context, id int64, database, table string, request MySQLDataImportRequest) (*MySQLDataImportResult, error) {
	result := &MySQLDataImportResult{}
	request, err := mysqlDataImportOwnedRequest(request)
	if err != nil {
		return result, err
	}
	result.Rows = make([]MySQLDataImportRowResult, len(request.Edits))
	for i := range result.Rows {
		result.Rows[i] = MySQLDataImportRowResult{Row: i + 1, Status: "not_processed"}
	}
	target, err := mysqlEditTable(database, table)
	if err != nil {
		return result, err
	}
	var columns []MySQLStructureColumn
	var createSQL string
	if request.NewTable {
		columns, err = MySQLDataImportColumnMetadata(request.Columns)
		if err != nil {
			return result, err
		}
		createSQL, err = mysqlDataImportCreateSQL(database, table, request.Columns, columns)
		if err != nil {
			return result, err
		}
		if err = ValidateMySQLDataImportRequest(columns, request); err != nil {
			return result, err
		}
		valid := 0
		for i, edit := range request.Edits {
			if err = mysqlDataImportValidateRow(columns, edit, true); err != nil {
				result.Rows[i].Kind, result.Rows[i].Status, result.Rows[i].Error = "error", "failed", mysqlDataImportErrorSummary(err)
				if !request.ContinueOnError {
					return result, err
				}
			} else {
				valid++
			}
		}
		if valid == 0 {
			return result, ErrMySQLDataPage
		}
	}
	session, qctx, cancel, err := m.requestContext(ctx, id, 120*time.Second)
	if err != nil {
		return result, err
	}
	defer cancel()
	conn, err := session.db.Conn(qctx)
	if err != nil {
		return result, err
	}
	defer conn.Close()
	var originalMode string
	if err = conn.QueryRowContext(qctx, "SELECT @@SESSION.sql_mode").Scan(&originalMode); err != nil {
		return result, err
	}
	if _, err = conn.ExecContext(qctx, "SET SESSION sql_mode = ?", originalMode+",STRICT_ALL_TABLES"); err != nil {
		return result, err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if _, restoreErr := conn.ExecContext(cleanup, "SET SESSION sql_mode = ?", originalMode); restoreErr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if err = qctx.Err(); err != nil {
		return result, err
	}
	if request.NewTable {
		if _, err = conn.ExecContext(qctx, createSQL); err != nil {
			var serverError *mysqldriver.MySQLError
			result.CreationUncertain = !errors.As(err, &serverError)
			return result, err
		}
		result.Created = true
	}
	// Keep database/sql from marking the transaction done and returning before
	// an automatic rollback has reached the driver. Explicit deferred rollback
	// below settles cancellation before this method acknowledges completion.
	tx, err := conn.BeginTx(context.WithoutCancel(qctx), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	defer func() {
		if !result.Committed && !result.CommitUncertain {
			result.Saved = nil
			for i := range result.Rows {
				if result.Rows[i].Status == "prepared" {
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
	if mysqlDataImportNeedsKey(request.Mode) {
		keys, keyErr := mysqlEditUniqueKeys(qctx, tx, database, table)
		if keyErr != nil {
			return result, keyErr
		}
		key, err = mysqlDataImportMatchKey(request.MatchColumns, keys)
		if err != nil {
			return result, err
		}
		// A partial/NULL identity is a review error, not an ignorable row error.
		for _, edit := range request.Edits {
			if _, err = mysqlDataImportRef(database, table, key, edit); err != nil {
				return result, err
			}
		}
	}
	allowed := make(map[string]MySQLStructureColumn, len(columns))
	for _, col := range columns {
		allowed[col.Name] = col
	}
	// Reject mismapped/generated field identities before any row is changed.
	for _, edit := range request.Edits {
		for name := range edit.Values {
			col, ok := allowed[name]
			if !ok || MySQLDataColumnGenerated(col) {
				return result, ErrMySQLDataPage
			}
		}
	}
	if err = ValidateMySQLDataImportRequest(columns, request); err != nil {
		return result, err
	}
	result.Saved = &MySQLDataEditResult{}
	if request.Mode == "replace" {
		if _, err = tx.ExecContext(qctx, "DELETE FROM "+target); err != nil {
			return result, err
		}
	}
	for i, input := range request.Edits {
		if err = qctx.Err(); err != nil {
			return result, err
		}
		if result.Rows[i].Status == "failed" {
			continue
		}
		if request.ContinueOnError {
			if _, err = tx.ExecContext(qctx, "SAVEPOINT nyadb_import_row"); err != nil {
				return result, err
			}
		}
		edit := input
		var rowErr error
		if mysqlDataImportNeedsKey(request.Mode) {
			ref, _ := mysqlDataImportRef(database, table, key, edit)
			current, readErr := mysqlEditSnapshot(qctx, tx, database, table, ref, columns, true)
			switch {
			case errors.Is(readErr, sql.ErrNoRows):
				if request.Mode == "update" || request.Mode == "delete" {
					result.Rows[i].Kind, result.Rows[i].Status = "skip", "skipped"
				}
			case readErr != nil:
				rowErr = readErr
			default:
				if request.Mode == "insert_missing" {
					result.Rows[i].Kind, result.Rows[i].Status = "skip", "skipped"
				} else if request.Mode == "delete" {
					edit.Kind, edit.Original, edit.Values = "delete", current, nil
				} else {
					edit.Kind, edit.Original = "update", current
				}
			}
		}
		if rowErr == nil && result.Rows[i].Kind != "skip" {
			if edit.Kind != "delete" {
				rowErr = mysqlDataImportValidateRow(columns, edit, edit.Kind == "insert")
			}
			if rowErr == nil {
				var saved *MySQLDataEditResult
				// Imports reload the grid once after completion. Keep only the
				// identities/fingerprints needed for uncertain-result inspection,
				// rather than caching a grid preview for every imported row.
				saved, rowErr = applyMySQLDataEditsWithColumns(qctx, tx, database, table, target, []MySQLDataEdit{edit}, columns, false)
				if rowErr == nil {
					outcome := saved.Outcomes[0]
					result.Rows[i].Kind, result.Rows[i].Status, result.Rows[i].Outcome = edit.Kind, "prepared", &outcome
					result.Saved.Outcomes = append(result.Saved.Outcomes, outcome)
				}
			}
		}
		if rowErr != nil {
			result.Rows[i].Kind, result.Rows[i].Status, result.Rows[i].Error = "error", "failed", mysqlDataImportErrorSummary(rowErr)
			if !request.ContinueOnError || qctx.Err() != nil {
				return result, rowErr
			}
			cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
			_, rollbackErr := tx.ExecContext(cleanup, "ROLLBACK TO SAVEPOINT nyadb_import_row")
			stop()
			if rollbackErr != nil {
				return result, errors.Join(rowErr, rollbackErr)
			}
		}
		if request.ContinueOnError {
			if _, err = tx.ExecContext(qctx, "RELEASE SAVEPOINT nyadb_import_row"); err != nil {
				return result, err
			}
		}
	}
	if err = qctx.Err(); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		result.CommitUncertain, result.Saved.CommitUncertain = true, true
		for i := range result.Rows {
			if result.Rows[i].Status == "prepared" {
				result.Rows[i].Status = "uncertain"
			}
		}
		return result, fmt.Errorf("%w: commit reply unavailable", ErrMySQLDataCommitUncertain)
	}
	result.Committed = true
	for i := range result.Rows {
		if result.Rows[i].Status == "prepared" {
			result.Rows[i].Status = "committed"
		}
	}
	return result, nil
}
