package dbruntime

import (
	"context"
	"errors"
	"reflect"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type kvHTTPACLPlanner struct {
	base     *kvRequestLease
	mutating contract.KVHTTPACLMutating
}
type kvHTTPACLPlannedLease struct {
	*kvHTTPACLImpactLease
	*kvHTTPACLPlanner
}
type kvHTTPACLPlannedKeysLease struct {
	*kvHTTPACLImpactKeysLease
	*kvHTTPACLPlanner
}
type kvHTTPACLPlannedDestroyLease struct {
	*kvHTTPACLImpactDestroyLease
	*kvHTTPACLPlanner
}

// Both embedded facets offer mutation execution; select the planner explicitly
// while preserving the original read, held-key and destroy interfaces.
func (l *kvHTTPACLPlannedLease) ExecuteHTTPACLMutation(ctx context.Context, p contract.KVHTTPACLMutationPreparation) (contract.KVHTTPACLMutationResult, error) {
	return l.kvHTTPACLPlanner.ExecuteHTTPACLMutation(ctx, p)
}
func (l *kvHTTPACLPlannedKeysLease) ExecuteHTTPACLMutation(ctx context.Context, p contract.KVHTTPACLMutationPreparation) (contract.KVHTTPACLMutationResult, error) {
	return l.kvHTTPACLPlanner.ExecuteHTTPACLMutation(ctx, p)
}
func (l *kvHTTPACLPlannedDestroyLease) ExecuteHTTPACLMutation(ctx context.Context, p contract.KVHTTPACLMutationPreparation) (contract.KVHTTPACLMutationResult, error) {
	return l.kvHTTPACLPlanner.ExecuteHTTPACLMutation(ctx, p)
}

func bindKVHTTPACLMutationPlanning(bound contract.KVRequestLease) contract.KVRequestLease {
	base := originalKVRequestLease(bound)
	if base == nil || base.scope.HTTP.Protocol != "consul-http" {
		return bound
	}
	if _, ok := base.raw.(contract.KVHTTPACLMutationPlanning); !ok {
		return bound
	}
	mutating, ok := bound.(contract.KVHTTPACLMutating)
	if !ok {
		return bound
	}
	planner := &kvHTTPACLPlanner{base: base, mutating: mutating}
	switch l := bound.(type) {
	case *kvHTTPACLImpactDestroyLease:
		return &kvHTTPACLPlannedDestroyLease{l, planner}
	case *kvHTTPACLImpactKeysLease:
		return &kvHTTPACLPlannedKeysLease{l, planner}
	case *kvHTTPACLImpactLease:
		return &kvHTTPACLPlannedLease{l, planner}
	}
	return bound
}

type kvHTTPACLMutationPlan struct {
	owner     *kvHTTPACLPlanner
	authority *kvHTTPACLMutationPlan
	raw       contract.KVHTTPACLMutationPlan
	record    string
	mu        sync.Mutex
	input     contract.KVHTTPACLMutationRequest
	preview   contract.KVHTTPACLImpact
	closed    bool
}

func (p *kvHTTPACLMutationPlan) LocalRecordID() string { return p.record }
func (p *kvHTTPACLMutationPlan) Request() contract.KVHTTPACLMutationRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.input.Clone()
}
func (p *kvHTTPACLMutationPlan) Impact() contract.KVHTTPACLImpact {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.preview.Clone()
}
func (p *kvHTTPACLMutationPlan) Close() error {
	if p == nil || p.authority != p {
		return contract.ErrKVPlanUsed
	}
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	err := p.raw.Close() // waits for the original physical executor
	p.owner.base.aclMu.Lock()
	delete(p.owner.base.aclMutationPlans, p)
	p.owner.base.aclMu.Unlock()
	p.mu.Lock()
	p.input = contract.KVHTTPACLMutationRequest{}
	p.preview = contract.KVHTTPACLImpact{}
	p.mu.Unlock()
	return err
}

