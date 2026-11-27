package prestosql

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
)

func dataQuery(m *contract.TableStructure, p *dataProjection, o contract.DataOptions) (string, error) {
	return dataSelect(m, p, o, true)
}
func dataSelect(m *contract.TableStructure, p *dataProjection, o contract.DataOptions, paged bool) (string, error) {
	if o.Condition != nil || o.UseKeyset || o.After != nil {
		return "", contract.ErrUnsupported
	}
	if o.Object != (contract.ObjectRef{}) && o.Object != m.Object || o.Offset < 0 || o.Limit < 0 || paged && (o.Limit < 1 || o.Limit > contract.DataMaxPageSize) {
		return "", connections.ErrInvalid
	}
	known := map[string]*insertType{}
	for _, c := range m.Columns {
		known[c.Name], _ = parseInsertType(c.Type)
	}
	filters := append([]contract.DataFilterRule(nil), o.Filters...)
	if o.FilterColumn != "" {
		filters = append(filters, contract.DataFilterRule{Column: o.FilterColumn, Mode: o.FilterMode, Value: o.FilterValue})
	}
	if len(filters) > 64 {
		return "", connections.ErrInvalid
	}
	var where []string
	for _, f := range filters {
		typ, ok := known[f.Column]
		if !ok {
			return "", connections.ErrInvalid
		}
		if typ == nil {
			return "", contract.ErrUnsupported
		}
		op := ""
		switch f.Mode {
		case "", "equal":
			op = "="
		case "not_equal":
			op = "<>"
		case "less":
			op = "<"
		case "greater":
			op = ">"
		case "like":
			op = "LIKE"
		case "is_null":
			op = "IS NULL"
		case "is_not_null":
			op = "IS NOT NULL"
		default:
			return "", contract.ErrUnsupported
		}
		clause := quoteName("__nyadb_data") + "." + quoteName(f.Column) + " " + op
		if op != "IS NULL" && op != "IS NOT NULL" {
			if typ.kind == contract.JSON || typ.kind == contract.Binary || op == "LIKE" && typ.kind != contract.Text {
				return "", contract.ErrUnsupported
			}
			literal, err := insertLiteral(typ, contract.DataValue{Kind: typ.kind, Text: f.Value})
			if err != nil {
				return "", err
			}
			clause += " " + literal
		}
		where = append(where, clause)
	}
	sorts := append([]contract.DataSortRule(nil), o.Sorts...)
	if len(sorts) == 0 && o.SortColumn != "" {
		sorts = append(sorts, contract.DataSortRule{Column: o.SortColumn, Descending: o.SortDescending})
	}
	if len(sorts) > 64 {
		return "", connections.ErrInvalid
	}
	var order []string
	for _, sort := range sorts {
		typ, ok := known[sort.Column]
		if !ok {
			return "", connections.ErrInvalid
		}
		if typ == nil {
			return "", contract.ErrUnsupported
		}
		if typ.kind == contract.JSON || typ.kind == contract.Binary {
			return "", contract.ErrUnsupported
		}
		direction := " ASC"
		if sort.Descending {
			direction = " DESC"
		}
		order = append(order, quoteName("__nyadb_data")+"."+quoteName(sort.Column)+direction)
	}
	source := "SELECT " + strings.Join(p.selects, ",") + " FROM " + qualified(m.Object) + " AS " + quoteName("__nyadb_data")
	if len(where) > 0 {
		source += " WHERE " + strings.Join(where, " AND ")
	}
	if len(order) > 0 {
		source += " ORDER BY " + strings.Join(order, ",")
	}
	if paged {
		source += fmt.Sprintf(" OFFSET %d LIMIT %d", o.Offset, o.Limit+1)
	}
	if len(source) > 8<<20 {
		return "", contract.ErrDataPage
	}
	return source, nil
}
func (l *dataLease) load(ctx context.Context, id int64, db, table string, offset int64, limit int) (*contract.DataPage, error) {
	return l.page(ctx, id, db, table, contract.DataOptions{Offset: offset, Limit: limit})
}
func (l *dataLease) page(ctx context.Context, id int64, db, table string, o contract.DataOptions) (*contract.DataPage, error) {
	if err := l.identity(id, db, table); err != nil {
		return nil, err
	}
	if o.Object != (contract.ObjectRef{}) && o.Object != l.object {
		return nil, connections.ErrInvalid
	}
	if o.Condition != nil || o.After != nil || o.UseKeyset {
		return nil, contract.ErrUnsupported
	}
	o.Filters = append([]contract.DataFilterRule(nil), o.Filters...)
	o.Sorts = append([]contract.DataSortRule(nil), o.Sorts...)
	child, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	l.mu.Lock()
	if l.pageSequence == ^uint64(0) {
		l.mu.Unlock()
		return nil, contract.ErrSQLSessionLimit
	}
	l.pageSequence++
	sequence := l.pageSequence
	l.mu.Unlock()
	start := time.Now()
	m, err := l.session.TableMetadata(child, l.object)
	if err != nil {
		return nil, err
	}
	p, err := newDataProjection(m, dataPreviewUnits)
	if err != nil {
		return nil, err
	}
	p.selects = append(p.selects, p.rowDigest+" AS "+quoteName(dataDigestColumn))
	source, err := dataQuery(m, p, o)
	if err != nil {
		return nil, err
	}
	result, _, err := l.primary.executeNative(child, jdbcplugin.Query{SQL: source, MaxRows: o.Limit + 1, TimeoutSeconds: l.session.config.QueryTimeoutSeconds})
	if err != nil {
		return nil, err
	}
	rows, digests, err := p.decodeCaptured(result)
	if err != nil {
		return nil, err
	}
	editable := l.object.Kind == "table"
	page := &contract.DataPage{Object: l.object, Database: db, Table: table, ColumnMetadata: p.columns, Offset: o.Offset, Limit: o.Limit, LiveRead: true, Editable: &editable, ColumnEditKinds: map[string][]string{}}
	for _, c := range p.columns {
		page.Columns = append(page.Columns, c.Name)
		page.ColumnKinds = append(page.ColumnKinds, c.Type)
		if editable && !c.Generated && !contract.DataColumnGenerated(c) {
			page.ColumnEditKinds[c.Name] = []string{"insert"}
		}
	}
	page.HasMore = len(rows) > o.Limit
	if page.HasMore {
		rows = rows[:o.Limit]
	}
	page.Rows = rows
	if err = l.publishGrants(child, sequence, m, page, digests); err != nil {
		return nil, err
	}
	page.CompletedAt = time.Now()
	page.Duration = time.Since(start)
	return page, nil
}
func (l *dataLease) count(ctx context.Context, id int64, db, table string) (int64, error) {
	if err := l.identity(id, db, table); err != nil {
		return 0, err
	}
	child, done, err := l.request(ctx)
	if err != nil {
		return 0, err
	}
	defer done()
	l.session.mu.Lock()
	owner, err := l.session.dataOwnerLocked(l.ctx, "data-count", l.session.auxSlots)
	l.session.mu.Unlock()
	if err != nil {
		return 0, err
	}
	defer owner.Release()
	r, _, err := owner.executeNative(child, jdbcplugin.Query{SQL: "SELECT count(*) FROM " + qualified(l.object), MaxRows: 2, TimeoutSeconds: l.session.config.QueryTimeoutSeconds})
	if err != nil {
		return 0, err
	}
	if !r.completed || r.truncated || len(r.columns) != 1 || len(r.rows) != 1 || len(r.rows[0]) != 1 {
		return 0, ErrResponse
	}
	count, err := strconv.ParseInt(string(r.rows[0][0]), 10, 64)
	if err != nil || count < 0 {
		return 0, ErrResponse
	}
	return count, nil
}
