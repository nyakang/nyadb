package dbruntime

import (
	"context"
	"errors"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (m *Manager) GraphSchemaManagement(id int64) (contract.GraphSchemaManagementProvider, error) {
	published, e := m.Session(id)
	if e != nil {
		return nil, e
	}
	s := published.(*session)
	if _, e = contract.Require[contract.GraphSchemaManagementProvider](s.raw); e != nil {
		return nil, e
	}
	return s, nil
}
func (s *session) SupportsGraphSchemaManagement(o contract.ObjectRef) bool {
	if s.require(s.Capabilities().Metadata, o.ProfileID) != nil {
		return false
	}
	p, e := contract.Require[contract.GraphSchemaManagementProvider](s.raw)
	return e == nil && p.SupportsGraphSchemaManagement(o)
}
func (s *session) ReserveGraphSchemaManagement(o contract.ObjectRef, id uint64) (contract.GraphSchemaManagementLease, error) {
	if e := s.require(s.Capabilities().Metadata, o.ProfileID); e != nil {
		return nil, e
	}
	p, e := contract.Require[contract.GraphSchemaManagementProvider](s.raw)
	if e != nil {
		return nil, e
	}
	raw, e := p.ReserveGraphSchemaManagement(o, id)
	if e != nil {
		return nil, e
	}
	if raw == nil {
		return nil, contract.ErrUnsupported
	}
	if raw.Object() != o {
		return nil, errors.Join(contract.ErrRevoked, raw.Close())
	}
	if e := s.require(s.Capabilities().Metadata, o.ProfileID); e != nil {
		return nil, errors.Join(e, raw.Close())
	}
	return &graphSchemaManagementLease{graphSchemaPreparationLease: &graphSchemaPreparationLease{session: s, raw: raw, object: o}, management: raw}, nil
}

type graphSchemaManagementLease struct {
	*graphSchemaPreparationLease
	management contract.GraphSchemaManagementLease
	evidenceMu sync.Mutex
	last       contract.GraphSchemaManagementResult
}

func (d *graphSchemaManagementLease) retained() contract.GraphSchemaManagementResult {
	d.evidenceMu.Lock()
	defer d.evidenceMu.Unlock()
	r := d.last.Clone()
	if r.State == "" {
		r.State = "not_submitted"
	}
	return r
}
func (d *graphSchemaManagementLease) retain(r contract.GraphSchemaManagementResult) contract.GraphSchemaManagementResult {
	d.evidenceMu.Lock()
	if !d.closed.Load() && r.Preview.Request.Query != "" {
		d.last = r.Clone()
	}
	d.evidenceMu.Unlock()
	return r.Clone()
}
func (d *graphSchemaManagementLease) Submit(ctx context.Context, prepared contract.GraphSchemaPreparation, confirmation contract.GraphSchemaConfirmation) (contract.GraphSchemaManagementResult, error) {
	if e := d.check(); e != nil {
		return d.retained(), e
	}
	p, ok := prepared.(*graphSchemaPreparation)
	if !ok || p == nil || p.owner != d.graphSchemaPreparationLease {
		return d.retained(), contract.ErrGraphSchemaChanged
	}
	r, e := d.management.Submit(ctx, p.raw, confirmation)
	// A completed original receipt survives revocation during this operation;
	// no new metadata is published for a replacement generation.
	if post := d.check(); post != nil {
		e = errors.Join(e, post)
	}
	return d.retain(r), e
}
func (d *graphSchemaManagementLease) Verify(ctx context.Context) (contract.GraphSchemaManagementResult, error) {
	if e := d.check(); e != nil {
		return d.retained(), e
	}
	r, e := d.management.Verify(ctx)
	if post := d.check(); post != nil {
		return d.retained(), errors.Join(e, post)
	}
	return d.retain(r), e
}
func (d *graphSchemaManagementLease) Close() error {
	e := d.graphSchemaPreparationLease.Close()
	d.evidenceMu.Lock()
	d.last = contract.GraphSchemaManagementResult{}
	d.evidenceMu.Unlock()
	return e
}
