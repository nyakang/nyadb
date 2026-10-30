package mysql

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

const (
	MaxMySQLDataImportRows  = 10000
	MaxMySQLDataImportBytes = 16 << 20
)

// An import row always describes mapped input values, not a forged existing
// row snapshot. Update identities are resolved and locked by the executor.
type MySQLDataImportRequest struct {
	Mode            string // append | update | upsert | insert_missing | delete | replace; empty means append
	MatchColumns    []string
	ContinueOnError bool
	NewTable        bool
	Columns         []MySQLDataImportColumn
	Edits           []MySQLDataEdit
}

type MySQLDataImportRowResult struct {
	Database,Table string `json:",omitempty"`
	Row     int
	Kind    string // insert | update | delete | skip | error
	Status  string // committed | skipped | failed | rolled_back | not_processed | uncertain
	Error   string // safe diagnostic; never includes input values or server SQL
	Outcome *MySQLDataEditOutcome
}

type MySQLDataImportResult struct {
	Created, CreationUncertain bool
	Committed, CommitUncertain bool
	Rows                       []MySQLDataImportRowResult
	Saved                      *MySQLDataEditResult
}

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
	if request.Mode == "" {
		request.Mode = "append"
	}
	if request.Mode != "append" && request.Mode != "update" && request.Mode != "upsert" && request.Mode != "insert_missing" && request.Mode != "delete" && request.Mode != "replace" || request.NewTable && request.Mode != "append" || !request.NewTable && len(request.Columns) != 0 {
		return request, ErrMySQLDataPage
	}
	if len(request.Edits) == 0 || len(request.Edits) > MaxMySQLDataImportRows || len(request.MatchColumns) > 16 || (request.Mode == "append" || request.Mode == "replace") && len(request.MatchColumns) != 0 {
		return request, ErrMySQLDataPage
	}
	request.Columns = append([]MySQLDataImportColumn(nil), request.Columns...)
	request.MatchColumns = append([]string(nil), request.MatchColumns...)
	owned := make([]MySQLDataEdit, len(request.Edits))
	total := 0
	for i, edit := range request.Edits {
		if edit.Kind != "insert" || edit.Original != nil || len(edit.Values) == 0 || len(edit.Values) > 128 {
			return request, ErrMySQLDataPage
		}
		owned[i] = MySQLDataEdit{Kind: "insert", Values: make(map[string]MySQLDataValue, len(edit.Values))}
		for name, value := range edit.Values {
			if _, err := quotedMySQLIdentifier(name); err != nil || value.Default {
				return request, ErrMySQLDataPage
			}
			total += len(value.Text) + len(value.Bytes)
			if total > MaxMySQLDataImportBytes {
				return request, ErrMySQLDataEditLimit
			}
			value.Bytes = bytes.Clone(value.Bytes)
			if value.Binary && value.Bytes == nil {
				value.Bytes = []byte{}
			}
			owned[i].Values[name] = value
		}
	}
	request.Edits = owned
	return request, nil
}

func mysqlDataImportNeedsKey(mode string) bool {
	return mode == "update" || mode == "upsert" || mode == "insert_missing" || mode == "delete"
}

// ValidateMySQLDataImportRequest is the shared, bounded review preflight. It
// cannot establish that a chosen field set is a real unique index; that check
// is repeated under the target's transaction metadata lock by the executor.
func ValidateMySQLDataImportRequest(metadata []MySQLStructureColumn, request MySQLDataImportRequest) error {
	request, err := mysqlDataImportOwnedRequest(request)
	if err != nil {
		return err
	}
	if request.NewTable {
		metadata, err = MySQLDataImportColumnMetadata(request.Columns)
		if err != nil {
			return err
		}
	}
	if len(metadata) == 0 || len(metadata) > 128 {
		return ErrMySQLDataPage
	}
	allowed := make(map[string]MySQLStructureColumn, len(metadata))
	for _, col := range metadata {
		allowed[col.Name] = col
	}
	if mysqlDataImportNeedsKey(request.Mode) {
		if _, err = mysqlDataImportMatchKey(request.MatchColumns, [][]string{request.MatchColumns}); err != nil {
			return err
		}
	}
	for _, edit := range request.Edits {
		for name := range edit.Values {
			col, ok := allowed[name]
			if !ok || MySQLDataColumnGenerated(col) {
				return ErrMySQLDataPage
			}
		}
		if mysqlDataImportNeedsKey(request.Mode) {
			if _, err = mysqlDataImportRef("db", "table", request.MatchColumns, edit); err != nil {
				return err
			}
			for _, name := range request.MatchColumns {
				if !mysqlDataImportValueValid(allowed[name], edit.Values[name]) {
					return ErrMySQLDataPage
				}
			}
		}
		if request.Mode == "delete" {
			continue
		}
		if err = mysqlDataImportValidateRow(metadata, edit, request.Mode == "append" || request.Mode == "replace"); err != nil && !request.ContinueOnError {
			return err
		}
	}
	return nil
}

func mysqlDataImportValidateRow(columns []MySQLStructureColumn, edit MySQLDataEdit, insert bool) error {
	byName := make(map[string]MySQLStructureColumn, len(columns))
	for _, col := range columns {
		byName[col.Name] = col
		if insert && !col.Nullable && col.Default == nil && !MySQLDataColumnGenerated(col) && !strings.Contains(strings.ToLower(col.Extra), "auto_increment") {
			if _, ok := edit.Values[col.Name]; !ok {
				return ErrMySQLDataPage
			}
		}
	}
	for name, value := range edit.Values {
		col, ok := byName[name]
		if !ok || value.Default || value.Null && value.Binary || !mysqlDataImportValueValid(col, value) {
			return ErrMySQLDataPage
		}
	}
	_, _, _, err := mysqlEditAssignments(columns, edit.Values, insert)
	return err
}

// Resolve the chosen field set to the metadata index's actual order. Nullable,
// prefix and expression indexes are excluded by the same identity policy used
// by ordinary data editing.
func mysqlDataImportMatchKey(chosen []string, keys [][]string) ([]string, error) {
	if len(chosen) == 0 {
		return nil, ErrMySQLDataReadOnly
	}
	seen := make(map[string]bool, len(chosen))
	for _, name := range chosen {
		if _, err := quotedMySQLIdentifier(name); err != nil || seen[name] {
			return nil, ErrMySQLDataReadOnly
		}
		seen[name] = true
	}
	for _, key := range keys {
		if len(key) != len(chosen) {
			continue
		}
		matches := true
		for _, name := range key {
			matches = matches && seen[name]
		}
		if matches {
			return append([]string(nil), key...), nil
		}
	}
	return nil, ErrMySQLDataReadOnly
}

func mysqlDataImportRef(database, table string, key []string, edit MySQLDataEdit) (MySQLRowRef, error) {
	ref := MySQLRowRef{Database: database, Table: table, Columns: append([]string(nil), key...)}
	for _, name := range key {
		value, ok := edit.Values[name]
		if !ok || value.Null || value.Default {
			return ref, ErrMySQLDataReadOnly
		}
		if value.Binary {
			ref.Values = append(ref.Values, append([]byte{}, value.Bytes...))
		} else {
			ref.Values = append(ref.Values, []byte(value.Text))
		}
	}
	_, _, err := mysqlEditWhere(ref, database, table)
	return ref, err
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
