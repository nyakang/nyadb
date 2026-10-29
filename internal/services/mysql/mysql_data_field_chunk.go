package mysql

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const MySQLFieldChunkMaxUnits = 4096

// MySQLFieldChunk is a bounded, explicitly requested segment of a field.
// Offsets count MySQL characters for text and bytes for binary data.
type MySQLFieldChunk struct {
	Text                           string
	Offset, NextOffset, TotalUnits int64
	Binary, Null, HasMore          bool
}

// ReadMySQLFieldChunk is never called by ordinary table pagination. It
// revalidates the unique row identity against server metadata and retrieves
// one small segment without transferring the remainder of the field.
func (m *MySQLSessionManager) ReadMySQLFieldChunk(ctx context.Context, id int64,
	database, table, column string, ref MySQLRowRef, offset int64, units int) (*MySQLFieldChunk, error) {
	dbName, err := quotedMySQLIdentifier(database)
	if err != nil {
		return nil, err
	}
	tableName, err := quotedMySQLIdentifier(table)
	if err != nil {
		return nil, err
	}
	colName, err := quotedMySQLIdentifier(column)
	if err != nil {
		return nil, err
	}
	if ref.Database != database || ref.Table != table || offset < 0 || offset > 1_000_000_000 ||
		units < 1 || units > MySQLFieldChunkMaxUnits || len(ref.Columns) == 0 ||
		len(ref.Columns) != len(ref.Values) || len(ref.Columns) > 16 {
		return nil, ErrMySQLDataPage
	}
	for i, name := range ref.Columns {
		if _, err := quotedMySQLIdentifier(name); err != nil {
			return nil, err
		}
		if ref.Values[i] == nil || len(ref.Values[i]) > 4096 {
			return nil, ErrMySQLDataPage
		}
	}
	session, qctx, cancel, err := m.requestContext(ctx, id, 20*time.Second)
	if err != nil {
		return nil, err
	}
	defer cancel()

	var kind string
	err = session.db.QueryRowContext(qctx,
		`SELECT DATA_TYPE FROM information_schema.COLUMNS
 WHERE TABLE_SCHEMA=? AND TABLE_NAME=? AND COLUMN_NAME=?`, database, table, column).Scan(&kind)
	if err != nil {
		return nil, err
	}
	kind = strings.ToLower(kind)
	binary := mysqlDataBinaryType(kind)
	if !binary && !mysqlDataTextType(kind) {
		return nil, ErrMySQLDataPage
	}

	// Verify that the supplied ref names an entire NOT NULL UNIQUE index.
	const indexesSQL = `SELECT s.INDEX_NAME,s.COLUMN_NAME,s.SUB_PART,c.IS_NULLABLE
 FROM information_schema.STATISTICS s
 LEFT JOIN information_schema.COLUMNS c ON c.TABLE_SCHEMA=s.TABLE_SCHEMA
 AND c.TABLE_NAME=s.TABLE_NAME AND c.COLUMN_NAME=s.COLUMN_NAME
 WHERE s.TABLE_SCHEMA=? AND s.TABLE_NAME=? AND s.NON_UNIQUE=0
 ORDER BY s.INDEX_NAME,s.SEQ_IN_INDEX`
	rows, err := session.db.QueryContext(qctx, indexesSQL, database, table)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		cols  []string
		valid bool
	}
	candidates := map[string]*candidate{}
	for rows.Next() {
		var index string
		var name, nullable *string
		var prefix *int64
		if err = rows.Scan(&index, &name, &prefix, &nullable); err != nil {
			break
		}
		c := candidates[index]
		if c == nil {
			c = &candidate{valid: true}
			candidates[index] = c
		}
		if name == nil || nullable == nil || *nullable != "NO" || prefix != nil {
			c.valid = false
			continue
		}
		c.cols = append(c.cols, *name)
	}
	if err == nil {
		err = rows.Err()
	}
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	approved := false
	for _, c := range candidates {
		if !c.valid || len(c.cols) != len(ref.Columns) {
			continue
		}
		same := true
		for i, name := range c.cols {
			if ref.Columns[i] != name {
				same = false
				break
			}
		}
		if same {
			approved = true
			break
		}
	}
	if !approved {
		return nil, ErrMySQLDataPage
	}

	where := make([]string, len(ref.Columns))
	args := []any{offset + 1, units}
	for i, name := range ref.Columns {
		quoted, err := quotedMySQLIdentifier(name)
		if err != nil {
			return nil, err
		}
		where[i] = quoted + " = ?"
		args = append(args, ref.Values[i])
	}
	lengthFn := "CHAR_LENGTH"
	if binary {
		lengthFn = "LENGTH"
	}
	sqlText := "SELECT SUBSTRING(" + colName + ", ?, ?), " + lengthFn + "(" + colName + ") FROM " +
		dbName + "." + tableName + " WHERE " + strings.Join(where, " AND ") + " LIMIT 2"
	data, err := session.db.QueryContext(qctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer data.Close()
	if !data.Next() {
		if err = data.Err(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	var fragment []byte
	var total sql.NullInt64
	if err = data.Scan(&fragment, &total); err != nil {
		return nil, err
	}
	if data.Next() {
		return nil, fmt.Errorf("%w: multiple rows for unique identity", ErrMySQLDataPage)
	}
	if err = data.Err(); err != nil {
		return nil, err
	}
	if err = qctx.Err(); err != nil {
		return nil, err
	}
	if session.ctx.Err() != nil {
		return nil, context.Canceled
	}
	if !total.Valid {
		return &MySQLFieldChunk{Null: true, Offset: offset, Binary: binary}, nil
	}
	if total.Int64 < 0 {
		return nil, errors.New("invalid field length")
	}
	chunk := &MySQLFieldChunk{Offset: offset, TotalUnits: total.Int64, Binary: binary}
	if binary {
		chunk.Text = "0x" + hex.EncodeToString(fragment)
		chunk.NextOffset = offset + int64(len(fragment))
	} else {
		if !utf8.Valid(fragment) {
			return nil, fmt.Errorf("%w: non-UTF8 text chunk", ErrMySQLDataPage)
		}
		chunk.Text = string(fragment)
		chunk.NextOffset = offset + int64(utf8.RuneCount(fragment))
	}
	chunk.HasMore = chunk.NextOffset < chunk.TotalUnits
	return chunk, nil
}
