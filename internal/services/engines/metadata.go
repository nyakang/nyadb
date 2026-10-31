package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"strings"
)

func (s *Session) Schemas(ctx context.Context) ([]string, error) {
	ctx, done := s.requestContext(ctx)
	defer done()
	if s.isClosed() {
		return nil, ErrRevoked
	}
	if s.kind == connections.SQLite {
		return []string{"main"}, nil
	}
	rows, e := s.db.QueryContext(ctx, `SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname <> 'information_schema' ORDER BY nspname`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if e = rows.Scan(&n); e != nil {
			return nil, e
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
func (s *Session) Objects(ctx context.Context, schema string) ([]contract.Object, error) {
	ctx, done := s.requestContext(ctx)
	defer done()
	if s.isClosed() {
		return nil, ErrRevoked
	}
	var rows *sql.Rows
	var e error
	if s.kind == connections.SQLite {
		if schema != "main" {
			return nil, errors.New("SQLite supports main only")
		}
		rows, e = s.db.QueryContext(ctx, `SELECT name, type FROM main.sqlite_schema WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	} else {
		rows, e = s.db.QueryContext(ctx, `SELECT c.relname, CASE WHEN c.relkind IN ('v','m') THEN 'view' ELSE 'table' END FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN ('r','p','v','m','f') ORDER BY c.relname`, schema)
	}
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []contract.Object
	for rows.Next() {
		var n, k string
		if e = rows.Scan(&n, &k); e != nil {
			return nil, e
		}
		out = append(out, contract.Object{Ref: contract.ObjectRef{ProfileID: s.id, Catalog: s.Catalog(), Schema: schema, Kind: k, Name: n}})
	}
	return out, rows.Err()
}
func (s *Session) Structure(ctx context.Context, o contract.ObjectRef) (contract.Table, error) {
	ctx, done := s.requestContext(ctx)
	defer done()
	return s.structure(ctx, s.db, o)
}
func (s *Session) structure(ctx context.Context, q queryer, o contract.ObjectRef) (contract.Table, error) {
	t := contract.Table{Object: o}
	if e := s.check(o); e != nil {
		return t, e
	}
	if s.kind == connections.SQLite {
		return s.sqliteStructure(ctx, q, o)
	}
	var relkind string
	kr, ke := q.QueryContext(ctx, `SELECT c.relkind FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2`, o.Schema, o.Name)
	if ke != nil {
		return t, ke
	}
	if kr.Next() {
		ke = kr.Scan(&relkind)
	} else {
		ke = errors.New("object absent")
	}
	kr.Close()
	if ke != nil {
		return t, ke
	}
	rows, e := q.QueryContext(ctx, `SELECT a.attname,format_type(a.atttypid,a.atttypmod),NOT a.attnotnull,a.attgenerated<>'',pg_get_expr(d.adbin,d.adrelid) FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE n.nspname=$1 AND c.relname=$2 AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, o.Schema, o.Name)
	if e != nil {
		return t, e
	}
	for rows.Next() {
		var f contract.Field
		var def sql.NullString
		if e = rows.Scan(&f.Name, &f.DatabaseType, &f.Nullable, &f.Generated, &def); e != nil {
			rows.Close()
			return t, e
		}
		if def.Valid {
			f.Default = &def.String
		}
		t.Columns = append(t.Columns, f)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return t, e
	}
	if len(t.Columns) == 0 {
		return t, errors.New("object absent or no visible fields")
	}
	rows, e = q.QueryContext(ctx, `SELECT i.indexrelid,a.attname,i.indisprimary FROM pg_index i JOIN pg_class c ON c.oid=i.indrelid JOIN pg_namespace n ON n.oid=c.relnamespace CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY k(attnum,ord) JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=k.attnum WHERE n.nspname=$1 AND c.relname=$2 AND i.indisunique AND i.indisvalid AND i.indpred IS NULL AND i.indexprs IS NULL AND k.ord <= i.indnkeyatts AND NOT EXISTS (SELECT 1 FROM unnest(i.indkey) WITH ORDINALITY kk(num,ord) JOIN pg_attribute aa ON aa.attrelid=c.oid AND aa.attnum=kk.num WHERE kk.ord <= i.indnkeyatts AND NOT aa.attnotnull) ORDER BY i.indisprimary DESC,i.indexrelid,k.ord`, o.Schema, o.Name)
	if e != nil {
		return t, e
	}
	var selected int64
	for rows.Next() {
		var idx int64
		var n string
		var pk bool
		if e = rows.Scan(&idx, &n, &pk); e != nil {
			rows.Close()
			return t, e
		}
		if selected == 0 {
			selected = idx
		}
		if selected == idx {
			t.Key = append(t.Key, n)
			for i := range t.Columns {
				if t.Columns[i].Name == n {
					t.Columns[i].Primary = pk
				}
			}
		}
	}
	e = rows.Err()
	rows.Close()
	t.Editable = o.Kind == "table" && (relkind == "r" || relkind == "p") && len(t.Key) > 0
	if e != nil {
		return t, e
	}
	defs, err := q.QueryContext(ctx, `SELECT con.conname,'constraint',pg_get_constraintdef(con.oid,true) FROM pg_constraint con JOIN pg_class c ON c.oid=con.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 UNION ALL SELECT indexname,'index',indexdef FROM pg_indexes WHERE schemaname=$1 AND tablename=$2 ORDER BY 2,1`, o.Schema, o.Name)
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
	if err != nil {
		return t, err
	}
	if relkind == "v" || relkind == "m" {
		view, er := q.QueryContext(ctx, `SELECT pg_get_viewdef(c.oid,true) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2`, o.Schema, o.Name)
		if er != nil {
			return t, er
		}
		if view.Next() {
			er = view.Scan(&t.Definition)
		}
		view.Close()
		if er != nil {
			return t, er
		}
	}
	return t, nil
}
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
func validColumns(t contract.Table) map[string]contract.Field {
	m := make(map[string]contract.Field)
	for _, f := range t.Columns {
		m[f.Name] = f
	}
	return m
}
func (s *Session) Page(ctx context.Context, r contract.PageRequest) (contract.Page, error) {
	ctx, done := s.requestContext(ctx)
	defer done()
	return s.page(ctx, s.db, r)
}
func (s *Session) page(ctx context.Context, q queryer, r contract.PageRequest) (contract.Page, error) {
	t, e := s.structure(ctx, q, r.Object)
	p := contract.Page{Table: t, Offset: r.Offset}
	if e != nil {
		return p, e
	}
	if r.Limit <= 0 {
		r.Limit = 100
	}
	if r.Limit > 1000 || r.Offset < 0 {
		return p, errors.New("invalid bounded page")
	}
	cols := validColumns(t)
	var args []any
	var where []string
	for _, f := range r.Filters {
		if _, ok := cols[f.Column]; !ok {
			return p, errors.New("unknown filter field")
		}
		op := strings.ToUpper(f.Operator)
		switch op {
		case "=", "!=", "<", ">", "<=", ">=", "LIKE":
			args = append(args, parameter(f.Value))
			where = append(where, quote(f.Column)+" "+op+" "+s.arg(len(args)))
		case "IS NULL", "IS NOT NULL":
			where = append(where, quote(f.Column)+" "+op)
		default:
			return p, errors.New("unsupported filter operator")
		}
	}
	sqlText := "SELECT "
	for i, f := range t.Columns {
		if i > 0 {
			sqlText += ","
		}
		sqlText += quote(f.Name)
	}
	sqlText += " FROM " + qualified(r.Object)
	if len(where) > 0 {
		sqlText += " WHERE " + strings.Join(where, " AND ")
	}
	sorts := r.Sorts
	if len(sorts) == 0 {
		for _, k := range t.Key {
			sorts = append(sorts, contract.Sort{Column: k})
		}
	}
	if len(sorts) > 0 {
		sqlText += " ORDER BY "
		for i, sort := range sorts {
			if _, ok := cols[sort.Column]; !ok {
				return p, errors.New("unknown sort field")
			}
			if i > 0 {
				sqlText += ","
			}
			sqlText += quote(sort.Column)
			if sort.Descending {
				sqlText += " DESC"
			}
		}
	}
	sqlText += fmt.Sprintf(" LIMIT %d OFFSET %d", r.Limit+1, r.Offset)
	rows, e := q.QueryContext(ctx, sqlText, args...)
	if e != nil {
		return p, e
	}
	defer rows.Close()
	budget := 16 << 20
	set, e := scan(rows, s.kind, 0, nil, &budget, r.Limit+1, 1<<20)
	if e != nil {
		return p, e
	}
	p.Rows = set.Rows
	p.Truncation = append([]string(nil), set.Truncation...)
	if len(p.Rows) > r.Limit {
		p.More = true
		p.Rows = p.Rows[:r.Limit]
	}
	positions := map[string]int{}
	for i, f := range t.Columns {
		positions[f.Name] = i
	}
	for _, row := range p.Rows {
		ident := contract.RowIdentity{Columns: append([]string(nil), t.Key...)}
		for _, k := range t.Key {
			ident.Values = append(ident.Values, row[positions[k]].Clone())
		}
		p.Identities = append(p.Identities, ident)
	}
	return p, nil
}
