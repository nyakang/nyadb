package neo4j

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphschema"
)

type schemaPreparationLease struct {
	s                  *Session
	object             contract.ObjectRef
	document           uint64
	ops                sync.Mutex
	mu                 sync.Mutex
	ctx                context.Context
	cancel             context.CancelFunc
	closed, lost       bool
	client             *Client // Owned only under ops; never replaced implicitly.
	sequence           uint64
	latest             *schemaObservation
	operation          *schemaOperation
	verificationClient *Client // Explicit read-only recovery; never a write client.
	evidenceMu         sync.Mutex
	last               contract.GraphSchemaManagementResult
	closeOnce          sync.Once
	closeErr           error
}
type schemaObservation struct {
	owner    *schemaPreparationLease
	snapshot contract.GraphSchemaCatalog
}
type schemaPreparation struct {
	preview contract.GraphSchemaPreview
	owner   *schemaPreparationLease
	before  *schemaObservation
	native  Plan
	used    atomic.Bool
}

func (s *Session) SupportsGraphSchemaPreparation(o contract.ObjectRef) bool {
	return s.Alive() && s.identity.Agent == "Neo4j/5.26.21" && o.Kind == "graph_schema" && o.Name == "graph_schema" && o.Catalog != "system" && s.checkNamespace(o.Namespace()) == nil
}
func (s *Session) ReserveGraphSchemaPreparation(o contract.ObjectRef, document uint64) (contract.GraphSchemaPreparationLease, error) {
	if !s.Alive() {
		return nil, contract.ErrRevoked
	}
	if document == 0 || !s.SupportsGraphSchemaPreparation(o) {
		return nil, contract.ErrUnsupported
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked {
		return nil, contract.ErrRevoked
	}
	if len(s.schemaLeases) >= 8 {
		return nil, ErrBusy
	}
	for l := range s.schemaLeases {
		if l.document == document {
			return nil, ErrBusy
		}
	}
	ctx, cancel := context.WithCancel(s.ctx)
	l := &schemaPreparationLease{s: s, object: o, document: document, ctx: ctx, cancel: cancel}
	s.schemaLeases[l] = true
	return l, nil
}
func (l *schemaPreparationLease) Object() contract.ObjectRef { return l.object }
func (l *schemaPreparationLease) Generation() uint64         { return l.s.Generation() }
func (l *schemaPreparationLease) check() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.lost || !l.s.Alive() {
		return contract.ErrRevoked
	}
	return nil
}
func (o *schemaObservation) Snapshot() contract.GraphSchemaCatalog { return o.snapshot.Clone() }
func (p *schemaPreparation) Preview() contract.GraphSchemaPreview  { return p.preview.Clone() }
func (l *schemaPreparationLease) request(parent context.Context) (context.Context, func(), error) {
	if e := l.check(); e != nil {
		return nil, nil, e
	}
	ctx, done, e := l.s.request(parent)
	if e != nil {
		return nil, nil, e
	}
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	return child, func() { stop(); cancel(); done() }, nil
}
func (l *schemaPreparationLease) settle(e error) error {
	if l.client != nil && (!l.client.Alive() || errors.Is(e, ErrProtocol) || errors.Is(e, graphschema.ErrIncomplete)) {
		l.mu.Lock()
		l.lost = true
		l.mu.Unlock()
		l.latest = nil
		return errors.Join(contract.ErrRevoked, e, l.client.Close())
	}
	return e
}
func (l *schemaPreparationLease) Read(parent context.Context) (contract.GraphSchemaObservation, error) {
	if !l.ops.TryLock() {
		return nil, ErrBusy
	}
	defer l.ops.Unlock()
	ctx, done, e := l.request(parent)
	if e != nil {
		return nil, e
	}
	defer done()
	if l.operation != nil && !l.operation.resolved {
		return nil, contract.ErrGraphSchemaBusy
	}
	// Even a failed refresh revokes the old local observation as authorization.
	l.latest = nil
	if l.client == nil {
		l.client, e = l.s.open(ctx)
		if e != nil {
			return nil, e
		}
	}
	// Lease close is published synchronously; AfterFunc propagation to the
	// request context may still be pending when an uncooperative opener returns.
	// Keep the captured client for Close, but never issue metadata after close.
	if e = l.check(); e != nil {
		return nil, e
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	if !l.client.Alive() {
		return nil, l.settle(ErrRetired)
	}
	c, e := readSchemaCatalog(ctx, l.client, l.object)
	if e != nil {
		return nil, l.settle(e)
	}
	if e = l.check(); e != nil {
		return nil, e
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	l.sequence++
	c.Sequence = l.sequence
	l.latest = &schemaObservation{owner: l, snapshot: c.Clone()}
	return l.latest, nil
}
func (l *schemaPreparationLease) Prepare(before contract.GraphSchemaObservation, draft contract.GraphSchemaDraft) (contract.GraphSchemaPreparation, error) {
	if !l.ops.TryLock() {
		return nil, ErrBusy
	}
	defer l.ops.Unlock()
	if e := l.check(); e != nil {
		return nil, e
	}
	o, ok := before.(*schemaObservation)
	if !ok || o == nil || o.owner != l || o != l.latest {
		return nil, contract.ErrGraphSchemaChanged
	}
	p, e := graphschema.Prepare(o.snapshot, draft)
	if e != nil {
		return nil, e
	}
	if e = l.check(); e != nil {
		return nil, e
	}
	params, e := graphschema.Parameters(p.Request)
	if e != nil {
		return nil, e
	}
	native, e := l.client.Prepare(Request{Query: p.Request.Query, Database: p.Request.Database, Parameters: params, MaxRows: 1, MaxBytes: 1 << 20})
	if e != nil {
		return nil, e
	}
	return &schemaPreparation{preview: p.Clone(), owner: l, before: o, native: native}, nil
}
func (l *schemaPreparationLease) Close() error {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		l.closed = true
		l.cancel()
		l.mu.Unlock()
		l.ops.Lock()
		if l.client != nil {
			l.closeErr = l.client.Close()
			l.client = nil
		}
		if l.verificationClient != nil {
			l.closeErr = errors.Join(l.closeErr, l.verificationClient.Close())
			l.verificationClient = nil
		}
		l.latest = nil
		l.operation = nil
		l.evidenceMu.Lock()
		l.last = contract.GraphSchemaManagementResult{}
		l.evidenceMu.Unlock()
		l.ops.Unlock()
		l.s.mu.Lock()
		delete(l.s.schemaLeases, l)
		l.s.mu.Unlock()
	})
	return l.closeErr
}

var _ contract.GraphSchemaPreparationProvider = (*Session)(nil)
var _ contract.GraphSchemaPreparationLease = (*schemaPreparationLease)(nil)
