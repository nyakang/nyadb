package consul

import (
	"bytes"
	"context"
	"crypto/rand"
	"reflect"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type httpACLMutationPreparation struct {
	owner *documentLease
	input contract.KVHTTPACLMutationRequest
	plan  *documentPreparation
}

func (p *httpACLMutationPreparation) LocalRecordID() string { return p.plan.aclRecordID }
func (p *httpACLMutationPreparation) Request() contract.KVHTTPACLMutationRequest {
	return p.input.Clone()
}

func aclNativeMutationRequest(input contract.KVHTTPACLMutationRequest) (Request, error) {
	resource := ACLReadResource(input.Resource)
	if input.Operation != "clone" && input.Description != nil {
		return Request{}, ErrInvalid
	}
	switch input.Operation {
	case "create":
		if input.Identity != "" {
			return Request{}, ErrInvalid
		}
		return ACLCreateRequest(resource, input.Definition)
	case "update":
		return ACLUpdateRequest(resource, input.Identity, input.Definition)
	case "delete":
		if len(input.Definition) != 0 {
			return Request{}, ErrInvalid
		}
		return ACLDeleteRequest(resource, input.Identity)
	case "clone":
		if resource != ACLReadToken || len(input.Definition) != 0 {
			return Request{}, ErrInvalid
		}
		return ACLTokenCloneRequest(input.Identity, input.Description)
	}
	return Request{}, ErrUnsupported
}

func (l *documentLease) PrepareHTTPACLMutation(request contract.KVHTTPACLMutationRequest) (contract.KVHTTPACLMutationPreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	if l.writeUnknown.Load() {
		return nil, contract.ErrKVWriteUnknown
	}
	input := request.Clone()
	native, err := aclNativeMutationRequest(input)
	if err != nil {
		return nil, err
	}
	kind, _, _, err := validateRequest(native)
	if err != nil {
		return nil, err
	}
	scope := l.Scope()
	if aclMutationUsesAgent(kind) && scope.HTTP.Datacenter != "" && (!scope.HTTP.AgentAvailable || scope.HTTP.Datacenter != scope.HTTP.AgentDatacenter) {
		return nil, ErrUnsupported
	}
	envelope, err := documentRequestEnvelope(native)
	if err != nil {
		return nil, err
	}
	plan := &documentPreparation{owner: l, native: native.Clone(), request: envelope, write: true,
		privateACLWrite: true, aclMutationRequest: input.Clone(), aclRecordID: rand.Text()}
	return &httpACLMutationPreparation{owner: l, input: input, plan: plan}, nil
}

func (l *documentLease) ExecuteHTTPACLMutation(ctx context.Context, prepared contract.KVHTTPACLMutationPreparation) (contract.KVHTTPACLMutationResult, error) {
	if p, ok := prepared.(*httpACLMutationPlan); ok {
		return l.executeACLMutationPlan(ctx, p)
	}
	p, ok := prepared.(*httpACLMutationPreparation)
	if !ok || p == nil || p.owner != l {
		return contract.KVHTTPACLMutationResult{}, contract.ErrKVPlanUsed
	}
	out := contract.KVHTTPACLMutationResult{LocalRecordID: p.LocalRecordID(), Scope: l.Scope(), Request: p.input.Clone()}
	if !l.ops.TryLock() {
		return out, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	if l.destroyPlan != nil || l.aclMutationPlan != nil {
		return out, contract.ErrKVBusy
	}
	result, err := l.executeLocked(ctx, p.plan)
	out.Receipt = result
	if result.Attempted {
		out = l.LastHTTPACLMutation()
	}
	return out, err
}

func projectACLMutationResult(scope contract.KVScope, input contract.KVHTTPACLMutationRequest, receipt contract.KVResult) contract.KVHTTPACLMutationResult {
	out := contract.KVHTTPACLMutationResult{Scope: scope.Clone(), Request: input.Clone(), Receipt: receipt}
	h := receipt.HTTP
	if h == nil || !receipt.Completed || receipt.WriteOutcomeUnknown || h.Status != 200 || !h.Captured || !h.EOF || !h.Acknowledged || !h.Succeeded || h.WriteOutcomeUnknown {
		return out
	}
	expected, err := aclNativeMutationRequest(input)
	if err != nil || h.Request.Method != expected.Method || h.Request.Path != expected.Path || !reflect.DeepEqual(h.Request.Query, map[string][]string(expected.Query)) || !bytes.Equal(h.Request.Body, expected.Body) {
		out.ProjectionError = "ACL mutation projection unavailable; original private receipt retained"
		return out
	}
	if input.Operation == "delete" {
		return out // the original native true acknowledgement is not an object
	}
	row, err := decodeACLRow(h.Body, input.Resource)
	if err != nil {
		out.ProjectionError = "ACL mutation projection unavailable; original private receipt retained"
		return out
	}
	read := contract.KVHTTPACLReadRequest{Resource: input.Resource, Lookup: "id", Identity: row.Identity}
	if input.Resource == string(ACLReadAuthMethod) {
		read.Lookup = "name"
	}
	projection, err := decodeACLProjection(h.Body, read)
	if err != nil {
		out.ProjectionError = "ACL mutation projection unavailable; original private receipt retained"
		return out
	}
	projection.ProjectionComplete = true
	projection.Complete = !projection.Null && h.ProjectionError == "" && (h.Metadata.ACLFiltered == nil || !*h.Metadata.ACLFiltered)
	out.Projection = &projection
	return out
}

func (l *documentLease) LastHTTPACLMutation() contract.KVHTTPACLMutationResult {
	l.receiptMu.Lock()
	last := l.aclMutationLast
	l.receiptMu.Unlock()
	if last == nil {
		return contract.KVHTTPACLMutationResult{}
	}
	return last.Clone()
}

func (l *documentLease) storeACLMutation(result contract.KVHTTPACLMutationResult) {
	l.receiptMu.Lock()
	defer l.receiptMu.Unlock()
	v := result.Clone()
	cost := aclMutationResultCost(&v) + aclReadResultCost(l.aclLast) + resultCost(l.last)
	if l.unknown != nil && l.unknown != l.last {
		cost += resultCost(l.unknown)
	}
	receiptBudgetMu.Lock()
	if l.reserved {
		l.s.reserved -= pendingReceiptBudget
		l.reserved = false
	}
	l.s.retained += cost - l.cacheBytes
	l.cacheBytes = cost
	receiptBudgetMu.Unlock()
	l.aclMutationLast = &v
}

func aclMutationResultCost(r *contract.KVHTTPACLMutationResult) int64 {
	if r == nil {
		return 0
	}
	cost := resultCost(&r.Receipt) + int64(512+len(r.LocalRecordID)+len(r.Request.Resource)+len(r.Request.Operation)+len(r.Request.Identity)+len(r.Request.Definition)+len(r.ProjectionError))
	if r.Impact != nil {
		cost += 128 + aclImpactCost(r.Impact.Preview) + aclImpactCost(r.Impact.Recheck)
	}
	if r.Request.Description != nil {
		cost += int64(24 + len(*r.Request.Description))
	}
	if r.Projection != nil {
		cost += int64(128 + len(r.Projection.Resource) + len(r.Projection.Lookup))
		for _, row := range r.Projection.Rows {
			cost += aclRowCost(row)
		}
	}
	return cost
}

var _ contract.KVHTTPACLMutating = (*documentLease)(nil)
