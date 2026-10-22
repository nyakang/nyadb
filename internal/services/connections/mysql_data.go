package connections

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MySQLDataDefaultPageSize = 100
	MySQLDataMaxPageSize     = 200
	mysqlDataCellMaxBytes    = 2048
)

var ErrMySQLDataPage = errors.New("invalid MySQL data page")

// MySQLDataCell preserves SQL NULL separately from text and bounds cell text.
// Truncated applies to the displayed preview only, never modifies the database.
type MySQLDataCell struct {
	Text      string
	Null      bool
	Truncated bool
}

// MySQLDataPage is a bounded, read-only result from an authenticated session.
// No total COUNT is run; HasMore is determined by fetching one extra row.
type MySQLDataPage struct {
	Database, Table string
	Columns         []string
	Rows            [][]MySQLDataCell
	Offset          int64
	Limit           int
	HasMore         bool
	StableOrder     bool // true only when the table has a primary-key order.
}

func quotedMySQLIdentifier(name string) (string, error) {
	if !validMySQLMetadataName(name) || !utf8.ValidString(name) || strings.ContainsAny(name, "\r\n") {
		return "", fmt.Errorf("%w: invalid database or table name", ErrInvalid)
	}
	// Backtick is the MySQL identifier delimiter. Escaping it by doubling
	// prevents even user-specified names from changing the SQL structure.
	return "`" + strings.ReplaceAll(name, "`", "``") + "`", nil
}

// TableDataPage reads up to 200 rows and one lookahead row on the existing
// managed session, subject to cancellation, disconnect and a 20s deadline.
// No writes, transaction state changes, SELECT COUNT(*) or full-table load.
func (m *MySQLSessionManager) TableDataPage(ctx context.Context, id int64, database, table string, offset int64, limit int) (*MySQLDataPage, error) {
	dbName, err := quotedMySQLIdentifier(database)
	if err != nil {
		return nil, err
	}
	tableName, err := quotedMySQLIdentifier(table)
	if err != nil {
		return nil, err
	}
	if offset < 0 || limit < 1 || limit > MySQLDataMaxPageSize || offset > 1_000_000_000 {
		return nil, ErrMySQLDataPage
	}
	m.mu.Lock()
	session := m.sessions[id]
	m.mu.Unlock()
	if session == nil {
		return nil, ErrMySQLNotConnected
	}
	queryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	stop := context.AfterFunc(session.ctx, cancel)
	defer func() { stop(); cancel() }()

	// A primary key provides stable ordering for paging within a snapshot.
	// Tables lacking one and views may paginate nondeterministically when other
	// sessions modify rows; the UI explicitly reports this limitation.
	const pkQuery = `SELECT COLUMN_NAME FROM information_schema.KEY_COLUMN_USAGE
WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND CONSTRAINT_NAME = 'PRIMARY'
ORDER BY ORDINAL_POSITION`
	keys, err := session.db.QueryContext(queryCtx, pkQuery, database, table)
	if err != nil {
		return nil, err
	}
	var order []string
	for keys.Next() {
		var key string
		if err = keys.Scan(&key); err != nil {
			break
		}
		var escaped string
		escaped, err = quotedMySQLIdentifier(key)
		if err != nil {
			break
		}
		order = append(order, escaped)
	}
	if err == nil {
		err = keys.Err()
	}
	closeErr := keys.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}

	query := "SELECT * FROM " + dbName + "." + tableName
	if len(order) > 0 {
		query += " ORDER BY " + strings.Join(order, ", ")
	}
	query += " LIMIT ? OFFSET ?"
	rows, err := session.db.QueryContext(queryCtx, query, limit+1, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if len(names) > maxMySQLMetadataObjects {
		return nil, ErrMySQLMetadataLimit
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	result := &MySQLDataPage{
		Database: database, Table: table, Columns: names, Offset: offset,
		Limit: limit, StableOrder: len(order) > 0,
	}
	scans := make([]any, len(names))
	values := make([]any, len(names))
	for i := range scans {
		scans[i] = &values[i]
	}
	for rows.Next() {
		if len(result.Rows) == limit {
			result.HasMore = true
			break
		}
		if err := rows.Scan(scans...); err != nil {
			return nil, err
		}
		cells := make([]MySQLDataCell, len(names))
		for i, value := range values {
			typeName := ""
			if i < len(types) {
				typeName = types[i].DatabaseTypeName()
			}
			cells[i] = mysqlDataPreviewCell(value, typeName)
		}
		result.Rows = append(result.Rows, cells)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := queryCtx.Err(); err != nil {
		return nil, err
	}
	if session.ctx.Err() != nil {
		return nil, context.Canceled
	}
	return result, nil
}

func mysqlDataPreviewCell(v any, dbType string) MySQLDataCell {
	if v == nil {
		return MySQLDataCell{Null: true}
	}
	var text string
	switch value := v.(type) {
	case []byte:
		upper := strings.ToUpper(dbType)
		if strings.Contains(upper, "BLOB") || strings.Contains(upper, "BINARY") || strings.Contains(upper, "BIT") || !utf8.Valid(value) {
			// Raw binary must never be interpreted as terminal control sequences.
			text = "0x" + hex.EncodeToString(value)
		} else {
			text = string(value)
		}
	case string:
		text = value
	default:
		text = fmt.Sprint(value)
	}
	cell := MySQLDataCell{}
	if len(text) > mysqlDataCellMaxBytes {
		text = text[:mysqlDataCellMaxBytes]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		cell.Truncated = true
	}
	cell.Text = text
	return cell
}
