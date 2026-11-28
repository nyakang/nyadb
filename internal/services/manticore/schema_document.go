package manticore

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type schemaDocument struct {
	s               *Session
	id              string
	target          contract.SchemaDesignTarget
	primary         *queryLease
	ctx             context.Context
	cancel          context.CancelFunc
	mu              sync.Mutex
	active          sync.WaitGroup
	serial          chan struct{}
	done            chan struct{}
	closed, blocked bool
	key             [32]byte
	sequence        uint64
	original        *contract.SchemaDesignOutcome
}

func (s *Session) checkSchemaTarget(t contract.SchemaDesignTarget) error {
	if t.Generation != s.Generation() {
		return contract.ErrRevoked
	}
	if err := s.checkNamespace(t.Object.Namespace()); err != nil {
		return err
	}
	if t.Object.Kind != "table" || schemaddl.ManticoreIdentifier(t.Object.Name) != nil {
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
	d := &schemaDocument{s: s, id: r.DocumentID, target: r.Target, serial: make(chan struct{}, 1), done: make(chan struct{})}
	if _, e := rand.Read(d.key[:]); e != nil {
		return nil, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.aliveLocked() {
		return nil, contract.ErrRevoked
	}
	if s.schemaDocuments[r.DocumentID] != nil {
		return nil, connections.ErrInvalid
	}
	if len(s.schemaDocuments) >= 32 || s.nextOwner == ^uint64(0) {
		return nil, contract.ErrSQLSessionLimit
	}
	if s.schemaDocuments == nil {
		s.schemaDocuments = map[string]*schemaDocument{}
	}
	s.nextOwner++
	d.ctx, d.cancel = context.WithCancel(s.ctx)
	d.primary = s.ownerLocked(d.ctx, s.nextOwner, s.dataSlots)
	s.schemaDocuments[d.id] = d
	return d, nil
}
func (d *schemaDocument) admission(ctx context.Context, t contract.SchemaDesignTarget) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, connections.ErrInvalid
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, nil, contract.ErrRevoked
	}
	if t != d.target {
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
	if err := child.Err(); err != nil {
		<-d.serial
		done()
		return nil, nil, err
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
	return contract.SchemaDesignOperations{Snapshot: d.snapshot, Plan: d.plan, CanApply: d.writable, Apply: d.apply, CanDrop: d.writable, Drop: d.drop, CanTruncate: d.writable, Truncate: d.truncate}
}
func (s *Session) SchemaDesignDocumentFeatures() contract.SchemaDesignDocumentFeatures {
	can := func(t contract.SchemaDesignTarget) bool { return s.checkSchemaTarget(t) == nil }
	return contract.SchemaDesignDocumentFeatures{CanApply: can, CanDrop: can, CanTruncate: can}
}
func (s *Session) SchemaDesign() contract.SchemaDesignOperations {
	return contract.SchemaDesignOperations{Snapshot: func(ctx context.Context, t contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
		var key [32]byte
		if _, e := rand.Read(key[:]); e != nil {
			return contract.SchemaDesignSnapshot{}, e
		}
		return s.schemaSnapshot(ctx, t, key[:])
	}}
}
func sealSchema(v contract.SchemaDesignSnapshot, key []byte) string {
	v = v.Clone()
	v.Schema.Revision = ""
	raw, _ := json.Marshal(v)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}
func (s *Session) schemaSnapshot(ctx context.Context, t contract.SchemaDesignTarget, key []byte) (contract.SchemaDesignSnapshot, error) {
	var result contract.SchemaDesignSnapshot
	if ctx == nil {
		return result, connections.ErrInvalid
	}
	if e := s.checkSchemaTarget(t); e != nil {
		return result, e
	}
	// Only a complete native table list establishes absence. SHOW CREATE
	// errors vary between versions and must never authorize a new table.
	if err := s.schemaNameAbsent(ctx, t); err == nil {
		result = contract.SchemaDesignSnapshot{Target: t, Schema: schemaddl.Snapshot{Engine: "manticoresearch", Table: schemaddl.Table{Name: t.Object.Name}}}
		result.Schema.Revision = sealSchema(result, key)
		if err = ctx.Err(); err != nil {
			return contract.SchemaDesignSnapshot{}, err
		}
		if err = s.checkSchemaTarget(t); err != nil {
			return contract.SchemaDesignSnapshot{}, err
		}
		return result, nil
	} else if !errors.Is(err, schemaddl.ErrConflict) {
		return result, err
	}
	object := t.Object
	object.Kind = "TABLE"
	metadata, e := s.tableMetadata(ctx, object, false)
	if e != nil {
		return result, e
	}
	if metadata.Engine != "rt" {
		return result, contract.ErrUnsupported
	}
	table, e := schemaddl.ParseManticoreRT(metadata.Definition, object.Name)
	if e != nil {
		return result, e
	}
	// Native declaration and metadata must describe the same complete source.
	if len(table.Clauses) != len(metadata.Columns) {
		return result, contract.ErrUnsupported
	}
	for i, c := range table.Clauses {
		native := metadata.Columns[i]
		body, _ := schemaddl.ManticoreColumnDefinition(c.SQL)
		typ := body
		switch body {
		case "integer":
			typ = "uint"
		case "string attribute":
			typ = "string"
		case "multi":
			typ = "mva"
		case "multi64":
			typ = "mva64"
		}
		if native.Name != c.Name || native.DataType != typ || (typ == "text" && native.Extra != "indexed stored") {
			return result, contract.ErrUnsupported
		}
	}
	result = contract.SchemaDesignSnapshot{Target: t, Schema: schemaddl.Snapshot{Engine: "manticoresearch", Exists: true, DDL: metadata.Definition, Table: table}}
	result.Schema.Revision = sealSchema(result, key)
	if e = ctx.Err(); e != nil {
		return contract.SchemaDesignSnapshot{}, e
	}
	if e = s.checkSchemaTarget(t); e != nil {
		return contract.SchemaDesignSnapshot{}, e
	}
	return result, nil
}
func (d *schemaDocument) snapshot(ctx context.Context, t contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
	child, done, e := d.admission(ctx, t)
	if e != nil {
		return contract.SchemaDesignSnapshot{}, e
	}
	defer done()
	return d.s.schemaSnapshot(child, t, d.key[:])
}
func (d *schemaDocument) checkProof(original contract.SchemaDesignSnapshot) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return contract.ErrRevoked
	}
	if d.blocked {
		return contract.ErrSchemaCommitUncertain
	}
	if original.Target != d.target || !hmac.Equal([]byte(original.Schema.Revision), []byte(sealSchema(original, d.key[:]))) {
		return schemaddl.ErrConflict
	}
	return nil
}
func (d *schemaDocument) plan(r contract.SchemaDesignRequest) (schemaddl.Plan, error) {
	r = r.Clone()
	if err := d.checkProof(r.Original); err != nil {
		return schemaddl.Plan{}, err
	}
	p, err := schemaddl.PlanManticoreRT(r.Original.Schema, r.Desired, r.AllowColumnDrop)
	if err == nil && r.Original.Schema.Exists {
		p.ScopeHint = "每次只执行一条原生 RT 字段增删，不能通过回滚撤销。提交前检查原始定义，不提供并发结构锁；检查后的更改仍可能影响结果。成功回执之后会再次核对实际结构。未知或未核验回执保留草稿并禁止重放。"
	}
	return p, err
}
func (d *schemaDocument) apply(ctx context.Context, r contract.SchemaDesignRequest) (out contract.SchemaDesignOutcome) {
	r = r.Clone()
	out.State = "not_executed"
	child, done, e := d.admission(ctx, r.Original.Target)
	if e != nil {
		out.Error = e
		return
	}
	defer done()
	p, e := d.plan(r)
	if e != nil {
		out.Error = e
		return
	}
	desired := r.Desired.Clone()
	receipt := contract.SchemaDesignWriteReceipt{Operation: "apply", Original: r.Original.Clone(), Desired: &desired, Destination: r.Desired.Name, AllowColumnDrop: r.AllowColumnDrop, Statements: append([]string(nil), p.SQL...)}
	return d.executeSchema(child, receipt, func(ctx context.Context) (contract.SchemaDesignSnapshot, error) {
		actual, err := d.s.schemaSnapshot(ctx, r.Original.Target, d.key[:])
		if err == nil {
			err = schemaddl.VerifyManticoreRT(r.Desired, actual.Schema.Table)
		}
		return actual, err
	})
}

