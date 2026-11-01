package engines

import (
	"context"
	"database/sql"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"strings"
)

func (s *Session) sqliteStructure(ctx context.Context, q queryer, o contract.ObjectRef) (contract.Table, error) {
	t := contract.Table{Object: o}
	rows, e := q.QueryContext(ctx, `SELECT sql,type FROM main.sqlite_schema WHERE name=?`, o.Name)
	if e != nil {
		return t, e
	}
	var typ string
	if !rows.Next() {
		rows.Close()
		return t, errors.New("object absent")
	}
	e = rows.Scan(&t.Definition, &typ)
	if typ == "view" {
		t.Object.Kind = "view"
	}
	rows.Close()
	if e != nil {
		return t, e
	}
	rows, e = q.QueryContext(ctx, `PRAGMA main.table_xinfo(`+quote(o.Name)+`)`)
	if e != nil {
		return t, e
	}
	keys := map[int]string{}
	rawKeys := map[int]string{}
	for rows.Next() {
		var cid, notnull, pk, hidden int
		var f contract.Field
		var def sql.NullString
		if e = rows.Scan(&cid, &f.Name, &f.DatabaseType, &notnull, &def, &pk, &hidden); e != nil {
			rows.Close()
			return t, e
		}
		f.Nullable = notnull == 0
		f.Generated = hidden == 2 || hidden == 3
		f.Primary = pk > 0
		if pk > 0 {
			rawKeys[pk] = f.Name
		}
		if def.Valid {
			f.Default = &def.String
		}
		if pk > 0 && notnull > 0 {
			keys[pk] = f.Name
			f.Nullable = false
		}
		if hidden != 1 {
			t.Columns = append(t.Columns, f)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return t, e
	}
	indexedPrimary := false
	checkIndexes, er := q.QueryContext(ctx, `PRAGMA main.index_list(`+quote(o.Name)+`)`)
	if er != nil {
		return t, er
	}
	for checkIndexes.Next() {
		var seq, unique, partial int
		var name, origin string
		if er = checkIndexes.Scan(&seq, &name, &unique, &origin, &partial); er != nil {
			checkIndexes.Close()
			return t, er
		}
		if origin == "pk" {
			indexedPrimary = true
		}
	}
	er = checkIndexes.Err()
	checkIndexes.Close()
	if er != nil {
		return t, er
	}
	if len(rawKeys) == 1 && !indexedPrimary {
		for i := range t.Columns {
			f := &t.Columns[i]
			if f.Primary && strings.EqualFold(f.DatabaseType, "INTEGER") {
				keys[1] = f.Name
				f.Nullable = false
			}
		}
	}
	if len(keys) != len(rawKeys) {
		keys = map[int]string{}
	}
	for i := 1; i <= len(keys); i++ {
		if keys[i] == "" {
			t.Key = nil
			break
		}
		t.Key = append(t.Key, keys[i])
	}
	if len(t.Key) == 0 {
		rows, e = q.QueryContext(ctx, `PRAGMA main.index_list(`+quote(o.Name)+`)`)
		if e != nil {
			return t, e
		}
		var indexes []string
		for rows.Next() {
			var seq, unique, partial int
			var name, origin string
			if e = rows.Scan(&seq, &name, &unique, &origin, &partial); e != nil {
				rows.Close()
				return t, e
			}
			if unique == 1 && partial == 0 {
				indexes = append(indexes, name)
			}
		}
		rows.Close()
		for _, name := range indexes {
			rs, er := q.QueryContext(ctx, `PRAGMA main.index_info(`+quote(name)+`)`)
			if er != nil {
				return t, er
			}
			var key []string
			ok := true
			for rs.Next() {
				var seq, cid int
				var n sql.NullString
				er = rs.Scan(&seq, &cid, &n)
				if er != nil {
					rs.Close()
					return t, er
				}
				if !n.Valid {
					ok = false
					continue
				}
				found := false
				for _, f := range t.Columns {
					if f.Name == n.String && !f.Nullable {
						found = true
					}
				}
				if !found {
					ok = false
				}
				key = append(key, n.String)
			}
			rs.Close()
			if ok && len(key) > 0 {
				t.Key = key
				break
			}
		}
	}
	t.Editable = typ == "table" && o.Kind == "table" && len(t.Key) > 0
	defs, err := q.QueryContext(ctx, `SELECT name,type,sql FROM main.sqlite_schema WHERE tbl_name=? AND type IN ('index','trigger') AND sql IS NOT NULL ORDER BY type,name`, o.Name)
	if err != nil {
		return t, err
	}
	for defs.Next() {
		var d contract.Definition
		if err = defs.Scan(&d.Name, &d.Kind, &d.SQL); err != nil {
			defs.Close()
			return t, err
		}
		if len(t.Definitions) >= 1000 || len(d.SQL) > 1<<20 {
			defs.Close()
			return t, errors.New("structure definition limit exceeded")
		}
		t.Definitions = append(t.Definitions, d)
	}
	err = defs.Err()
	defs.Close()
	return t, err
}

func (sqliteDialect) structure(ctx context.Context, s *Session, q queryer, o contract.ObjectRef) (contract.Table, error) {
	return s.sqliteStructure(ctx, q, o)
}
func (sqliteDialect) schemas(context.Context, queryer) ([]string, error) {
	return []string{"main"}, nil
}
func (d sqliteDialect) objects(ctx context.Context, q queryer, schema string) (*sql.Rows, error) {
	if e := d.namespace(schema); e != nil {
		return nil, e
	}
	return q.QueryContext(ctx, `SELECT name, type FROM main.sqlite_schema WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY name`)
}
