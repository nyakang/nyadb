package neo4j

import (
	"context"
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphschema"
)

type schemaOperation struct{ resolved bool }

func (s *Session) SupportsGraphSchemaManagement(o contract.ObjectRef) bool {
	return s.SupportsGraphSchemaPreparation(o)
}
func (s *Session) ReserveGraphSchemaManagement(o contract.ObjectRef, id uint64) (contract.GraphSchemaManagementLease, error) {
	l, e := s.ReserveGraphSchemaPreparation(o, id)
	if e != nil {
		return nil, e
	}
	return l.(*schemaPreparationLease), nil
}
func readSchemaCatalog(ctx context.Context, c *Client, o contract.ObjectRef) (contract.GraphSchemaCatalog, error) {
	if _, e := databaseInfo(ctx, c, o.Catalog); e != nil {
		return contract.GraphSchemaCatalog{}, e
	}
	out := contract.GraphSchemaCatalog{Object: o}
	var e error
	out.Indexes, e = metadataQuery(ctx, c, o.Catalog, "SHOW INDEXES YIELD * RETURN *")
	if e == nil {
		out.Constraints, e = metadataQuery(ctx, c, o.Catalog, "SHOW CONSTRAINTS YIELD * RETURN *")
	}
	if e == nil {
		_, e = graphschema.Definitions(out)
	}
	if e == nil {
		_, e = databaseInfo(ctx, c, o.Catalog)
	}
	if e != nil {
		return contract.GraphSchemaCatalog{}, e
	}
	return out, nil
}
func (l *schemaPreparationLease) retained() contract.GraphSchemaManagementResult {
	l.evidenceMu.Lock()
	defer l.evidenceMu.Unlock()
	r := l.last.Clone()
	if r.State == "" {
		r.State = "not_submitted"
	}
	return r
}
func (l *schemaPreparationLease) retain(r contract.GraphSchemaManagementResult) contract.GraphSchemaManagementResult {
	l.evidenceMu.Lock()
	l.last = r.Clone()
	l.evidenceMu.Unlock()
	return r.Clone()
}
func (l *schemaPreparationLease) Submit(parent context.Context, prepared contract.GraphSchemaPreparation, confirmation contract.GraphSchemaConfirmation) (contract.GraphSchemaManagementResult, error) {
	if !l.ops.TryLock() {
		return l.retained(), ErrBusy
	}
	defer l.ops.Unlock()
	if e := l.check(); e != nil {
		return l.retained(), e
	}
	if l.operation != nil && !l.operation.resolved {
		return l.retained(), contract.ErrGraphSchemaBusy
	}
	p, ok := prepared.(*schemaPreparation)
	if !ok || p == nil || p.owner != l || p.before != l.latest || p.native.owner != l.client {
		return l.retained(), contract.ErrGraphSchemaChanged
	}
	if p.preview.NonAtomicDrop && (confirmation.Name != p.preview.Draft.Name || !confirmation.AcceptNonAtomicDrop) {
		return l.retained(), contract.ErrGraphSchemaConfirmation
	}
	if !p.used.CompareAndSwap(false, true) {
		return l.retained(), ErrUsed
	}
	l.latest = nil
	r := contract.GraphSchemaManagementResult{State: "not_submitted", Preview: p.preview.Clone(), Before: p.before.snapshot.Clone()}
	ctx, done, e := l.request(parent)
	if e != nil {
		return l.retain(r), e
	}
	defer done()
	r.Preflight, e = readSchemaCatalog(ctx, l.client, l.object)
	if e != nil {
		return l.retain(r), l.settle(e)
	}
	current, e := graphschema.Prepare(r.Preflight, p.preview.Draft)
	if e != nil || !graphschema.PreflightMatches(p.preview, current) {
		return l.retain(r), errors.Join(contract.ErrGraphSchemaChanged, e)
	}
	if e := l.check(); e != nil {
		return l.retain(r), e
	}
	// The original client and immutable native plan execute exactly one Run.
	// Native metadata preflight is not an atomic compare-and-drop primitive.
	r.Receipt, e = l.client.Execute(ctx, p.native)
	if r.Receipt.Attempted {
		l.operation = &schemaOperation{}
		if r.Receipt.Completed {
			r.State = "completed"
			r.ReceiptValid = schemaReceiptValid(r)
			if !r.ReceiptValid {
				r.State = "receipt_incomplete"
				e = errors.Join(e, contract.ErrGraphSchemaReceipt)
			}
		} else {
			r.State = "uncertain"
		}
	}
	if r.Receipt.Uncertain || !l.client.Alive() || r.Receipt.Completed && r.Receipt.Summary.Database != l.object.Catalog {
		l.mu.Lock()
		l.lost = true
		l.mu.Unlock()
		e = errors.Join(e, l.client.Close())
	}
	if !l.s.Alive() {
		e = errors.Join(e, contract.ErrRevoked)
	}
	return l.retain(r), e
}
func schemaReceiptValid(r contract.GraphSchemaManagementResult) bool {
	if !r.Receipt.Attempted || !r.Receipt.Completed || r.Receipt.Uncertain || r.Receipt.Truncated || r.Receipt.Summary.Database != r.Preview.Request.Database || r.Receipt.FailureCode != "" {
		return false
	}
	key := map[string]string{"create_index": "indexes_added", "drop_index": "indexes_removed", "create_constraint": "constraints_added", "drop_constraint": "constraints_removed"}[r.Preview.Draft.Action]
	count, recorded := r.Receipt.Summary.Counters[key]
	return key != "" && recorded && count == 1
}
func (l *schemaPreparationLease) verificationRequest(parent context.Context) (context.Context, func(), error) {
	l.mu.Lock()
	dead := l.closed
	l.mu.Unlock()
	if dead || !l.s.Alive() {
		return nil, nil, contract.ErrRevoked
	}
	ctx, done, e := l.s.request(parent)
	if e != nil {
		return nil, nil, e
	}
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	return child, func() { stop(); cancel(); done() }, nil
}
func (l *schemaPreparationLease) Verify(parent context.Context) (contract.GraphSchemaManagementResult, error) {
	if !l.ops.TryLock() {
		return l.retained(), ErrBusy
	}
	defer l.ops.Unlock()
	r := l.retained()
	ctx, done, e := l.verificationRequest(parent)
	if e != nil {
		return r, e
	}
	defer done()
	if l.operation == nil {
		return r, contract.ErrUnsupported
	}
	// Explicit verification may open a separate original-session read client
	// after the write client retired. It never prepares or submits another write.
	c := l.client
	if c == nil || !c.Alive() {
		if l.verificationClient != nil && !l.verificationClient.Alive() {
			_ = l.verificationClient.Close()
			l.verificationClient = nil
		}
		if l.verificationClient == nil {
			l.verificationClient, e = l.s.open(ctx)
			if e != nil {
				return r, e
			}
		}
		c = l.verificationClient
	}
	// A late original verification opener must observe synchronous lease close
	// even before its request context receives AfterFunc cancellation.
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed || !l.s.Alive() {
		return r, contract.ErrRevoked
	}
	if e := ctx.Err(); e != nil {
		return r, e
	}
	observed, e := readSchemaCatalog(ctx, c, l.object)
	if e != nil {
		if !c.Alive() || errors.Is(e, ErrProtocol) || errors.Is(e, graphschema.ErrIncomplete) {
			e = errors.Join(e, c.Close())
		}
		return r, e
	}
	l.mu.Lock()
	dead := l.closed
	l.mu.Unlock()
	if dead || !l.s.Alive() {
		return r, contract.ErrRevoked
	}
	if e = ctx.Err(); e != nil {
		return r, e
	}
	matched, e := graphschema.DesiredMatches(r.Preview, observed)
	if e != nil {
		return r, e
	}
	r.Verification, r.HasVerification, r.Matched = observed.Clone(), true, matched
	if r.ReceiptValid {
		if matched {
			r.State = "verified"
			l.operation.resolved = true
		} else {
			r.State = "diverged"
			l.operation.resolved = false
		}
	}
	return l.retain(r), nil
}

var _ contract.GraphSchemaManagementProvider = (*Session)(nil)
var _ contract.GraphSchemaManagementLease = (*schemaPreparationLease)(nil)
