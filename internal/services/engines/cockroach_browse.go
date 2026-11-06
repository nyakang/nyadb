package engines

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (s *Session) cockroachBrowse(ctx context.Context, n contract.NamespaceRef) (out []contract.DBObject, err error) {
	if err = s.check(n.Object("table", "_")); err != nil {
		return nil, err
	}
	ctx, done := s.requestContext(ctx)
	defer done()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	defer func() {
		if err != nil {
			out = nil
		}
	}()
	used := 0
	add := func(o contract.DBObject) error {
		used += len(o.Name) + len(o.Definition) + len(o.Comment) + len(o.Parameters) + 1024
		if len(out) >= postgresBrowseObjectLimit || used > postgresBrowseByteLimit || len(o.Definition) > postgresProgramByteLimit {
			return errors.New("CockroachDB object metadata limit exceeded")
		}
		o.Engine = "CockroachDB"
		out = append(out, o)
		return nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT table_name,type,owner,estimated_row_count FROM [SHOW TABLES FROM "+quote(n.Schema)+"] WHERE CASE WHEN type='sequence' THEN has_sequence_privilege(quote_ident(schema_name)||'.'||quote_ident(table_name),'USAGE,SELECT,UPDATE') ELSE true END ORDER BY table_name LIMIT 50001")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var o contract.DBObject
		var kind string
		var estimate sql.NullInt64
		if err = rows.Scan(&o.Name, &kind, &o.Definer, &estimate); err != nil {
			break
		}
		neutral := "table"
		o.Kind = "表"
		switch kind {
		case "view", "materialized view":
			neutral, o.Kind = "view", "视图"
		case "sequence":
			neutral, o.Kind = "sequence", "序列"
		case "table":
			if estimate.Valid {
				o.RowsEstimate = &estimate.Int64
			}
		default:
			continue
		}
		o.Ref = n.Object(neutral, o.Name)
		if err = add(o); err != nil {
			break
		}
	}
	err = errors.Join(err, rows.Err())
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Ref.Kind != "sequence" {
			continue
		}
		o := &out[i]
		o.Definition, err = cockroachCreate(ctx, tx, o.Ref)
		if err != nil {
			return nil, err
		}
		o.Sequence, err = cockroachSequence(o.Definition)
		if err != nil {
			return nil, err
		}
		used += len(o.Definition)
		if used > postgresBrowseByteLimit {
			return nil, errors.New("CockroachDB object metadata limit exceeded")
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT format('%I(%s)',p.proname,oidvectortypes(p.proargtypes)),p.prokind,pg_get_userbyid(p.proowner),pg_get_function_arguments(p.oid),COALESCE(pg_get_function_result(p.oid),''),CASE WHEN p.prosecdef THEN 'DEFINER' ELSE 'INVOKER' END,octet_length(pg_get_functiondef(p.oid)),CASE WHEN octet_length(pg_get_functiondef(p.oid))<=$2 THEN pg_get_functiondef(p.oid) ELSE '' END FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=$1 AND p.prokind IN ('f','p','w') ORDER BY 1 LIMIT 50001`, n.Schema, postgresProgramByteLimit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var o contract.DBObject
		var kind string
		var size int64
		if err = rows.Scan(&o.Name, &kind, &o.Definer, &o.Parameters, &o.ReturnType, &o.SQLSecurity, &size, &o.Definition); err != nil {
			break
		}
		if size > postgresProgramByteLimit {
			err = errors.New("routine definition exceeds 1 MiB")
			break
		}
		neutral := "function"
		o.Kind = "函数"
		if kind == "p" {
			neutral, o.Kind = "procedure", "存储过程"
		}
		o.Ref = n.Object(neutral, o.Name)
		if err = add(o); err != nil {
			break
		}
	}
	err = errors.Join(err, rows.Err())
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if err = s.check(n.Object("table", "_")); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
