package consul

import (
	"context"
	"crypto/rand"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type httpManagementPreparation struct {
	owner *documentLease
	input contract.KVHTTPManagementRequest
	scope contract.KVScope
	plan  *documentPreparation
}

func (p *httpManagementPreparation) Request() contract.KVHTTPManagementRequest {
	return p.input.Clone()
}
func (p *httpManagementPreparation) Scope() contract.KVScope { return p.scope.Clone() }
func (p *httpManagementPreparation) LocalRecordID() string   { return p.plan.aclRecordID }
func (p *httpManagementPreparation) Writes() bool            { return p.plan.write }
func (p *httpManagementPreparation) HTTP() contract.KVHTTPRequest {
	r := p.plan.native.Clone()
	return contract.KVHTTPRequest{Method: r.Method, Path: r.Path, Query: r.Query, Body: r.Body}
}

func (l *documentLease) HTTPManagementOperations() []contract.KVHTTPManagementOperation {
	var out []contract.KVHTTPManagementOperation
	for _, op := range managementOperations {
		v := op.KVHTTPManagementOperation
		v.Options = append([]string(nil), v.Options...)
		out = append(out, v)
	}
	return out
}
func (l *documentLease) PrepareHTTPManagement(input contract.KVHTTPManagementRequest) (contract.KVHTTPManagementPreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	r, err := ManagementRequest(input)
	if err != nil {
		return nil, err
	}
	if input.Operation == "namespace-delete" || input.Operation == "partition-delete" {
		return nil, ErrUnsupported
	}
	op, _ := managementSpec(input.Operation)
	if (op.Name == "intention-create" || op.Name == "intention-update") && l.s.cfg.Partition != "" && l.s.cfg.Partition != "default" {
		return nil, ErrUnsupported
	}
	if op.Write && (l.s.cfg.ReadOnly || !managementGuard(l.s.cfg, op.Guard)) {
		return nil, ErrUnsupported
	}
	if op.Write && l.writeUnknown.Load() {
		return nil, contract.ErrKVWriteUnknown
	}
	envelope, err := documentRequestEnvelope(r)
	if err != nil {
		return nil, err
	}
	p := &documentPreparation{owner: l, native: r, request: envelope, write: op.Write, privateManagement: true, managementRequest: input.Clone(), aclRecordID: rand.Text()}
	return &httpManagementPreparation{owner: l, input: input.Clone(), scope: l.Scope(), plan: p}, nil
}
func (l *documentLease) ExecuteHTTPManagement(ctx context.Context, prepared contract.KVHTTPManagementPreparation) (out contract.KVHTTPManagementResult, err error) {
	p, ok := prepared.(*httpManagementPreparation)
	deletion, d := prepared.(*httpEnterpriseDeletionPreparation)
	if d {
		p = deletion.httpManagementPreparation
		ok = true
	}
	if !ok || p == nil || p.owner != l {
		return out, contract.ErrKVPlanUsed
	}
	out = contract.KVHTTPManagementResult{Scope: p.Scope(), Request: p.Request(), LocalRecordID: p.LocalRecordID()}
	if !l.ops.TryLock() {
		return out, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	if p.Writes() && (l.destroyPlan != nil || l.aclMutationPlan != nil || l.enterprisePlan != nil && l.enterprisePlan != deletion) {
		return out, contract.ErrKVBusy
	}
	if d {
		defer deletion.closeLocked()
		deletion.mu.Lock()
		closed := deletion.closed
		deletion.mu.Unlock()
		if closed || l.enterprisePlan != deletion {
			return out, contract.ErrKVPlanUsed
		}
		impact := deletion.Impact()
		out.EnterpriseImpact = &impact
		requestCtx, done, e := l.request(ctx)
		if e != nil {
			return out, e
		}
		verification, e := l.observeEnterpriseImpact(requestCtx, p.Request())
		done()
		out.EnterpriseVerification = &verification
		if e != nil || !sameEnterpriseImpact(impact, verification) {
			l.storeManagement(out)
			return out, errors.Join(e, ErrConflict)
		}
	}
	originalImpact, originalVerification := out.EnterpriseImpact, out.EnterpriseVerification
	r, err := l.executeLocked(ctx, p.plan)
	out.Receipt = r
	if r.Attempted {
		out = l.LastHTTPManagement()
		out.EnterpriseImpact, out.EnterpriseVerification = originalImpact, originalVerification
		if d {
			l.storeManagement(out)
		}
	}
	return out, err
}
func (l *documentLease) LastHTTPManagement() contract.KVHTTPManagementResult {
	l.receiptMu.Lock()
	defer l.receiptMu.Unlock()
	if l.managementLast == nil {
		return contract.KVHTTPManagementResult{}
	}
	return l.managementLast.Clone()
}
func (l *documentLease) storeManagement(result contract.KVHTTPManagementResult) {
	l.receiptMu.Lock()
	defer l.receiptMu.Unlock()
	v := result.Clone()
	unknown := l.managementUnknown
	if v.Receipt.WriteOutcomeUnknown {
		unknown = &v
	}
	cost := l.restoreCacheCost() + managementResultCost(&v) + resultCost(l.last) + aclReadResultCost(l.aclLast) + aclMutationResultCost(l.aclMutationLast)
	if l.unknown != nil && l.unknown != l.last {
		cost += resultCost(l.unknown)
	}
	if unknown != nil && unknown != &v {
		cost += managementResultCost(unknown)
	}
	receiptBudgetMu.Lock()
	if l.reserved {
		l.s.reserved -= pendingReceiptBudget
		l.reserved = false
	}
	l.s.retained += cost - l.cacheBytes
	l.cacheBytes = cost
	receiptBudgetMu.Unlock()
	l.managementLast, l.managementUnknown = &v, unknown
}
func managementResultCost(r *contract.KVHTTPManagementResult) int64 {
	if r == nil {
		return 0
	}
	cost := enterpriseImpactCost(r.EnterpriseImpact) + enterpriseImpactCost(r.EnterpriseVerification) + resultCost(&r.Receipt) + int64(512+len(r.Request.Body)+len(r.Request.Operation)+len(r.Request.Kind)+len(r.Request.Identity))
	for k, vs := range r.Request.Options {
		cost += int64(len(k) + 64)
		for _, v := range vs {
			cost += int64(len(v) + 24)
		}
	}
	return cost
}
func (l *documentLease) managementCacheCost() int64 {
	cost := l.restoreCacheCost() + managementResultCost(l.managementLast)
	if l.managementUnknown != l.managementLast {
		cost += managementResultCost(l.managementUnknown)
	}
	return cost
}

var _ contract.KVHTTPManaging = (*documentLease)(nil)

func enterpriseImpactCost(i *contract.KVHTTPEnterpriseImpact) int64 {
	if i == nil {
		return 0
	}
	cost := int64(1024)
	for _, o := range i.Observations {
		cost += resultCost(&o.HTTP)
	}
	return cost
}
