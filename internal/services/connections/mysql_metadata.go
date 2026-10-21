package connections

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrMySQLMetadataLimit = errors.New("MySQL metadata result exceeds the allowed item count")

// MySQLDBObject describes only server metadata: it never contains table data
// or user-authored SQL. Kind is either "表" or "视图".
type MySQLDBObject struct {
	Name    string
	Kind    string
	Engine  string
	Comment string
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

	const query = `SELECT TABLE_NAME, TABLE_TYPE, COALESCE(ENGINE, ''), COALESCE(TABLE_COMMENT, '')
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
		if err := rows.Scan(&obj.Name, &tableType, &obj.Engine, &obj.Comment); err != nil {
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
