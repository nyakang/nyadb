package mysql

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

type mysqlDataStreamExecutor struct {
	ctx                     context.Context
	tx                      *sql.Tx
	database, table, target string
	columns                 []MySQLStructureColumn
	key                     []string
	request                 MySQLDataImportRequest
	stream                  MySQLDataImportStreamRequest
	result                  *MySQLDataImportStreamResult
}

func (e *mysqlDataStreamExecutor) publish(row MySQLDataImportRowResult) error {
	if int64(row.Row) != e.result.Progress.Processed+1 {
		return ErrMySQLDataPage
	}
	e.result.Progress.Processed++
	switch row.Status {
	case "prepared":
		e.result.Progress.Prepared++
	case "skipped":
		e.result.Progress.Skipped++
	case "failed":
		e.result.Progress.Failed++
	default:
		return ErrMySQLDataPage
	}
	// Keep the diagnostic sample even when the durable journal itself fails.
	// The transaction is rolled back before the executor returns in that case.
	if len(e.result.Rows) < 100 {
		e.result.Rows = append(e.result.Rows, row)
	}
	if e.stream.Record != nil {
		if err := e.stream.Record(row); err != nil {
			return err
		}
	}
	if e.stream.Progress != nil && (e.result.Progress.Processed%100 == 0 || e.result.Progress.Processed == e.stream.Rows) {
		e.stream.Progress(e.result.Progress)
	}
	return nil
}
func (e *mysqlDataStreamExecutor) failed(index int, err error) error {
	return e.publish(MySQLDataImportRowResult{Row: index, Kind: "error", Status: "failed", Error: mysqlDataImportErrorSummary(err)})
}
func (e *mysqlDataStreamExecutor) row(input MySQLDataEdit, index int) error {
	owned, err := mysqlDataImportOwnedRequest(MySQLDataImportRequest{Edits: []MySQLDataEdit{input}})
	if err != nil {
		if journalErr := e.failed(index, err); journalErr != nil {
			return journalErr
		}
		return err
	}
	edit := owned.Edits[0]
	row := MySQLDataImportRowResult{Row: index}
	if e.request.ContinueOnError {
		if _, err = e.tx.ExecContext(e.ctx, "SAVEPOINT nyadb_import_row"); err != nil {
			if journalErr := e.failed(index, err); journalErr != nil {
				return journalErr
			}
			return err
		}
	}
	var rowErr error
	if mysqlDataImportNeedsKey(e.request.Mode) {
		ref, refErr := mysqlDataImportRef(e.database, e.table, e.key, edit)
		if refErr != nil {
			rowErr = refErr
		} else {
			current, readErr := mysqlEditSnapshot(e.ctx, e.tx, e.database, e.table, ref, e.columns, true)
			switch {
			case errors.Is(readErr, sql.ErrNoRows):
				if e.request.Mode == "update" || e.request.Mode == "delete" {
					row.Kind, row.Status = "skip", "skipped"
				}
			case readErr != nil:
				rowErr = readErr
			case e.request.Mode == "insert_missing":
				row.Kind, row.Status = "skip", "skipped"
			case e.request.Mode == "delete":
				edit.Kind, edit.Original, edit.Values = "delete", current, nil
			default:
				edit.Kind, edit.Original = "update", current
			}
		}
	}
	if rowErr == nil && row.Kind != "skip" {
		if edit.Kind != "delete" {
			rowErr = mysqlDataImportValidateRow(e.columns, edit, edit.Kind == "insert")
		}
		if rowErr == nil && e.stream.StatementMaxBytes > 0 {
			// This conservative bound includes the escaped input and qualified SQL.
			// It is also used for keyed writes so the configured cap is never ignored.
			if mysqlDataImportStatementBound(e.target, e.columns, edit) > e.stream.StatementMaxBytes {
				rowErr = ErrMySQLDataEditLimit
			}
		}
		if rowErr == nil {
			saved, writeErr := applyMySQLDataEditsWithColumns(e.ctx, e.tx, e.database, e.table, e.target, []MySQLDataEdit{edit}, e.columns, false, MaxMySQLDataImportBytes)
			rowErr = writeErr
			if rowErr == nil {
				row.Kind, row.Status = edit.Kind, "prepared"
				if saved != nil && len(saved.Outcomes) > 0 {
					outcome := saved.Outcomes[0]
					row.Outcome = &outcome
				}
			}
		}
	}
	if rowErr != nil {
		row.Kind, row.Status, row.Error = "error", "failed", mysqlDataImportErrorSummary(rowErr)
		if !e.request.ContinueOnError || e.ctx.Err() != nil {
			if journalErr := e.publish(row); journalErr != nil {
				return journalErr
			}
			return rowErr
		}
		// Never continue after a transport/driver failure. A confirmed savepoint
		// rollback alone cannot recover an unclassified lost executor response.
		if mysqlDataImportFatalRowError(rowErr) {
			if journalErr := e.publish(row); journalErr != nil {
				return journalErr
			}
			return rowErr
		}
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		_, rollbackErr := e.tx.ExecContext(cleanup, "ROLLBACK TO SAVEPOINT nyadb_import_row")
		stop()
		if rollbackErr != nil {
			if journalErr := e.publish(row); journalErr != nil {
				return journalErr
			}
			return errors.Join(rowErr, rollbackErr)
		}
	}
	if e.request.ContinueOnError {
		if _, err = e.tx.ExecContext(e.ctx, "RELEASE SAVEPOINT nyadb_import_row"); err != nil {
			if journalErr := e.publish(row); journalErr != nil {
				return journalErr
			}
			return err
		}
	}
	return e.publish(row)
}
func mysqlDataImportFatalRowError(err error) bool {
	if errors.Is(err, ErrMySQLDataPage) || errors.Is(err, ErrMySQLDataEditLimit) || errors.Is(err, ErrMySQLDataReadOnly) {
		return false
	}
	var server *mysqldriver.MySQLError
	return !errors.As(err, &server)
}
func mysqlDataImportArgumentBound(args []any) int {
	bytes := 0
	for _, a := range args {
		switch v := a.(type) {
		case nil:
			bytes += 4
		case string:
			bytes += 2*len(v) + 16
		case []byte:
			bytes += 2*len(v) + 16
		default:
			bytes += 32
		}
	}
	return bytes
}
func mysqlDataImportStatementBound(target string, columns []MySQLStructureColumn, edit MySQLDataEdit) int {
	var where string
	var keyArgs []any
	if edit.Kind != "insert" {
		if edit.Original == nil {
			return MaxMySQLDataImportBytes + 1
		}
		ref := edit.Original.Ref
		var err error
		where, keyArgs, err = mysqlEditWhere(ref, ref.Database, ref.Table)
		if err != nil {
			return MaxMySQLDataImportBytes + 1
		}
	}
	if edit.Kind == "delete" {
		return len("DELETE FROM "+target+" WHERE "+where) + mysqlDataImportArgumentBound(keyArgs)
	}
	names, expr, args, err := mysqlAssignmentsBounded(columns, edit.Values, edit.Kind == "insert", MaxMySQLDataImportBytes)
	if err != nil {
		return MaxMySQLDataImportBytes + 1
	}
	if edit.Kind == "insert" {
		return len("INSERT INTO "+target+" ("+strings.Join(names, ",")+") VALUES ("+strings.Join(expr, ",")+")") + mysqlDataImportArgumentBound(args)
	}
	parts := make([]string, len(names))
	for i := range names {
		parts[i] = names[i] + " = " + expr[i]
	}
	return len("UPDATE "+target+" SET "+strings.Join(parts, ",")+" WHERE "+where) + mysqlDataImportArgumentBound(args) + mysqlDataImportArgumentBound(keyArgs)
}

