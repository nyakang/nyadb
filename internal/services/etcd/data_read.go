package etcd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

func dataColumns() []contract.StructureColumn {
	return []contract.StructureColumn{
		{Name: "key", Position: 1, Type: "bytes", DataType: "bytes", ValueKind: contract.Binary, Key: "PRI"},
		{Name: "value", Position: 2, Type: "bytes", DataType: "bytes", ValueKind: contract.Binary},
		{Name: "create_revision", Position: 3, Type: "int64", DataType: "int64", ValueKind: contract.Integer, Generated: true},
		{Name: "mod_revision", Position: 4, Type: "int64", DataType: "int64", ValueKind: contract.Integer, Generated: true},
		{Name: "version", Position: 5, Type: "int64", DataType: "int64", ValueKind: contract.Integer, Generated: true},
		{Name: "lease", Position: 6, Type: "int64", DataType: "int64", ValueKind: contract.Integer},
	}
}
func (l *dataLease) metadata(ctx context.Context, id int64, catalog, name string) (*contract.TableStructure, error) {
	if err := l.check(id, catalog, name); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &contract.TableStructure{Object: l.object, Database: catalog, Name: name, Kind: "键", Engine: "etcd", Columns: dataColumns(), Comment: "Original byte prefix; fixed MVCC revision. Range authorization is checked by the server."}, nil
}
func (l *dataLease) info(ctx context.Context, id int64, catalog, name string) (*contract.TableInfo, error) {
	metadata, err := l.metadata(ctx, id, catalog, name)
	if err != nil {
		return nil, err
	}
	return &contract.TableInfo{Database: catalog, Name: name, Kind: metadata.Kind, Engine: metadata.Engine, Comment: metadata.Comment}, nil
}
func (l *dataLease) originalRevision(ctx context.Context, scope int) (int64, error) {
	l.snapshotMu.Lock()
	defer l.snapshotMu.Unlock()
	l.mu.Lock()
	revision := l.revision
	l.mu.Unlock()
	if revision != 0 {
		return revision, nil
	}
	request := PrefixRange(l.session.prefix, 1, 0)
	request.KeysOnly = true
	result, err := l.native(ctx, scope, request)
	if err != nil {
		return 0, err
	}
	if !result.Completed || result.Header.Revision <= 0 {
		return 0, ErrInvalid
	}
	l.mu.Lock()
	l.revision = result.Header.Revision
	l.mu.Unlock()
	return result.Header.Revision, nil
}

type dataBoundary struct {
	Epoch            uint64 `json:"epoch"`
	Revision, Offset int64
	Limit            int
	Start            []byte
}

