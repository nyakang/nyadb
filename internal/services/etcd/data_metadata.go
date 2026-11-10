package etcd

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
	return []contract.DBObject{{Ref: n.Object("keyspace", "keys"), Name: "keys", Kind: "键", Engine: "etcd", Comment: "Original byte prefix; each data document binds its own fixed MVCC revision."}}, nil
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
	return &contract.TableStructure{Object: o, Database: o.Catalog, Name: o.Name, Kind: "键", Engine: "etcd", Columns: dataColumns(), Comment: "Original byte prefix; fixed MVCC revision. Range authorization is checked by the server."}, nil
}

var _ contract.MetadataProvider = (*Session)(nil)
