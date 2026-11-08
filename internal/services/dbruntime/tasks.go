package dbruntime

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"time"
)

func (m *Manager) TaskDocuments(id int64) (contract.TaskDocumentsProvider, error) {
	published, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	s := published.(*session)
	if _, err = contract.Require[contract.TaskDocumentsProvider](s.raw); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *session) SupportsTaskDocument(o contract.ObjectRef) bool {
	if s.require(s.Capabilities().Metadata, o.ProfileID) != nil {
		return false
	}
	p, err := contract.Require[contract.TaskDocumentsProvider](s.raw)
	return err == nil && p.SupportsTaskDocument(o)
}
func (s *session) ReserveTaskDocument(o contract.ObjectRef) (contract.TaskDocumentLease, error) {
	if err := s.require(s.Capabilities().Metadata, o.ProfileID); err != nil {
		return nil, err
	}
	p, err := contract.Require[contract.TaskDocumentsProvider](s.raw)
	if err != nil {
		return nil, err
	}
	raw, err := p.ReserveTaskDocument(o)
	if err != nil {
		return nil, err
	}
	return &taskDocument{session: s, object: o, raw: raw}, nil
}

type taskDocument struct {
	session *session
	object  contract.ObjectRef
	raw     contract.TaskDocumentLease
}

func (d *taskDocument) check() error {
	return d.session.require(d.session.Capabilities().Metadata, d.session.id)
}
func (d *taskDocument) List(ctx context.Context, q contract.TaskQuery) (contract.TaskPage, error) {
	if err := d.check(); err != nil {
		return contract.TaskPage{}, err
	}
	return d.raw.List(ctx, q)
}
func (d *taskDocument) Read(ctx context.Context, r contract.TaskRecord) (contract.TaskRecord, error) {
	if err := d.check(); err != nil {
		return contract.TaskRecord{}, err
	}
	return d.raw.Read(ctx, r)
}
func (d *taskDocument) Observe(ctx context.Context, r contract.TaskRecord, bound time.Duration) (contract.TaskRecord, error) {
	if err := d.check(); err != nil {
		return contract.TaskRecord{}, err
	}
	return d.raw.Observe(ctx, r, bound)
}
func (d *taskDocument) Close() error { return d.raw.Close() }