func (l *dataLease) boundary(start []byte, offset int64, limit int, revision int64) *contract.DataCursor {
	l.mu.Lock()
	epoch := l.epoch
	l.mu.Unlock()
	raw := l.encode(dataBoundary{Epoch: epoch, Revision: revision, Offset: offset, Limit: limit, Start: bytes.Clone(start)})
	return &contract.DataCursor{Offset: offset, Database: l.object.Catalog, Table: l.object.Name, Columns: []string{"etcd_mvcc"}, Values: [][]byte{raw}}
}
func (l *dataLease) page(ctx context.Context, id int64, catalog, name string, options contract.DataOptions) (page *contract.DataPage, err error) {
	if err = l.check(id, catalog, name); err != nil {
		return nil, err
	}
	if options.Object != (contract.ObjectRef{}) && options.Object != l.object || options.Limit < 1 || options.Limit > 1000 || options.Offset < 0 || options.Condition != nil || len(options.Filters) > 0 || len(options.Sorts) > 0 || options.FilterColumn != "" || options.FilterMode != "" || options.FilterValue != "" || options.SortColumn != "" || options.SortDescending {
		return nil, contract.ErrUnsupported
	}
	if !l.pageMu.TryLock() {
		return nil, contract.ErrKVBusy
	}
	defer l.pageMu.Unlock()
	var prior *dataBoundary
	if options.After != nil {
		cursor := options.After
		if cursor.Database != catalog || cursor.Table != name || !slices.Equal(cursor.Columns, []string{"etcd_mvcc"}) || len(cursor.Values) != 1 {
			return nil, ErrInvalid
		}
		var boundary dataBoundary
		if err := l.decode(cursor.Values[0], &boundary); err != nil {
			return nil, err
		}
		l.mu.Lock()
		epoch, revision := l.epoch, l.revision
		l.mu.Unlock()
		if revision == 0 || boundary.Epoch != epoch || boundary.Revision != revision || boundary.Limit != options.Limit || boundary.Offset != options.Offset || cursor.Offset != options.Offset || !validKey(boundary.Start) || !bytes.HasPrefix(boundary.Start, l.session.prefix) {
			return nil, ErrInvalid
		}
		prior = &boundary
	} else if options.Offset != 0 {
		return nil, ErrInvalid
	}
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	// Returning to an authenticated earlier boundary preserves its revision.
	// An explicit first-page request starts a new healthy read range; the UI
	// sends that request on Refresh and clears its old cursor history.
	if prior == nil {
		l.snapshotMu.Lock()
		l.mu.Lock()
		if l.pageStarted {
			l.revision = 0
			l.epoch++
			l.pageStarted = false
		}
		l.mu.Unlock()
		l.snapshotMu.Unlock()
	}
	defer func() {
		if err != nil {
			l.mu.Lock()
			l.rangeLost = true
			l.mu.Unlock()
		}
	}()
	revision, err := l.originalRevision(ctx, dataPageScope)
	if err != nil {
		return nil, err
	}
	// Range returns complete values, even when the grid shows only a preview.
	// Aggregate one key per RPC from the outset so a page of individually
	// valid large values cannot exceed the native response bound.
	request := PrefixRange(l.session.prefix, 1, revision)
	if prior != nil {
		request.Key = bytes.Clone(prior.Start)
	}
	started := time.Now()
	editable := true
	page = &contract.DataPage{Object: l.object, Editable: &editable, Database: catalog, Table: name, Offset: options.Offset, Limit: options.Limit, ColumnMetadata: dataColumns(), PagingMode: "cursor", StableOrder: true, KeysetAvailable: true, CurrentCursor: l.boundary(request.Key, options.Offset, options.Limit, revision), QuerySQL: "/etcdserverpb.KV/Range (fixed revision, one key per RPC)"}
	for _, c := range page.ColumnMetadata {
		page.Columns = append(page.Columns, c.Name)
		page.ColumnKinds = append(page.ColumnKinds, c.DataType)
	}
	var previous []byte
	for len(page.Rows) < options.Limit {
		result, readErr := l.native(ctx, dataPageScope, request)
		if readErr != nil {
			return nil, readErr
		}
		if !result.Completed || result.Reply == nil || result.ProjectionError != "" || result.Reply.Kind != "range" || len(result.Reply.Entries) > 1 {
			return nil, ErrInvalid
		}
		page.HasMore = result.Reply.More
		if len(result.Reply.Entries) == 0 {
			if page.HasMore {
				return nil, ErrInvalid
			}
			break
		}
		entry := result.Reply.Entries[0]
		if !validKey(entry.Key) || !bytes.HasPrefix(entry.Key, l.session.prefix) || bytes.Compare(entry.Key, request.Key) < 0 || previous != nil && bytes.Compare(entry.Key, previous) <= 0 || !validDataEntry(entry) || entry.ModRevision > revision {
			return nil, ErrInvalid
		}
		previous = bytes.Clone(entry.Key)
		page.Rows = append(page.Rows, entryCells(entry, true))
		ref := l.ref(entry.Key)
		page.RowRefs = append(page.RowRefs, &ref)
		page.RowSnapshots = append(page.RowSnapshots, l.snapshot(entry, false))
		if !page.HasMore {
			break
		}
		request.Key = dataAfterKey(entry.Key)
		if request.Key == nil || !bytes.HasPrefix(request.Key, l.session.prefix) {
			return nil, ErrInvalid
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = l.check(id, catalog, name); err != nil {
		return nil, err
	}
	if page.HasMore {
		page.NextCursor = l.boundary(request.Key, options.Offset+int64(len(page.Rows)), options.Limit, revision)
	}
	page.CompletedAt, page.Duration = time.Now(), time.Since(started)
	l.mu.Lock()
	l.pageStarted = true
	l.mu.Unlock()
	return page, nil
}

// Keys are limited to 4096 bytes. At that length, appending zero would make an
// invalid Range key; carry within the bounded byte domain instead.
func dataAfterKey(key []byte) []byte {
	if len(key) < 4096 {
		return AfterKey(key)
	}
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] != 255 {
			next := bytes.Clone(key[:i+1])
			next[i]++
			return next
		}
	}
	return nil
}

