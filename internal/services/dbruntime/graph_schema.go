package dbruntime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphschema"
)

func (m *Manager) GraphSchemaPreparation(id int64) (contract.GraphSchemaPreparationProvider, error) {
	published, e := m.Session(id)
	if e != nil {
		return nil, e
	}
	s := published.(*session)
	if _, e = contract.Require[contract.GraphSchemaPreparationProvider](s.raw); e != nil {
		return nil, e
	}
	return s, nil
}
func (s *session) SupportsGraphSchemaPreparation(o contract.ObjectRef) bool {
	if s.require(s.Capabilities().Metadata, o.ProfileID) != nil {
		return false
	}
	p, e := contract.Require[contract.GraphSchemaPreparationProvider](s.raw)
	return e == nil && p.SupportsGraphSchemaPreparation(o)
}
func (s *session) ReserveGraphSchemaPreparation(o contract.ObjectRef, id uint64) (contract.GraphSchemaPreparationLease, error) {
	if e := s.require(s.Capabilities().Metadata, o.ProfileID); e != nil {
		return nil, e
	}
	p, e := contract.Require[contract.GraphSchemaPreparationProvider](s.raw)
	if e != nil {
		return nil, e
	}
	raw, e := p.ReserveGraphSchemaPreparation(o, id)
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
	return &graphSchemaPreparationLease{session: s, raw: raw, object: o}, nil
}

type graphSchemaPreparationLease struct {
	session   *session
	raw       contract.GraphSchemaPreparationLease
	object    contract.ObjectRef
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}
type graphSchemaObservation struct {
	owner    *graphSchemaPreparationLease
	raw      contract.GraphSchemaObservation
	snapshot contract.GraphSchemaCatalog
}
type graphSchemaPreparation struct {
	preview contract.GraphSchemaPreview
	owner   *graphSchemaPreparationLease
	raw     contract.GraphSchemaPreparation
}

func (d *graphSchemaPreparationLease) Object() contract.ObjectRef { return d.object }
func (d *graphSchemaPreparationLease) Generation() uint64         { return d.session.generation }
func (d *graphSchemaPreparationLease) check() error {
	if d.closed.Load() {
		return contract.ErrRevoked
	}
	return d.session.require(d.session.Capabilities().Metadata, d.object.ProfileID)
}
func (o *graphSchemaObservation) Snapshot() contract.GraphSchemaCatalog { return o.snapshot.Clone() }
func (p *graphSchemaPreparation) Preview() contract.GraphSchemaPreview  { return p.preview.Clone() }
func (d *graphSchemaPreparationLease) Read(ctx context.Context) (contract.GraphSchemaObservation, error) {
	if e := d.check(); e != nil {
		return nil, e
	}
	o, e := d.raw.Read(ctx)
	if e != nil {
		return nil, e
	}
	if e = d.check(); e != nil {
		return nil, e
	}
	if o == nil {
		return nil, contract.ErrUnsupported
	}
	snapshot := o.Snapshot().Clone()
	if snapshot.Object != d.object {
		return nil, contract.ErrRevoked
	}
	if e = d.check(); e != nil {
		return nil, e
	}
	return &graphSchemaObservation{owner: d, raw: o, snapshot: snapshot}, nil
}
func (d *graphSchemaPreparationLease) Prepare(before contract.GraphSchemaObservation, draft contract.GraphSchemaDraft) (contract.GraphSchemaPreparation, error) {
	if e := d.check(); e != nil {
		return nil, e
	}
	o, ok := before.(*graphSchemaObservation)
	if !ok || o == nil || o.owner != d {
		return nil, contract.ErrGraphSchemaChanged
	}
	if e := graphschema.ValidateDraftBounds(draft); e != nil {
		return nil, e
	}
	p, e := d.raw.Prepare(o.raw, draft.Clone())
	if e != nil {
		return nil, e
	}
	if e = d.check(); e != nil {
		return nil, e
	}
	if p == nil {
		return nil, contract.ErrUnsupported
	}
	preview := p.Preview().Clone()
	if e = d.check(); e != nil {
		return nil, e
	}
	return &graphSchemaPreparation{preview: preview, owner: d, raw: p}, nil
}
func (d *graphSchemaPreparationLease) Close() error {
	d.closeOnce.Do(func() { d.closed.Store(true); d.closeErr = d.raw.Close() })
	return d.closeErr
}
