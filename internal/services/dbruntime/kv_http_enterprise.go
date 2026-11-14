package dbruntime

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"reflect"
)

type kvHTTPEnterpriseDeletionPreparation struct {
	*kvHTTPManagementPreparation
	rawDeletion contract.KVHTTPEnterpriseDeletionPreparation
}

func (p *kvHTTPEnterpriseDeletionPreparation) Impact() contract.KVHTTPEnterpriseImpact {
	i := p.rawDeletion.Impact().Clone()
	i.Scope = p.scope.Clone()
	return i
}
func (p *kvHTTPEnterpriseDeletionPreparation) Close() error { return p.rawDeletion.Close() }
func (l *kvHTTPManagementLease) prepareHTTPEnterpriseDeletion(ctx context.Context, in contract.KVHTTPManagementRequest) (contract.KVHTTPEnterpriseDeletionPreparation, contract.KVHTTPEnterpriseImpact, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, contract.KVHTTPEnterpriseImpact{}, err
	}
	f, ok := l.base.raw.(contract.KVHTTPEnterpriseDeletionPlanning)
	if !ok {
		return nil, contract.KVHTTPEnterpriseImpact{}, contract.ErrUnsupported
	}
	p, impact, err := f.PrepareHTTPEnterpriseDeletion(ctx, in.Clone())
	impact = impact.Clone()
	raw := l.Scope()
	raw.Generation = l.base.session.raw.Generation()
	if !sameKVACLScope(impact.Scope, raw) || !reflect.DeepEqual(impact.Request, in) {
		if p != nil {
			_ = p.Close()
		}
		return nil, impact, errors.Join(err, contract.ErrRevoked)
	}
	impact.Scope = l.Scope()
	if err != nil || p == nil {
		if p != nil {
			_ = p.Close()
		}
		return nil, impact, err
	}
	if !sameKVACLScope(p.Scope(), raw) || !reflect.DeepEqual(p.Request(), in) || !reflect.DeepEqual(p.Impact().Request, in) || !p.Impact().Complete {
		_ = p.Close()
		return nil, impact, contract.ErrRevoked
	}
	return &kvHTTPEnterpriseDeletionPreparation{&kvHTTPManagementPreparation{l, p, l.Scope()}, p}, impact, l.CheckKVLease()
}
func translateManagementImpacts(r *contract.KVHTTPManagementResult, raw, public contract.KVScope) bool {
	for _, i := range []*contract.KVHTTPEnterpriseImpact{r.EnterpriseImpact, r.EnterpriseVerification} {
		if i != nil {
			if !sameKVACLScope(i.Scope, raw) || !reflect.DeepEqual(i.Request, r.Request) {
				return false
			}
			i.Scope = public.Clone()
		}
	}
	return true
}
