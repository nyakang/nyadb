package meilisearch

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"reflect"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// Grid paging belongs to the original lease. All remaining native search
// options retain their server-defined semantics and errors.
func validateSearchSelector(raw string) error {
	if len(raw) > requestLimit {
		return ErrBounds
	}
	fields, err := nativeObject([]byte(raw))
	if err != nil {
		return ErrInvalid
	}
	for _, name := range []string{"offset", "limit", "page", "hitsPerPage"} {
		if _, exists := fields[name]; exists {
			return ErrInvalid
		}
	}
	if raw, ok := fields["attributesToRetrieve"]; ok {
		var fields []string
		if decodeJSON(raw, &fields) != nil || fields == nil {
			return ErrInvalid
		}
	}
	return nil
}

func dataSearchQuery(o contract.DataOptions) (string, error) {
	if o.Condition != nil || len(o.Filters) != 0 || len(o.Sorts) != 0 || o.SortColumn != "" || o.SortDescending || o.UseKeyset {
		return "", contract.ErrUnsupported
	}
	if o.FilterMode == "" {
		if o.FilterColumn != "" || o.FilterValue != "" {
			return "", contract.ErrUnsupported
		}
		return "", nil
	}
	if o.FilterColumn != "document" || o.FilterMode != "meilisearch_search" {
		return "", contract.ErrUnsupported
	}
	if err := validateSearchSelector(o.FilterValue); err != nil {
		return "", err
	}
	raw, err := canonicalJSON([]byte(o.FilterValue))
	return string(raw), err
}

type nativeSearchPage struct {
	documents []json.RawMessage
	total     uint64
	hasMore   bool
	info      *contract.DataSearchResult
}

func searchBody(query, primary string, offset int64, limit int) ([]byte, error) {
	fields, err := nativeObject([]byte(query))
	if err != nil {
		return nil, err
	}
	fields["offset"], _ = json.Marshal(offset)
	fields["limit"], _ = json.Marshal(limit)
	if raw, ok := fields["attributesToRetrieve"]; ok {
		var names []string
		if decodeJSON(raw, &names) != nil || names == nil {
			return nil, ErrInvalid
		}
		if primary != "" && !slices.Contains(names, "*") && !slices.Contains(names, primary) {
			names = append(names, primary)
		}
		fields["attributesToRetrieve"], _ = json.Marshal(names)
	}
	body, err := json.Marshal(fields)
	if err != nil || len(body) > requestLimit {
		return nil, ErrBounds
	}
	return body, nil
}

func (l *dataLease) searchDocuments(ctx context.Context, c *Client, query string, offset int64, limit int) (nativeSearchPage, error) {
	primary, hasPrimary := l.primaryKey()
	body, err := searchBody(query, primary, offset, limit)
	if err != nil {
		return nativeSearchPage{}, err
	}
	result, response, err := c.Search(ctx, l.object.Catalog, body)
	if err != nil {
		return nativeSearchPage{}, err
	}
	if response.Status != 200 {
		return nativeSearchPage{}, ErrProtocol
	}
	fields, err := nativeObject(result.Raw)
	if err != nil {
		return nativeSearchPage{}, err
	}
	o, e1 := nativeUint(fields["offset"], 64)
	n, e2 := nativeUint(fields["limit"], 64)
	total, e3 := nativeUint(fields["estimatedTotalHits"], 64)
	timing, e4 := nativeUint(fields["processingTimeMs"], 64)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || o != uint64(offset) || n != uint64(limit) || len(result.Hits) > limit {
		return nativeSearchPage{}, ErrProtocol
	}
	if total > math.MaxInt64 {
		return nativeSearchPage{}, ErrBounds
	}
	if len(result.Hits) != 0 && !hasPrimary {
		return nativeSearchPage{}, ErrProtocol
	}
	page := nativeSearchPage{total: total, hasMore: len(result.Hits) == limit && uint64(offset)+uint64(len(result.Hits)) < total,
		info: &contract.DataSearchResult{EstimatedTotalHits: total, ProcessingTimeMS: timing, RawJSON: string(result.Raw)}}
	seen := map[string]bool{}
	budget := responseLimit
	for _, hit := range result.Hits {
		identity, err := l.document(hit)
		if err != nil || seen[identity.ID] {
			return nativeSearchPage{}, ErrProtocol
		}
		seen[identity.ID] = true
		// Search hits may be projected, formatted or enriched with ranking and
		// vectors. They select IDs; only /documents/<ID> is the original source.
		raw, _, err := c.ReadDocument(ctx, l.object.Catalog, identity.ID)
		if err != nil {
			return nativeSearchPage{}, err
		}
		doc, err := l.document(raw)
		if err != nil {
			return nativeSearchPage{}, err
		}
		if doc.ID != identity.ID || !reflect.DeepEqual(doc.IDValue, identity.IDValue) {
			return nativeSearchPage{}, contract.ErrDataConflict
		}
		budget -= len(raw)
		if budget < 0 {
			return nativeSearchPage{}, ErrBounds
		}
		page.documents = append(page.documents, bytes.Clone(raw))
	}
	return page, ctx.Err()
}

// A filtered tabular export follows the server-accessible search range, which
// may be capped or approximate. Complete JSONL uses the documents endpoint.
func (l *dataLease) streamSearch(ctx context.Context, c *Client, query string, sink func(dataDocument) error) error {
	spool, err := newExportSpool()
	if err != nil {
		return err
	}
	defer spool.close()
	var total uint64
	for offset := int64(0); ; {
		page, err := l.searchDocuments(ctx, c, query, offset, 100)
		if err != nil {
			return err
		}
		if offset == 0 {
			total = page.total
		} else if total != page.total {
			return contract.ErrDataConflict
		}
		if _, err = l.guard(ctx, c); err != nil {
			return err
		}
		for _, raw := range page.documents {
			doc, err := l.document(raw)
			if err != nil {
				return err
			}
			var compact bytes.Buffer
			if err = json.Compact(&compact, doc.JSON); err != nil {
				return ErrProtocol
			}
			if err = spool.add(doc.ID, compact.Bytes()); err != nil {
				return err
			}
		}
		if !page.hasMore {
			break
		}
		offset += int64(len(page.documents))
	}
	if err = spool.validate(ctx); err != nil {
		return err
	}
	return spool.emit(ctx, func(raw []byte) error {
		doc, err := l.document(raw)
		if err != nil {
			return err
		}
		return sink(doc)
	})
}
