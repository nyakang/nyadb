package qdrant

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/json"
	"slices"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type dataQuery struct{ Filter json.RawMessage }
type dataBoundary struct {
	Epoch uint64
	ID    json.RawMessage `json:"id,omitempty"`
}

func canonicalJSON(raw []byte) ([]byte, error) {
	var value any
	if err := decodeJSON(raw, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
func validatePointFilter(raw string) error {
	if len(raw) > requestLimit {
		return ErrBounds
	}
	if _, err := object([]byte(raw)); err != nil {
		return ErrInvalid
	}
	return nil
}
func cloneDataOptions(o contract.DataOptions) (contract.DataOptions, error) {
	if len(o.Filters) > contract.DataMaxFilterRules || len(o.Sorts) > 1 {
		return o, ErrBounds
	}
	if o.Condition != nil {
		if _, err := o.Condition.Rules(contract.DataMaxFilterRules, 3); err != nil {
			return o, ErrInvalid
		}
		o.Condition = o.Condition.Clone(func(r contract.DataFilterRule) contract.DataFilterRule { return r })
	}
	o.Filters = slices.Clone(o.Filters)
	o.Sorts = slices.Clone(o.Sorts)
	if o.After != nil {
		c := *o.After
		if len(c.Columns) != 1 || len(c.Descending) > 0 || len(c.Values) != 1 || len(c.Values[0]) > 1024 || len(c.RuleSignature) != 64 {
			return o, ErrInvalid
		}
		c.Columns = slices.Clone(c.Columns)
		c.Descending = slices.Clone(c.Descending)
		c.Values = [][]byte{bytes.Clone(c.Values[0])}
		o.After = &c
	}
	return o, nil
}
func pointDataQuery(o contract.DataOptions) (dataQuery, error) {
	q := dataQuery{}
	if len(o.Sorts) != 0 || o.SortColumn != "" || o.SortDescending {
		return q, contract.ErrUnsupported
	}
	if o.Condition == nil && o.FilterMode == "" && (o.FilterColumn != "" || o.FilterValue != "") {
		return q, ErrInvalid
	}
	e, err := contract.EffectiveDataExpression(o)
	if err != nil {
		return q, ErrInvalid
	}
	if e == nil {
		return q, nil
	}
	var compile func(*contract.FilterExpression[contract.DataFilterRule]) (json.RawMessage, error)
	compile = func(e *contract.FilterExpression[contract.DataFilterRule]) (json.RawMessage, error) {
		if e.Rule != nil {
			r := *e.Rule
			if r.Column != "point" || r.Mode != "qdrant_filter" {
				return nil, contract.ErrUnsupported
			}
			if err := validatePointFilter(r.Value); err != nil {
				return nil, err
			}
			return canonicalJSON([]byte(r.Value))
		}
		children := make([]json.RawMessage, len(e.Children))
		for i := range e.Children {
			v, err := compile(&e.Children[i])
			if err != nil {
				return nil, err
			}
			children[i] = v
		}
		operator := "must"
		if e.Op == "or" {
			operator = "should"
		}
		return json.Marshal(map[string]any{operator: children})
	}
	q.Filter, err = compile(e)
	if err == nil && len(q.Filter) > requestLimit {
		err = ErrBounds
	}
	return q, err
}
func (q dataQuery) signature() string { return string(q.Filter) }
func pointColumns() []contract.StructureColumn {
	return []contract.StructureColumn{
		{Name: "id", Type: "point_id", DataType: "uint64_or_uuid", Key: "point_id", Position: 1, Generated: true, Comment: "Exact native uint64 or UUID point ID"},
		{Name: "vector", Type: "json", DataType: "json", ValueKind: contract.JSON, Position: 2, Generated: true, Comment: "Original unnamed, named, sparse or multi-vector JSON"},
		{Name: "payload", Type: "json", DataType: "json", ValueKind: contract.JSON, Position: 3, Generated: true, Comment: "Original payload JSON; no field flattening"},
		{Name: "point", Type: "json", DataType: "json", ValueKind: contract.JSON, Position: 4, Comment: "Complete original native point JSON; version is not CAS"},
	}
}
func (l *dataLease) cursor(epoch uint64, offset int64, limit int, q dataQuery, p dataBoundary) *contract.DataCursor {
	p.Epoch = epoch
	raw, _ := json.Marshal(p)
	c := &contract.DataCursor{Database: l.object.Catalog, Table: l.object.Name, Offset: offset, Columns: []string{"_qdrant_live_cursor"}, Values: [][]byte{raw}}
	c.RuleSignature = l.sign(struct {
		Cursor             *contract.DataCursor
		Object             contract.ObjectRef
		Query, Observation string
		Limit              int
	}{c, l.object, q.signature(), l.binding(), limit})
	return c
}
func (l *dataLease) decodeCursor(o contract.DataOptions, q dataQuery) (dataBoundary, error) {
	c := o.After
	if c == nil || c.Database != l.object.Catalog || c.Table != l.object.Name || c.Offset != o.Offset || o.Limit != l.limit || q.signature() != l.query || !slices.Equal(c.Columns, []string{"_qdrant_live_cursor"}) || len(c.Descending) != 0 || len(c.Values) != 1 {
		return dataBoundary{}, ErrInvalid
	}
	copy := *c
	copy.RuleSignature = ""
	expected := l.sign(struct {
		Cursor             *contract.DataCursor
		Object             contract.ObjectRef
		Query, Observation string
		Limit              int
	}{&copy, l.object, q.signature(), l.binding(), o.Limit})
	var p dataBoundary
	if !hmac.Equal([]byte(c.RuleSignature), []byte(expected)) || decodeJSON(c.Values[0], &p) != nil || p.Epoch != l.epoch || len(p.ID) > 0 && !validPointID(p.ID) {
		return p, ErrInvalid
	}
	return p, nil
}
func (l *dataLease) page(ctx context.Context, id int64, db, name string, o contract.DataOptions) (*contract.DataPage, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	if o.Object != l.object || o.Offset < 0 || o.Limit < 1 || o.Limit > 1000 {
		return nil, ErrInvalid
	}
	o, err := cloneDataOptions(o)
	if err != nil {
		return nil, err
	}
	q, err := pointDataQuery(o)
	if err != nil {
		return nil, err
	}
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	l.pageMu.Lock()
	defer l.pageMu.Unlock()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	p := dataBoundary{}
	epoch := l.epoch + 1
	if o.After == nil {
		if o.Offset != 0 {
			return nil, contract.ErrUnsupported
		}
	} else {
		p, err = l.decodeCursor(o, q)
		if err != nil {
			return nil, err
		}
		epoch = l.epoch
	}
	c, closeClient, err := l.client(ctx)
	if err != nil {
		return nil, err
	}
	defer closeClient()
	info, err := l.guard(ctx, c)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	points, _, err := c.Scroll(ctx, db, p.ID, o.Limit, q.Filter)
	if err != nil {
		return nil, err
	}
	_, schemaErr := vectorWriteSchema(info)
	editable := schemaErr == nil
	path, _ := collectionPath(db)
	page := &contract.DataPage{Object: l.object, Database: db, Table: name, Columns: []string{"id", "vector", "payload", "point"}, ColumnKinds: []string{"point_id", "json", "json", "json"}, ColumnMetadata: pointColumns(), Editable: &editable, Offset: o.Offset, Limit: o.Limit, HasMore: len(points.NextOffset) > 0, PagingMode: "cursor", LiveRead: true, StableOrder: true, KeysetAvailable: true, QuerySQL: "POST " + path + "/points/scroll"}
	budget := responseLimit
	for _, point := range points.Points {
		row, snapshot, err := l.preview(point, &budget)
		if err != nil {
			return nil, err
		}
		page.Rows = append(page.Rows, row)
		page.RowSnapshots = append(page.RowSnapshots, snapshot)
		ref := snapshot.Ref
		page.RowRefs = append(page.RowRefs, &ref)
	}
	if _, err = l.guard(ctx, c); err != nil {
		return nil, err
	}
	// Work admission keeps the key alive until final publication or rejection.
	page.CurrentCursor = l.cursor(epoch, o.Offset, o.Limit, q, p)
	if page.HasMore {
		page.NextCursor = l.cursor(epoch, o.Offset+int64(len(page.Rows)), o.Limit, q, dataBoundary{ID: points.NextOffset})
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, contract.ErrRevoked
	}
	if l.changed {
		return nil, ErrDataTargetChanged
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if o.After == nil {
		l.epoch = epoch
		l.limit = o.Limit
		l.query = q.signature()
	}
	page.CompletedAt = time.Now()
	page.Duration = time.Since(start)
	return page, nil
}
