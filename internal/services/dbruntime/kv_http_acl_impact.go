package dbruntime

import (
	"context"
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type kvHTTPACLImpact struct{ base *kvRequestLease }
type kvHTTPACLImpactLease struct {
	*kvHTTPACLMutationLease
	*kvHTTPACLImpact
}
type kvHTTPACLImpactKeysLease struct {
	*kvHTTPACLMutationKeysLease
	*kvHTTPACLImpact
}
type kvHTTPACLImpactDestroyLease struct {
	*kvHTTPACLMutationDestroyLease
	*kvHTTPACLImpact
}

func bindKVHTTPACLImpact(bound contract.KVRequestLease) contract.KVRequestLease {
	base := originalKVRequestLease(bound)
	if base == nil || base.scope.HTTP.Protocol != "consul-http" {
		return bound
	}
	if _, ok := base.raw.(contract.KVHTTPACLImpactBrowsing); !ok {
		return bound
	}
	impact := &kvHTTPACLImpact{base: base}
	switch l := bound.(type) {
	case *kvHTTPACLMutationDestroyLease:
		return &kvHTTPACLImpactDestroyLease{l, impact}
	case *kvHTTPACLMutationKeysLease:
		return &kvHTTPACLImpactKeysLease{l, impact}
	case *kvHTTPACLMutationLease:
		return &kvHTTPACLImpactLease{l, impact}
	}
	return bound
}

func translateACLImpact(o *contract.KVHTTPACLImpact, basis kvACLRecordScope) bool {
	ok := sameKVACLScope(o.Scope, basis.raw)
	if ok {
		o.Scope.Generation = basis.public.Generation
	}
	for i := range o.Reads {
		r := &o.Reads[i].Result
		if !sameKVACLScope(r.Scope, basis.raw) {
			r.Projection = nil
			r.ProjectionError = "ACL projection unavailable; original receipt retained"
			ok = false
		} else {
			r.Scope.Generation = basis.public.Generation
		}
	}
	if !ok {
		o.Complete = false
	}
	return ok
}

func (l *kvHTTPACLImpact) ObserveHTTPACLImpact(ctx context.Context, input contract.KVHTTPACLImpactRequest) (contract.KVHTTPACLImpact, error) {
	if err := l.base.CheckKVLease(); err != nil {
		return contract.KVHTTPACLImpact{Scope: l.base.Scope(), Request: input}, err
	}
	raw := l.base.scope.Clone()
	raw.Generation = l.base.session.raw.Generation()
	basis := kvACLRecordScope{raw: raw, public: l.base.Scope()}
	out, err := l.base.raw.(contract.KVHTTPACLImpactBrowsing).ObserveHTTPACLImpact(ctx, input)
	out = out.Clone()
	if out.Request != input || !translateACLImpact(&out, basis) {
		out.Complete = false
		err = errors.Join(err, contract.ErrRevoked)
	}
	if check := l.base.CheckKVLease(); check != nil {
		out.Complete = false
		err = errors.Join(err, check)
	}
	return out, err
}

var _ contract.KVHTTPACLImpactBrowsing = (*kvHTTPACLImpact)(nil)
