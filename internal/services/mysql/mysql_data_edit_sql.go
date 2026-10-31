package mysql

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"
)

func MySQLDataColumnGenerated(col MySQLStructureColumn) bool {
	extra := strings.ToUpper(col.Extra)
	return strings.Contains(extra, "VIRTUAL GENERATED") || strings.Contains(extra, "STORED GENERATED")
}
func mysqlEditTable(database, table string) (string, error) {
	db, e := quotedMySQLIdentifier(database)
	if e != nil {
		return "", e
	}
	tbl, e := quotedMySQLIdentifier(table)
	if e != nil {
		return "", e
	}
	return db + "." + tbl, nil
}
func mysqlEditWhere(ref MySQLRowRef, database, table string) (string, []any, error) {
	if ref.Database != database || ref.Table != table || len(ref.Columns) == 0 || len(ref.Columns) > 16 || len(ref.Columns) != len(ref.Values) {
		return "", nil, ErrMySQLDataReadOnly
	}
	parts := make([]string, len(ref.Columns))
	args := make([]any, len(parts))
	seen := map[string]bool{}
	for i, name := range ref.Columns {
		q, e := quotedMySQLIdentifier(name)
		if e != nil {
			return "", nil, e
		}
		if seen[name] || ref.Values[i] == nil || len(ref.Values[i]) > 4096 {
			return "", nil, ErrMySQLDataReadOnly
		}
		seen[name] = true
		parts[i] = q + " = ?"
		args[i] = ref.Values[i]
	}
	return strings.Join(parts, " AND "), args, nil
}
func mysqlEditUniqueKeys(ctx context.Context, q mysqlEditQueryer, database, table string) ([][]string, error) {
	rows, e := q.QueryContext(ctx, `SELECT s.INDEX_NAME,s.COLUMN_NAME,s.SUB_PART,c.IS_NULLABLE
 FROM information_schema.STATISTICS s LEFT JOIN information_schema.COLUMNS c
 ON c.TABLE_SCHEMA=s.TABLE_SCHEMA AND c.TABLE_NAME=s.TABLE_NAME AND c.COLUMN_NAME=s.COLUMN_NAME
 WHERE s.TABLE_SCHEMA=? AND s.TABLE_NAME=? AND s.NON_UNIQUE=0 ORDER BY s.INDEX_NAME,s.SEQ_IN_INDEX`, database, table)
	if e != nil {
		return nil, e
	}
	type candidate struct {
		names   []string
		invalid bool
	}
	all := map[string]*candidate{}
	for rows.Next() {
		var index string
		var name, nullable *string
		var prefix *int64
		if e = rows.Scan(&index, &name, &prefix, &nullable); e != nil {
			break
		}
		c := all[index]
		if c == nil {
			c = &candidate{}
			all[index] = c
		}
		if name == nil || nullable == nil || *nullable != "NO" || prefix != nil {
			c.invalid = true
		} else {
			c.names = append(c.names, *name)
		}
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
	var keys [][]string
	for _, c := range all {
		if !c.invalid && len(c.names) > 0 {
			keys = append(keys, c.names)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return strings.Join(keys[i], "\x00") < strings.Join(keys[j], "\x00") })
	return keys, nil
}
func mysqlEditValidateRef(ctx context.Context, q mysqlEditQueryer, database, table string, ref MySQLRowRef) error {
	if _, _, e := mysqlEditWhere(ref, database, table); e != nil {
		return e
	}
	keys, e := mysqlEditUniqueKeys(ctx, q, database, table)
	if e != nil {
		return e
	}
	for _, names := range keys {
		if reflect.DeepEqual(names, ref.Columns) {
			return nil
		}
	}
	return ErrMySQLDataReadOnly
}

func mysqlEditAssignments(columns []MySQLStructureColumn, values map[string]MySQLDataValue, insert bool) ([]string, []string, []any, error) {
	return mysqlAssignmentsBounded(columns, values, insert, MySQLDataEditMaxBytes)
}
func mysqlAssignmentsBounded(columns []MySQLStructureColumn, values map[string]MySQLDataValue, insert bool, limit int) ([]string, []string, []any, error) {
	allowed := map[string]MySQLStructureColumn{}
	for _, col := range columns {
		allowed[col.Name] = col
	}
	keys := make([]string, 0, len(values))
	for name := range values {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	names := make([]string, 0, len(keys))
	expressions := make([]string, 0, len(keys))
	var args []any
	total := 0
	for _, name := range keys {
		col, ok := allowed[name]
		if !ok || MySQLDataColumnGenerated(col) {
			return nil, nil, nil, ErrMySQLDataPage
		}
		v := values[name]
		if (v.Null && v.Default) || (v.Binary && v.Text != "") || (!v.Binary && len(v.Bytes) > 0) {
			return nil, nil, nil, ErrMySQLDataPage
		}
		if v.Null && !col.Nullable {
			return nil, nil, nil, ErrMySQLDataPage
		}
		if (v.Null || v.Default) && (v.Text != "" || len(v.Bytes) > 0) {
			return nil, nil, nil, ErrMySQLDataPage
		}
		total += len(v.Text) + len(v.Bytes)
		if total > limit {
			return nil, nil, nil, ErrMySQLDataEditLimit
		}
		if !v.Binary && !utf8.ValidString(v.Text) {
			return nil, nil, nil, ErrMySQLDataPage
		}
		q, _ := quotedMySQLIdentifier(name)
		names = append(names, q)
		switch {
		case v.Default:
			expressions = append(expressions, "DEFAULT")
		case v.Null:
			expressions = append(expressions, "?")
			args = append(args, nil)
		case v.Binary:
			if mysqlGeometryType(col.DataType) {
				if len(v.Bytes) < 9 {
					return nil, nil, nil, ErrMySQLDataPage
				}
				expressions = append(expressions, "ST_GeomFromWKB(?, ?, 'axis-order=long-lat')")
				args = append(args, v.Bytes[4:], binary.LittleEndian.Uint32(v.Bytes[:4]))
				continue
			}
			expressions = append(expressions, "?")
			// A nil []byte is SQL NULL to the driver. Binary without Null
			// means an empty binary value, including hex/base64 import cells.
			if v.Bytes == nil {
				v.Bytes = []byte{}
			}
			args = append(args, v.Bytes)
		default:
			expressions = append(expressions, "?")
			args = append(args, v.Text)
		}
	}
	if !insert && len(keys) == 0 {
		return nil, nil, nil, ErrMySQLDataPage
	}
	return names, expressions, args, nil
}
func mysqlEditChangedRef(ref MySQLRowRef, values map[string]MySQLDataValue) (MySQLRowRef, error) {
	next := MySQLRowRef{Database: ref.Database, Table: ref.Table, Columns: append([]string(nil), ref.Columns...), Values: make([][]byte, len(ref.Values))}
	for i, name := range ref.Columns {
		next.Values[i] = bytes.Clone(ref.Values[i])
		if v, ok := values[name]; ok {
			if v.Default || v.Null {
				return next, ErrMySQLDataPage
			}
			if v.Binary {
				next.Values[i] = append([]byte{}, v.Bytes...)
			} else {
				next.Values[i] = []byte(v.Text)
			}
			if len(next.Values[i]) > 4096 {
				return next, ErrMySQLDataPage
			}
		}
	}
	return next, nil
}

func mysqlGeometryType(kind string) bool {
	switch strings.ToLower(kind) {
	case "geometry", "point", "linestring", "polygon", "multipoint", "multilinestring", "multipolygon", "geometrycollection":
		return true
	}
	return false
}
