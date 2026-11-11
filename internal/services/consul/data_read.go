package consul

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type dataBoundary struct {
	Object   contract.ObjectRef
	Scope    string
	Epoch    uint64
	Offset   int64
	Limit    int
	AfterKey string
}

func (l *dataLease) scopeHash() string {
	raw, _ := json.Marshal(l.session.KVScope())
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func (l *dataLease) boundary(after string, offset int64, limit int, epoch uint64) *contract.DataCursor {
	raw := l.encode(dataBoundary{Object: l.object, Scope: l.scopeHash(), Epoch: epoch, Offset: offset, Limit: limit, AfterKey: after})
	return &contract.DataCursor{Offset: offset, Database: l.object.Catalog, Table: l.object.Name, Columns: []string{"consul_live"}, Values: [][]byte{raw}}
}

func validDataOptions(object contract.ObjectRef, options contract.DataOptions) bool {
	return (options.Object == (contract.ObjectRef{}) || options.Object == object) && options.Offset >= 0 && options.Condition == nil && len(options.Filters) == 0 && len(options.Sorts) == 0 && options.FilterColumn == "" && options.FilterMode == "" && options.FilterValue == "" && options.SortColumn == "" && !options.SortDescending
}

func (l *dataLease) fullObservation(ctx context.Context, scope int) (contract.KVResult, []contract.KVHTTPEntry, error) {
	request, err := KeyListRequest(string(l.session.scope.Prefix), false)
	if err != nil {
		return contract.KVResult{}, nil, err
	}
	result, err := l.read(ctx, scope, request)
	if err != nil {
		return result, nil, err
	}
	entries, err := l.entries(result)
	if err != nil {
		return result, nil, err
	}
	return result, entries, nil
}

func (l *dataLease) entries(result contract.KVResult) ([]contract.KVHTTPEntry, error) {
	h := result.HTTP
	if h == nil || !result.Completed || !h.Ended || !h.Captured || !h.EOF || !h.Acknowledged || !h.Succeeded || result.ProjectionError != "" || h.ProjectionError != "" {
		return nil, ErrProtocol
	}
	entries := h.Entries
	if h.Txn != nil {
		if !h.Txn.Committed || len(h.Txn.Errors) > 0 {
			return nil, ErrProtocol
		}
		entries = h.Txn.Results
	}
	if len(entries) > entryLimit {
		return nil, ErrBounds
	}
	seen := make(map[string]bool, len(entries))
	values := 0
	for _, e := range entries {
		if !validKey(e.Key, false) || !strings.HasPrefix(e.Key, string(l.session.scope.Prefix)) || seen[e.Key] || !e.ValuePresent || len(e.Value) > valueLimit {
			return nil, ErrProtocol
		}
		seen[e.Key] = true
		values += len(e.Value)
		if values > dataValueBudget {
			return nil, ErrBounds
		}
	}
	// Sorting owns the slice; original captured response order is unchanged.
	entries = slices.Clone(entries)
	slices.SortFunc(entries, func(a, b contract.KVHTTPEntry) int { return strings.Compare(a.Key, b.Key) })
	return entries, nil
}

func (l *dataLease) page(parent context.Context, id int64, catalog, name string, options contract.DataOptions) (page *contract.DataPage, err error) {
	if err = l.checkPage(id, catalog, name); err != nil {
		return nil, err
	}
	if !validDataOptions(l.object, options) || options.Limit < 1 || options.Limit > 1000 {
		return nil, contract.ErrUnsupported
	}
	if !l.pageMu.TryLock() {
		return nil, contract.ErrKVBusy
	}
	defer l.pageMu.Unlock()
	l.mu.Lock()
	epoch := l.epoch
	l.mu.Unlock()
	after := ""
	if options.After != nil {
		cursor := options.After
		if cursor.Offset != options.Offset || cursor.Database != catalog || cursor.Table != name || !slices.Equal(cursor.Columns, []string{"consul_live"}) || len(cursor.Values) != 1 {
			return nil, ErrInvalid
		}
		var boundary dataBoundary
		if err := l.decode(bytes.Clone(cursor.Values[0]), &boundary); err != nil || boundary.Object != l.object || boundary.Scope != l.scopeHash() || boundary.Epoch != epoch || boundary.Offset != options.Offset || boundary.Limit != options.Limit || !validKey(boundary.AfterKey, true) || boundary.AfterKey != "" && !strings.HasPrefix(boundary.AfterKey, string(l.session.scope.Prefix)) {
			return nil, ErrInvalid
		}
		after = boundary.AfterKey
	} else if options.Offset != 0 {
		return nil, ErrInvalid
	}
	ctx, done, err := l.begin(parent)
	if err != nil {
		return nil, err
	}
	defer done()
	defer func() {
		if err != nil {
			l.mu.Lock()
			l.pageLost = true
			l.mu.Unlock()
		}
	}()
	if options.After == nil {
		l.mu.Lock()
		l.epoch++
		epoch = l.epoch
		l.mu.Unlock()
	}
	started := time.Now()
	result, entries, err := l.fullObservation(ctx, dataPageScope)
	if err != nil {
		return nil, err
	}
	start := 0
	if after != "" {
		start, _ = slices.BinarySearchFunc(entries, after, func(e contract.KVHTTPEntry, key string) int { return strings.Compare(e.Key, key) })
		for start < len(entries) && entries[start].Key <= after {
			start++
		}
	}
	end := min(start+options.Limit, len(entries))
	editable := true
	page = &contract.DataPage{Object: l.object, Editable: &editable, Database: catalog, Table: name, Offset: options.Offset, Limit: options.Limit,
		ColumnMetadata: dataColumns(), PagingMode: "cursor", LiveRead: true, StableOrder: true, KeysetAvailable: true,
		CurrentCursor: l.boundary(after, options.Offset, options.Limit, epoch), HasMore: end < len(entries), QuerySQL: "Consul KV recurse/get-tree (bounded live observation)"}
	for _, column := range page.ColumnMetadata {
		page.Columns = append(page.Columns, column.Name)
		page.ColumnKinds = append(page.ColumnKinds, column.DataType)
	}
	for _, entry := range entries[start:end] {
		if entry.Session != "" {
			if page.ReadOnlyRows == nil {
				page.ReadOnlyRows = map[int]bool{}
			}
			page.ReadOnlyRows[len(page.Rows)] = true
		}
		page.Rows = append(page.Rows, dataCells(entry, true))
		snapshot := l.snapshot(entry, result.HTTP.Metadata)
		ref := cloneDataRef(snapshot.Ref)
		page.RowRefs = append(page.RowRefs, &ref)
		page.RowSnapshots = append(page.RowSnapshots, snapshot)
	}
	if page.HasMore && end > start {
		page.NextCursor = l.boundary(entries[end-1].Key, options.Offset+int64(end-start), options.Limit, epoch)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = l.checkPage(id, catalog, name); err != nil {
		return nil, err
	}
	page.CompletedAt, page.Duration = time.Now(), time.Since(started)
	return page, nil
}

func (l *dataLease) count(parent context.Context, id int64, catalog, name string) (int64, error) {
	if err := l.check(id, catalog, name); err != nil {
		return 0, err
	}
	ctx, done, err := l.begin(parent)
	if err != nil {
		return 0, err
	}
	defer done()
	_, entries, err := l.fullObservation(ctx, dataCountScope)
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := l.check(id, catalog, name); err != nil {
		return 0, err
	}
	return int64(len(entries)), nil
}
