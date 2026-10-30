package mysql

import (
	"bytes"
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode/utf8"
)

// Explicit full read uses a transaction to keep the length check, bytes and
// fingerprints consistent. No ordinary preview can be submitted as a full value.
func (m *MySQLSessionManager) ReadMySQLDataRow(ctx context.Context, id int64, database, table string, ref MySQLRowRef) (*MySQLDataRow, error) {
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
	columns, e := readMySQLColumnMetadata(qctx, conn, database, table, 128)
	if e != nil {
		return nil, e
	}
	if len(columns) == 0 {
		return nil, ErrMySQLTableNotFound
	}
	if e = mysqlEditValidateRef(qctx, conn, database, table, ref); e != nil {
		return nil, e
	}
	tx, e := conn.BeginTx(qctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	result, e := readMySQLDataRowInTransaction(qctx, tx, database, table, target, ref, columns)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return result, nil
}

func readMySQLDataRowInTransaction(qctx context.Context, tx *sql.Tx, database, table, target string, ref MySQLRowRef, columns []MySQLStructureColumn) (*MySQLDataRow, error) {
	where, args, e := mysqlEditWhere(ref, database, table)
	if e != nil {
		return nil, e
	}
	names := make([]string, len(columns))
	lengths := make([]string, len(columns))
	for i, col := range columns {
		names[i], _ = quotedMySQLIdentifier(col.Name)
		lengths[i] = "COALESCE(OCTET_LENGTH(CAST(" + names[i] + " AS BINARY)),0)"
	}
	var size int64
	if e = tx.QueryRowContext(qctx, "SELECT "+strings.Join(lengths, " + ")+" FROM "+target+" WHERE "+where, args...).Scan(&size); e != nil {
		return nil, e
	}
	if size > MySQLDataEditMaxBytes {
		return nil, ErrMySQLDataEditLimit
	}
	rows, e := tx.QueryContext(qctx, "SELECT "+strings.Join(names, ",")+" FROM "+target+" WHERE "+where+" LIMIT 2", args...)
	if e != nil {
		return nil, e
	}
	raw := make([][]byte, len(columns))
	dest := make([]any, len(raw))
	for i := range raw {
		dest[i] = &raw[i]
	}
	if !rows.Next() {
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		return nil, sql.ErrNoRows
	}
	e = rows.Scan(dest...)
	if rows.Next() {
		e = ErrMySQLDataReadOnly
	}
	if e == nil {
		e = rows.Err()
	}
	ce := rows.Close()
	if e != nil {
		return nil, e
	}
	if ce != nil {
		return nil, ce
	}
	result := &MySQLDataRow{Columns: columns, Values: map[string]MySQLDataValue{}}
	for i, col := range columns {
		v := MySQLDataValue{Null: raw[i] == nil, Binary: mysqlDataBinaryType(strings.ToLower(col.DataType))}
		if v.Binary {
			v.Bytes = bytes.Clone(raw[i])
		} else {
			if !utf8.Valid(raw[i]) {
				return nil, ErrMySQLDataPage
			}
			v.Text = string(raw[i])
		}
		result.Values[col.Name] = v
	}
	result.Snapshot, e = mysqlEditSnapshot(qctx, tx, database, table, ref, columns, false)
	if e != nil {
		return nil, e
	}
	return result, nil
}
