package meilisearch

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/json"
	"slices"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type dataBoundary struct{ Epoch, Total uint64 }

func documentColumns() []contract.StructureColumn {
	return []contract.StructureColumn{{Name: "id", Generated: true, Type: "native_id", DataType: "json", ValueKind: contract.JSON, Key: "document_id", Position: 1, Comment: "Original native primary-key value"}, {Name: "document", Type: "json", DataType: "json", ValueKind: contract.JSON, Position: 2, Comment: "Complete original native JSON"}}
}
func validateDataOptions(o contract.DataOptions) error {
	_, err := dataSearchQuery(o)
	return err
}
func (l *dataLease) cursor(offset int64, limit int, epoch, total uint64) *contract.DataCursor {
	raw, _ := json.Marshal(dataBoundary{epoch, total})
	c := &contract.DataCursor{Database: l.object.Catalog, Table: l.object.Name, Offset: offset, Columns: []string{"_meilisearch_offset"}, Values: [][]byte{raw}}
	c.RuleSignature = l.sign(struct {
		Cursor  *contract.DataCursor
		Object  contract.ObjectRef
		Binding string
		Query   string
		Limit   int
	}{c, l.object, l.binding(), l.query, limit})
	return c
}
func (l *dataLease) decodeCursor(o contract.DataOptions, query string) (dataBoundary, error) {
	c := o.After
	if c == nil || c.Database != l.object.Catalog || c.Table != l.object.Name || c.Offset != o.Offset || o.Limit != l.limit || query != l.query || !slices.Equal(c.Columns, []string{"_meilisearch_offset"}) || len(c.Descending) != 0 || len(c.Values) != 1 || len(c.Values[0]) > 4096 || c.FilterColumn != "" || c.FilterMode != "" || c.FilterValue != "" || c.SortColumn != "" || c.SortDescending {
		return dataBoundary{}, ErrInvalid
	}
	copy := *c
	copy.RuleSignature = ""
	expected := l.sign(struct {
		Cursor  *contract.DataCursor
		Object  contract.ObjectRef
		Binding string
		Query   string
		Limit   int
	}{&copy, l.object, l.binding(), query, o.Limit})
	var boundary dataBoundary
	if !hmac.Equal([]byte(c.RuleSignature), []byte(expected)) || decodeJSON(c.Values[0], &boundary) != nil || boundary.Epoch != l.epoch || boundary.Total != l.total {
		return boundary, ErrInvalid
	}
	return boundary, nil
}
func (l *dataLease) page(ctx context.Context, id int64, db, name string, o contract.DataOptions) (*contract.DataPage, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	if o.Object != l.object || o.Offset < 0 || o.Limit < 1 || o.Limit > documentPageLimit {
		return nil, ErrInvalid
	}
	query, err := dataSearchQuery(o)
	if err != nil {
		return nil, err
	}
	if o.After != nil {
		raw, err := json.Marshal(o.After)
		if err != nil || len(raw) > 64<<10 {
			return nil, ErrBounds
		}
		var copy contract.DataCursor
		if json.Unmarshal(raw, &copy) != nil {
			return nil, ErrInvalid
		}
		o.After = &copy
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
	var boundary dataBoundary
	if o.After != nil {
		boundary, err = l.decodeCursor(o, query)
		if err != nil {
			return nil, err
		}
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
	var documents []json.RawMessage
	var total uint64
	var more bool
	var searchInfo *contract.DataSearchResult
	if query == "" {
		native, _, err := c.ListDocuments(ctx, db, DocumentsRequest{Offset: uint64(o.Offset), Limit: o.Limit})
		if err != nil {
			return nil, err
		}
		documents, total = native.Documents, native.Total
		more = uint64(o.Offset)+uint64(len(documents)) < total
	} else {
		native, err := l.searchDocuments(ctx, c, query, o.Offset, o.Limit)
		if err != nil {
			return nil, err
		}
		documents, total, more, searchInfo = native.documents, native.total, native.hasMore, native.info
	}
	if o.After != nil && total != boundary.Total {
		return nil, contract.ErrDataConflict
	}
	_, editable := l.primaryKey()
	page := &contract.DataPage{Object: l.object, Database: db, Table: name, Columns: []string{"id", "document"}, ColumnKinds: []string{"json", "json"}, ColumnMetadata: documentColumns(), Editable: &editable, Offset: o.Offset, Limit: o.Limit, HasMore: more, PagingMode: "offset", LiveRead: true, StableOrder: false, QuerySQL: "GET /indexes/" + db + "/documents"}
	page.Search = searchInfo
	if searchInfo != nil {
		page.QuerySQL = "POST /indexes/" + db + "/search"
	}
	budget := 16 << 20
	seen := map[string]bool{}
	for _, raw := range documents {
		doc, err := l.document(raw)
		if err != nil {
			return nil, err
		}
		if seen[doc.ID] {
			return nil, ErrProtocol
		}
		seen[doc.ID] = true
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
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	dead := l.closed || l.changed
	l.mu.Unlock()
	if dead {
		return nil, contract.ErrRevoked
	}
	if o.After == nil {
		l.epoch++
		l.limit = o.Limit
		l.total = total
		l.query = query
	}
	page.CurrentCursor = l.cursor(o.Offset, o.Limit, l.epoch, total)
	if page.HasMore {
		page.NextCursor = l.cursor(o.Offset+int64(len(page.Rows)), o.Limit, l.epoch, total)
	}
	page.CompletedAt = time.Now()
	page.Duration = time.Since(start)
	return page, nil
}

// JSON canonicalization retains JSON numbers; it never converts through float64.
func canonicalJSON(raw []byte) ([]byte, error) {
	var value any
	if err := decodeJSON(bytes.Clone(raw), &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
