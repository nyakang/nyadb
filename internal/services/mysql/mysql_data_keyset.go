package mysql

import (
	"errors"
	"fmt"
	"strings"
)

// MySQLDataCursor is a bounded, opaque-to-the-UI page boundary. Only cursors
// returned by this read-only service should be reused. This is not a
// cross-transaction snapshot: concurrent changes can still move rows.
type MySQLDataCursor struct {
	RuleSignature                         string
	Database, Table                       string
	FilterColumn, FilterMode, FilterValue string
	SortColumn                            string
	SortDescending                        bool
	Columns                               []string
	Descending                            []bool
	Values                                [][]byte
}

type mysqlOrderKey struct {
	name string
	desc bool
}

// mysqlKeysetPredicate constructs a lexicographic seek, with each column's
// own direction. The identity keys must be unique and non-null; a nullable
// user sort column is not safe for a seek and must fall back to OFFSET.
func mysqlKeysetPredicate(keys []mysqlOrderKey, cursor *MySQLDataCursor) (string, []any, error) {
	if cursor == nil || len(keys) == 0 || len(keys) != len(cursor.Values) ||
		len(keys) != len(cursor.Columns) || len(keys) != len(cursor.Descending) {
		return "", nil, ErrMySQLDataPage
	}
	clauses := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys)*(len(keys)+1)/2)
	for i, key := range keys {
		if cursor.Columns[i] != key.name || cursor.Descending[i] != key.desc ||
			len(cursor.Values[i]) > 4096 || cursor.Values[i] == nil {
			return "", nil, ErrMySQLDataPage
		}
		prefix := make([]string, 0, i+1)
		for j := 0; j < i; j++ {
			name, err := quotedMySQLIdentifier(keys[j].name)
			if err != nil {
				return "", nil, err
			}
			prefix = append(prefix, name+" = ?")
			args = append(args, cursor.Values[j])
		}
		name, err := quotedMySQLIdentifier(key.name)
		if err != nil {
			return "", nil, err
		}
		op := ">"
		if key.desc {
			op = "<"
		}
		prefix = append(prefix, name+" "+op+" ?")
		args = append(args, cursor.Values[i])
		clauses = append(clauses, "("+strings.Join(prefix, " AND ")+")")
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args, nil
}

func mysqlNextDataCursor(keys []mysqlOrderKey, values []any, scope MySQLDataCursor) (*MySQLDataCursor, error) {
	if len(keys) != len(values) {
		return nil, errors.New("cursor values do not match sort keys")
	}
	scope.Columns = make([]string, len(keys))
	scope.Descending = make([]bool, len(keys))
	scope.Values = make([][]byte, len(keys))
	for i, k := range keys {
		scope.Columns[i], scope.Descending[i] = k.name, k.desc
		var buf []byte
		switch v := values[i].(type) {
		case nil:
			return nil, ErrMySQLDataPage
		case []byte:
			buf = append(make([]byte, 0, len(v)), v...)
		case string:
			buf = []byte(v)
		default:
			buf = []byte(fmt.Sprint(v))
		}
		if len(buf) > 4096 {
			return nil, ErrMySQLDataPage
		}
		scope.Values[i] = buf
	}
	return &scope, nil
}

// MySQLRowRef is an identity from a verified full-length NOT NULL UNIQUE
// index. It is used only for per-cell, bounded explicit reads.
type MySQLRowRef struct {
	Database, Table string
	Columns         []string
	Values          [][]byte
}

func containsOrderColumn(keys []mysqlOrderKey, name string) bool {
	for _, key := range keys {
		if key.name == name {
			return true
		}
	}
	return false
}
