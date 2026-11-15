package rqlite

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// A structure document owns its original target and its own random proof key.
// Session snapshots and sibling documents cannot authorize its future writes.
type schemaDocument struct {
	s        *Session
	id       string
	target   contract.SchemaDesignTarget
	mu       sync.Mutex
	closed   bool
	ctx      context.Context
	cancel   context.CancelFunc
	active   sync.WaitGroup
	done     chan struct{}
	scope    string
	writes   chan struct{}
	sequence uint64
	original *contract.SchemaDesignOutcome
	blocked  bool
}

func (s *Session) ReserveSchemaDesignDocument(r contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
	if !r.Valid() {
		return nil, connections.ErrInvalid
	}
	if err := s.checkSchemaTarget(r.Target); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, contract.ErrRevoked
	}
	if s.schemaDocuments[r.DocumentID] != nil {
		return nil, connections.ErrInvalid
	}
	var scope [32]byte
	if _, err := rand.Read(scope[:]); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &schemaDocument{s: s, id: r.DocumentID, target: r.Target, ctx: ctx, cancel: cancel, done: make(chan struct{}), writes: make(chan struct{}, 1), scope: hex.EncodeToString(scope[:])}
	if s.schemaDocuments == nil {
		s.schemaDocuments = make(map[string]*schemaDocument)
	}
	s.schemaDocuments[r.DocumentID] = d
	return d, nil
}

func (d *schemaDocument) admission(ctx context.Context, target contract.SchemaDesignTarget) (context.Context, string, func(), error) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, "", nil, contract.ErrRevoked
	}
	if target != d.target {
		d.mu.Unlock()
		return nil, "", nil, connections.ErrInvalid
	}
	d.active.Add(1)
	scope := d.scope
	d.mu.Unlock()
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(d.ctx, cancel)
	done := func() { stop(); cancel(); d.active.Done() }
	if err := child.Err(); err != nil {
		done()
		return nil, "", nil, err
	}
	return child, scope, done, nil
}

func (d *schemaDocument) snapshot(ctx context.Context, target contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
	child, scope, done, err := d.admission(ctx, target)
	if err != nil {
		return contract.SchemaDesignSnapshot{}, err
	}
	defer done()
	out, err := d.s.schemaSnapshotScoped(child, target, scope)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return contract.SchemaDesignSnapshot{}, contract.ErrRevoked
	}
	return out, err
}

func (d *schemaDocument) Operations() contract.SchemaDesignOperations {
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return contract.SchemaDesignOperations{}
	}
	ops := contract.SchemaDesignOperations{Snapshot: d.snapshot}
	// These lease-bound adapter operations are distinct from public workbench
	// capability publication, which still requires its full UI acceptance.
	if !d.s.config.Options.ReadOnly {
		ops.CanApply, ops.Apply = d.writable, d.apply
		ops.CanDrop, ops.Drop = d.writable, d.drop
		ops.CanRename, ops.Rename = d.writable, d.rename
	}
	return ops
}

func (d *schemaDocument) OriginalOutcome() *contract.SchemaDesignOutcome {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.original == nil {
		return nil
	}
	out := d.original.Clone()
	return &out
}

func (d *schemaDocument) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		<-d.done
		return nil
	}
	d.closed = true
	d.cancel()
	d.mu.Unlock()
	d.active.Wait()
	d.mu.Lock()
	d.scope = ""
	d.original = nil
	d.mu.Unlock()
	d.s.mu.Lock()
	delete(d.s.schemaDocuments, d.id)
	d.s.mu.Unlock()
	close(d.done)
	return nil
}

var _ contract.SchemaDesignDocumentsProvider = (*Session)(nil)
var _ contract.SchemaDesignDocumentLease = (*schemaDocument)(nil)
