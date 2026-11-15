package rqlite

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"reflect"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// A document owns its original identity, admission lifetime and unknown write
// receipt. Reads have independent scopes. Writes serialize and create TEMP
// guards only inside one original request/transaction, never between requests.
type dataDocument struct {
	s       *Session
	object  contract.ObjectRef
	mu      sync.Mutex
	closed  bool
	ctx     context.Context
	cancel  context.CancelFunc
	active  sync.WaitGroup
	done    chan struct{}
	writes  chan struct{}
	unknown *contract.DataEditResult
	scope   string
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
	var scope [32]byte
	if _, err := rand.Read(scope[:]); err != nil {
		cancel()
		return nil, err
	}
	d := &dataDocument{s: s, object: o, ctx: ctx, cancel: cancel, done: make(chan struct{}), writes: make(chan struct{}, 1), scope: hex.EncodeToString(scope[:])}
	s.documents[d] = true
	return d, nil
}

func (s *Session) DataDocumentCapabilities() contract.Capabilities { return s.Capabilities() }

func (d *dataDocument) admission(ctx context.Context, id int64, db, table string) (context.Context, func(), error) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, nil, contract.ErrRevoked
	}
	if id != d.object.ProfileID || db != "main" || table != d.object.Name {
		d.mu.Unlock()
		return nil, nil, connections.ErrInvalid
	}
	d.active.Add(1)
	d.mu.Unlock()
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(d.ctx, cancel)
	done := func() { stop(); cancel(); d.active.Done() }
	if err := child.Err(); err != nil {
		done()
		return nil, nil, err
	}
	return child, done, nil
}

func dataCall[T any](d *dataDocument, ctx context.Context, id int64, db, table string, work func(context.Context) (T, error)) (out T, err error) {
	child, done, err := d.admission(ctx, id, db, table)
	if err != nil {
		return out, err
	}
	defer done()
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
	r := contract.DataRuntime{Capabilities: s.Capabilities(), FilterModes: s.DataFilterModes(), PageCancellationAcknowledged: true}
	r.Exporter = func(ctx context.Context, id int64, db, table string, o contract.DataOptions, fields []string, sink func([]contract.DataValue) error) error {
		if o.Object != (contract.ObjectRef{}) && o.Object != d.object {
			return connections.ErrInvalid
		}
		fields, err := copyDataFields(fields)
		if err != nil {
			return err
		}
		captured, err := copyDataOptions(o)
		if err != nil {
			return err
		}
		captured.Object = d.object
		_, err = dataCall(d, ctx, id, db, table, func(ctx context.Context) (bool, error) {
			return true, s.exportData(ctx, id, db, table, captured, fields, sink)
		})
		return err
	}
	r.Analyzer = func(ctx context.Context, id int64, db, table string, o contract.DataOptions, fields []string) (*contract.DataAnalysis, error) {
		if o.Object != (contract.ObjectRef{}) && o.Object != d.object {
			return nil, connections.ErrInvalid
		}
		fields, err := copyDataFields(fields)
		if err != nil {
			return nil, err
		}
		captured, err := copyDataOptions(o)
		if err != nil {
			return nil, err
		}
		captured.Object = d.object
		return dataCall(d, ctx, id, db, table, func(ctx context.Context) (*contract.DataAnalysis, error) {
			return s.analyzeData(ctx, id, db, table, captured, fields)
		})
	}
	r.JSONL = func(ctx context.Context, o contract.ObjectRef, w io.Writer) error {
		if o != d.object {
			return connections.ErrInvalid
		}
		_, err := dataCall(d, ctx, o.ProfileID, "main", o.Name, func(ctx context.Context) (bool, error) { return true, s.exportJSONL(ctx, o, w) })
		return err
	}
	if !s.config.Options.ReadOnly {
		r.ImportDialect = "sqlite"
		r.ImportAtomicBatches = true
		r.StreamImporter = d.importStream
		r.Importer = d.importData
		r.NewTableImporter = d.importNewTable
		r.NativeReceipts = true
		r.EditValueKinds = []contract.CellKind{contract.Text, contract.Integer, contract.Floating, contract.Binary}
		r.Saver = d.save
		r.Verify = func(ctx context.Context, id int64, db, table string, result *contract.DataEditResult) (*contract.DataVerification, error) {
			captured := copyDataResult(result)
			return dataCall(d, ctx, id, db, table, func(context.Context) (*contract.DataVerification, error) {
				d.mu.Lock()
				defer d.mu.Unlock()
				if captured == nil || d.unknown == nil || !reflect.DeepEqual(captured, d.unknown) {
					return nil, connections.ErrInvalid
				}
				// A fresh read cannot establish the original transaction/trigger
				// commit. Return its captured evidence without I/O or replay.
				return &contract.DataVerification{Status: "indeterminate", Recovered: copyDataResult(d.unknown)}, nil
			})
		}
	}
	r.AdvancedLoader = func(ctx context.Context, id int64, db, table string, o contract.DataOptions) (*contract.DataPage, error) {
		if o.Object != (contract.ObjectRef{}) && o.Object != d.object {
			return nil, connections.ErrInvalid
		}
		// These neutral requests contain only data. Deep-copy both expression
		// trees and cursor bytes before worker admission.
		captured, err := copyDataOptions(o)
		if err != nil {
			return nil, err
		}
		o = captured
		o.Object = d.object
		return dataCall(d, ctx, id, db, table, func(ctx context.Context) (*contract.DataPage, error) {
			return s.pageDataScope(ctx, id, db, table, o, d.scope)
		})
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

func copyDataFields(fields []string) ([]string, error) {
	if len(fields) == 0 || len(fields) > maxDataColumns {
		return nil, connections.ErrInvalid
	}
	for _, field := range fields {
		if !validName(field) {
			return nil, connections.ErrInvalid
		}
	}
	return append([]string(nil), fields...), nil
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
	d.mu.Lock()
	d.unknown = nil
	d.scope = ""
	d.mu.Unlock()
	d.s.mu.Lock()
	delete(d.s.documents, d)
	d.s.mu.Unlock()
	close(d.done)
	return nil
}

var _ contract.DataDocumentLease = (*dataDocument)(nil)
var _ contract.DataDocumentsProvider = (*Session)(nil)
var _ contract.DataDocumentFeaturesProvider = (*Session)(nil)
