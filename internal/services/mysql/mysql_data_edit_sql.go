package mysql

import (
	"bytes"
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/mysqlspec"
	"reflect"
	"sort"
	"strings"
)

func MySQLDataColumnGenerated(col MySQLStructureColumn) bool {
	return contract.DataColumnGenerated(col)
}

func mysqlEditTable(database, table string) (string, error) {
	return mysqlspec.QualifiedTable(database, table)
}
func mysqlEditWhere(ref MySQLRowRef, database, table string) (string, []any, error) {
	return mysqlspec.RowPredicate(ref, database, table)
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
	return mysqlspec.AssignmentsBounded(columns, values, insert, limit)
}
func mysqlEditChangedRef(ref MySQLRowRef, values map[string]MySQLDataValue) (MySQLRowRef, error) {
	next := MySQLRowRef{Object: ref.Object, Database: ref.Database, Table: ref.Table, Columns: append([]string(nil), ref.Columns...), Values: make([][]byte, len(ref.Values))}
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

func mysqlGeometryType(kind string) bool { return mysqlspec.GeometryType(kind) }
