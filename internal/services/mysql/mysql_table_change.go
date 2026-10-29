package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// MySQLTableAction enumerates table changes supported by NyaDB. Callers must
// obtain explicit user confirmation: MySQL DDL can implicitly commit sessions.
type MySQLTableAction string

const (
	MySQLTableCreate        MySQLTableAction = "create"
	MySQLTableRename        MySQLTableAction = "rename"
	MySQLTableDrop          MySQLTableAction = "drop"
	MySQLTableDeleteRows    MySQLTableAction = "delete_rows"
	MySQLTableTruncate      MySQLTableAction = "truncate"
	MySQLTableCopyStructure MySQLTableAction = "copy_structure"
	MySQLTableCopyData      MySQLTableAction = "copy_data"
)

type MySQLTableChange struct {
	Action   MySQLTableAction
	Database string
	Table    string
	NewName  string
}

// tableChangeSQL contains no unquoted database identifiers. Values used to
// identify existing objects are additionally checked against INFORMATION_SCHEMA.
func tableChangeSQL(change MySQLTableChange) (string, error) {
	schema, err := quotedMySQLIdentifier(change.Database)
	if err != nil {
		return "", err
	}
	table, err := quotedMySQLIdentifier(change.Table)
	if err != nil {
		return "", err
	}
	source := schema + "." + table
	var target string
	switch change.Action {
	case MySQLTableCreate:
		// This is an explicitly described starter table, not a full designer.
		return "CREATE TABLE " + source + " (`id` BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY) ENGINE=InnoDB", nil
	case MySQLTableDeleteRows:
		return "DELETE FROM " + source, nil
	case MySQLTableTruncate:
		return "TRUNCATE TABLE " + source, nil
	case MySQLTableDrop:
		return "DROP TABLE " + source, nil
	case MySQLTableRename, MySQLTableCopyStructure, MySQLTableCopyData:
		name, err := quotedMySQLIdentifier(change.NewName)
		if err != nil {
			return "", err
		}
		if change.NewName == change.Table {
			return "", fmt.Errorf("%w: destination must differ", connections.ErrInvalid)
		}
		target = schema + "." + name
		switch change.Action {
		case MySQLTableRename:
			return "RENAME TABLE " + source + " TO " + target, nil
		default:
			return "CREATE TABLE " + target + " LIKE " + source, nil
		}
	default:
		return "", fmt.Errorf("%w: unsupported table action", connections.ErrInvalid)
	}
}

// tableCopyDataSQL explicitly includes invisible columns and excludes generated
// columns. EXTRA may contain DEFAULT_GENERATED for an ordinary writable column,
// so only GENERATION_EXPRESSION determines whether a column is generated.
func tableCopyDataSQL(ctx context.Context, db *sql.DB, change MySQLTableChange) (string, error) {
	rows, err := db.QueryContext(ctx, `SELECT COLUMN_NAME, GENERATION_EXPRESSION
FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
ORDER BY ORDINAL_POSITION`, change.Database, change.Table)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var name string
		var expression sql.NullString
		if err := rows.Scan(&name, &expression); err != nil {
			return "", err
		}
		if expression.Valid && expression.String != "" {
			continue
		}
		quoted, err := quotedMySQLIdentifier(name)
		if err != nil {
			return "", err
		}
		columns = append(columns, quoted)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(columns) == 0 {
		return "", fmt.Errorf("%w: no writable columns found for copying rows", connections.ErrInvalid)
	}
	schema, err := quotedMySQLIdentifier(change.Database)
	if err != nil {
		return "", err
	}
	source, err := quotedMySQLIdentifier(change.Table)
	if err != nil {
		return "", err
	}
	target, err := quotedMySQLIdentifier(change.NewName)
	if err != nil {
		return "", err
	}
	list := strings.Join(columns, ", ")
	return "INSERT INTO " + schema + "." + target + " (" + list + ") SELECT " + list + " FROM " + schema + "." + source, nil
}

// ChangeTable runs only on an already authenticated connection's metadata
// pool, not on a SQL tab's pinned transactional session. This operation never
// silently retries or replays DDL after a connection failure.
func (m *MySQLSessionManager) ChangeTable(ctx context.Context, id int64, change MySQLTableChange) error {
	first, err := tableChangeSQL(change)
	if err != nil {
		return err
	}
	session, opCtx, cancel, err := m.requestContext(ctx, id, 45*time.Second)
	if err != nil {
		return err
	}
	defer cancel()
	if err := opCtx.Err(); err != nil {
		return err
	}

	if change.Action != MySQLTableCreate {
		var kind string
		err = session.db.QueryRowContext(opCtx, `SELECT TABLE_TYPE FROM information_schema.TABLES
WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, change.Database, change.Table).Scan(&kind)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMySQLTableNotFound
		}
		if err != nil {
			return err
		}
		if kind != "BASE TABLE" {
			return fmt.Errorf("%w: table action requires a base table", connections.ErrInvalid)
		}
	}
	var second string
	if change.Action == MySQLTableCopyData {
		// Resolve columns before CREATE so a metadata failure leaves no new table.
		second, err = tableCopyDataSQL(opCtx, session.db, change)
		if err != nil {
			return err
		}
	}
	if _, err := session.db.ExecContext(opCtx, first); err != nil {
		return err
	}
	if second == "" {
		return nil
	}
	// CREATE LIKE and INSERT SELECT cannot be atomic in MySQL. An INSERT
	// failure intentionally leaves the new table for inspection/recovery.
	if _, err := session.db.ExecContext(opCtx, second); err != nil {
		return fmt.Errorf("new table was created, but copying rows failed (the new table remains): %w", err)
	}
	return nil
}
