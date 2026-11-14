package dbruntime

import (
	"context"
	"errors"
	"reflect"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type kvHTTPACLMutationLease struct {
	contract.KVRequestLease
	contract.KVHTTPACLReading
	base *kvRequestLease
}

type kvHTTPACLMutationKeysLease struct {
	*kvHTTPACLMutationLease
	contract.KVHTTPSessionKeysBrowsing
}

type kvHTTPACLMutationDestroyLease struct {
	*kvHTTPACLMutationKeysLease
	contract.KVHTTPSessionDestroyImpactBrowsing
	contract.KVHTTPSessionDestroyPlanning
}

type kvHTTPACLMutationPreparation struct {
	owner  *kvHTTPACLMutationLease
	raw    contract.KVHTTPACLMutationPreparation
	input  contract.KVHTTPACLMutationRequest
	record string
}

func (p *kvHTTPACLMutationPreparation) Request() contract.KVHTTPACLMutationRequest {
	return p.input.Clone()
}
func (p *kvHTTPACLMutationPreparation) LocalRecordID() string { return p.record }

func bindKVHTTPACLMutation(bound contract.KVRequestLease) contract.KVRequestLease {
	base := originalKVRequestLease(bound)
	if base == nil || base.scope.HTTP.Protocol != "consul-http" {
		return bound
	}
	if _, ok := base.raw.(contract.KVHTTPACLMutating); !ok {
		return bound
	}
	reading, ok := bound.(contract.KVHTTPACLReading)
	if !ok {
		return bound
	}
	acl := &kvHTTPACLMutationLease{KVRequestLease: bound, KVHTTPACLReading: reading, base: base}
	if keys, ok := bound.(contract.KVHTTPSessionKeysBrowsing); ok {
		withKeys := &kvHTTPACLMutationKeysLease{acl, keys}
		impact, hasImpact := bound.(contract.KVHTTPSessionDestroyImpactBrowsing)
		planning, hasPlanning := bound.(contract.KVHTTPSessionDestroyPlanning)
		if hasImpact && hasPlanning {
			return &kvHTTPACLMutationDestroyLease{withKeys, impact, planning}
		}
		return withKeys
	}
	return acl
}

func revokeACLMutationProjection(r *contract.KVHTTPACLMutationResult) {
	r.Projection = nil
	r.ProjectionError = "ACL mutation projection unavailable; original receipt retained"
	if r.Impact != nil {
		r.Impact.Preview.Complete, r.Impact.Recheck.Complete, r.Impact.Revalidated = false, false, false
	}
}

func (l *kvRequestLease) aclMutationOriginalRecord() kvACLRecord {
	l.aclMu.Lock()
	defer l.aclMu.Unlock()
	return kvACLRecord{l.aclMutationRecordID, kvACLRecordScope{l.aclMutationRecordScope.raw.Clone(), l.aclMutationRecordScope.public.Clone()}}
}

func (l *kvRequestLease) translateACLMutationResult(r *contract.KVHTTPACLMutationResult, original kvACLRecord) bool {
	if r.Request.Resource == "" && r.Receipt.HTTP == nil && r.LocalRecordID == "" {
		return true
	}
	if r.LocalRecordID == "" || len(r.LocalRecordID) > 128 {
		revokeACLMutationProjection(r)
		return false
	}
	basis := original.basis
	if original.id != r.LocalRecordID {
		raw := l.scope.Clone()
		raw.Generation = l.session.raw.Generation()
		basis = kvACLRecordScope{raw: raw, public: l.scope.Clone()}
	}
	if !sameKVACLScope(r.Scope, basis.raw) {
		revokeACLMutationProjection(r)
		return false
	}
	r.Scope.Generation = basis.public.Generation
	if e := r.Impact; e != nil {
		valid := e.Preview.Request.Resource == r.Request.Resource && e.Preview.Request.Identity == r.Request.Identity
		valid = validACLMutationImpactInput(r.Request, e.Preview) && valid
		valid = translateACLImpact(&e.Preview, basis) && valid
		if e.Recheck.Request.Resource != "" {
			valid = e.Recheck.Request == e.Preview.Request && valid
			valid = validACLMutationImpactInput(r.Request, e.Recheck) && valid
			valid = translateACLImpact(&e.Recheck, basis) && valid
		}
		if !valid {
			revokeACLMutationProjection(r)
			return false
		}
	}
	return true
}

// The private mutation keeps its own original publication independently of
// later ACL reads. Recovery transfers evidence, never write authority.
func (l *kvRequestLease) inheritACLMutationRecord(rawScope contract.KVScope, result *contract.KVHTTPACLMutationResult) {
	if result == nil {
		return
	}
	l.aclMu.Lock()
	l.aclMutationRecordID = result.LocalRecordID
	l.aclMutationRecordScope = kvACLRecordScope{raw: rawScope.Clone(), public: result.Scope.Clone()}
	l.aclMu.Unlock()
}

func (l *kvHTTPACLMutationLease) PrepareHTTPACLMutation(request contract.KVHTTPACLMutationRequest) (contract.KVHTTPACLMutationPreparation, error) {
	input := request.Clone()
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	raw, err := l.base.raw.(contract.KVHTTPACLMutating).PrepareHTTPACLMutation(input.Clone())
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, contract.ErrUnsupported
	}
	id := raw.LocalRecordID()
	if id == "" || len(id) > 128 || !reflect.DeepEqual(raw.Request(), input) {
		return nil, contract.ErrRevoked
	}
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	return &kvHTTPACLMutationPreparation{owner: l, raw: raw, input: input, record: id}, nil
}

func (l *kvHTTPACLMutationLease) ExecuteHTTPACLMutation(ctx context.Context, prepared contract.KVHTTPACLMutationPreparation) (contract.KVHTTPACLMutationResult, error) {
	p, ok := prepared.(*kvHTTPACLMutationPreparation)
	if !ok || p == nil || p.owner != l {
		return contract.KVHTTPACLMutationResult{}, contract.ErrKVPlanUsed
	}
	if err := l.CheckKVLease(); err != nil {
		return contract.KVHTTPACLMutationResult{LocalRecordID: p.record, Scope: l.Scope(), Request: p.input.Clone()}, err
	}
	original := l.base.aclMutationOriginalRecord()
	r, err := l.base.raw.(contract.KVHTTPACLMutating).ExecuteHTTPACLMutation(ctx, p.raw)
	r = r.Clone()
	if r.LocalRecordID != p.record || !reflect.DeepEqual(r.Request, p.input) {
		revokeACLMutationProjection(&r)
		err = errors.Join(err, contract.ErrRevoked)
	} else if !l.base.translateACLMutationResult(&r, original) {
		err = errors.Join(err, contract.ErrRevoked)
	}
	return r, errors.Join(err, l.CheckKVLease())
}

func (l *kvHTTPACLMutationLease) LastHTTPACLMutation() contract.KVHTTPACLMutationResult {
	original := l.base.aclMutationOriginalRecord()
	r := l.base.raw.(contract.KVHTTPACLMutating).LastHTTPACLMutation().Clone()
	l.base.translateACLMutationResult(&r, original)
	return r
}

var _ contract.KVHTTPACLMutating = (*kvHTTPACLMutationLease)(nil)
