package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const (
	postgresBrowseObjectLimit = 50000
	postgresBrowseByteLimit   = 32 << 20
	postgresProgramByteLimit  = 1 << 20
)

// One read-only snapshot keeps relations, routine definitions and sequence
// settings on the original session. Local settings make overload names stable
// regardless of a pooled connection's search_path, without changing that path.
func (s *Session) postgresBrowse(ctx context.Context, namespace contract.NamespaceRef) (out []contract.DBObject, err error) {
	defer func() {
		if err != nil {
			out = nil
		}
	}()
	ref := namespace.Object("table", "_")
	if err = s.check(ref); err != nil {
		return nil, err
	}
	ctx, done := s.requestContext(ctx)
	defer done()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET LOCAL search_path = pg_catalog"); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "SET LOCAL standard_conforming_strings = on"); err != nil {
		return nil, err
	}
	used := 0
	appendObject := func(object contract.DBObject) error {
		used += len(object.Name) + len(object.Comment) + len(object.Definition) + len(object.Parameters) + len(object.ReturnType) + len(object.Definer) + 1024
		if len(out) >= postgresBrowseObjectLimit || used > postgresBrowseByteLimit || len(object.Definition) > postgresProgramByteLimit {
			return errors.New("PostgreSQL object metadata exceeds the 50000-object, 32 MiB snapshot or 1 MiB definition limit")
		}
		object.Engine = "PostgreSQL"
		out = append(out, object)
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.relname,c.relkind,COALESCE(obj_description(c.oid,'pg_class'),''),pg_get_userbyid(c.relowner),
 COALESCE(format_type(seq.seqtypid,NULL),''),seq.seqstart,seq.seqincrement,seq.seqmin,seq.seqmax,seq.seqcache,seq.seqcycle,
 CASE WHEN c.relkind IN ('r','p','m','f') AND c.reltuples>=0 THEN c.reltuples::bigint END,
 CASE WHEN c.relkind IN ('r','p','m') THEN pg_total_relation_size(c.oid) END
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_sequence seq ON seq.seqrelid=c.oid
 WHERE n.nspname=$1 AND c.relkind IN ('r','p','v','m','f','S') ORDER BY c.relname LIMIT 50001`, namespace.Schema)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var object contract.DBObject
		var kind, dataType string
		var start, increment, min, max, cache sql.NullInt64
		var cycle sql.NullBool
		var estimate, size sql.NullInt64
		if err = rows.Scan(&object.Name, &kind, &object.Comment, &object.Definer, &dataType, &start, &increment, &min, &max, &cache, &cycle, &estimate, &size); err != nil {
			break
		}
		if estimate.Valid {
			object.RowsEstimate = &estimate.Int64
		}
		if size.Valid {
			object.DataLength = &size.Int64
		}
		neutralKind := "table"
		object.Kind = "表"
		if kind == "v" || kind == "m" {
			neutralKind, object.Kind = "view", "视图"
		} else if kind == "S" {
			neutralKind, object.Kind = "sequence", "序列"
			if !start.Valid || !increment.Valid || !min.Valid || !max.Valid || !cache.Valid || !cycle.Valid || dataType == "" {
				err = errors.New("PostgreSQL sequence settings are unavailable")
				break
			}
			object.Sequence = &contract.SequenceInfo{DataType: dataType, Start: start.Int64, Increment: increment.Int64, Min: min.Int64, Max: max.Int64, Cache: cache.Int64, Cycle: cycle.Bool}
			cycleSQL := "NO CYCLE"
			if cycle.Bool {
				cycleSQL = "CYCLE"
			}
			object.Definition = fmt.Sprintf("CREATE SEQUENCE %s.%s\n AS %s\n INCREMENT BY %d\n MINVALUE %d\n MAXVALUE %d\n START WITH %d\n CACHE %d\n %s;", quote(namespace.Schema), quote(object.Name), dataType, increment.Int64, min.Int64, max.Int64, start.Int64, cache.Int64, cycleSQL)
		}
		object.Ref = namespace.Object(neutralKind, object.Name)
		if err = appendObject(object); err != nil {
			break
		}
	}
	err = errors.Join(err, rows.Err())
	rows.Close()
	if err != nil {
		return nil, err
	}
	// Input types, including qualified user types, distinguish all overloads.
	// OUT names/types and argument defaults do not change a routine's identity.
	rows, err = tx.QueryContext(ctx, `SELECT format('%I(%s)',p.proname,oidvectortypes(p.proargtypes)),p.prokind,
 COALESCE(obj_description(p.oid,'pg_proc'),''),pg_get_userbyid(p.proowner),pg_get_function_arguments(p.oid),
 COALESCE(pg_get_function_result(p.oid),''),CASE WHEN p.prosecdef THEN 'DEFINER' ELSE 'INVOKER' END,
 octet_length(pg_get_functiondef(p.oid)),CASE WHEN octet_length(pg_get_functiondef(p.oid)) <= $2 THEN pg_get_functiondef(p.oid) ELSE '' END
 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
 WHERE n.nspname=$1 AND p.prokind IN ('f','p','w') ORDER BY 1 LIMIT 50001`, namespace.Schema, postgresProgramByteLimit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var object contract.DBObject
		var kind string
		var size int64
		if err = rows.Scan(&object.Name, &kind, &object.Comment, &object.Definer, &object.Parameters, &object.ReturnType, &object.SQLSecurity, &size, &object.Definition); err != nil {
			break
		}
		if size > postgresProgramByteLimit {
			err = errors.New("PostgreSQL routine definition exceeds the 1 MiB limit")
			break
		}
		neutralKind := "function"
		object.Kind = "函数"
		if kind == "p" {
			neutralKind, object.Kind = "procedure", "存储过程"
		}
		object.Ref = namespace.Object(neutralKind, object.Name)
		if err = appendObject(object); err != nil {
			break
		}
	}
	err = errors.Join(err, rows.Err())
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = s.check(ref); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if err = s.check(ref); err != nil {
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
