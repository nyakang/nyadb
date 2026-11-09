package neo4j

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

// This binding owns the original graph range for the shared data document.
// Label/type and element IDs are live observations, not incarnation or CAS tokens.
type dataLease struct {
	mu, pageMu   sync.Mutex
	writeMu      sync.Mutex
	session      *Session
	object       contract.ObjectRef
	ctx          context.Context
	cancel       context.CancelFunc
	closed, lost bool
	writeLost    bool
	done         chan struct{}
	work         sync.WaitGroup
	active       int
	key          [32]byte
	epoch        uint64
	limit        int
	properties   []graphPropertyColumn
	entityPlan   *entityDataPlan
	previewEpoch uint64
}

func (s *Session) ReserveDataDocument(o contract.ObjectRef) (contract.DataDocumentLease, error) {
	if e := s.checkNamespace(o.Namespace()); e != nil {
		return nil, e
	}
	if o.Catalog == "system" || (o.Kind != "label" && o.Kind != "relationship") {
		return nil, contract.ErrUnsupported
	}
	if !validDatabase(o.Name) {
		return nil, ErrInvalid
	}
	l := &dataLease{session: s, object: o, done: make(chan struct{})}
	if _, e := rand.Read(l.key[:]); e != nil {
		return nil, e
	}
	l.ctx, l.cancel = context.WithCancel(s.ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked {
		l.cancel()
		return nil, contract.ErrRevoked
	}
	if len(s.dataLeases) >= 32 {
		l.cancel()
		return nil, ErrBounds
	}
	if s.dataLeases == nil {
		s.dataLeases = map[*dataLease]bool{}
		s.dataSlots = make(chan struct{}, 4)
	}
	s.dataLeases[l] = true
	return l, nil
}

func (*Session) DataDocumentCapabilities() contract.Capabilities {
	return contract.Capabilities{EditableTables: true, Documents: true, Metadata: true, Keyset: true, Count: true, FieldChunks: true, Export: true, JSONL: true}
}
func (l *dataLease) Runtime() contract.DataRuntime {
	kinds, preview := []string{"update", "insert", "delete"}, []string{"insert", "delete"}
	if l.object.Kind == "label" {
		kinds, preview = []string{"update", "insert", "delete", "detach_delete"}, []string{"insert", "delete", "detach_delete"}
	}
	out := contract.DataRuntime{PageFailureRequiresReopen: true, NativeReceipts: true, InsertOmittedValues: true, PreviewEdits: l.previewEntityEdits, SubmitEdits: l.submitEntityPlan, PreviewEditKinds: preview, Capabilities: l.session.DataDocumentCapabilities(), AdvancedLoader: l.page, RowReader: l.editRow, Saver: l.save, EditKinds: kinds, EditValueKinds: []contract.CellKind{contract.Null, contract.Text, contract.Integer, contract.Floating, contract.Boolean, contract.Binary, contract.JSON}, WriteDescription: "SET · locked native graph property transaction", Count: l.count, Chunk: l.chunk, TableMetadata: l.metadata, TableInfo: l.info, Exporter: l.exportRows, JSONL: l.jsonl,
		ReadDescription: func(contract.DataOptions) string { return "MATCH · elementId ordered native graph read" },
		FilterColumns:   []string{}, SortColumns: []string{}, ExportFormats: []string{"csv", "tsv", "json", "jsonl", "xml", "html", "xlsx"}}
	if l.object.Kind == "relationship" {
		out.ReadGraphEndpoints = l.readGraphEndpoints
	}
	return out
}
func (l *dataLease) identity(id int64, db, name string) error {
	if id != l.object.ProfileID || db != l.object.Catalog || name != l.object.Name {
		return ErrInvalid
	}
	return l.session.checkNamespace(l.object.Namespace())
}
func (l *dataLease) request(ctx context.Context) (context.Context, func(), error) {
	l.mu.Lock()
	if l.closed || l.lost {
		l.mu.Unlock()
		return nil, nil, contract.ErrRevoked
	}
	if e := ctx.Err(); e != nil {
		l.mu.Unlock()
		return nil, nil, e
	}
	if l.active >= 8 {
		l.mu.Unlock()
		return nil, nil, ErrBusy
	}
	l.work.Add(1)
	l.active++
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	l.mu.Unlock()
	child, parentDone, e := l.session.request(child)
	finish := func() {
		stop()
		cancel()
		l.mu.Lock()
		l.active--
		l.mu.Unlock()
		l.work.Done()
	}
	if e != nil {
		finish()
		return nil, nil, e
	}
	return child, func() { parentDone(); finish() }, nil
}

// Each operation captures an independent original driver. Auxiliary cancellation
// cannot retire a page range or another field/count reader. No operation retries.
func (l *dataLease) client(ctx context.Context) (*Client, func(), error) {
	select {
	case l.session.dataSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	finish := func() { <-l.session.dataSlots }
	c, e := l.session.open(ctx)
	if e != nil {
		finish()
		return nil, nil, e
	}
	return c, func() { _ = c.Close(); finish() }, nil
}
func (l *dataLease) read(ctx context.Context, c *Client, q string, p map[string]any, maxRows int) (Response, error) {
	parameters := make(map[string]any, len(p)+1)
	for name, value := range p {
		parameters[name] = value
	}
	parameters["name"] = l.object.Name
	plan, e := c.Prepare(Request{Database: l.object.Catalog, Query: q, Parameters: parameters, Mode: "read", MaxRows: maxRows, MaxBytes: 16 << 20})
	if e != nil {
		return Response{}, e
	}
	r, e := c.Execute(ctx, plan)
	if e != nil {
		if r.Uncertain {
			e = errors.Join(ErrRetired, e)
		}
		return r, e
	}
	if !r.Completed || r.Uncertain || r.Summary.Database != l.object.Catalog {
		return r, ErrProtocol
	}
	if r.Truncated {
		return r, ErrBounds
	}
	for _, v := range r.Summary.Counters {
		if v != 0 {
			return r, ErrProtocol
		}
	}
	if !l.session.Alive() {
		return r, contract.ErrRevoked
	}
	return r, ctx.Err()
}
func (l *dataLease) sign(v any) string {
	raw, _ := json.Marshal(v)
	// Field validation can precede request registration. Capture the key under
	// the same lock as close, which clears it after registered work settles.
	l.mu.Lock()
	key := l.key
	l.mu.Unlock()
	h := hmac.New(sha256.New, key[:])
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
	l.work.Wait()
	l.mu.Lock()
	l.properties = nil
	if l.entityPlan != nil {
		l.entityPlan.releaseLocked()
	}
	clear(l.key[:])
	l.mu.Unlock()
	l.session.mu.Lock()
	delete(l.session.dataLeases, l)
	l.session.mu.Unlock()
	close(l.done)
	return nil
}
func (l *dataLease) pageFailure(e error) error {
	if errors.Is(e, ErrRetired) || errors.Is(e, ErrProtocol) {
		l.mu.Lock()
		l.lost = true
		// Revocation blocks new reads, but does not cancel independently running
		// field/count requests. Explicit document/session Close cancels them all.
		l.mu.Unlock()
		return errors.Join(contract.ErrRevoked, e)
	}
	return e
}

var _ contract.DataDocumentsProvider = (*Session)(nil)
var _ contract.DataDocumentFeaturesProvider = (*Session)(nil)
