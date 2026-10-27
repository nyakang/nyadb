package connections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrMySQLMetadataLimit = errors.New("MySQL metadata result exceeds the allowed item count")

// MySQLDBObject describes only server metadata: it never contains table data
// or user-authored SQL. Kind can be 表, 视图, 函数, 存储过程 or 事件.
type MySQLDBObject struct {
	Name          string
	Kind          string
	Engine        string
	Comment       string
	AutoIncrement *int64 // nil if the server has no next AUTO_INCREMENT value
	ModifiedAt    string // server UPDATE_TIME; empty if unknown
	DataLength    *int64 // server DATA_LENGTH in bytes; nil if unavailable
	RowsEstimate  *int64 // TABLE_ROWS is an estimate for engines such as InnoDB
	Definer       string
	Parameters    string
	ReturnType    string
	SQLSecurity   string
	CreatedAt     string
	Status        string
	Schedule      string
	Starts        string
	Ends          string
	OnCompletion  string
	Definition    string // only populated when server privileges permit
}

const maxMySQLMetadataObjects = 10000

// Objects uses an INFORMATION_SCHEMA predicate bound to the requested schema,
// never dynamic SQL identifiers. The query belongs to the session and is
// canceled by Disconnect/Close as well as by the caller.
func (m *MySQLSessionManager) Objects(ctx context.Context, id int64, database string) ([]MySQLDBObject, error) {
	if database == "" || len(database) > 256 || strings.ContainsRune(database, 0) {
		return nil, fmt.Errorf("%w: invalid database name", ErrInvalid)
	}
	m.mu.Lock()
	session := m.sessions[id]
	m.mu.Unlock()
	if session == nil {
		return nil, ErrMySQLNotConnected
	}
	queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	stop := context.AfterFunc(session.ctx, cancel)
	defer func() { stop(); cancel() }()

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
