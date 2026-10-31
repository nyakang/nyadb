package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// The entire batch uses a dedicated managed metadata-pool connection and one
// InnoDB transaction. sqlDB and pinned SQL-tab leases are never touched.
func (m *MySQLSessionManager) ApplyMySQLDataEdits(ctx context.Context, id int64, database, table string, edits []MySQLDataEdit) (*MySQLDataEditResult, error) {
	if len(edits) == 0 || len(edits) > MySQLDataMaxPageSize {
		return nil, ErrMySQLDataPage
	}
	session, qctx, cancel, e := m.requestContext(ctx, id, 30*time.Second)
	if e != nil {
		return nil, e
	}
	defer cancel()
	target, e := mysqlEditTable(database, table)
	if e != nil {
		return nil, e
	}
	conn, e := session.db.Conn(qctx)
	if e != nil {
		return nil, e
	}
	defer conn.Close()
	// Metadata-pool connections never inherit SQL-tab session options. Enforce
	// strict coercion checks temporarily and restore before returning to pool.
	var originalMode string
	if e = conn.QueryRowContext(qctx, "SELECT @@SESSION.sql_mode").Scan(&originalMode); e != nil {
		return nil, e
	}
	if _, e = conn.ExecContext(qctx, "SET SESSION sql_mode = ?", originalMode+",STRICT_ALL_TABLES"); e != nil {
		return nil, e
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cleanupCancel()
		if _, restoreErr := conn.ExecContext(cleanupCtx, "SET SESSION sql_mode = ?", originalMode); restoreErr != nil {
			// Mark the connection bad instead of returning altered session state.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()

	tx, e := conn.BeginTx(qctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	result, e := applyMySQLDataEditsInTransaction(qctx, tx, database, table, target, edits)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		result.CommitUncertain = true
		return result, fmt.Errorf("%w: %v", ErrMySQLDataCommitUncertain, e)
	}
	return result, nil
}

// applyMySQLDataEditsInTransaction shares exact-value, identity and conflict
// checks between atomic saves and explicit data-document transactions.
func applyMySQLDataEditsInTransaction(qctx context.Context, tx *sql.Tx, database, table, target string, edits []MySQLDataEdit) (*MySQLDataEditResult, error) {
	columns, e := mysqlEditTransactionColumns(qctx, tx, database, table, target)
	if e != nil {
		return nil, e
	}
	return applyMySQLDataEditsWithColumns(qctx, tx, database, table, target, edits, columns, true)
}

func mysqlEditTransactionColumns(qctx context.Context, tx *sql.Tx, database, table, target string) ([]MySQLStructureColumn, error) {
	var e error
	// Open the target first to hold its metadata lock until COMMIT/ROLLBACK.
	// Revalidate the engine and fields under that lock so a concurrent ALTER
	// cannot swap in a nontransactional engine after our safety check.
	probe, e := tx.QueryContext(qctx, "SELECT 1 FROM "+target+" LIMIT 0")
	if e != nil {
		return nil, e
	}
	if e = probe.Close(); e != nil {
		return nil, e
	}
	var engine, kind string
	if e = tx.QueryRowContext(qctx, `SELECT COALESCE(ENGINE,''),TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?`, database, table).Scan(&engine, &kind); e != nil {
		return nil, e
	}
	if !strings.EqualFold(engine, "InnoDB") || kind != "BASE TABLE" {
		return nil, ErrMySQLDataReadOnly
	}
	columns, e := readMySQLColumnMetadata(qctx, tx, database, table, 128)
	if e != nil {
		return nil, e
	}
	if len(columns) == 0 {
		return nil, ErrMySQLTableNotFound
	}

	return columns, nil
}

func applyMySQLDataEditsWithColumns(qctx context.Context, tx *sql.Tx, database, table, target string, edits []MySQLDataEdit, columns []MySQLStructureColumn, readPreviews bool, limits ...int) (*MySQLDataEditResult, error) {
	limit := MySQLDataEditMaxBytes
	if len(limits) > 0 {
		limit = limits[0]
	}
	var e error
	result := &MySQLDataEditResult{}
	for _, edit := range edits {
		outcome := MySQLDataEditOutcome{Kind: edit.Kind, Original: edit.Original}
		var where string
		var whereArgs []any
		if edit.Kind != "insert" {
			if edit.Original == nil {
				return nil, ErrMySQLDataReadOnly
			}
			if e = mysqlEditValidateRef(qctx, tx, database, table, edit.Original.Ref); e != nil {
				return nil, e
			}
			current, e := mysqlEditSnapshot(qctx, tx, database, table, edit.Original.Ref, columns, true)
			if errors.Is(e, sql.ErrNoRows) {
				return nil, ErrMySQLDataConflict
			}
			if e != nil {
				return nil, e
			}
			if !mysqlSnapshotsEqual(current, edit.Original) {
				return nil, ErrMySQLDataConflict
			}
			where, whereArgs, e = mysqlEditWhere(edit.Original.Ref, database, table)
			if e != nil {
				return nil, e
			}
		}
		var query string
		var args []any
		var nextRef *MySQLRowRef
		switch edit.Kind {
		case "insert", "update":
			names, expressions, bound, e := mysqlAssignmentsBounded(columns, edit.Values, edit.Kind == "insert", limit)
			if e != nil {
				return nil, e
			}
			args = bound
			if edit.Kind == "insert" {
				query = "INSERT INTO " + target + " (" + strings.Join(names, ",") + ") VALUES (" + strings.Join(expressions, ",") + ")"
			} else {
				assignments := make([]string, len(names))
				for i := range names {
					assignments[i] = names[i] + " = " + expressions[i]
				}
				query = "UPDATE " + target + " SET " + strings.Join(assignments, ",") + " WHERE " + where
				args = append(args, whereArgs...)
				nr, e := mysqlEditChangedRef(edit.Original.Ref, edit.Values)
				if e != nil {
					return nil, e
				}
				nextRef = &nr
			}
		case "delete":
			if len(edit.Values) != 0 {
				return nil, ErrMySQLDataPage
			}
			query = "DELETE FROM " + target + " WHERE " + where
			args = whereArgs
		default:
			return nil, ErrMySQLDataPage
		}
		applied, e := tx.ExecContext(qctx, query, args...)
		if e != nil {
			return nil, e
		}
		warnings, warningErr := tx.QueryContext(qctx, "SHOW WARNINGS LIMIT 1")
		if warningErr != nil {
			return nil, warningErr
		}
		if warnings.Next() {
			var level, message string
			var code int
			warningErr = warnings.Scan(&level, &code, &message)
			warnings.Close()
			if warningErr != nil {
				return nil, warningErr
			}
			return nil, fmt.Errorf("%w: server rejected exact value preservation (%d)", ErrMySQLDataPage, code)
		}
		warningErr = warnings.Err()
		warnings.Close()
		if warningErr != nil {
			return nil, warningErr
		}

		if edit.Kind == "insert" {
			outcome.GeneratedID, e = applied.LastInsertId()
			if e != nil {
				return nil, e
			}
			// Recover an inserted identity from a full NOT NULL primary/unique key.
			candidates, keyErr := mysqlEditUniqueKeys(qctx, tx, database, table)
			if keyErr != nil {
				return nil, keyErr
			}
			autoNames := map[string]bool{}
			for _, col := range columns {
				if strings.Contains(strings.ToLower(col.Extra), "auto_increment") {
					autoNames[col.Name] = true
				}
			}

			for _, names := range candidates {
				ref := MySQLRowRef{Database: database, Table: table, Columns: names}
				valid := true
				for _, name := range names {
					v, ok := edit.Values[name]
					if ok && !v.Default && !v.Null {
						if v.Binary {
							ref.Values = append(ref.Values, append([]byte{}, v.Bytes...))
						} else {
							ref.Values = append(ref.Values, []byte(v.Text))
						}
					} else if outcome.GeneratedID > 0 && autoNames[name] {
						ref.Values = append(ref.Values, []byte(fmt.Sprint(outcome.GeneratedID)))
					} else {
						valid = false
					}
				}
				if valid && mysqlEditValidateRef(qctx, tx, database, table, ref) == nil {
					nextRef = &ref
					break
				}
			}
		}
		if nextRef != nil {
			if edit.Kind == "insert" || !reflect.DeepEqual(nextRef, &edit.Original.Ref) {
				nextRef, e = mysqlEditSavedRef(qctx, tx, database, table, *nextRef)
				if e != nil {
					return nil, e
				}
			}
			// Values such as decimal spellings are normalized by MySQL; reading the
			// committed candidate, rather than hashing user text, preserves semantics.
			outcome.Saved, e = mysqlEditSnapshot(qctx, tx, database, table, *nextRef, columns, false)
			if e != nil {
				return nil, e
			}
			if readPreviews {
				outcome.PreviewColumns = make([]string, len(columns))
				for i, col := range columns {
					outcome.PreviewColumns[i] = col.Name
				}
				outcome.Previews, e = mysqlEditPreviews(qctx, tx, database, table, *nextRef, columns)
				if e != nil {
					return nil, e
				}
			}
		}
		result.Outcomes = append(result.Outcomes, outcome)
	}
	return result, nil
}