func validDataEntry(e contract.KVEntry) bool {
	return validKey(e.Key) && e.CreateRevision > 0 && e.ModRevision >= e.CreateRevision && e.Version > 0
}
func (l *dataLease) count(ctx context.Context, id int64, catalog, name string) (int64, error) {
	if err := l.check(id, catalog, name); err != nil {
		return 0, err
	}
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer done()
	revision, err := l.originalRevision(ctx, dataCountScope)
	if err != nil {
		return 0, err
	}
	request := PrefixRange(l.session.prefix, 0, revision)
	request.CountOnly = true
	result, err := l.native(ctx, dataCountScope, request)
	if err != nil {
		return 0, err
	}
	if !result.Completed || result.Reply == nil {
		return 0, ErrInvalid
	}
	return result.Reply.Count, nil
}
func entryCells(entry contract.KVEntry, preview bool) []contract.DataCell {
	values := []contract.Cell{{Kind: contract.Binary, Bytes: bytes.Clone(entry.Key)}, {Kind: contract.Binary, Bytes: bytes.Clone(entry.Value)}}
	for _, number := range []int64{entry.CreateRevision, entry.ModRevision, entry.Version, entry.Lease} {
		values = append(values, contract.Cell{Kind: contract.Integer, Text: strconv.FormatInt(number, 10)})
	}
	cells := make([]contract.DataCell, len(values))
	for i := range values {
		cell := values[i].Clone()
		if preview && cell.Kind == contract.Binary && len(cell.Bytes) > 1024 {
			cell.Bytes = bytes.Clone(cell.Bytes[:1024])
			cell.Truncated = true
		}
		cells[i] = contract.DataCell{Text: cell.Display(), Truncated: cell.Truncated, Value: &cell}
	}
	return cells
}
func (l *dataLease) ref(key []byte) contract.RowRef {
	return contract.RowRef{Object: l.object, Database: l.object.Catalog, Table: l.object.Name, Columns: []string{"key"}, Values: [][]byte{bytes.Clone(key)}, TypedValues: []contract.Cell{{Kind: contract.Binary, Bytes: bytes.Clone(key)}}}
}

type dataObservation struct {
	Key                                         []byte
	CreateRevision, ModRevision, Version, Lease int64
	ValueHash                                   string
}

