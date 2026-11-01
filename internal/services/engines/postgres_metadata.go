package engines

import (
	"context"
	"database/sql"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func (s *Session) postgresStructure(ctx context.Context, q queryer, o contract.ObjectRef) (contract.Table, error) {
	t := contract.Table{Object: o}
	if e := s.check(o); e != nil {
		return t, e
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
	if relkind == "v" || relkind == "m" {
		t.Object.Kind = "view"
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

func (postgresDialect) structure(ctx context.Context, s *Session, q queryer, o contract.ObjectRef) (contract.Table, error) {
	return s.postgresStructure(ctx, q, o)
}
func (postgresDialect) schemas(ctx context.Context, q queryer) ([]string, error) {
	rows, e := q.QueryContext(ctx, `SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname <> 'information_schema' ORDER BY nspname`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			return nil, e
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
func (postgresDialect) objects(ctx context.Context, q queryer, schema string) (*sql.Rows, error) {
	return q.QueryContext(ctx, `SELECT c.relname, CASE WHEN c.relkind IN ('v','m') THEN 'view' ELSE 'table' END FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN ('r','p','v','m','f') ORDER BY c.relname`, schema)
}
