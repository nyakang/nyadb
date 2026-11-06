package engines

import (
	"context"
	"errors"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) Schemas(ctx context.Context) ([]string, error) {
	ctx, done := s.requestContext(ctx)
	defer done()
	if s.isClosed() {
		return nil, ErrRevoked
	}
	return s.strategy.schemas(ctx, s.db)
}
func (s *Session) Objects(ctx context.Context, schema string) ([]contract.Object, error) {
	if s.kind == connections.OpenGauss {
		objects, err := s.openGaussBrowse(ctx, s.Namespace(schema))
		if err != nil {
			return nil, err
		}
		out := make([]contract.Object, len(objects))
		for i, o := range objects {
			out[i] = contract.Object{Ref: o.Ref}
		}
		return out, nil
	}
	if s.kind == connections.CockroachDB {
		objects, err := s.cockroachBrowse(ctx, s.Namespace(schema))
		if err != nil {
			return nil, err
		}
		out := make([]contract.Object, len(objects))
		for i, object := range objects {
			out[i] = contract.Object{Ref: object.Ref}
		}
		return out, nil
	}
	if s.kind == connections.PostgreSQL {
		objects, err := s.postgresBrowse(ctx, s.Namespace(schema))
		if err != nil {
			return nil, err
		}
		out := make([]contract.Object, len(objects))
		for i, object := range objects {
			out[i] = contract.Object{Ref: object.Ref}
		}
		return out, nil
	}
	ctx, done := s.requestContext(ctx)
	defer done()
	if s.isClosed() {
		return nil, ErrRevoked
	}
	rows, e := s.strategy.objects(ctx, s.db, schema)
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
	if e := s.check(o); e != nil {
		return contract.Table{Object: o}, e
	}
	return s.strategy.structure(ctx, s, q, o)
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
	where, args, err := s.dataWhere(t, r)
	if err != nil {
		return p, err
	}
	sqlText := "SELECT " + s.dataProjection(t, 1<<20) + " FROM " + qualified(r.Object) + " WHERE " + where
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
	p.Rows, e = s.dataScan(rows, t, 1<<20, r.Limit+1)
	if e != nil {
		return p, e
	}
	for _, row := range p.Rows {
		for _, c := range row {
			if c.Truncated {
				p.Truncation = append(p.Truncation, "cell")
				break
			}
		}
	}
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