type mysqlDataImportInsertRow struct {
	edit               MySQLDataEdit
	index              int
	names, expressions []string
	args               []any
	bound, bytes       int
}

func (e *mysqlDataStreamExecutor) insertRow(input MySQLDataEdit, index int) (mysqlDataImportInsertRow, error) {
	owned, err := mysqlDataImportOwnedRequest(MySQLDataImportRequest{Edits: []MySQLDataEdit{input}})
	if err != nil {
		return mysqlDataImportInsertRow{}, err
	}
	edit := owned.Edits[0]
	if err = mysqlDataImportValidateRow(e.columns, edit, true); err != nil {
		return mysqlDataImportInsertRow{}, err
	}
	names, expressions, args, err := mysqlAssignmentsBounded(e.columns, edit.Values, true, MaxMySQLDataImportBytes)
	if err != nil {
		return mysqlDataImportInsertRow{}, err
	}
	bytes := 0
	for _, v := range edit.Values {
		bytes += len(v.Text) + len(v.Bytes)
	}
	return mysqlDataImportInsertRow{edit: edit, index: index, names: names, expressions: expressions, args: args, bound: len(strings.Join(expressions, ",")) + 2 + mysqlDataImportArgumentBound(args), bytes: bytes}, nil
}
func (e *mysqlDataStreamExecutor) groupFailed(batch []mysqlDataImportInsertRow, err error) error {
	for _, row := range batch {
		if journalErr := e.failed(row.index, err); journalErr != nil {
			return journalErr
		}
	}
	return err
}
func (e *mysqlDataStreamExecutor) group(batch []mysqlDataImportInsertRow) error {
	if len(batch) == 0 {
		return nil
	}
	if len(batch) == 1 {
		return e.row(batch[0].edit, batch[0].index)
	}
	if _, err := e.tx.ExecContext(e.ctx, "SAVEPOINT nyadb_import_batch"); err != nil {
		if journalErr := e.failed(batch[0].index, err); journalErr != nil {
			return journalErr
		}
		return err
	}
	query := "INSERT INTO " + e.target + " (" + strings.Join(batch[0].names, ",") + ") VALUES "
	var expressions []string
	var args []any
	for _, row := range batch {
		expressions = append(expressions, "("+strings.Join(row.expressions, ",")+")")
		args = append(args, row.args...)
	}
	query += strings.Join(expressions, ",")
	output, err := e.tx.ExecContext(e.ctx, query, args...)
	if err != nil {
		var server *mysqldriver.MySQLError
		if !e.request.ContinueOnError || e.ctx.Err() != nil || !errors.As(err, &server) {
			return e.groupFailed(batch, err)
		}
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		_, rollbackErr := e.tx.ExecContext(cleanup, "ROLLBACK TO SAVEPOINT nyadb_import_batch")
		stop()
		if rollbackErr != nil {
			return e.groupFailed(batch, errors.Join(err, rollbackErr))
		}
		if _, releaseErr := e.tx.ExecContext(e.ctx, "RELEASE SAVEPOINT nyadb_import_batch"); releaseErr != nil {
			return e.groupFailed(batch, releaseErr)
		}
		// The server rejected this group and rollback acknowledged that no group
		// row remains applied. Only now is it safe to isolate rows individually.
		for _, row := range batch {
			if err = e.row(row.edit, row.index); err != nil {
				return err
			}
		}
		return nil
	}
	affected, err := output.RowsAffected()
	if err != nil || affected != int64(len(batch)) {
		if err == nil {
			err = ErrMySQLDataPage
		}
		return e.groupFailed(batch, err)
	}
	for _, row := range batch {
		if err = e.publish(MySQLDataImportRowResult{Row: row.index, Kind: "insert", Status: "prepared"}); err != nil {
			return err
		}
	}
	if _, err = e.tx.ExecContext(e.ctx, "RELEASE SAVEPOINT nyadb_import_batch"); err != nil {
		return err
	}
	return nil
}
func (e *mysqlDataStreamExecutor) run(rows MySQLDataImportRowSource) error {
	limit := e.stream.StatementMaxBytes
	if limit == 0 {
		limit = MaxMySQLDataImportBytes
	}
	extended := e.stream.ExtendedInsert && (e.request.Mode == "append" || e.request.Mode == "replace")
	var batch []mysqlDataImportInsertRow
	batchBound, batchBytes, batchArgs := 0, 0, 0
	index := 0
	flush := func() error {
		err := e.group(batch)
		batch = nil
		batchBound, batchBytes, batchArgs = 0, 0, 0
		return err
	}
	for {
		input, readErr := rows.Next(e.ctx)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
		if err := e.ctx.Err(); err != nil {
			return err
		}
		index++
		if int64(index) > e.stream.Rows {
			return ErrMySQLDataPage
		}
		if !extended {
			if err := e.row(input, index); err != nil {
				return err
			}
			continue
		}
		candidate, err := e.insertRow(input, index)
		if err != nil {
			if err = flush(); err != nil {
				return err
			}
			if err = e.row(input, index); err != nil {
				return err
			}
			continue
		}
		prefix := len("INSERT INTO ") + len(e.target) + len(" () VALUES ") + len(strings.Join(candidate.names, ","))
		if len(batch) > 0 && (!reflect.DeepEqual(batch[0].names, candidate.names) || len(batch) >= MaxMySQLDataImportRows || batchBytes+candidate.bytes > MaxMySQLDataImportBytes || batchArgs+len(candidate.args) > 65535 || batchBound+candidate.bound+1 > limit) {
			if err = flush(); err != nil {
				return err
			}
		}
		if len(batch) == 0 {
			batchBound = prefix
		}
		if batchBound+candidate.bound > limit {
			if err = e.row(candidate.edit, candidate.index); err != nil {
				return err
			}
			batchBound = 0
			continue
		}
		batch = append(batch, candidate)
		batchBound += candidate.bound + 1
		batchBytes += candidate.bytes
		batchArgs += len(candidate.args)
	}
	return flush()
}
