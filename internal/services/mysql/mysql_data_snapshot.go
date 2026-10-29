package mysql

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

func mysqlEditMetadata(columns []MySQLStructureColumn) string {
	raw, _ := json.Marshal(columns)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}
func mysqlSnapshotFromRaw(ref MySQLRowRef, columns []MySQLStructureColumn, raw []any) *MySQLRowSnapshot {
	s := &MySQLRowSnapshot{Ref: ref, Metadata: mysqlEditMetadata(columns)}
	for i, col := range columns {
		s.Columns = append(s.Columns, col.Name)
		var fingerprint *string
		if raw[i] != nil {
			value := string(mysqlEditRawBytes(raw[i]))
			fingerprint = &value
		}
		s.Fingerprints = append(s.Fingerprints, fingerprint)
	}
	return s
}
func mysqlEditRawBytes(v any) []byte {
	switch value := v.(type) {
	case []byte:
		return bytes.Clone(value)
	case string:
		return []byte(value)
	default:
		return []byte(fmt.Sprint(v))
	}
}

func mysqlEditSnapshot(ctx context.Context, q mysqlEditQueryer, database, table string, ref MySQLRowRef, columns []MySQLStructureColumn, lock bool) (*MySQLRowSnapshot, error) {
	target, e := mysqlEditTable(database, table)
	if e != nil {
		return nil, e
	}
	where, args, e := mysqlEditWhere(ref, database, table)
	if e != nil {
		return nil, e
	}
	fields := make([]string, len(columns))
	for i, col := range columns {
		quoted, _ := quotedMySQLIdentifier(col.Name)
		fields[i] = "SHA2(CAST(" + quoted + " AS BINARY),256)"
	}
	query := "SELECT " + strings.Join(fields, ",") + " FROM " + target + " WHERE " + where + " LIMIT 2"
	if lock {
		query += " FOR UPDATE"
	}
	rows, e := q.QueryContext(ctx, query, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	if !rows.Next() {
		if e = rows.Err(); e != nil {
			return nil, e
		}
		return nil, sql.ErrNoRows
	}
	raw := make([]any, len(columns))
	dest := make([]any, len(raw))
	for i := range raw {
		dest[i] = &raw[i]
	}
	if e = rows.Scan(dest...); e != nil {
		return nil, e
	}
	if rows.Next() {
		return nil, ErrMySQLDataReadOnly
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	return mysqlSnapshotFromRaw(ref, columns, raw), nil
}

// Read saved values without turning a long/blob field into an unbounded transfer.
func mysqlEditPreviews(ctx context.Context, q mysqlEditQueryer, database, table string, ref MySQLRowRef, columns []MySQLStructureColumn) ([]MySQLDataCell, error) {
	target, e := mysqlEditTable(database, table)
	if e != nil {
		return nil, e
	}
	where, args, e := mysqlEditWhere(ref, database, table)
	if e != nil {
		return nil, e
	}
	fields := make([]string, len(columns))
	for i, col := range columns {
		quoted, _ := quotedMySQLIdentifier(col.Name)
		switch {
		case mysqlDataBinaryType(strings.ToLower(col.DataType)):
			fields[i] = "SUBSTRING(" + quoted + ",1,1025)"
		case mysqlDataTextType(strings.ToLower(col.DataType)):
			fields[i] = "SUBSTRING(" + quoted + ",1,2049)"
		default:
			fields[i] = quoted
		}
	}
	rows, e := q.QueryContext(ctx, "SELECT "+strings.Join(fields, ",")+" FROM "+target+" WHERE "+where+" LIMIT 2", args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	if !rows.Next() {
		if e = rows.Err(); e != nil {
			return nil, e
		}
		return nil, sql.ErrNoRows
	}
	raw := make([]any, len(columns))
	dest := make([]any, len(raw))
	for i := range raw {
		dest[i] = &raw[i]
	}
	if e = rows.Scan(dest...); e != nil {
		return nil, e
	}
	if rows.Next() {
		return nil, ErrMySQLDataReadOnly
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	previews := make([]MySQLDataCell, len(columns))
	for i, col := range columns {
		previews[i] = mysqlDataPreviewCell(raw[i], col.DataType)
	}
	return previews, nil
}

func mysqlSnapshotsEqual(a, b *MySQLRowSnapshot) bool {
	return a != nil && b != nil && a.Metadata == b.Metadata && reflect.DeepEqual(a.Columns, b.Columns) && reflect.DeepEqual(a.Fingerprints, b.Fingerprints)
}

// A submitted key can compare equal to its stored value while using different
// bytes (for example, 00042 becomes 42 and DECIMAL gains its declared scale).
// Return the server spelling so subsequent edits and SQL clipboard predicates
// use the same identity as a freshly loaded page. Bound every key independently
// rather than relying on the shorter display preview.
func mysqlEditSavedRef(ctx context.Context, q mysqlEditQueryer, database, table string, ref MySQLRowRef) (*MySQLRowRef, error) {
	target, e := mysqlEditTable(database, table)
	if e != nil {
		return nil, e
	}
	where, args, e := mysqlEditWhere(ref, database, table)
	if e != nil {
		return nil, e
	}
	fields := make([]string, len(ref.Columns))
	for i, name := range ref.Columns {
		quoted, _ := quotedMySQLIdentifier(name)
		fields[i] = "SUBSTRING(CAST(" + quoted + " AS BINARY),1,4097)"
	}
	rows, e := q.QueryContext(ctx, "SELECT "+strings.Join(fields, ",")+" FROM "+target+" WHERE "+where+" LIMIT 2", args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	if !rows.Next() {
		if e = rows.Err(); e != nil {
			return nil, e
		}
		return nil, sql.ErrNoRows
	}
	next := &MySQLRowRef{Database: database, Table: table, Columns: append([]string(nil), ref.Columns...), Values: make([][]byte, len(ref.Columns))}
	dest := make([]any, len(next.Values))
	for i := range dest {
		dest[i] = &next.Values[i]
	}
	if e = rows.Scan(dest...); e != nil {
		return nil, e
	}
	if rows.Next() {
		return nil, ErrMySQLDataReadOnly
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if _, _, e = mysqlEditWhere(*next, database, table); e != nil {
		return nil, e
	}
	return next, nil
}
