package couchdb

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/json"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type dataQuery struct {
	Mango      bool
	Selector   json.RawMessage
	Descending bool
}
type dataBoundary struct {
	Epoch         uint64
	Key, Bookmark string
}

func canonicalJSON(raw []byte) ([]byte, error) {
	var value any
	if err := decodeJSON(raw, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
func cloneDataOptions(o contract.DataOptions) (contract.DataOptions, error) {
	// Bound and validate before cloning: Children slices can contain cycles.
	if len(o.Filters) > contract.DataMaxFilterRules {
		return o, ErrBounds
	}
	if len(o.Sorts) > 1 {
		return o, contract.ErrUnsupported
	}
	if o.Condition != nil {
		if _, err := o.Condition.Rules(contract.DataMaxFilterRules, 3); err != nil {
			return o, ErrInvalid
		}
		o.Condition = o.Condition.Clone(func(r contract.DataFilterRule) contract.DataFilterRule { return r })
	}
	o.Filters = slices.Clone(o.Filters)
	o.Sorts = slices.Clone(o.Sorts)
	return o, nil
}
func documentDataQuery(o contract.DataOptions) (dataQuery, error) {
	q := dataQuery{}
	if o.Condition == nil && o.FilterMode == "" && (o.FilterColumn != "" || o.FilterValue != "") {
		return q, ErrInvalid
	}
	expression, err := contract.EffectiveDataExpression(o)
	if err != nil {
		return q, ErrInvalid
	}
	var compile func(*contract.FilterExpression[contract.DataFilterRule]) (any, error)
	compile = func(e *contract.FilterExpression[contract.DataFilterRule]) (any, error) {
		if e.Rule != nil {
			r := *e.Rule
			if !utf8.ValidString(r.Value) || len(r.Value) > requestLimit {
				return nil, ErrInvalid
			}
			if r.Mode == "mango" && r.Column == "document" {
				var selector map[string]any
				if decodeJSON([]byte(r.Value), &selector) != nil || selector == nil {
					return nil, ErrInvalid
				}
				return selector, nil
			}
			if r.Column != "_id" && r.Column != "_rev" || r.Mode != "equal" && r.Mode != "not_equal" {
				return nil, contract.ErrUnsupported
			}
			operator := "$eq"
			if r.Mode == "not_equal" {
				operator = "$ne"
			}
			return map[string]any{r.Column: map[string]any{operator: r.Value}}, nil
		}
		children := make([]any, len(e.Children))
		for i := range e.Children {
			v, err := compile(&e.Children[i])
			if err != nil {
				return nil, err
			}
			children[i] = v
		}
		return map[string]any{"$" + e.Op: children}, nil
	}
	if expression != nil {
		v, err := compile(expression)
		if err != nil {
			return q, err
		}
		q.Selector, err = json.Marshal(v)
		if err != nil || len(q.Selector) > requestLimit {
			return q, ErrBounds
		}
		q.Mango = true
	}
	if len(o.Sorts) > 0 && o.SortColumn != "" || len(o.Sorts) > 1 {
		return q, contract.ErrUnsupported
	}
	if len(o.Sorts) == 1 {
		if o.Sorts[0].Column != "_id" {
			return q, contract.ErrUnsupported
		}
		q.Descending = o.Sorts[0].Descending
	} else {
		if o.SortColumn != "" && o.SortColumn != "_id" || o.SortDescending && o.SortColumn == "" {
			return q, contract.ErrUnsupported
		}
		q.Descending = o.SortDescending
	}
	return q, nil
}
func (q dataQuery) signature() string { raw, _ := json.Marshal(q); return string(raw) }
func (q dataQuery) find(bookmark string, limit int) FindRequest {
	direction := "asc"
	if q.Descending {
		direction = "desc"
	}
	sort, _ := json.Marshal([]map[string]string{{"_id": direction}})
	return FindRequest{Selector: bytes.Clone(q.Selector), Sort: sort, Bookmark: bookmark, Limit: limit}
}
func (l *dataLease) cursor(epoch uint64, offset int64, limit int, q dataQuery, p dataBoundary) *contract.DataCursor {
	p.Epoch = epoch
	raw, _ := json.Marshal(p)
	c := &contract.DataCursor{Database: l.object.Catalog, Table: l.object.Name, Offset: offset, Columns: []string{"_couchdb_live_cursor"}, Descending: []bool{q.Descending}, Values: [][]byte{raw}}
	c.RuleSignature = l.sign(struct {
		Cursor          *contract.DataCursor
		Object          contract.ObjectRef
		Query, Creation string
		Limit           int
	}{c, l.object, q.signature(), l.binding(), limit})
	return c
}
func (l *dataLease) decodeCursor(o contract.DataOptions, q dataQuery) (dataBoundary, error) {
	c := o.After
	if c == nil || c.Database != l.object.Catalog || c.Table != l.object.Name || c.Offset != o.Offset || o.Limit != l.limit || q.signature() != l.query || !slices.Equal(c.Columns, []string{"_couchdb_live_cursor"}) || !slices.Equal(c.Descending, []bool{q.Descending}) || len(c.Values) != 1 || len(c.Values[0]) > 128<<10 {
		return dataBoundary{}, ErrInvalid
	}
	copy := *c
	copy.RuleSignature = ""
	expected := l.sign(struct {
		Cursor          *contract.DataCursor
		Object          contract.ObjectRef
		Query, Creation string
		Limit           int
	}{&copy, l.object, q.signature(), l.binding(), o.Limit})
	var p dataBoundary
	if !hmac.Equal([]byte(c.RuleSignature), []byte(expected)) || json.Unmarshal(c.Values[0], &p) != nil || p.Epoch != l.epoch || q.Mango && p.Key != "" || !q.Mango && p.Bookmark != "" {
		return p, ErrInvalid
	}
	return p, nil
}
func (l *dataLease) readIDs(ctx context.Context, c *Client, q dataQuery, p dataBoundary, limit int) (*DocumentIdentityPage, error) {
	if q.Mango {
		return c.FindDocumentIdentities(ctx, l.object.Catalog, q.find(p.Bookmark, limit))
	}
	return c.AllDocumentIdentities(ctx, l.object.Catalog, p.Key, limit, q.Descending)
}
func documentColumns() []contract.StructureColumn {
	return []contract.StructureColumn{{Name: "_id", Type: "text", DataType: "text", ValueKind: contract.Text, Key: "document_id", Position: 1, Comment: "Native document ID"}, {Name: "_rev", Type: "text", DataType: "text", ValueKind: contract.Text, Position: 2, Comment: "Original native revision"}, {Name: "document", Type: "json", DataType: "json", ValueKind: contract.JSON, Position: 3, Comment: "Complete original native JSON; attachment stubs retained"}}
}
func (l *dataLease) page(ctx context.Context, id int64, db, name string, o contract.DataOptions) (*contract.DataPage, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	if o.Object != l.object || o.Offset < 0 || o.Limit < 1 || o.Limit > 1000 {
		return nil, ErrInvalid
	}
	if o.After != nil {
		raw, err := json.Marshal(o.After)
		if err != nil || len(raw) > 256<<10 {
			return nil, ErrBounds
		}
		var copy contract.DataCursor
		if json.Unmarshal(raw, &copy) != nil {
			return nil, ErrInvalid
		}
		o.After = &copy
	}
	o, err := cloneDataOptions(o)
	if err != nil {
		return nil, err
	}
	q, err := documentDataQuery(o)
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
	if _, err = l.guard(ctx, c); err != nil {
		return nil, err
	}
	start := time.Now()
	ids, err := l.readIDs(ctx, c, q, p, o.Limit)
	if err != nil {
		return nil, err
	}
	editable := false
	page := &contract.DataPage{Object: l.object, Database: db, Table: name, Columns: []string{"_id", "_rev", "document"}, ColumnKinds: []string{"text", "text", "json"}, ColumnMetadata: documentColumns(), Editable: &editable, Offset: o.Offset, Limit: o.Limit, HasMore: ids.HasMore, PagingMode: "cursor", LiveRead: true, StableOrder: true, KeysetAvailable: true, QuerySQL: "GET /" + db + "/_all_docs"}
	if q.Mango {
		page.QuerySQL = "POST /" + db + "/_find"
	}
	budget := 16 << 20
	for _, identity := range ids.Documents {
		doc, err := c.ReadDocument(ctx, db, identity)
		if err != nil {
			return nil, err
		}
		row, snapshot, err := l.preview(doc, &budget)
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
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, contract.ErrRevoked
	}
	l.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if o.After == nil {
		l.epoch = epoch
		l.limit = o.Limit
		l.query = q.signature()
	}
	page.CurrentCursor = l.cursor(epoch, o.Offset, o.Limit, q, p)
	if ids.HasMore {
		page.NextCursor = l.cursor(epoch, o.Offset+int64(len(page.Rows)), o.Limit, q, dataBoundary{Key: ids.NextKey, Bookmark: ids.Bookmark})
	}
	page.CompletedAt = time.Now()
	page.Duration = time.Since(start)
	return page, ctx.Err()
}