// The caller owns serial admission, validates its document proof and rebuilds
// exactly one fixed native statement before invoking this common settlement.
func (d *schemaDocument) executeSchema(child context.Context, receipt contract.SchemaDesignWriteReceipt, verify func(context.Context) (contract.SchemaDesignSnapshot, error)) (out contract.SchemaDesignOutcome) {
	receipt = receipt.Clone()
	out.State = "not_executed"
	if len(receipt.Statements) != 1 {
		out.Error = contract.ErrUnsupported
		return
	}
	current, e := d.s.schemaSnapshot(child, receipt.Original.Target, d.key[:])
	if e != nil {
		out.Error = e
		return
	}
	if !reflect.DeepEqual(current, receipt.Original) {
		out.Error = schemaddl.ErrConflict
		return
	}
	if e = d.primary.Preconnect(child); e != nil {
		out.Error = e
		return
	}
	native, callErr := d.primary.write(child, receipt.Statements[0])
	out.Error = callErr
	if !native.Dispatched {
		return
	}
	// Dispatch consumes the source proof, including on an acknowledged error.
	// Returning to the exact old definition cannot authorize its old request.
	d.mu.Lock()
	d.key = sha256.Sum256(d.key[:])
	d.sequence++
	d.mu.Unlock()
	out.State = "uncertain"
	if native.Acknowledged {
		if native.ErrorCode == 0 {
			out.State = "committed"
			out.Rows = native.AffectedRows
		} else {
			out.State = "not_executed"
		}
	}
	if out.State == "committed" {
		actual, err := verify(child)
		if err == nil && callErr == nil && child.Err() == nil {
			out.Verified = true
			out.Actual = &actual
		}
		out.Error = errors.Join(out.Error, err, child.Err())
	}
	raw, _ := json.Marshal(native)
	d.mu.Lock()
	defer d.mu.Unlock()
	receipt.DocumentID, receipt.Sequence = d.id, d.sequence
	receipt.NativeJSON, receipt.State, receipt.Verified, receipt.Rows, receipt.Actual = string(raw), out.State, out.Verified, out.Rows, out.Actual
	out.Receipt = &receipt
	if out.State == "uncertain" || out.State == "committed" && !out.Verified {
		d.blocked = true
		out.Error = errors.Join(out.Error, contract.ErrSchemaCommitUncertain)
	}
	original := out.Clone()
	d.original = &original
	return
}
func (d *schemaDocument) OriginalOutcome() *contract.SchemaDesignOutcome {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.original == nil {
		return nil
	}
	copy := d.original.Clone()
	return &copy
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
	d.primary.Release()
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