func entryObservation(entry contract.KVEntry) dataObservation {
	hash := sha256.Sum256(entry.Value)
	return dataObservation{Key: bytes.Clone(entry.Key), CreateRevision: entry.CreateRevision, ModRevision: entry.ModRevision, Version: entry.Version, Lease: entry.Lease, ValueHash: hex.EncodeToString(hash[:])}
}
func (l *dataLease) snapshot(entry contract.KVEntry, values bool) *contract.RowSnapshot {
	snapshot := &contract.RowSnapshot{Mode: "fingerprints", Ref: l.ref(entry.Key), Metadata: string(l.encode(entryObservation(entry)))}
	for i, cell := range entryCells(entry, false) {
		snapshot.Columns = append(snapshot.Columns, dataColumns()[i].Name)
		value := []byte(cell.Value.Text)
		if cell.Value.Kind == contract.Binary {
			value = cell.Value.Bytes
		}
		hash := sha256.Sum256(value)
		fingerprint := hex.EncodeToString(hash[:])
		snapshot.Fingerprints = append(snapshot.Fingerprints, &fingerprint)
		if values {
			snapshot.Original = append(snapshot.Original, cell.Value.Clone())
		}
	}
	if values {
		snapshot.Mode = "values"
		snapshot.Fingerprints = nil
	}
	return snapshot
}
func (l *dataLease) validateRef(ref contract.RowRef) error {
	if ref.Object != l.object || ref.Database != l.object.Catalog || ref.Table != l.object.Name || !slices.Equal(ref.Columns, []string{"key"}) || len(ref.Values) != 1 || !validKey(ref.Values[0]) || !bytes.HasPrefix(ref.Values[0], l.session.prefix) {
		return ErrInvalid
	}
	if len(ref.TypedValues) > 0 && (len(ref.TypedValues) != 1 || ref.TypedValues[0].Kind != contract.Binary || ref.TypedValues[0].Truncated || ref.TypedValues[0].Text != "" || !bytes.Equal(ref.TypedValues[0].Bytes, ref.Values[0])) {
		return ErrInvalid
	}
	return nil
}
func (l *dataLease) readEntry(ctx context.Context, scope int, ref contract.RowRef) (contract.KVEntry, error) {
	if err := l.validateRef(ref); err != nil {
		return contract.KVEntry{}, err
	}
	result, err := l.native(ctx, scope, &pb.RangeRequest{Key: bytes.Clone(ref.Values[0])})
	if err != nil {
		return contract.KVEntry{}, err
	}
	if !result.Completed || result.Reply == nil || len(result.Reply.Entries) != 1 {
		return contract.KVEntry{}, contract.ErrDataConflict
	}
	entry := result.Reply.Entries[0]
	if !bytes.Equal(entry.Key, ref.Values[0]) || !validDataEntry(entry) {
		return contract.KVEntry{}, ErrInvalid
	}
	return entry.Clone(), nil
}
func (l *dataLease) row(ctx context.Context, id int64, catalog, name string, ref contract.RowRef) (*contract.DataRow, error) {
	if err := l.check(id, catalog, name); err != nil {
		return nil, err
	}
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	entry, err := l.readEntry(ctx, dataFieldScope, ref)
	if err != nil {
		return nil, err
	}
	if len(entry.Value) > contract.DataEditMaxBytes {
		return nil, contract.ErrDataEditLimit
	}
	// Complete field reads keep the page's original fingerprint representation,
	// so the shared grid can compare the exact observation before admitting bytes.
	row := &contract.DataRow{Columns: dataColumns(), Snapshot: l.snapshot(entry, false), Values: map[string]contract.DataValue{}}
	for i, cell := range entryCells(entry, false) {
		row.Values[row.Columns[i].Name] = contract.DataValue{Kind: cell.Value.Kind, Text: cell.Value.Text, Bytes: bytes.Clone(cell.Value.Bytes), Binary: cell.Value.Kind == contract.Binary}
	}
	return row, nil
}
func (l *dataLease) chunk(ctx context.Context, id int64, catalog, name, field string, ref contract.RowRef, offset int64, limit int) (*contract.FieldChunk, error) {
	if err := l.check(id, catalog, name); err != nil {
		return nil, err
	}
	if (field != "key" && field != "value") || offset < 0 || limit < 1 || limit > 1024 {
		return nil, ErrInvalid
	}
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	entry, err := l.readEntry(ctx, dataFieldScope, ref)
	if err != nil {
		return nil, err
	}
	value := entry.Value
	if field == "key" {
		value = entry.Key
	}
	total := int64(len(value))
	start := min(offset, total)
	end := min(start+int64(limit), total)
	return &contract.FieldChunk{Text: "0x" + hex.EncodeToString(value[start:end]), Binary: true, Offset: offset, NextOffset: end, TotalUnits: total, HasMore: end < total}, nil
}
