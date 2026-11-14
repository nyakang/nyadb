package consul

import (
	"context"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type httpACLMutationPlan struct {
	owner        *documentLease
	authority    *httpACLMutationPlan
	prepared     *httpACLMutationPreparation
	record       string
	mu           sync.Mutex
	preview      contract.KVHTTPACLImpact
	used, closed bool
	cancel       context.CancelFunc
}

func (p *httpACLMutationPlan) LocalRecordID() string { return p.record }
func (p *httpACLMutationPlan) Request() contract.KVHTTPACLMutationRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.prepared == nil {
		return contract.KVHTTPACLMutationRequest{}
	}
	return p.prepared.Request()
}
func (p *httpACLMutationPlan) Impact() contract.KVHTTPACLImpact {
	if p == nil || p.authority != p {
		return contract.KVHTTPACLImpact{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.preview.Clone()
}

func (l *documentLease) releaseACLMutationPlan() {
	if p := l.aclMutationPlan; p != nil {
		p.mu.Lock()
		p.closed = true
		p.preview = contract.KVHTTPACLImpact{}
		p.prepared = nil
		p.mu.Unlock()
		l.aclMutationPlan = nil
		receiptBudgetMu.Lock()
		l.s.reserved -= l.planReserved
		l.planReserved = 0
		receiptBudgetMu.Unlock()
	}
}

func (l *documentLease) PrepareHTTPACLMutationPlan(parent context.Context, input contract.KVHTTPACLMutationRequest, limits contract.KVHTTPACLImpactLimits) (contract.KVHTTPACLMutationPlan, contract.KVHTTPACLImpact, error) {
	request := contract.KVHTTPACLImpactRequest{Resource: input.Resource, Identity: input.Identity, MaxRows: limits.MaxRows, MaxReads: limits.MaxReads, MaxBytes: limits.MaxBytes}
	out := contract.KVHTTPACLImpact{Scope: l.Scope(), Request: request}
	if input.Operation != "create" && input.Operation != "update" && input.Operation != "delete" && input.Operation != "clone" {
		return nil, out, ErrUnsupported
	}
	if input.Operation == "create" && !validACLCreationImpact(input, request) || input.Operation != "create" && !validACLImpactRequest(request) {
		return nil, out, ErrInvalid
	}
	prepared, err := l.PrepareHTTPACLMutation(input)
	if err != nil {
		return nil, out, err
	}
	if !l.ops.TryLock() {
		return nil, out, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	ctx, done, err := l.request(parent)
	if err != nil {
		return nil, out, err
	}
	defer done()
	if l.writeUnknown.Load() {
		return nil, out, contract.ErrKVWriteUnknown
	}
	if l.destroyPlan != nil || l.aclMutationPlan != nil || l.planReserved != 0 {
		return nil, out, contract.ErrKVBusy
	}
	if err := l.reserveDestroyPlan(2*limits.MaxBytes + pendingReceiptBudget + (8 << 20)); err != nil {
		return nil, out, err
	}
	p := &httpACLMutationPlan{owner: l, prepared: prepared.(*httpACLMutationPreparation), record: prepared.LocalRecordID()}
	p.authority = p
	l.aclMutationPlan = p
	if input.Operation == "create" {
		out, err = l.observeACLCreationImpact(ctx, prepared.Request(), request)
	} else {
		out, err = l.observeHTTPACLImpact(ctx, request)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = l.CheckKVLease()
	}
	if err != nil {
		out.Complete = false
		l.releaseACLMutationPlan()
		return nil, out, err
	}
	p.preview = out.Clone()
	return p, out, nil
}

func (l *documentLease) executeACLMutationPlan(parent context.Context, p *httpACLMutationPlan) (out contract.KVHTTPACLMutationResult, err error) {
	if p == nil || p.owner != l || p.authority != p {
		return out, contract.ErrKVPlanUsed
	}
	out = contract.KVHTTPACLMutationResult{LocalRecordID: p.LocalRecordID(), Scope: l.Scope(), Request: p.Request()}
	if !l.ops.TryLock() {
		return out, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	p.mu.Lock()
	if p.used || p.closed || l.aclMutationPlan != p {
		p.mu.Unlock()
		return out, contract.ErrKVPlanUsed
	}
	p.used = true
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel
	evidence := &contract.KVHTTPACLMutationImpactEvidence{Preview: p.preview.Clone()}
	p.mu.Unlock()
	defer func() {
		cancel()
		p.mu.Lock()
		p.cancel = nil
		p.mu.Unlock()
		l.releaseACLMutationPlan()
		out.Impact = evidence
		if out.Receipt.Attempted {
			l.storeACLMutation(out)
		}
	}()
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if err = l.CheckKVLease(); err != nil {
		return out, err
	}
	if l.writeUnknown.Load() {
		return out, contract.ErrKVWriteUnknown
	}
	if evidence.Preview.Creation != nil {
		evidence.Recheck, err = l.observeACLCreationImpact(ctx, p.prepared.Request(), evidence.Preview.Request)
	} else {
		evidence.Recheck, err = l.observeHTTPACLImpact(ctx, evidence.Preview.Request)
	}
	if err != nil {
		return out, err
	}
	if err = CompareACLImpact(evidence.Preview, evidence.Recheck); err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if err = l.CheckKVLease(); err != nil {
		return out, err
	}
	evidence.Revalidated = true
	// The same executor sends one original request. Another client can change
	// native ACL state after the final read; Consul exposes no mutation CAS.
	result, err := l.executeLocked(ctx, p.prepared.plan)
	out.Receipt = result
	if result.Attempted {
		out = l.LastHTTPACLMutation()
	}
	return out, err
}

func (p *httpACLMutationPlan) Close() error {
	if p == nil || p.authority != p {
		return contract.ErrKVPlanUsed
	}
	p.mu.Lock()
	p.closed = true
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.Unlock()
	p.owner.ops.Lock()
	defer p.owner.ops.Unlock()
	if p.owner.aclMutationPlan == p {
		p.owner.releaseACLMutationPlan()
	}
	return nil
}

var _ contract.KVHTTPACLMutationPlanning = (*documentLease)(nil)
var _ contract.KVHTTPACLMutationPlan = (*httpACLMutationPlan)(nil)
