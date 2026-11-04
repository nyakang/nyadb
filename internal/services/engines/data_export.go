package engines

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"strings"
)

// Export reads the same parameterized condition tree as paging and analysis.
// Values are bounded before transfer and streamed without retaining result rows.
func (s *Session) exportData(ctx context.Context, id int64, schema, name string, options contract.DataOptions, fields []string, sink func([]contract.DataValue) error) error {
	if sink == nil || len(fields) == 0 || len(fields) > 128 || options.After != nil || options.UseKeyset || options.Offset < 0 || options.Limit < 0 || options.Limit > 1000 {
		return connections.ErrInvalid
	}
	r, err := s.pageRequest(id, schema, name, options)
	if err != nil {
		return err
	}
	return s.dataQuery(ctx, r.Object, nil, func(ctx context.Context, q queryer) error {
		t, err := s.structure(ctx, q, r.Object)
		if err != nil {
			return err
		}
		columns := validColumns(t)
		projection := contract.Table{Object: t.Object}
		seen := map[string]bool{}
		for _, name := range fields {
			f, ok := columns[name]
			if !ok || seen[name] {
				return connections.ErrInvalid
			}
			seen[name] = true
			projection.Columns = append(projection.Columns, f)
		}
		where, args, err := s.dataWhere(t, r)
		if err != nil {
			return err
		}
		order, _, _, err := s.dataOrder(ctx, q, t, r)
		if err != nil {
			return err
		}
		sql := "SELECT " + s.dataProjection(projection, 1<<20) + " FROM " + qualified(t.Object) + " WHERE " + where
		terms := []string{}
		for _, term := range order {
			v := term.Expr
			if term.Descending {
				v += " DESC"
			}
			terms = append(terms, v)
		}
		if len(terms) > 0 {
			sql += " ORDER BY " + strings.Join(terms, ",")
		}
		if options.Limit > 0 {
			args = append(args, options.Limit)
			sql += " LIMIT " + s.arg(len(args))
		} else if options.Offset > 0 && s.kind == connections.SQLite {
			sql += " LIMIT -1"
		}
		if options.Offset > 0 {
			args = append(args, options.Offset)
			sql += " OFFSET " + s.arg(len(args))
		}
		rows, err := q.QueryContext(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			raw := make([]any, len(fields))
			dest := make([]any, len(fields))
			for i := range raw {
				dest[i] = &raw[i]
			}
			if err := rows.Scan(dest...); err != nil {
				return err
			}
			values := make([]contract.DataValue, len(fields))
			size := 0
			for i, value := range raw {
				c := cell(value, canonicalDataType(projection.Columns[i].DatabaseType), s.kind)
				bytes := len(c.Text) + len(c.Bytes)
				size += bytes
				if bytes > 1<<20 || size > 16<<20 {
					return errors.New("export value exceeds the 1 MiB field or 16 MiB row limit")
				}
				values[i] = contract.DataValue{Kind: c.Kind, Text: c.Text, Bytes: append([]byte(nil), c.Bytes...), Null: c.Kind == contract.Null, Binary: c.Kind == contract.Binary}
			}
			if err := sink(values); err != nil {
				return err
			}
		}
		return rows.Err()
	})
}