func (l *kvHTTPACLPlanner) PrepareHTTPACLMutationPlan(ctx context.Context, input contract.KVHTTPACLMutationRequest, limits contract.KVHTTPACLImpactLimits) (contract.KVHTTPACLMutationPlan, contract.KVHTTPACLImpact, error) {
	request := input.Clone()
	expected := contract.KVHTTPACLImpactRequest{Resource: request.Resource, Identity: request.Identity, MaxRows: limits.MaxRows, MaxReads: limits.MaxReads, MaxBytes: limits.MaxBytes}
	out := contract.KVHTTPACLImpact{Scope: l.base.Scope(), Request: expected}
	if err := l.base.CheckKVLease(); err != nil {
		return nil, out, err
	}
	rawScope := l.base.Scope()
	rawScope.Generation = l.base.session.raw.Generation()
	basis := kvACLRecordScope{raw: rawScope, public: l.base.Scope()}
	raw, out, err := l.base.raw.(contract.KVHTTPACLMutationPlanning).PrepareHTTPACLMutationPlan(ctx, request.Clone(), limits)
	out = out.Clone()
	valid := out.Request == expected
	if raw != nil || out.Creation != nil || len(out.Reads) > 0 {
		valid = validACLMutationImpactInput(request, out) && valid
	}
	if raw != nil {
		valid = raw.LocalRecordID() != "" && len(raw.LocalRecordID()) <= 128 && reflect.DeepEqual(raw.Request(), request) && reflect.DeepEqual(raw.Impact(), out) && valid
	}
	valid = translateACLImpact(&out, basis) && valid
	if !valid {
		out.Complete = false
		err = errors.Join(err, contract.ErrRevoked)
	}
	if check := l.base.CheckKVLease(); check != nil {
		out.Complete = false
		err = errors.Join(err, check)
	}
	if err != nil || raw == nil || !out.Complete {
		if err == nil {
			err = contract.ErrUnsupported
		}
		if raw != nil {
			err = errors.Join(err, raw.Close())
		}
		return nil, out, err
	}
	p := &kvHTTPACLMutationPlan{owner: l, raw: raw, record: raw.LocalRecordID(), input: request, preview: out.Clone()}
	p.authority = p
	l.base.aclMu.Lock()
	if l.base.aclMutationPlans == nil {
		l.base.aclMutationPlans = make(map[*kvHTTPACLMutationPlan]bool)
	}
	l.base.aclMutationPlans[p] = true
	l.base.aclMu.Unlock()
	if err := l.base.CheckKVLease(); err != nil {
		out.Complete = false
		return nil, out, errors.Join(err, p.Close())
	}
	return p, out, nil
}

func (l *kvHTTPACLPlanner) ExecuteHTTPACLMutation(ctx context.Context, prepared contract.KVHTTPACLMutationPreparation) (contract.KVHTTPACLMutationResult, error) {
	p, ok := prepared.(*kvHTTPACLMutationPlan)
	if !ok {
		return l.mutating.ExecuteHTTPACLMutation(ctx, prepared)
	}
	if p == nil || p.owner != l || p.authority != p {
		return contract.KVHTTPACLMutationResult{}, contract.ErrKVPlanUsed
	}
	p.mu.Lock()
	input, preview, closed := p.input.Clone(), p.preview.Clone(), p.closed
	p.mu.Unlock()
	out := contract.KVHTTPACLMutationResult{LocalRecordID: p.record, Scope: l.base.Scope(), Request: input, Impact: &contract.KVHTTPACLMutationImpactEvidence{Preview: preview}}
	if closed {
		return out, contract.ErrKVPlanUsed
	}
	if err := l.base.CheckKVLease(); err != nil {
		return out, errors.Join(err, p.Close())
	}
	original := l.base.aclMutationOriginalRecord()
	r, err := l.base.raw.(contract.KVHTTPACLMutating).ExecuteHTTPACLMutation(ctx, p.raw)
	r = r.Clone()
	if r.LocalRecordID != p.record || !reflect.DeepEqual(r.Request, input) || r.Impact == nil && !errors.Is(err, contract.ErrKVBusy) {
		revokeACLMutationProjection(&r)
		err = errors.Join(err, contract.ErrRevoked)
	}
	if !l.base.translateACLMutationResult(&r, original) {
		err = errors.Join(err, contract.ErrRevoked)
	}
	if r.Impact != nil && !reflect.DeepEqual(r.Impact.Preview, preview) {
		revokeACLMutationProjection(&r)
		err = errors.Join(err, contract.ErrRevoked)
	}
	if !errors.Is(err, contract.ErrKVBusy) {
		err = errors.Join(err, p.Close())
	}
	return r, errors.Join(err, l.base.CheckKVLease())
}

func (l *kvRequestLease) closeACLMutationPlans() {
	l.aclMu.Lock()
	plans := make([]*kvHTTPACLMutationPlan, 0, len(l.aclMutationPlans))
	for p := range l.aclMutationPlans {
		plans = append(plans, p)
	}
	l.aclMutationPlans = nil
	l.aclMu.Unlock()
	for _, p := range plans {
		p.Close()
	}
}

var _ contract.KVHTTPACLMutationPlanning = (*kvHTTPACLPlanner)(nil)
var _ contract.KVHTTPACLMutating = (*kvHTTPACLPlannedDestroyLease)(nil)

func validACLMutationImpactInput(input contract.KVHTTPACLMutationRequest, out contract.KVHTTPACLImpact) bool {
	if input.Operation == "create" {
		return out.Creation != nil && reflect.DeepEqual(*out.Creation, input)
	}
	return out.Creation == nil && out.UpsertIdentity == "" && len(out.Dependencies) == 0
}
