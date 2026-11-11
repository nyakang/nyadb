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
	return []contract.DBObject{{Ref: n.Object("keyspace", "keys"), Name: "keys", Kind: "键", Engine: "Consul", Comment: "Bounded live observations in the original visible prefix; no fixed MVCC snapshot."}}, nil
}
func (s *Session) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	if o != s.KVScope().Namespace.Object("keyspace", "keys") {
		return nil, contract.ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !s.Alive() {
		return nil, contract.ErrRevoked
	}
	return &contract.TableStructure{Object: o, Database: o.Catalog, Name: o.Name, Kind: "键", Engine: "Consul", Columns: dataColumns(), Comment: "Bounded live observations in the original visible prefix; no fixed MVCC snapshot."}, nil
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
