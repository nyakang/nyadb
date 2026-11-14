package dbruntime

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"reflect"
)

type kvHTTPManagementLease struct {
	contract.KVRequestLease
	base *kvRequestLease
}
type kvHTTPManagementFullLease struct {
	*kvHTTPManagementLease
	contract.KVHTTPArchiveCapturing
	contract.KVHTTPACLReading
	contract.KVHTTPACLMutating
	contract.KVHTTPACLImpactBrowsing
	contract.KVHTTPACLMutationPlanning
	contract.KVHTTPSessionKeysBrowsing
	contract.KVHTTPSessionDestroyImpactBrowsing
	contract.KVHTTPSessionDestroyPlanning
}
type kvHTTPManagementPreparation struct {
	owner *kvHTTPManagementLease
	raw   contract.KVHTTPManagementPreparation
	scope contract.KVScope
}

func (p *kvHTTPManagementPreparation) Request() contract.KVHTTPManagementRequest {
	return p.raw.Request().Clone()
}
func (p *kvHTTPManagementPreparation) Scope() contract.KVScope      { return p.scope.Clone() }
func (p *kvHTTPManagementPreparation) LocalRecordID() string        { return p.raw.LocalRecordID() }
func (p *kvHTTPManagementPreparation) HTTP() contract.KVHTTPRequest { return p.raw.HTTP().Clone() }
func (p *kvHTTPManagementPreparation) Writes() bool                 { return p.raw.Writes() }
func bindKVHTTPManagement(bound contract.KVRequestLease) contract.KVRequestLease {
	base := originalKVRequestLease(bound)
	if base == nil {
		return bound
	}
	if _, ok := base.raw.(contract.KVHTTPManaging); !ok {
		return bound
	}
	l := &kvHTTPManagementLease{bound, base}
	archive, a := bound.(contract.KVHTTPArchiveCapturing)
	read, b := bound.(contract.KVHTTPACLReading)
	write, c := bound.(contract.KVHTTPACLMutating)
	impact, d := bound.(contract.KVHTTPACLImpactBrowsing)
	planning, e := bound.(contract.KVHTTPACLMutationPlanning)
	keys, f := bound.(contract.KVHTTPSessionKeysBrowsing)
	destroy, g := bound.(contract.KVHTTPSessionDestroyImpactBrowsing)
	destroyPlan, h := bound.(contract.KVHTTPSessionDestroyPlanning)
	if a && b && c && d && e && f && g && h {
		return bindManagementExtensions(l, &kvHTTPManagementFullLease{l, archive, read, write, impact, planning, keys, destroy, destroyPlan})
	}
	return bindManagementExtensions(l, nil)
}
func (l *kvHTTPManagementLease) HTTPManagementOperations() []contract.KVHTTPManagementOperation {
	return l.base.raw.(contract.KVHTTPManaging).HTTPManagementOperations()
}
func (l *kvHTTPManagementLease) PrepareHTTPManagement(input contract.KVHTTPManagementRequest) (contract.KVHTTPManagementPreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	r, err := l.base.raw.(contract.KVHTTPManaging).PrepareHTTPManagement(input.Clone())
	if err != nil {
		return nil, err
	}
	rawScope := l.Scope()
	rawScope.Generation = l.base.session.raw.Generation()
	if r == nil || !reflect.DeepEqual(r.Request(), input) || !sameKVACLScope(r.Scope(), rawScope) || r.LocalRecordID() == "" {
		return nil, contract.ErrRevoked
	}
	return &kvHTTPManagementPreparation{l, r, l.Scope()}, nil
}
func (l *kvHTTPManagementLease) ExecuteHTTPManagement(ctx context.Context, prepared contract.KVHTTPManagementPreparation) (contract.KVHTTPManagementResult, error) {
	p, ok := prepared.(*kvHTTPManagementPreparation)
	if deletion, has := prepared.(*kvHTTPEnterpriseDeletionPreparation); has {
		p = deletion.kvHTTPManagementPreparation
		ok = true
	}
	if !ok || p == nil || p.owner != l {
		return contract.KVHTTPManagementResult{}, contract.ErrKVPlanUsed
	}
	if err := l.CheckKVLease(); err != nil {
		return contract.KVHTTPManagementResult{}, err
	}
	r, err := l.base.raw.(contract.KVHTTPManaging).ExecuteHTTPManagement(ctx, p.raw)
	r = r.Clone()
	rawScope := p.scope.Clone()
	rawScope.Generation = l.base.session.raw.Generation()
	if !sameKVACLScope(r.Scope, rawScope) || !reflect.DeepEqual(r.Request, p.Request()) || r.LocalRecordID != p.LocalRecordID() {
		return r, errors.Join(err, contract.ErrRevoked)
	}
	if !translateManagementImpacts(&r, rawScope, p.scope) {
		return r, errors.Join(err, contract.ErrRevoked)
	}
	r.Scope = p.scope.Clone()
	return r, errors.Join(err, l.CheckKVLease())
}
func (l *kvHTTPManagementLease) LastHTTPManagement() contract.KVHTTPManagementResult {
	r := l.base.raw.(contract.KVHTTPManaging).LastHTTPManagement().Clone()
	l.base.translateManagementResult(&r)
	return r
}
func (l *kvRequestLease) translateManagementResult(r *contract.KVHTTPManagementResult) bool {
	if r == nil || r.LocalRecordID == "" {
		return r == nil || !r.Receipt.Attempted
	}
	rawScope := l.scope.Clone()
	rawScope.Generation = l.session.raw.Generation()
	l.aclMu.Lock()
	basis, ok := l.managementRecords[r.LocalRecordID]
	l.aclMu.Unlock()
	if ok {
		rawScope = basis.raw
	}
	if !sameKVACLScope(r.Scope, rawScope) {
		return false
	}
	public := l.scope.Clone()
	if ok {
		public = basis.public.Clone()
	}
	if !translateManagementImpacts(r, rawScope, public) {
		return false
	}
	if ok {
		r.Scope = basis.public.Clone()
	} else {
		r.Scope = l.scope.Clone()
	}
	return true
}
