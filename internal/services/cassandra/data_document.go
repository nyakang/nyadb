package cassandra

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
)

var ErrDataTargetChanged = errors.New("Cassandra original table/schema changed; reopen the data document explicitly")

type dataLease struct {
	mu, pageMu, bindingMu sync.Mutex
	session               *Session
	object                contract.ObjectRef
	ctx                   context.Context
	cancel                context.CancelFunc
	closed, lost, changed bool
	done                  chan struct{}
	work                  sync.WaitGroup
	key                   [32]byte
	binding               *dataBinding
	pageClient            *Client
	pageColumns           []Column
	epoch                 uint64
	limit                 int
	auxQueue              chan struct{}
}

func (s *Session) ReserveDataDocument(o contract.ObjectRef) (contract.DataDocumentLease, error) {
	if err := s.checkObject(o); err != nil {
		return nil, err
	}
	if o.Kind != "table" && o.Kind != "view" {
		return nil, contract.ErrUnsupported
	}
	l := &dataLease{session: s, object: o, done: make(chan struct{}), auxQueue: make(chan struct{}, 4)}
	if _, err := rand.Read(l.key[:]); err != nil {
		return nil, err
	}
	l.ctx, l.cancel = context.WithCancel(s.ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked {
		l.cancel()
		return nil, contract.ErrRevoked
	}
	if len(s.datas) >= 32 {
		l.cancel()
		return nil, ErrBounds
	}
	if s.datas == nil {
		s.datas = map[*dataLease]bool{}
	}
	if s.dataSlots == nil {
		s.dataSlots = make(chan struct{}, 4)
	}
	s.datas[l] = true
	return l, nil
}
func (*Session) DataDocumentCapabilities() contract.Capabilities {
	return contract.Capabilities{Metadata: true, Keyset: true, Count: true, FieldChunks: true, Export: true, JSONL: true}
}
func (l *dataLease) Runtime() contract.DataRuntime {
	return contract.DataRuntime{Capabilities: l.session.DataDocumentCapabilities(), AdvancedLoader: l.page, RowReader: l.row, Count: l.count, Chunk: l.chunk, Exporter: l.exportRows, JSONL: l.jsonl, TableMetadata: l.metadata, TableInfo: l.info, FilterModes: []string{}, FilterColumns: []string{}, SortColumns: []string{}, ExportFormats: []string{"csv", "tsv", "json", "jsonl", "xml", "html", "xlsx"}}
}
func (l *dataLease) identity(id int64, db, name string) error {
	if id != l.object.ProfileID || db != l.object.Catalog || name != l.object.Name {
		return ErrInvalid
	}
	return l.session.checkObject(l.object)
}
func (l *dataLease) request(ctx context.Context) (context.Context, func(), error) {
	l.mu.Lock()
	if l.closed || l.lost {
		l.mu.Unlock()
		return nil, nil, contract.ErrRevoked
	}
	if l.changed {
		l.mu.Unlock()
		return nil, nil, ErrDataTargetChanged
	}
	if err := ctx.Err(); err != nil {
		l.mu.Unlock()
		return nil, nil, err
	}
	l.work.Add(1)
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	l.mu.Unlock()
	child, done, err := l.session.request(child)
	if err != nil {
		stop()
		cancel()
		l.work.Done()
		return nil, nil, err
	}
	return child, func() { done(); stop(); cancel(); l.work.Done() }, nil
}

// Auxiliary operations own separate physical clients; their cancellation never
// retires the persistent page cursor. There is no retry inside an operation.
func (l *dataLease) auxiliary(ctx context.Context) (context.Context, *Client, func(), error) {
	ctx, done, err := l.request(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	select {
	case l.auxQueue <- struct{}{}:
	default:
		done()
		return nil, nil, nil, ErrBusy
	}
	select {
	case l.session.dataSlots <- struct{}{}:
	case <-ctx.Done():
		<-l.auxQueue
		done()
		return nil, nil, nil, ctx.Err()
	}
	finish := func() { <-l.session.dataSlots; <-l.auxQueue; done() }
	if err = ctx.Err(); err != nil {
		finish()
		return nil, nil, nil, err
	}
	c, err := l.session.open(ctx)
	if err != nil {
		finish()
		return nil, nil, nil, err
	}
	return ctx, c, func() { c.Close(); finish() }, nil
}
func (l *dataLease) pageError(err error) error {
	if errors.Is(err, ErrRetired) {
		l.mu.Lock()
		l.lost = true
		l.cancel()
		l.mu.Unlock()
		return errors.Join(contract.ErrRevoked, err)
	}
	return err
}
func (l *dataLease) sign(v any) string {
	raw, _ := json.Marshal(v)
	h := hmac.New(sha256.New, l.key[:])
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}
func (l *dataLease) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		<-l.done
		return nil
	}
	l.closed = true
	l.cancel()
	l.mu.Unlock()
	// Active driver calls acknowledge cancellation before work.Done. No fields
	// captured by an executor are released until all registered work has settled.
	l.work.Wait()
	l.pageMu.Lock()
	if l.pageClient != nil {
		l.pageClient.Close()
		l.pageClient = nil
	}
	l.pageColumns = nil
	l.pageMu.Unlock()
	l.binding = nil
	clear(l.key[:])
	l.session.mu.Lock()
	delete(l.session.datas, l)
	l.session.mu.Unlock()
	close(l.done)
	return nil
}

var _ contract.DataDocumentsProvider = (*Session)(nil)
var _ contract.DataDocumentFeaturesProvider = (*Session)(nil)
