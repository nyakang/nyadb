package connections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
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
func tableChangeSQL(change MySQLTableChange) (string, string, error) {
	schema, err := quotedMySQLIdentifier(change.Database)
	if err != nil {
		return "", "", err
	}
	table, err := quotedMySQLIdentifier(change.Table)
	if err != nil {
		return "", "", err
	}
	source := schema + "." + table
	var target string
	switch change.Action {
	case MySQLTableCreate:
		// This is an explicitly described starter table, not a full designer.
		return "CREATE TABLE " + source + " (`id` BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY) ENGINE=InnoDB", "", nil
	case MySQLTableDeleteRows:
		return "DELETE FROM " + source, "", nil
	case MySQLTableTruncate:
		return "TRUNCATE TABLE " + source, "", nil
	case MySQLTableDrop:
		return "DROP TABLE " + source, "", nil
	case MySQLTableRename, MySQLTableCopyStructure, MySQLTableCopyData:
		name, err := quotedMySQLIdentifier(change.NewName)
		if err != nil {
			return "", "", err
		}
		if change.NewName == change.Table {
			return "", "", fmt.Errorf("%w: destination must differ", ErrInvalid)
		}
		target = schema + "." + name
		switch change.Action {
		case MySQLTableRename:
			return "RENAME TABLE " + source + " TO " + target, "", nil
		case MySQLTableCopyStructure:
			return "CREATE TABLE " + target + " LIKE " + source, "", nil
		default:
			return "CREATE TABLE " + target + " LIKE " + source, "INSERT INTO " + target + " SELECT * FROM " + source, nil
		}
	default:
		return "", "", fmt.Errorf("%w: unsupported table action", ErrInvalid)
	}
}

// ChangeTable runs only on an already authenticated connection's metadata
// pool, not on a SQL tab's pinned transactional session. This operation never
// silently retries or replays DDL after a connection failure.
func (m *MySQLSessionManager) ChangeTable(ctx context.Context, id int64, change MySQLTableChange) error {
	first, second, err := tableChangeSQL(change)
	if err != nil {
		return err
	}
	m.mu.Lock()
	session := m.sessions[id]
	m.mu.Unlock()
	if session == nil {
		return ErrMySQLNotConnected
	}

	opCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	stop := context.AfterFunc(session.ctx, cancel)
	defer func() { stop(); cancel() }()
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
			return fmt.Errorf("%w: table action requires a base table", ErrInvalid)
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
