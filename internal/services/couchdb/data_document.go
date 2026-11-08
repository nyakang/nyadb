package couchdb

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

var ErrDataTargetChanged = errors.New("CouchDB original database observation changed; reopen the document explicitly")

type dataLease struct {
	mu, pageMu      sync.Mutex
	session         *Session
	object          contract.ObjectRef
	ctx             context.Context
	cancel          context.CancelFunc
	closed, changed bool
	done            chan struct{}
	work            sync.WaitGroup
	active          int
	queue           chan struct{}
	key             [32]byte
	creation        string
	epoch           uint64
	limit           int
	query           string
}

// ReserveNativeDocument is private to the native data acceptance stage. It does
// not implement DataDocumentsProvider or publish unverified product capabilities.
func (s *Session) ReserveNativeDocument(o contract.ObjectRef) (contract.DataDocumentLease, error) {
	if err := s.checkNamespace(o.Namespace()); err != nil {
		return nil, err
	}
	if o.Kind != "documents" || o.Name != "_all_docs" {
		return nil, contract.ErrUnsupported
	}
	l := &dataLease{session: s, object: o, done: make(chan struct{}), queue: make(chan struct{}, 4)}
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
	if len(s.documents) >= 32 {
		l.cancel()
		return nil, ErrBounds
	}
	if s.documents == nil {
		s.documents = map[*dataLease]bool{}
	}
	if s.dataSlots == nil {
		s.dataSlots = make(chan struct{}, 4)
	}
	s.documents[l] = true
	return l, nil
}

func nativeDataCapabilities() contract.Capabilities {
	return contract.Capabilities{Documents: true, Metadata: true, Keyset: true, Count: true, FieldChunks: true, Export: true, JSONL: true}
}
func (l *dataLease) Runtime() contract.DataRuntime {
	return contract.DataRuntime{Capabilities: nativeDataCapabilities(), AdvancedLoader: l.page, RowReader: l.row, Count: l.count, Chunk: l.chunk, TableMetadata: l.metadata, TableInfo: l.info, Exporter: l.exportRows, JSONL: l.jsonl,
		FilterModes: []string{"equal", "not_equal"}, FilterColumns: []string{"_id", "_rev"}, SortColumns: []string{"_id"}, ExportFormats: []string{"csv", "tsv", "json", "jsonl", "xml", "html", "xlsx"}}
}
func (l *dataLease) identity(id int64, db, name string) error {
	if id != l.object.ProfileID || db != l.object.Catalog || name != l.object.Name {
		return ErrInvalid
	}
	return l.session.checkNamespace(l.object.Namespace())
}
func (l *dataLease) begin(ctx context.Context) (context.Context, func(), error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, nil, contract.ErrRevoked
	}
	if l.changed {
		l.mu.Unlock()
		return nil, nil, ErrDataTargetChanged
	}
	if l.active >= 8 {
		l.mu.Unlock()
		return nil, nil, ErrBusy
	}
	if err := ctx.Err(); err != nil {
		l.mu.Unlock()
		return nil, nil, err
	}
	l.active++
	l.work.Add(1)
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	l.mu.Unlock()
	child, parentDone, err := l.session.request(child)
	finish := func() {
		stop()
		cancel()
		l.mu.Lock()
		l.active--
		l.mu.Unlock()
		l.work.Done()
	}
	if err != nil {
		finish()
		return nil, nil, err
	}
	return child, func() { parentDone(); finish() }, nil
}
func (l *dataLease) client(ctx context.Context) (*Client, func(), error) {
	select {
	case l.queue <- struct{}{}:
	default:
		return nil, nil, ErrBusy
	}
	select {
	case l.session.dataSlots <- struct{}{}:
	case <-ctx.Done():
		<-l.queue
		return nil, nil, ctx.Err()
	}
	finish := func() { <-l.session.dataSlots; <-l.queue }
	if err := ctx.Err(); err != nil {
		finish()
		return nil, nil, err
	}
	c, err := l.session.open(ctx)
	if err != nil {
		finish()
		return nil, nil, err
	}
	return c, func() { c.Close(); finish() }, nil
}
func (l *dataLease) sign(v any) string {
	raw, _ := json.Marshal(v)
	h := hmac.New(sha256.New, l.key[:])
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}
func (l *dataLease) markChanged() {
	l.mu.Lock()
	l.changed = true
	l.cancel()
	l.mu.Unlock()
}

// Creation time is an observation, not a unique incarnation or an atomic lock.
// Same-second database recreation can alias it; data stays read-only.
func (l *dataLease) guard(ctx context.Context, c *Client) ([]byte, error) {
	raw, err := databaseInfo(ctx, c, l.object.Catalog)
	if err != nil {
		var status *HTTPError
		if errors.As(err, &status) && status.Status == 404 {
			l.markChanged()
			return nil, ErrDataTargetChanged
		}
		return nil, err
	}
	var info struct {
		Creation string `json:"instance_start_time"`
	}
	if decodeJSON(raw, &info) != nil || info.Creation == "" || len(info.Creation) > 128 {
		return nil, ErrProtocol
	}
	if err = l.session.validateObservation(ctx, c); err != nil {
		return nil, err
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
	if l.creation == "" {
		l.creation = info.Creation
	} else if l.creation != info.Creation {
		l.changed = true
		l.cancel()
		return nil, ErrDataTargetChanged
	}
	return raw, nil
}
func (l *dataLease) binding() string { l.mu.Lock(); defer l.mu.Unlock(); return l.creation }
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
	l.work.Wait()
	clear(l.key[:])
	l.creation = ""
	l.query = ""
	l.session.mu.Lock()
	delete(l.session.documents, l)
	l.session.mu.Unlock()
	close(l.done)
	return nil
}

var _ contract.DataDocumentLease = (*dataLease)(nil)
