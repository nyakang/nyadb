package consul

import (
	"context"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (s *Session) Namespace(name string) contract.NamespaceRef {
	if name == "kv" {
		return s.KVScope().Namespace
	}
	return contract.NamespaceRef{ProfileID: s.ProfileID()}
}
func (s *Session) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !s.Alive() {
		return nil, contract.ErrRevoked
	}
	return []contract.Namespace{{Ref: s.KVScope().Namespace, Label: "kv"}}, nil
}
func (s *Session) Browse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	if n != s.KVScope().Namespace {
		return nil, contract.ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !s.Alive() {
		return nil, contract.ErrRevoked
	}
	return nil, nil // native request documents are opened through the workbench
}
func (*Session) TableMetadata(context.Context, contract.ObjectRef) (*contract.TableStructure, error) {
	return nil, contract.ErrUnsupported
}
func (s *Session) KVRequestPresentation() contract.KVRequestPresentation {
	native, err := KeyListRequest(s.cfg.Prefix, true)
	if err != nil {
		return contract.KVRequestPresentation{}
	}
	request, err := DocumentRequest(native)
	if err != nil {
		return contract.KVRequestPresentation{}
	}
	return contract.KVRequestPresentation{Product: "Consul", Default: request, Methods: []contract.KVRequestMethod{{Name: "HTTP", Request: request}}, Tools: requestTools(s.cfg.Prefix)}
}

var _ contract.MetadataProvider = (*Session)(nil)
var _ contract.KVRequestPresentationProvider = (*Session)(nil)
