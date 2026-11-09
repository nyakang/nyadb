package neo4j

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/json"
	"slices"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (l *dataLease) match() string {
	if l.object.Kind == "label" {
		return "MATCH (entity) WHERE $name IN labels(entity)"
	}
	return "MATCH ()-[entity]->() WHERE type(entity) = $name"
}
func cloneDataOptions(o contract.DataOptions) contract.DataOptions {
	if o.After != nil {
		x := *o.After
		x.Columns = slices.Clone(x.Columns)
		x.Descending = slices.Clone(x.Descending)
		x.Values = slices.Clone(x.Values)
		for i := range x.Values {
			x.Values[i] = bytes.Clone(x.Values[i])
		}
		o.After = &x
	}
	return o
}
func (l *dataLease) options(o contract.DataOptions) error {
	if o.Object != (contract.ObjectRef{}) && o.Object != l.object {
		return ErrInvalid
	}
	if o.Condition != nil || len(o.Filters) != 0 || len(o.Sorts) != 0 || o.FilterColumn != "" || o.FilterMode != "" || o.FilterValue != "" || o.SortColumn != "" || o.SortDescending {
		return contract.ErrUnsupported
	}
	if o.Offset < 0 || o.Offset > 1<<53 || o.Limit < 0 || o.Limit > 1000 {
		return ErrBounds
	}
	return nil
}

type graphBoundary struct {
	Object contract.ObjectRef
	Epoch  uint64
	Limit  int
	Cursor contract.DataCursor
}

func (l *dataLease) cursor(offset int64, id string) *contract.DataCursor {
	c := &contract.DataCursor{Offset: offset, Database: l.object.Catalog, Table: l.object.Name, Columns: []string{"element_id"}, Values: [][]byte{[]byte(id)}}
	c.RuleSignature = l.sign(graphBoundary{l.object, l.epoch, l.limit, *c})
	return c
}
func (l *dataLease) boundary(c *contract.DataCursor, offset int64) (string, error) {
	if c == nil || c.Offset != offset || c.Database != l.object.Catalog || c.Table != l.object.Name || len(c.Values) != 1 || len(c.Values[0]) > 4096 || !slices.Equal(c.Columns, []string{"element_id"}) {
		return "", ErrInvalid
	}
	x := *c
	sig := x.RuleSignature
	x.RuleSignature = ""
	if !hmac.Equal([]byte(sig), []byte(l.sign(graphBoundary{l.object, l.epoch, l.limit, x}))) {
		return "", ErrInvalid
	}
	id := string(x.Values[0])
	if offset == 0 && id != "" || offset > 0 && !validDatabase(id) {
		return "", ErrInvalid
	}
	return id, nil
}
func (l *dataLease) entities(ctx context.Context, c *Client, after string, limit int) ([]Value, string, error) {
	q := l.match() + " AND elementId(entity) > $after RETURN entity ORDER BY elementId(entity) LIMIT $limit"
	r, e := l.read(ctx, c, q, map[string]any{"after": after, "limit": int64(limit)}, limit)
	if e != nil {
		return nil, q, e
	}
	if !slices.Equal(r.Columns, []string{"entity"}) {
		return nil, q, ErrProtocol
	}
	values := make([]Value, 0, len(r.Rows))
	last := after
	for _, row := range r.Rows {
		if len(row) != 1 {
			return nil, q, ErrProtocol
		}
		id, e := l.entityID(row[0])
		if e != nil || id <= last {
			return nil, q, ErrProtocol
		}
		last = id
		values = append(values, row[0])
	}
	return values, q, nil
}
func (l *dataLease) entityID(v Value) (string, error) {
	if l.object.Kind == "label" {
		if v.Kind != "node" || v.Node == nil || !validDatabase(v.Node.ElementID) || !slices.Contains(v.Node.Labels, l.object.Name) {
			return "", ErrProtocol
		}
		return v.Node.ElementID, nil
	}
	if v.Kind != "relationship" || v.Relationship == nil || !validDatabase(v.Relationship.ElementID) || !validDatabase(v.Relationship.StartElementID) || !validDatabase(v.Relationship.EndElementID) || v.Relationship.Type != l.object.Name {
		return "", ErrProtocol
	}
	return v.Relationship.ElementID, nil
}
func (l *dataLease) columns() []contract.StructureColumn {
	names := []string{"element_id", "labels", "properties", "node"}
	if l.object.Kind == "relationship" {
		names = []string{"element_id", "type", "start_element_id", "end_element_id", "properties", "relationship"}
	}
	out := make([]contract.StructureColumn, len(names))
	for i, n := range names {
		kind := contract.Text
		typ := "string"
		if n == "properties" || n == "labels" || n == "node" || n == "relationship" {
			kind = contract.JSON
			typ = "Neo4j native " + n
		}
		out[i] = contract.StructureColumn{Name: n, Position: int64(i + 1), Type: typ, DataType: typ, ValueKind: kind, Nullable: false}
	}
	return out
}
func (l *dataLease) page(ctx context.Context, id int64, db, name string, o contract.DataOptions) (*contract.DataPage, error) {
	o = cloneDataOptions(o)
	if e := l.identity(id, db, name); e != nil {
		return nil, e
	}
	if e := l.options(o); e != nil {
		return nil, e
	}
	ctx, done, e := l.request(ctx)
	if e != nil {
		return nil, e
	}
	defer done()
	if !l.pageMu.TryLock() {
		return nil, ErrBusy
	}
	defer l.pageMu.Unlock()
	if o.Limit == 0 {
		o.Limit = 100
	}
	after := ""
	if o.After == nil {
		if o.Offset != 0 {
			return nil, contract.ErrUnsupported
		}
		l.epoch++
		l.limit = o.Limit
	} else {
		if o.Limit != l.limit {
			return nil, ErrInvalid
		}
		after, e = l.boundary(o.After, o.Offset)
		if e != nil {
			return nil, e
		}
	}
	c, closeClient, e := l.client(ctx)
	if e != nil {
		return nil, l.pageFailure(e)
	}
	defer closeClient()
	start := time.Now()
	values, q, e := l.entities(ctx, c, after, o.Limit+1)
	if e != nil {
		return nil, l.pageFailure(e)
	}
	editable := false
	p := &contract.DataPage{Object: l.object, Editable: &editable, Database: db, Table: name, Offset: o.Offset, Limit: o.Limit, HasMore: len(values) > o.Limit, PagingMode: "cursor", LiveRead: true, KeysetAvailable: true, QuerySQL: q, ColumnMetadata: l.columns(), CurrentCursor: l.cursor(o.Offset, after)}
	for _, col := range p.ColumnMetadata {
		p.Columns = append(p.Columns, col.Name)
		p.ColumnKinds = append(p.ColumnKinds, col.Type)
	}
	budget := 16 << 20
	for _, v := range values[:min(len(values), o.Limit)] {
		cells, snapshot, e := l.preview(v, &budget)
		if e != nil {
			return nil, e
		}
		p.Rows = append(p.Rows, cells)
		p.RowSnapshots = append(p.RowSnapshots, snapshot)
		r := cloneRowRef(snapshot.Ref)
		p.RowRefs = append(p.RowRefs, &r)
	}
	if p.HasMore {
		last, _ := l.entityID(values[o.Limit-1])
		p.NextCursor = l.cursor(o.Offset+int64(o.Limit), last)
	}
	p.CompletedAt = time.Now()
	p.Duration = time.Since(start)
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	return p, nil
}
func graphJSON(v any) (contract.Cell, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return contract.Cell{}, ErrProtocol
	}
	return contract.Cell{Kind: contract.JSON, Text: string(raw)}, nil
}
