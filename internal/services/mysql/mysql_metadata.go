package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

var ErrMySQLMetadataLimit = errors.New("MySQL metadata result exceeds the allowed item count")

// MySQLDBObject describes only server metadata: it never contains table data
// or user-authored SQL. Kind can be 表, 视图, 函数, 存储过程 or 事件.
type MySQLDBObject = contract.DBObject

const maxMySQLMetadataObjects = 10000

// Objects uses an INFORMATION_SCHEMA predicate bound to the requested schema,
// never dynamic SQL identifiers. The query belongs to the session and is
// canceled by Disconnect/Close as well as by the caller.
func (m *MySQLSessionManager) Objects(ctx context.Context, id int64, database string) ([]MySQLDBObject, error) {
	if database == "" || len(database) > 256 || strings.ContainsRune(database, 0) {
		return nil, fmt.Errorf("%w: invalid database name", connections.ErrInvalid)
	}
	session, queryCtx, cancel, err := m.requestContext(ctx, id, 15*time.Second)
	if err != nil {
		return nil, err
	}
	defer cancel()

	const query = `SELECT TABLE_NAME, TABLE_TYPE, COALESCE(ENGINE, ''), COALESCE(TABLE_COMMENT, ''),
       AUTO_INCREMENT, CAST(UPDATE_TIME AS CHAR), DATA_LENGTH, TABLE_ROWS
FROM information_schema.TABLES
WHERE TABLE_SCHEMA = ? AND TABLE_TYPE IN ('BASE TABLE', 'VIEW')
ORDER BY TABLE_NAME`
	rows, err := session.db.QueryContext(queryCtx, query, database)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	objects := make([]MySQLDBObject, 0)
	for rows.Next() {
		var obj MySQLDBObject
		var tableType string
		var nextValue, dataLength, rowsEstimate sql.NullInt64
		var modified sql.NullString
		if err := rows.Scan(&obj.Name, &tableType, &obj.Engine, &obj.Comment,
			&nextValue, &modified, &dataLength, &rowsEstimate); err != nil {
			return nil, err
		}
		switch tableType {
		case "BASE TABLE":
			obj.Kind = "表"
		case "VIEW":
			obj.Kind = "视图"
		default:
			continue
		}
		if modified.Valid {
			obj.ModifiedAt = modified.String
		}
		// Server rows for views are NULL. Do not synthesize table statistics
		// for views even if a vendor happens to return numeric placeholders.
		if obj.Kind == "表" {
			if nextValue.Valid {
				obj.AutoIncrement = &nextValue.Int64
			}
			if dataLength.Valid {
				obj.DataLength = &dataLength.Int64
			}
			if rowsEstimate.Valid {
				obj.RowsEstimate = &rowsEstimate.Int64
			}
		}
		if len(objects) == maxMySQLMetadataObjects {
			return nil, ErrMySQLMetadataLimit
		}
		objects = append(objects, obj)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if queryCtx.Err() != nil {
		return nil, queryCtx.Err()
	}
	return objects, nil
}
