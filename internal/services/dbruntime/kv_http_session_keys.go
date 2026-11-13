package dbruntime

import (
	"bytes"
	"context"
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// Consul's native document exposes this facet on the same physical request
// lease. An ordinary KV lease never advertises unsupported Session browsing.
type kvHTTPSessionKeysLease struct{ *kvRequestLease }

func bindKVHTTPSessionKeys(bound contract.KVRequestLease) contract.KVRequestLease {
	base, ok := bound.(*kvRequestLease)
	if !ok || base.scope.HTTP.Protocol != "consul-http" {
		return bound
	}
	if _, ok := base.raw.(contract.KVHTTPSessionKeysBrowsing); !ok {
		return bound
	}
	return &kvHTTPSessionKeysLease{base}
}

func (l *kvHTTPSessionKeysLease) ObserveHTTPSessionKeys(ctx context.Context, request contract.KVHTTPSessionKeysRequest) (contract.KVHTTPSessionKeysObservation, error) {
	if err := l.CheckKVLease(); err != nil {
		return contract.KVHTTPSessionKeysObservation{Scope: l.scope.Clone(), Request: request}, err
	}
	o, err := l.raw.(contract.KVHTTPSessionKeysBrowsing).ObserveHTTPSessionKeys(ctx, request)
	o = o.Clone()
	expected := l.scope
	actual := o.Scope
	if actual.Namespace != expected.Namespace || actual.Generation != l.session.raw.Generation() || !bytes.Equal(actual.Prefix, expected.Prefix) || actual.Header != expected.Header || actual.HTTP != expected.HTTP || actual.Version != expected.Version || o.Request != request {
		// Retain the actual original receipt; foreign evidence cannot acquire
		// this publication's identity or a complete held-key projection.
		o.Matches, o.ProjectionComplete, o.Complete = nil, false, false
		return o, errors.Join(err, contract.ErrRevoked)
	}
	o.Scope.Generation = expected.Generation
	// Late original receipts retain their captured owning generation. The UI
	// admits results and tools only after the original executor acknowledges.
	return o, errors.Join(err, l.CheckKVLease())
}

var _ contract.KVHTTPSessionKeysBrowsing = (*kvHTTPSessionKeysLease)(nil)
