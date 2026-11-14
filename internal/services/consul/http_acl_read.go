package consul

import (
	"context"
	"crypto/rand"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type httpACLReadPreparation struct {
	owner *documentLease
	input contract.KVHTTPACLReadRequest
	plan  *documentPreparation
}

func (p *httpACLReadPreparation) Request() contract.KVHTTPACLReadRequest { return p.input.Clone() }
func (p *httpACLReadPreparation) LocalRecordID() string                  { return p.plan.aclRecordID }

func aclNativeReadRequest(input contract.KVHTTPACLReadRequest) (Request, error) {
	resource := ACLReadResource(input.Resource)
	var request Request
	var err error
	switch input.Lookup {
	case "list":
		if input.Identity != "" {
			return Request{}, ErrInvalid
		}
		request, err = ACLListReadRequest(resource)
	case "self":
		if resource != ACLReadToken || input.Identity != "" {
			return Request{}, ErrInvalid
		}
		request = ACLTokenSelfReadRequest()
	case "id":
		if resource == ACLReadAuthMethod || resource == ACLReadTemplatedPolicy {
			return Request{}, ErrInvalid
		}
		request, err = ACLItemReadRequest(resource, input.Identity)
	case "name":
		request, err = ACLNameReadRequest(resource, input.Identity)
	default:
		return Request{}, ErrUnsupported
	}
	if err != nil {
		return Request{}, err
	}
	return WithACLReadOptions(request, ACLReadOptions{Index: input.Index, Wait: input.Wait,
		Policy: input.Policy, Role: input.Role, AuthMethod: input.AuthMethod, ServiceName: input.ServiceName, Expanded: input.Expanded})
}

func (l *documentLease) PrepareHTTPACLRead(request contract.KVHTTPACLReadRequest) (contract.KVHTTPACLReadPreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	input := request.Clone()
	native, err := aclNativeReadRequest(input)
	if err != nil {
		return nil, err
	}
	envelope, err := documentRequestEnvelope(native)
	if err != nil {
		return nil, err
	}
	plan := &documentPreparation{owner: l, native: native.Clone(), request: envelope, privateACL: true, aclRequest: input.Clone(), aclRecordID: rand.Text()}
	return &httpACLReadPreparation{owner: l, input: input, plan: plan}, nil
}

func (l *documentLease) ExecuteHTTPACLRead(parent context.Context, prepared contract.KVHTTPACLReadPreparation) (contract.KVHTTPACLReadResult, error) {
	p, ok := prepared.(*httpACLReadPreparation)
	if !ok || p == nil || p.owner != l {
		return contract.KVHTTPACLReadResult{}, contract.ErrKVPlanUsed
	}
	out := contract.KVHTTPACLReadResult{LocalRecordID: p.LocalRecordID(), Scope: l.Scope(), Request: p.input.Clone()}
	if !l.ops.TryLock() {
		return out, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	result, err := l.executeLocked(parent, p.plan)
	out.Receipt = result
	if result.Attempted {
		out = l.LastHTTPACLRead()
	}
	return out, err
}

func (l *documentLease) LastHTTPACLRead() contract.KVHTTPACLReadResult {
	l.receiptMu.Lock()
	last := l.aclLast
	l.receiptMu.Unlock()
	if last == nil {
		return contract.KVHTTPACLReadResult{}
	}
	return last.Clone()
}

func (l *documentLease) storeACLRead(result contract.KVHTTPACLReadResult) {
	l.receiptMu.Lock()
	defer l.receiptMu.Unlock()
	v := result.Clone()
	cost := aclReadResultCost(&v) + resultCost(l.last) + aclMutationResultCost(l.aclMutationLast)
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
	l.aclLast = &v
}

func aclReadResultCost(r *contract.KVHTTPACLReadResult) int64 {
	if r == nil {
		return 0
	}
	cost := resultCost(&r.Receipt) + int64(512+len(r.LocalRecordID)+len(r.Request.Resource)+len(r.Request.Lookup)+len(r.Request.Identity)+len(r.ProjectionError))
	for _, field := range []*string{r.Request.Policy, r.Request.Role, r.Request.AuthMethod, r.Request.ServiceName, r.Request.Wait} {
		if field != nil {
			cost += int64(24 + len(*field))
		}
	}
	if r.Projection != nil {
		cost += int64(128 + len(r.Projection.Resource) + len(r.Projection.Lookup))
		for _, row := range r.Projection.Rows {
			cost += aclRowCost(row)
		}
	}
	return cost
}

var _ contract.KVHTTPACLReading = (*documentLease)(nil)
