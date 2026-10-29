package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// MySQLTableInfo is a snapshot of one table's server metadata. Nil statistics
// mean unavailable, not zero; dates retain the server's timezone and spelling.
type MySQLTableInfo struct {
	Database, Name, Kind, Engine, RowFormat, Collation, Comment, CreateOptions     string
	CreatedAt, ModifiedAt, CheckedAt                                               string
	EstimatedRows, AutoIncrement, DataLength, IndexLength, MaxDataLength, DataFree *int64
}

// TableInfo reads one object without loading its data, columns or indexes.
func (m *MySQLSessionManager) TableInfo(ctx context.Context, id int64, database, table string) (*MySQLTableInfo, error) {
	if !validMySQLMetadataName(database) || !validMySQLMetadataName(table) {
		return nil, fmt.Errorf("%w: invalid database or table name", connections.ErrInvalid)
	}
	session, queryCtx, cancel, err := m.requestContext(ctx, id, 20*time.Second)
	if err != nil {
		return nil, err
	}
	defer cancel()
	const query = `SELECT TABLE_TYPE, ENGINE, ROW_FORMAT, TABLE_COLLATION, TABLE_COMMENT,
CREATE_OPTIONS, CAST(CREATE_TIME AS CHAR), CAST(UPDATE_TIME AS CHAR), CAST(CHECK_TIME AS CHAR),
TABLE_ROWS, AUTO_INCREMENT, DATA_LENGTH, INDEX_LENGTH, MAX_DATA_LENGTH, DATA_FREE
FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
AND TABLE_TYPE IN ('BASE TABLE', 'VIEW')`
	info := &MySQLTableInfo{Database: database, Name: table}
	var engine, rowFormat, collation, comment, options, created, modified, checked sql.NullString
	var rows, autoIncrement, dataLength, indexLength, maxDataLength, dataFree sql.NullInt64
	err = session.db.QueryRowContext(queryCtx, query, database, table).Scan(
		&info.Kind, &engine, &rowFormat, &collation, &comment, &options, &created, &modified, &checked,
		&rows, &autoIncrement, &dataLength, &indexLength, &maxDataLength, &dataFree)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMySQLTableNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := queryCtx.Err(); err != nil {
		return nil, err
	}
	if session.ctx.Err() != nil {
		return nil, context.Canceled
	}
	info.Comment = comment.String
	if info.Kind == "BASE TABLE" {
		info.Engine, info.RowFormat, info.Collation = engine.String, rowFormat.String, collation.String
		info.CreateOptions = options.String
		info.CreatedAt, info.ModifiedAt, info.CheckedAt = created.String, modified.String, checked.String
		info.EstimatedRows, info.AutoIncrement = mysqlNullableInt(rows), mysqlNullableInt(autoIncrement)
		info.DataLength, info.IndexLength = mysqlNullableInt(dataLength), mysqlNullableInt(indexLength)
		info.MaxDataLength, info.DataFree = mysqlNullableInt(maxDataLength), mysqlNullableInt(dataFree)
	}
	return info, nil
}

// CountTableRows explicitly counts the whole object, independent of any data
// grid filter. It is never called as part of TableInfo or data page loading.
func (m *MySQLSessionManager) CountTableRows(ctx context.Context, id int64, database, table string) (int64, error) {
	dbName, err := quotedMySQLIdentifier(database)
	if err != nil {
		return 0, err
	}
	tableName, err := quotedMySQLIdentifier(table)
	if err != nil {
		return 0, err
	}
	session, queryCtx, cancel, err := m.requestContext(ctx, id, 20*time.Second)
	if err != nil {
		return 0, err
	}
	defer cancel()
	var count int64
	if err := session.db.QueryRowContext(queryCtx, "SELECT COUNT(*) FROM "+dbName+"."+tableName).Scan(&count); err != nil {
		return 0, err
	}
	if err := queryCtx.Err(); err != nil {
		return 0, err
	}
	if session.ctx.Err() != nil {
		return 0, context.Canceled
	}
	return count, nil
}
