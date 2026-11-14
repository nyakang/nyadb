package consul

import (
	"context"
	"crypto/rand"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type sessionDestroyPreparation struct {
	owner       *documentLease
	authority   *sessionDestroyPreparation
	request     contract.KVRequest
	recordID    string
	input       contract.KVHTTPSessionDestroyImpactRequest
	expectation contract.KVHTTPSessionDestroyExpectation
	mu          sync.Mutex
	preview     contract.KVHTTPSessionDestroyImpact
	used        bool
	closed      bool
	cancel      context.CancelFunc
}

func (p *sessionDestroyPreparation) Snapshot() contract.KVRequest { return p.request }
func (p *sessionDestroyPreparation) LocalRecordID() string        { return p.recordID }
func (*sessionDestroyPreparation) Writes() bool                   { return true }
func (p *sessionDestroyPreparation) Impact() contract.KVHTTPSessionDestroyImpact {
	if p == nil || p.authority != p {
		return contract.KVHTTPSessionDestroyImpact{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.preview.Clone()
}

// ops serializes reservation and release. Reserve before even the first read:
// two bounded observations plus one original incomplete response must fit.
// Each active native request also retains its existing pending reservation.
func (l *documentLease) reserveDestroyPlan(bytes int64) error {
	receiptBudgetMu.Lock()
	defer receiptBudgetMu.Unlock()
	if l.cacheBytes+bytes+pendingReceiptBudget > documentReceiptBudget || l.s.retained+l.s.reserved+bytes+pendingReceiptBudget > l.s.budget {
		return ErrBounds
	}
	l.planReserved = bytes
	l.s.reserved += bytes
	return nil
}

func (l *documentLease) releaseDestroyPlan() {
	if p := l.destroyPlan; p != nil {
		p.mu.Lock()
		p.closed = true
		p.preview = contract.KVHTTPSessionDestroyImpact{}
		p.expectation = contract.KVHTTPSessionDestroyExpectation{}
		p.mu.Unlock()
		l.destroyPlan = nil
	}
	receiptBudgetMu.Lock()
	l.s.reserved -= l.planReserved
	l.planReserved = 0
	receiptBudgetMu.Unlock()
}

func (l *documentLease) PrepareHTTPSessionDestroy(parent context.Context, input contract.KVHTTPSessionDestroyImpactRequest) (contract.KVHTTPSessionDestroyPreparation, error) {
	if !validSessionKeysRequest(contract.KVHTTPSessionKeysRequest{ID: input.ID, MaxRows: input.MaxRows, MaxBytes: input.MaxBytes}) {
		return nil, ErrInvalid
	}
	if !l.ops.TryLock() {
		return nil, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	ctx, done, err := l.request(parent)
	if err != nil {
		return nil, err
	}
	defer done()
	if l.writeUnknown.Load() {
		return nil, contract.ErrKVWriteUnknown
	}
	if l.destroyPlan != nil || l.aclMutationPlan != nil {
		return nil, contract.ErrKVBusy
	}
	request, err := DocumentRequest(Request{Method: "PUT", Path: "/v1/session/destroy/" + input.ID})
	if err != nil {
		return nil, err
	}
	if err = l.reserveDestroyPlan(2*input.MaxBytes + pendingReceiptBudget + (8 << 20)); err != nil {
		return nil, err
	}
	p := &sessionDestroyPreparation{owner: l, request: request, input: input, recordID: rand.Text()}
	p.authority = p
	l.destroyPlan = p
	preview, err := l.observeHTTPSessionDestroyImpact(ctx, input, l.executeLockedPreparation)
	if err != nil {
		l.releaseDestroyPlan()
		l.storeOwned(contract.KVResult{Request: request, HTTPSessionDestroy: &contract.KVHTTPSessionDestroyEvidence{LocalRecordID: p.recordID, Preview: preview}})
		return nil, err
	}
	expectation, err := SessionDestroyExpectation(preview)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = l.CheckKVLease()
	}
	if err != nil {
		l.releaseDestroyPlan()
		l.storeOwned(contract.KVResult{Request: request, HTTPSessionDestroy: &contract.KVHTTPSessionDestroyEvidence{LocalRecordID: p.recordID, Preview: preview}})
		return nil, err
	}
	p.preview, p.expectation = preview.Clone(), expectation.Clone()
	return p, nil
}

func (l *documentLease) executeSessionDestroy(parent context.Context, p *sessionDestroyPreparation) (result contract.KVResult, err error) {
	if p == nil || p.owner != l || p.authority != p {
		return result, contract.ErrKVPlanUsed
	}
	result.Request = p.request
	if !l.ops.TryLock() {
		return result, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	p.mu.Lock()
	if p.used || p.closed || l.destroyPlan != p {
		p.mu.Unlock()
		return result, contract.ErrKVPlanUsed
	}
	p.used = true
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel
	evidence := &contract.KVHTTPSessionDestroyEvidence{LocalRecordID: p.recordID, Preview: p.preview.Clone()}
	expectation := p.expectation.Clone()
	p.mu.Unlock()
	defer func() {
		cancel()
		p.mu.Lock()
		p.cancel = nil
		p.mu.Unlock()
		// Evidence replaces the consumed authority under the same original ops
		// lock. Close/recovery cannot observe an unaccounted intermediate cache.
		l.releaseDestroyPlan()
		result.HTTPSessionDestroy = evidence
		l.storeOwned(result)
	}()
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = l.CheckKVLease(); err != nil {
		return result, err
	}
	if l.writeUnknown.Load() {
		return result, contract.ErrKVWriteUnknown
	}
	evidence.Recheck, err = l.observeHTTPSessionDestroyImpact(ctx, p.input, l.executeLockedPreparation)
	if err != nil {
		return result, err
	}
	if err = CompareSessionDestroyExpectation(expectation, evidence.Recheck); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	evidence.Revalidated = true
	prepared, err := l.Prepare(p.request)
	if err != nil {
		return result, err
	}
	// This is one native PUT. Consul provides no atomic preflight/destroy CAS;
	// the exact true/false/unknown response remains the only write evidence.
	return l.executeLockedPreparation(ctx, prepared)
}

func (p *sessionDestroyPreparation) Close() error {
	if p == nil || p.authority != p {
		return contract.ErrKVPlanUsed
	}
	p.mu.Lock()
	p.closed = true
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.Unlock()
	// Cancellation is only a request; wait for the original executor to settle.
	p.owner.ops.Lock()
	defer p.owner.ops.Unlock()
	if p.owner.destroyPlan == p {
		p.owner.releaseDestroyPlan()
	}
	return nil
}

func destroyImpactCost(o contract.KVHTTPSessionDestroyImpact) int64 {
	n := resultCost(&o.Session) + resultCost(&o.Keys.Root)
	n += int64(256 + len(o.Request.ID) + len(o.Keys.Matches)*8)
	for _, scope := range []contract.KVScope{o.Scope, o.Keys.Scope} {
		h := scope.HTTP
		n += int64(256 + len(scope.Prefix) + len(scope.Version) + len(scope.Namespace.Catalog) + len(scope.Namespace.Schema) + len(h.Protocol) + len(h.Endpoint) + len(h.Datacenter) + len(h.Consistency) + len(h.Auth) + len(h.Configuration) + len(h.AgentID) + len(h.AgentName) + len(h.AgentDatacenter))
	}
	return n
}

var _ contract.KVHTTPSessionDestroyPlanning = (*documentLease)(nil)
var _ contract.KVHTTPSessionDestroyPreparation = (*sessionDestroyPreparation)(nil)
