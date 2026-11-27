package databend

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// Every confirmation belongs to one original document and native session.
// Reads cannot clear an unverified write or transfer another document's proof.
type schemaDocument struct {
	s               *Session
	id              string
	mu              sync.Mutex
	target          contract.SchemaDesignTarget
	ctx             context.Context
	cancel          context.CancelFunc
	closed, blocked bool
	active          sync.WaitGroup
	done            chan struct{}
	serial          chan struct{}
	key             [32]byte
	sequence        uint64
	original        *contract.SchemaDesignOutcome
}

func (s *Session) checkSchemaTarget(t contract.SchemaDesignTarget) error {
	if t.Generation == 0 || t.Generation != s.Generation() {
		return contract.ErrRevoked
	}
	if err := s.checkObject(t.Object); err != nil {
		return err
	}
	if t.Object.Kind != "table" || t.Object.Schema == "system" || t.Object.Schema == "information_schema" {
		return contract.ErrUnsupported
	}
	return nil
}

func (s *Session) ReserveSchemaDesignDocument(r contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
	if !r.Valid() {
		return nil, connections.ErrInvalid
	}
	if err := s.checkSchemaTarget(r.Target); err != nil {
		return nil, err
	}
	d := &schemaDocument{s: s, id: r.DocumentID, target: r.Target, done: make(chan struct{}), serial: make(chan struct{}, 1)}
	if _, err := rand.Read(d.key[:]); err != nil {
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
	if len(s.schemaDocuments) >= 32 {
		return nil, contract.ErrSQLSessionLimit
	}
	d.ctx, d.cancel = context.WithCancel(s.ctx)
	if s.schemaDocuments == nil {
		s.schemaDocuments = make(map[string]*schemaDocument)
	}
	s.schemaDocuments[d.id] = d
	return d, nil
}

func (d *schemaDocument) admission(ctx context.Context, target contract.SchemaDesignTarget) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, connections.ErrInvalid
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, nil, contract.ErrRevoked
	}
	if target != d.target {
		d.mu.Unlock()
		return nil, nil, connections.ErrInvalid
	}
	d.active.Add(1)
	d.mu.Unlock()
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(d.ctx, cancel)
	done := func() { stop(); cancel(); d.active.Done() }
	select {
	case <-child.Done():
		done()
		return nil, nil, child.Err()
	case d.serial <- struct{}{}:
	}
	d.mu.Lock()
	closed, changed := d.closed, d.target != target
	d.mu.Unlock()
	if closed || changed || child.Err() != nil {
		<-d.serial
		done()
		return nil, nil, contract.ErrRevoked
	}
	return child, func() { <-d.serial; done() }, nil
}

func (d *schemaDocument) writable(t contract.SchemaDesignTarget) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.closed && !d.blocked && t == d.target && d.s.checkSchemaTarget(t) == nil
}
func (d *schemaDocument) Operations() contract.SchemaDesignOperations {
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return contract.SchemaDesignOperations{}
	}
	return contract.SchemaDesignOperations{Snapshot: d.snapshot, CanRename: d.writable, Rename: d.rename, CanDrop: d.writable, Drop: d.drop, CanCopy: d.writable, Copy: d.copy, CanClear: d.writable, Clear: d.clear}
}
func (s *Session) SchemaDesignDocumentFeatures() contract.SchemaDesignDocumentFeatures {
	can := func(t contract.SchemaDesignTarget) bool { return s.checkSchemaTarget(t) == nil }
	return contract.SchemaDesignDocumentFeatures{CanRename: can, CanDrop: can, CanCopy: can, CanClear: can}
}
func (s *Session) SchemaDesign() contract.SchemaDesignOperations {
	return contract.SchemaDesignOperations{Snapshot: func(ctx context.Context, t contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
		if ctx == nil {
			return contract.SchemaDesignSnapshot{}, connections.ErrInvalid
		}
		var key [32]byte
		if _, err := rand.Read(key[:]); err != nil {
			return contract.SchemaDesignSnapshot{}, err
		}
		var out contract.SchemaDesignSnapshot
		if err := s.checkSchemaTarget(t); err != nil {
			return out, err
		}
		err := s.withMetadata(ctx, func(child context.Context, original *ManagedSession) error {
			var err error
			out, err = s.readSchemaSnapshot(child, original, t, key[:])
			return err
		})
		return out, err
	}}
}

func sealSchema(snapshot contract.SchemaDesignSnapshot, key []byte) string {
	snapshot = snapshot.Clone()
	snapshot.Schema.Revision = ""
	raw, _ := json.Marshal(snapshot)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Session) readSchemaSnapshot(ctx context.Context, original *ManagedSession, t contract.SchemaDesignTarget, key []byte) (contract.SchemaDesignSnapshot, error) {
	m, err := s.readTableMetadata(ctx, original, t.Object)
	if err != nil {
		return contract.SchemaDesignSnapshot{}, err
	}
	if _, err = schemaddl.DatabendTableBody(m.Definition, t.Object.Name); err != nil {
		return contract.SchemaDesignSnapshot{}, err
	}
	out := contract.SchemaDesignSnapshot{Target: t, Schema: schemaddl.Snapshot{Engine: "databend", Exists: true, DDL: m.Definition, Table: schemaddl.Table{Name: t.Object.Name}, Databend: &schemaddl.DatabendTableIdentity{Database: t.Object.Schema, TableID: m.Incarnation}}}
	for _, c := range m.Columns {
		out.Schema.Databend.Columns = append(out.Schema.Databend.Columns, schemaddl.DatabendColumn{Name: c.Name, Type: c.Type, Generated: c.Generated})
	}
	out.Schema.Revision = sealSchema(out, key)
	if err := ctx.Err(); err != nil {
		return contract.SchemaDesignSnapshot{}, err
	}
	if err := s.checkSchemaTarget(t); err != nil {
		return contract.SchemaDesignSnapshot{}, err
	}
	return out, nil
}

func (d *schemaDocument) snapshot(ctx context.Context, t contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
	child, done, err := d.admission(ctx, t)
	if err != nil {
		return contract.SchemaDesignSnapshot{}, err
	}
	defer done()
	var out contract.SchemaDesignSnapshot
	err = d.s.withMetadata(child, func(ctx context.Context, original *ManagedSession) error {
		out, err = d.s.readSchemaSnapshot(ctx, original, t, d.key[:])
		return err
	})
	if child.Err() != nil {
		return contract.SchemaDesignSnapshot{}, child.Err()
	}
	return out, err
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
	clear(d.key[:])
	d.original = nil
	d.mu.Unlock()
	d.s.mu.Lock()
	delete(d.s.schemaDocuments, d.id)
	d.s.mu.Unlock()
	close(d.done)
	return nil
}

var _ contract.SchemaDesignProvider = (*Session)(nil)
var _ contract.SchemaDesignDocumentsProvider = (*Session)(nil)
var _ contract.SchemaDesignDocumentFeaturesProvider = (*Session)(nil)
