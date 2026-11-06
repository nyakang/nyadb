package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (s *Session) openGaussBrowse(ctx context.Context, n contract.NamespaceRef) (out []contract.DBObject, err error) {
	if err = s.check(n.Object("table", "_")); err != nil {
		return nil, err
	}
	ctx, done := s.requestContext(ctx)
	defer done()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	defer func() {
		if err != nil {
			out = nil
		}
	}()
	if _, err = tx.ExecContext(ctx, "SET LOCAL search_path=pg_catalog"); err != nil {
		return nil, err
	}
	used := 0
	add := func(o contract.DBObject) error {
		used += len(o.Name) + len(o.Comment) + len(o.Definition) + len(o.Parameters) + len(o.ReturnType) + len(o.Definer) + 1024
		if len(out) >= postgresBrowseObjectLimit || used > postgresBrowseByteLimit || len(o.Definition) > postgresProgramByteLimit {
			return errors.New("openGauss object metadata limit exceeded")
		}
		o.Engine = "openGauss"
		out = append(out, o)
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.relname,c.relkind,COALESCE(obj_description(c.oid,'pg_class'),''),COALESCE(pg_get_userbyid(c.relowner),''),
 CASE WHEN c.relkind IN ('r','m','f') AND c.reltuples>=0 THEN c.reltuples::bigint END,
 CASE WHEN c.relkind IN ('r','m') THEN pg_total_relation_size(c.oid) END
 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace ns ON ns.oid=c.relnamespace
 WHERE ns.nspname=$1 AND c.relkind IN ('r','v','m','f','S') AND (c.relkind<>'S' OR has_sequence_privilege(c.oid,'SELECT')) ORDER BY c.relname LIMIT 50001`, n.Schema)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var o contract.DBObject
		var kind string
		var estimate, size sql.NullInt64
		if err = rows.Scan(&o.Name, &kind, &o.Comment, &o.Definer, &estimate, &size); err != nil {
			break
		}
		neutral := "table"
		o.Kind = "表"
		if kind == "v" || kind == "m" {
			neutral, o.Kind = "view", "视图"
		} else if kind == "S" {
			neutral, o.Kind = "sequence", "序列"
		}
		o.Ref = n.Object(neutral, o.Name)
		if estimate.Valid {
			o.RowsEstimate = &estimate.Int64
		}
		if size.Valid {
			o.DataLength = &size.Int64
		}
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
		o := &out[i]
		if o.Ref.Kind != "sequence" {
			continue
		}
		seq := &contract.SequenceInfo{DataType: "bigint"}
		if err = queryOne(ctx, tx, "SELECT start_value,increment_by,min_value,max_value,cache_value,is_cycled FROM "+qualified(o.Ref), nil, &seq.Start, &seq.Increment, &seq.Min, &seq.Max, &seq.Cache, &seq.Cycle); err != nil {
			return nil, err
		}
		cycle := "NO CYCLE"
		if seq.Cycle {
			cycle = "CYCLE"
		}
		o.Sequence = seq
		o.Definition = fmt.Sprintf("CREATE SEQUENCE %s\n INCREMENT BY %d\n MINVALUE %d\n MAXVALUE %d\n START WITH %d\n CACHE %d\n %s;", qualified(o.Ref), seq.Increment, seq.Min, seq.Max, seq.Start, seq.Cache, cycle)
		used += len(o.Definition)
		if used > postgresBrowseByteLimit {
			return nil, errors.New("sequence metadata limit exceeded")
		}
	}
	// Native pg_get_functiondef returns OUT headerlines/definition rather than
	// PostgreSQL's scalar text. Keep only the complete definition field.
	rows, err = tx.QueryContext(ctx, `SELECT format('%I(%s)',p.proname,oidvectortypes(p.proargtypes)),p.prokind,
 COALESCE(obj_description(p.oid,'pg_proc'),''),COALESCE(pg_get_userbyid(p.proowner),''),pg_get_function_arguments(p.oid),COALESCE(pg_get_function_result(p.oid),''),
 CASE WHEN p.prosecdef THEN 'DEFINER' ELSE 'INVOKER' END,octet_length((pg_get_functiondef(p.oid)).definition),
 CASE WHEN octet_length((pg_get_functiondef(p.oid)).definition)<=$2 THEN (pg_get_functiondef(p.oid)).definition ELSE '' END
 FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace ns ON ns.oid=p.pronamespace
 WHERE ns.nspname=$1 AND p.prokind IN ('f','p','w') AND NOT p.propackage ORDER BY 1 LIMIT 50001`, n.Schema, postgresProgramByteLimit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var o contract.DBObject
		var kind string
		var size int64
		if err = rows.Scan(&o.Name, &kind, &o.Comment, &o.Definer, &o.Parameters, &o.ReturnType, &o.SQLSecurity, &size, &o.Definition); err != nil {
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
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
