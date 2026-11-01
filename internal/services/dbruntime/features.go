package dbruntime

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"io"
)

func (s *session) require(enabled bool, id int64) error {
	if !s.alive() {
		return contract.ErrRevoked
	}
	if id != s.id {
		return connections.ErrInvalid
	}
	if !enabled {
		return contract.ErrUnsupported
	}
	return nil
}
func (s *session) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
	if e := s.require(s.Capabilities().Metadata, s.id); e != nil {
		return nil, e
	}
	p, e := contract.Require[contract.MetadataProvider](s.raw)
	if e != nil {
		return nil, e
	}
	return p.Namespaces(ctx)
}
func (s *session) Browse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	if e := s.require(s.Capabilities().Metadata, n.ProfileID); e != nil {
		return nil, e
	}
	p, e := contract.Require[contract.MetadataProvider](s.raw)
	if e != nil {
		return nil, e
	}
	return p.Browse(ctx, n)
}
func (s *session) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	if e := s.require(s.Capabilities().Metadata, o.ProfileID); e != nil {
		return nil, e
	}
	p, e := contract.Require[contract.MetadataProvider](s.raw)
	if e != nil {
		return nil, e
	}
	return p.TableMetadata(ctx, o)
}
func (s *session) Namespace(name string) contract.NamespaceRef {
	p, e := contract.Require[interface {
		Namespace(string) contract.NamespaceRef
	}](s.raw)
	if e != nil || !s.alive() {
		return contract.NamespaceRef{ProfileID: s.id}
	}
	return p.Namespace(name)
}
func (s *session) ReserveQuery(doc uint64) (contract.QueryLease, error) {
	if e := s.require(s.Capabilities().Query, s.id); e != nil {
		return nil, e
	}
	if doc == 0 {
		return nil, connections.ErrInvalid
	}
	p, e := contract.Require[contract.QueryProvider](s.raw)
	if e != nil {
		return nil, e
	}
	l, e := p.ReserveQuery(doc)
	if e != nil {
		return nil, e
	}
	return &queryLease{session: s, doc: doc, raw: l}, nil
}

type queryLease struct {
	session *session
	doc     uint64
	raw     contract.QueryLease
}

func (l *queryLease) Preconnect(ctx context.Context) error {
	if e := l.session.require(l.session.Capabilities().Query, l.session.id); e != nil {
		return e
	}
	return l.raw.Preconnect(ctx)
}
func (l *queryLease) Execute(ctx context.Context, ns, sql string) (contract.SQLResult, error) {
	if e := l.session.require(l.session.Capabilities().Query, l.session.id); e != nil {
		return contract.SQLResult{}, e
	}
	return l.raw.Execute(ctx, ns, sql)
}
func (l *queryLease) ExecuteStream(ctx context.Context, ns, sql string, sink func(int, []contract.Column, []contract.Cell) error) (contract.SQLResult, error) {
	if e := l.session.require(l.session.Capabilities().Query, l.session.id); e != nil {
		return contract.SQLResult{}, e
	}
	return l.raw.ExecuteStream(ctx, ns, sql, sink)
}
func (l *queryLease) Release() { l.raw.Release() }
func (l *queryLease) Replace() (contract.QueryLease, error) {
	if e := l.session.require(l.session.Capabilities().Query, l.session.id); e != nil {
		return nil, e
	}
	raw, e := l.raw.Replace()
	if e != nil {
		return nil, e
	}
	return &queryLease{session: l.session, doc: l.doc, raw: raw}, nil
}

func (l *queryLease) SupportsFiles() bool { _, ok := l.raw.(contract.QueryFileLease); return ok }
func (l *queryLease) ExecuteFile(ctx context.Context, ns string, r io.Reader) (contract.SQLResult, error) {
	if e := l.session.require(l.session.Capabilities().Query, l.session.id); e != nil {
		return contract.SQLResult{}, e
	}
	file, ok := l.raw.(contract.QueryFileLease)
	if !ok {
		return contract.SQLResult{}, contract.ErrUnsupported
	}
	return file.ExecuteFile(ctx, ns, r)
}
