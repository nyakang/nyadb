package rqlite

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// A document owns only its original identity and admission lifetime. It creates
// no server TEMP objects or hidden cross-request transaction. Page/count/field
// workers have independent request contexts and share only this close scope.
type dataDocument struct {
	s      *Session
	object contract.ObjectRef
	mu     sync.Mutex
	closed bool
	ctx    context.Context
	cancel context.CancelFunc
	active sync.WaitGroup
	done   chan struct{}
}

func (s *Session) ReserveDataDocument(o contract.ObjectRef) (contract.DataDocumentLease, error) {
	if err := s.checkObject(o); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, contract.ErrRevoked
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &dataDocument{s: s, object: o, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	s.documents[d] = true
	return d, nil
}

func (s *Session) DataDocumentCapabilities() contract.Capabilities { return s.Capabilities() }

func dataCall[T any](d *dataDocument, ctx context.Context, id int64, db, table string, work func(context.Context) (T, error)) (out T, err error) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return out, contract.ErrRevoked
	}
	if id != d.object.ProfileID || db != "main" || table != d.object.Name {
		d.mu.Unlock()
		return out, connections.ErrInvalid
	}
	d.active.Add(1)
	d.mu.Unlock()
	defer d.active.Done()
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(d.ctx, cancel)
	defer func() { stop(); cancel() }()
	if err := child.Err(); err != nil {
		return out, err
	}
	out, err = work(child)
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		var zero T
		return zero, contract.ErrRevoked
	}
	return out, err
}

func (d *dataDocument) Runtime() contract.DataRuntime {
	s := d.s
	r := contract.DataRuntime{Capabilities: s.Capabilities(), FilterModes: s.DataFilterModes()}
	r.AdvancedLoader = func(ctx context.Context, id int64, db, table string, o contract.DataOptions) (*contract.DataPage, error) {
		if o.Object != (contract.ObjectRef{}) && o.Object != d.object {
			return nil, connections.ErrInvalid
		}
		// These neutral requests contain only data. Deep-copy both expression
		// trees and cursor bytes before worker admission.
		b, err := json.Marshal(o)
		var captured contract.DataOptions
		if err != nil || json.Unmarshal(b, &captured) != nil {
			return nil, connections.ErrInvalid
		}
		o = captured
		o.Object = d.object
		return dataCall(d, ctx, id, db, table, func(ctx context.Context) (*contract.DataPage, error) { return s.pageData(ctx, id, db, table, o) })
	}
	r.Loader = func(ctx context.Context, id int64, db, table string, offset int64, limit int) (*contract.DataPage, error) {
		return r.AdvancedLoader(ctx, id, db, table, contract.DataOptions{Offset: offset, Limit: limit})
	}
	r.RowReader = func(ctx context.Context, id int64, db, table string, ref contract.RowRef) (*contract.DataRow, error) {
		if ref.Object != d.object {
			return nil, connections.ErrInvalid
		}
		ref = copyDataRef(ref)
		return dataCall(d, ctx, id, db, table, func(ctx context.Context) (*contract.DataRow, error) { return s.readDataRow(ctx, id, db, table, ref) })
	}
	r.Chunk = func(ctx context.Context, id int64, db, table, field string, ref contract.RowRef, offset int64, limit int) (*contract.FieldChunk, error) {
		if ref.Object != d.object {
			return nil, connections.ErrInvalid
		}
		ref = copyDataRef(ref)
		return dataCall(d, ctx, id, db, table, func(ctx context.Context) (*contract.FieldChunk, error) {
			return s.readDataChunk(ctx, id, db, table, field, ref, offset, limit)
		})
	}
	r.Count = func(ctx context.Context, id int64, db, table string) (int64, error) {
		return dataCall(d, ctx, id, db, table, func(ctx context.Context) (int64, error) { return s.countDataObject(ctx, id, db, table, d.object) })
	}
	r.TableMetadata = func(ctx context.Context, id int64, db, table string) (*contract.TableStructure, error) {
		return dataCall(d, ctx, id, db, table, func(ctx context.Context) (*contract.TableStructure, error) {
			t, err := s.dataTable(ctx, id, db, table, d.object)
			return t.structure, err
		})
	}
	r.TableInfo = func(ctx context.Context, id int64, db, table string) (*contract.TableInfo, error) {
		return dataCall(d, ctx, id, db, table, func(ctx context.Context) (*contract.TableInfo, error) {
			t, err := s.dataTable(ctx, id, db, table, d.object)
			if err != nil {
				return nil, err
			}
			return &contract.TableInfo{Database: "main", Name: table, Kind: t.structure.Kind, Engine: "RQLite"}, nil
		})
	}
	return r
}

func copyDataRef(ref contract.RowRef) contract.RowRef {
	ref.Columns = append([]string(nil), ref.Columns...)
	values, typed := ref.Values, ref.TypedValues
	ref.Values, ref.TypedValues = nil, nil
	for _, v := range values {
		ref.Values = append(ref.Values, append([]byte(nil), v...))
	}
	for _, v := range typed {
		ref.TypedValues = append(ref.TypedValues, v.Clone())
	}
	return ref
}

func (d *dataDocument) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		<-d.done
		return nil
	}
	d.closed = true
	d.cancel()
	d.mu.Unlock()
	d.active.Wait() // Dispatched native requests acknowledge independently.
	d.s.mu.Lock()
	delete(d.s.documents, d)
	d.s.mu.Unlock()
	close(d.done)
	return nil
}

var _ contract.DataDocumentLease = (*dataDocument)(nil)
var _ contract.DataDocumentsProvider = (*Session)(nil)
var _ contract.DataDocumentFeaturesProvider = (*Session)(nil)
