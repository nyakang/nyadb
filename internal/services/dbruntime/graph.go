package dbruntime

import (
	"context"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// Capture this wrapper on the UI thread. Both checks address the captured
// original session; a replacement publication cannot receive a late result.
func (s *session) GraphSchema(ctx context.Context, n contract.NamespaceRef) (contract.GraphSchemaSnapshot, error) {
	if e := s.require(s.Capabilities().Metadata, n.ProfileID); e != nil {
		return contract.GraphSchemaSnapshot{}, e
	}
	p, e := contract.Require[contract.GraphMetadataProvider](s.raw)
	if e != nil {
		return contract.GraphSchemaSnapshot{}, e
	}
	r, e := p.GraphSchema(ctx, n)
	if check := s.require(s.Capabilities().Metadata, n.ProfileID); check != nil {
		return contract.GraphSchemaSnapshot{}, check
	}
	return r, e
}

func (l *queryLease) SupportsGraph() bool {
	_, ok := l.raw.(contract.GraphQueryLease)
	return ok
}

func (l *queryLease) ExecuteGraph(ctx context.Context, r contract.GraphQueryRequest) (contract.GraphResult, error) {
	if e := l.session.require(l.session.Capabilities().Query, l.session.id); e != nil {
		return contract.GraphResult{}, e
	}
	p, ok := l.raw.(contract.GraphQueryLease)
	if !ok {
		return contract.GraphResult{}, contract.ErrUnsupported
	}
	// Never replace the receipt with an empty result after a submitted write.
	// Revocation closes and waits for this original executor at the adapter.
	return p.ExecuteGraph(ctx, r)
}

func (l *queryLease) SupportsGraphPropertyWrites() bool {
	_, ok := l.raw.(contract.GraphPropertyWriteLease)
	return ok
}

func (l *queryLease) WriteGraphProperty(ctx context.Context, r contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error) {
	if e := l.CheckQueryLease(); e != nil {
		return contract.GraphPropertyWriteReceipt{}, e
	}
	if r.Namespace.ProfileID != l.session.id {
		return contract.GraphPropertyWriteReceipt{}, contract.ErrRevoked
	}
	p, ok := l.raw.(contract.GraphPropertyWriteLease)
	if !ok {
		return contract.GraphPropertyWriteReceipt{}, contract.ErrUnsupported
	}
	return p.WriteGraphProperty(ctx, r.Clone())
}

func (l *queryLease) ValidateGraphPropertyWrite(r contract.GraphPropertyWriteRequest) error {
	if err := l.CheckQueryLease(); err != nil {
		return err
	}
	if r.Namespace.ProfileID != l.session.id {
		return contract.ErrRevoked
	}
	if p, ok := l.raw.(contract.GraphPropertyWriteValidator); ok {
		return p.ValidateGraphPropertyWrite(r.Clone())
	}
	return nil
}
