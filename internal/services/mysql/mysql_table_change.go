package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	RuntimeGeneration uint64

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
	case MySQLTableRename:
		name, e := quotedMySQLIdentifier(change.NewName)
		if e != nil {
			return "", e
		}
		if change.NewName == change.Table {
			return "", fmt.Errorf("%w: destination must differ", connections.ErrInvalid)
		}
		target = schema + "." + name
		return "RENAME TABLE " + source + " TO " + target, nil
	case MySQLTableCopyStructure, MySQLTableCopyData:
		return "", fmt.Errorf("%w: table copies require the full public DDL planner", connections.ErrInvalid)
	default:
		return "", fmt.Errorf("%w: unsupported table action", connections.ErrInvalid)
	}
}

// ChangeTable runs only on an already authenticated connection's metadata
// pool, not on a SQL tab's pinned transactional session. This operation never
// silently retries or replays DDL after a connection failure.
func (m *MySQLSessionManager) ChangeTable(ctx context.Context, id int64, change MySQLTableChange) error {
	if change.Action == MySQLTableCopyStructure || change.Action == MySQLTableCopyData {
		return m.copyCompleteTable(ctx, id, change)
	}
	first, err := tableChangeSQL(change)
	if err != nil {
		return err
	}
	session, opCtx, cancel, err := m.requestContext(ctx, id, 45*time.Second)
	if err != nil {
		return err
	}
	defer cancel()
	if change.RuntimeGeneration != 0 && m.SessionGeneration(id) != change.RuntimeGeneration {
		return ErrMySQLNotConnected
	}
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

	conn, err := session.db.Conn(opCtx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(opCtx, first); err != nil {
		return err
	}
	return nil
}
